package otelobserver

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// Metrics are the half of observability a trace cannot give you. A trace
// answers "what did this run do"; a counter answers "how much are we doing,
// and what is it costing" across every run in the process. The two share the
// same callbacks, so wiring a MeterProvider is the only extra step.
//
// The attribute rule is the one that decides whether these are usable at all:
// agent and model, never a task or session id. A metric's cost is the product
// of its label cardinalities, and a per-run label turns one time series into
// one per run — which is a trace, badly, at a hundred times the storage.

// Metric attribute keys. Every one of these is drawn from a small, bounded set
// (agent names, model names, tool names, lint names, error markers), which is
// what makes them safe as dimensions.
const (
	mAttrAgent   = "agentgo.agent"
	mAttrModel   = "agentgo.model"
	mAttrTool    = "agentgo.tool"
	mAttrStatus  = "agentgo.status"
	mAttrLint    = "agentgo.lint"
	mAttrVerdict = "agentgo.verdict"
	mAttrKind    = "agentgo.kind"
	mAttrTrigger = "agentgo.trigger"
	mAttrInner   = "agentgo.inner"
)

const (
	statusOK    = "ok"
	statusError = "error"
)

// instruments holds the metric handles. A nil *instruments is the no-meter
// case and every record helper below tolerates it, so metrics stay genuinely
// optional rather than "optional but branch everywhere".
type instruments struct {
	modelCalls    metric.Int64Counter
	modelDuration metric.Float64Histogram
	modelRetries  metric.Int64Counter

	toolCalls    metric.Int64Counter
	toolDuration metric.Float64Histogram

	lintRejections metric.Int64Counter
	compactions    metric.Int64Counter
	errors         metric.Int64Counter

	promptTokens     metric.Int64Counter
	completionTokens metric.Int64Counter
	cachedTokens     metric.Int64Counter

	costUSD       metric.Float64Counter
	unpricedTurns metric.Int64Counter

	// Process gauges. These are observable: the meter pulls them on its own
	// collection interval rather than waiting for a callback, so a process
	// that is leaking between runs — the interesting case, since a run's own
	// telemetry stops when the run does — is still visible.
	procRegistration metric.Registration
}

// newInstruments creates the instrument set, or returns nil when no
// MeterProvider was supplied.
func newInstruments(mp metric.MeterProvider) *instruments {
	if mp == nil {
		return nil
	}
	m := mp.Meter(instrumentationName)
	i := &instruments{}

	i.modelCalls, _ = m.Int64Counter("agentgo.model.calls",
		metric.WithDescription("Model turns that returned, successfully or not."))
	i.modelDuration, _ = m.Float64Histogram("agentgo.model.duration",
		metric.WithDescription("Wall time of a model turn, retries included."),
		metric.WithUnit("s"))
	i.modelRetries, _ = m.Int64Counter("agentgo.model.retries",
		metric.WithDescription("Re-asks of a model turn, by why."))

	i.toolCalls, _ = m.Int64Counter("agentgo.tool.calls",
		metric.WithDescription("Tool calls that returned, successfully or not."))
	i.toolDuration, _ = m.Float64Histogram("agentgo.tool.duration",
		metric.WithDescription("Wall time of a tool call."),
		metric.WithUnit("s"))

	i.lintRejections, _ = m.Int64Counter("agentgo.lint.rejections",
		metric.WithDescription("Final answers rejected by an output lint."))
	i.compactions, _ = m.Int64Counter("agentgo.compactions",
		metric.WithDescription("History folds performed."))
	i.errors, _ = m.Int64Counter("agentgo.errors",
		metric.WithDescription("Errors the runtime reported, by kind."))

	i.promptTokens, _ = m.Int64Counter("agentgo.tokens.prompt",
		metric.WithDescription("Prompt tokens billed, cache hits included."),
		metric.WithUnit("{token}"))
	i.completionTokens, _ = m.Int64Counter("agentgo.tokens.completion",
		metric.WithDescription("Completion tokens billed."),
		metric.WithUnit("{token}"))
	i.cachedTokens, _ = m.Int64Counter("agentgo.tokens.cached",
		metric.WithDescription("The cache-hit share of the prompt tokens."),
		metric.WithUnit("{token}"))

	i.costUSD, _ = m.Float64Counter("agentgo.cost.usd",
		metric.WithDescription("Estimated spend, priced per turn from the cache split."),
		metric.WithUnit("{USD}"))
	i.unpricedTurns, _ = m.Int64Counter("agentgo.model.unpriced_turns",
		metric.WithDescription("Turns nothing could price, which agentgo.cost.usd therefore omits."))

	i.registerProcessGauges(m)

	return i
}

// registerProcessGauges publishes what the process itself is using.
//
// The loop's own metrics say nothing about the program they run inside, and
// on a task measured in hours that is the half that kills you: a goroutine
// leaked per tool call, or a history nothing ever drops, does not fail a
// lint. It fails at 03:00 with the OOM killer, and every agent metric right
// up to the last one looks healthy.
//
// Observable, not pushed: they are read on the meter's schedule, so they keep
// reporting while a service sits idle between runs — which is exactly when a
// leak is easiest to see and when no run is emitting anything.
func (i *instruments) registerProcessGauges(m metric.Meter) {
	heap, err1 := m.Int64ObservableGauge("agentgo.process.heap.bytes",
		metric.WithDescription("Live Go heap."),
		metric.WithUnit("By"))
	objects, err2 := m.Int64ObservableGauge("agentgo.process.heap.objects",
		metric.WithDescription("Live heap objects, which separate one big buffer from a million small leaks."))
	goroutines, err3 := m.Int64ObservableGauge("agentgo.process.goroutines",
		metric.WithDescription("Goroutines alive right now."))
	rss, err4 := m.Int64ObservableGauge("agentgo.process.rss.bytes",
		metric.WithDescription("Resident set size, the number an OOM killer reads. Not reported where it cannot be read without cgo."),
		metric.WithUnit("By"))
	peakRSS, err5 := m.Int64ObservableGauge("agentgo.process.rss.peak_bytes",
		metric.WithDescription("Peak resident set size the kernel remembers."),
		metric.WithUnit("By"))
	cpu, err6 := m.Float64ObservableCounter("agentgo.process.cpu.seconds",
		metric.WithDescription("Cumulative process CPU time, user plus system."),
		metric.WithUnit("s"))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil {
		return
	}

	reg, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := agent.SampleProcess()
		o.ObserveInt64(heap, int64(s.HeapAllocBytes))
		o.ObserveInt64(objects, int64(s.HeapObjects))
		o.ObserveInt64(goroutines, int64(s.Goroutines))
		if s.RSSKnown {
			o.ObserveInt64(rss, int64(s.RSSBytes))
		}
		if s.PeakRSSBytes > 0 {
			o.ObserveInt64(peakRSS, int64(s.PeakRSSBytes))
		}
		if s.CPUKnown {
			o.ObserveFloat64(cpu, s.CPUSeconds())
		}
		return nil
	}, heap, objects, goroutines, rss, peakRSS, cpu)
	if err == nil {
		i.procRegistration = reg
	}
}

// recordModelEnd counts a finished turn, its duration, its tokens and — when
// the model's rates are known — its cost.
//
// A turn whose model nothing can price is counted separately rather than
// priced at zero. That distinction is the whole reason CalculateCostDetailed
// reports `known`: a silent 0 is indistinguishable from free, and a cost
// counter that quietly under-reports is worse than one that admits a gap.
//
// elapsed is the bridge's own measurement of the turn, from OnModelStart to
// OnModelEnd. It is the fallback for the case the runtime reports no result at
// all — a turn the provider failed still took time, and a histogram that skips
// those no longer counts one observation per turn, which is the first thing
// anyone divides by.
func (o *Observer) recordModelEnd(ctx context.Context, info agent.ModelInfo, res *agent.ModelResult, elapsed time.Duration, err error) {
	m := o.instrumentsOrNil()
	if m == nil {
		return
	}
	ctx = safeCtx(ctx)
	base := []attribute.KeyValue{
		attribute.String(mAttrAgent, info.AgentName),
		attribute.String(mAttrModel, info.Model),
	}
	addInt(ctx, m.modelCalls, 1, append(base, attribute.String(mAttrStatus, statusOf(err)))...)
	// The runtime's own timing wins when it reported one: it brackets the
	// retries, which is the number the histogram's description promises.
	seconds := elapsed.Seconds()
	if res != nil && res.DurationMs > 0 {
		seconds = float64(res.DurationMs) / 1000.0
	}
	recordFloat(ctx, m.modelDuration, seconds, base...)
	if res == nil {
		return
	}
	addInt(ctx, m.promptTokens, int64(res.PromptTokens), base...)
	addInt(ctx, m.completionTokens, int64(res.CompletionTokens), base...)
	addInt(ctx, m.cachedTokens, int64(res.CachedTokens), base...)

	cost, known := pool.CalculateCostDetailed(info.Model, res.PromptTokens, res.CachedTokens, res.CompletionTokens)
	if known {
		addFloat(ctx, m.costUSD, cost, base...)
		return
	}
	addInt(ctx, m.unpricedTurns, 1, base...)
}

// recordToolEnd counts a finished tool call and how long it took.
func (o *Observer) recordToolEnd(ctx context.Context, info agent.ToolInfo, elapsed time.Duration, err error) {
	m := o.instrumentsOrNil()
	if m == nil {
		return
	}
	ctx = safeCtx(ctx)
	attrs := []attribute.KeyValue{
		attribute.String(mAttrAgent, info.AgentName),
		attribute.String(mAttrTool, info.Tool),
		attribute.Bool(mAttrInner, info.Inner),
	}
	addInt(ctx, m.toolCalls, 1, append(attrs, attribute.String(mAttrStatus, statusOf(err)))...)
	if elapsed > 0 {
		recordFloat(ctx, m.toolDuration, elapsed.Seconds(), attrs...)
	}
}

func (o *Observer) recordLint(ctx context.Context, info agent.LintInfo) {
	m := o.instrumentsOrNil()
	if m == nil {
		return
	}
	addInt(safeCtx(ctx), m.lintRejections, 1,
		attribute.String(mAttrAgent, info.AgentName),
		attribute.String(mAttrLint, info.Lint),
		attribute.String(mAttrVerdict, lintVerdict(info.Retrying)),
	)
}

func (o *Observer) recordModelRetry(ctx context.Context, info agent.ModelRetryInfo) {
	m := o.instrumentsOrNil()
	if m == nil {
		return
	}
	addInt(safeCtx(ctx), m.modelRetries, 1,
		attribute.String(mAttrAgent, info.AgentName),
		attribute.String(mAttrKind, info.Kind),
	)
}

func (o *Observer) recordCompaction(ctx context.Context, info agent.CompactionInfo) {
	m := o.instrumentsOrNil()
	if m == nil {
		return
	}
	addInt(safeCtx(ctx), m.compactions, 1,
		attribute.String(mAttrAgent, info.AgentName),
		attribute.String(mAttrTrigger, info.Trigger),
	)
}

func (o *Observer) recordError(ctx context.Context, info agent.ErrorInfo) {
	m := o.instrumentsOrNil()
	if m == nil {
		return
	}
	addInt(safeCtx(ctx), m.errors, 1,
		attribute.String(mAttrAgent, info.AgentName),
		attribute.String(mAttrKind, errorKind(info.Marker)),
	)
}

func (o *Observer) instrumentsOrNil() *instruments {
	if o == nil {
		return nil
	}
	return o.metrics
}

func statusOf(err error) string {
	if err != nil {
		return statusError
	}
	return statusOK
}

func safeCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func addInt(ctx context.Context, c metric.Int64Counter, v int64, attrs ...attribute.KeyValue) {
	if c == nil || v < 0 {
		return
	}
	c.Add(ctx, v, metric.WithAttributes(attrs...))
}

func addFloat(ctx context.Context, c metric.Float64Counter, v float64, attrs ...attribute.KeyValue) {
	if c == nil || v < 0 {
		return
	}
	c.Add(ctx, v, metric.WithAttributes(attrs...))
}

func recordFloat(ctx context.Context, h metric.Float64Histogram, v float64, attrs ...attribute.KeyValue) {
	if h == nil || v < 0 {
		return
	}
	h.Record(ctx, v, metric.WithAttributes(attrs...))
}
