// Package main is a cross-check harness for the OpenTelemetry bridge in
// pkg/otelobserver. It runs a real agent against a real provider, captures
// every span and every metric the bridge produced, and prints them next to
// the run's own ExecutionResult.Usage / EstimatedCostUSD.
//
// The point is the comparison. A unit test can assert that the bridge adds up
// what it was handed; only a live run can say whether what it was handed adds
// up to what the run actually spent. The questions it answers:
//
//   - do agentgo.tokens.{prompt,completion,cached} sum to the run's Usage,
//   - does agentgo.cost.usd equal the run's EstimatedCostUSD,
//   - does the duration histogram count every model turn and tool call,
//   - does every span event land under the span it belongs to,
//   - does any metric carry a task / session / run id (it must not).
//
// Usage:
//
//	set -a; source ~/.config/llm/endpoints.env; set +a
//	go run ./examples/otel-probe                     # in-memory exporters, tables
//	go run ./examples/otel-probe -exporter=stdout    # OTLP-shaped JSON on stdout
//	go run ./examples/otel-probe -otlp=http://localhost:4318/v1/traces
//
// -run=false / -segments=false skip either half, so a single run can be
// inspected without paying for the segmented one.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/otelobserver"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
	"github.com/liliang-cn/agent-go/v3/pkg/providers"
)

func main() {
	var (
		exporterKind = flag.String("exporter", "memory", `"memory" (span tree + metric tables) or "stdout" (also OTLP-shaped JSON)`)
		otlpURL      = flag.String("otlp", "", "also export spans to this OTLP/HTTP traces URL (e.g. http://localhost:4318/v1/traces)")
		model        = flag.String("model", envOr("AGENTGO_PROBE_MODEL", "gemini-3.7-flash-high"), "model name")
		doRun        = flag.Bool("run", true, "run the single-Run probe")
		doSegments   = flag.Bool("segments", true, "run the RunSegments probe")
		roundsPer    = flag.Int("rounds-per-segment", 2, "RoundsPerSegment for the segmented probe")
		maxSegments  = flag.Int("max-segments", 2, "MaxSegments for the segmented probe")
		price        = flag.Bool("price", true, "register a pricing entry for the model, so the priced path is covered")
		identity     = flag.Bool("model-identity", true, "wrap the provider so Service.Info().Model reports the model name (see namedGenerator)")
		timeout      = flag.Duration("timeout", 8*time.Minute, "overall deadline")
	)
	flag.Parse()

	baseURL := os.Getenv("CPA_PROD_URL")
	apiKey := os.Getenv("CPA_PROD_KEY")
	if baseURL == "" || apiKey == "" {
		log.Fatal("CPA_PROD_URL / CPA_PROD_KEY must be set; source ~/.config/llm/endpoints.env first")
	}

	// Pricing is a deliberate knob. With -price=false the model is unknown to
	// pool, which is the case agentgo.model.unpriced_turns exists for; the
	// probe then shows the cost counter staying absent rather than reporting
	// a confident zero.
	if *price {
		pool.RegisterModelPricing(*model, pool.ModelPricing{
			InputPer1K:       0.0003,
			CachedInputPer1K: 0.000075,
			OutputPer1K:      0.0025,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	mem := tracetest.NewInMemoryExporter()
	processors := []sdktrace.SpanProcessor{sdktrace.NewSimpleSpanProcessor(mem)}

	var shutdowns []func(context.Context) error
	if *exporterKind == "stdout" {
		te, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			log.Fatalf("stdout trace exporter: %v", err)
		}
		processors = append(processors, sdktrace.NewSimpleSpanProcessor(te))
	}
	if *otlpURL != "" {
		oe, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(*otlpURL))
		if err != nil {
			log.Fatalf("otlp exporter: %v", err)
		}
		processors = append(processors, sdktrace.NewBatchSpanProcessor(oe))
		shutdowns = append(shutdowns, oe.Shutdown)
	}
	tpOpts := make([]sdktrace.TracerProviderOption, 0, len(processors))
	for _, p := range processors {
		tpOpts = append(tpOpts, sdktrace.WithSpanProcessor(p))
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)

	// A ManualReader is what makes the cross-check possible: it collects on
	// demand, so a before/after pair brackets exactly one Run.
	reader := sdkmetric.NewManualReader()
	mpOpts := []sdkmetric.Option{sdkmetric.WithReader(reader)}
	var stdoutMetricReader *sdkmetric.PeriodicReader
	if *exporterKind == "stdout" {
		me, err := stdoutmetric.New(stdoutmetric.WithPrettyPrint())
		if err != nil {
			log.Fatalf("stdout metric exporter: %v", err)
		}
		// An interval longer than the probe, so the only emission is the one
		// forced at the end and the JSON is a single readable document.
		stdoutMetricReader = sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(time.Hour))
		mpOpts = append(mpOpts, sdkmetric.WithReader(stdoutMetricReader))
	}
	mp := sdkmetric.NewMeterProvider(mpOpts...)

	obs := otelobserver.New(tp, otelobserver.WithMeterProvider(mp))

	llm, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		BaseURL:  baseURL,
		APIKey:   apiKey,
		LLMModel: *model,
	})
	if err != nil {
		log.Fatalf("provider: %v", err)
	}

	var generator domain.Generator = llm
	if *identity {
		generator = namedGenerator{Generator: llm, model: *model, baseURL: baseURL}
	}

	svc, err := agent.New("otel-probe").
		WithLLM(generator).
		WithObserver(obs).
		WithSystemPrompt("You are a terse assistant. Use the tools you are given rather than answering from memory.").
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()
	registerProbeTools(svc)

	fmt.Printf("model=%s base=%s priced=%v\n", *model, redactHost(baseURL), *price)

	if *doRun {
		before := snapshot(ctx, reader)
		res, err := svc.Run(ctx, "Use calculate to work out 1487*233, then use lookup_employee for the id E-42, then answer with both results in one sentence.")
		if err != nil {
			log.Printf("run: %v", err)
		}
		after := snapshot(ctx, reader)
		fmt.Println(banner("SINGLE RUN — result vs metrics"))
		reportRun(res)
		fmt.Println()
		reportMetricDelta(before, after)
		fmt.Println()
		crossCheckRun(res, before, after)
	}

	if *doSegments {
		before := snapshot(ctx, reader)
		lr, err := svc.RunSegments(ctx,
			"Compute 91*17 with the calculate tool, then look up employee E-7, then state both answers.",
			agent.LongRunConfig{
				MaxSegments:         *maxSegments,
				RoundsPerSegment:    *roundsPer,
				AllowIncompletePlan: true,
			})
		if err != nil {
			log.Printf("segments: %v", err)
		}
		after := snapshot(ctx, reader)
		fmt.Println(banner("RUN SEGMENTS — result vs metrics"))
		reportLongRun(lr)
		fmt.Println()
		reportMetricDelta(before, after)
		fmt.Println()
		crossCheckLongRun(lr, before, after)
	}

	// Observer first (so a root whose task never checkpointed is ended and
	// therefore exportable), then the providers, which flush.
	if err := obs.Shutdown(ctx); err != nil {
		log.Printf("observer shutdown: %v", err)
	}
	if err := tp.ForceFlush(ctx); err != nil {
		log.Printf("trace flush: %v", err)
	}

	spans := mem.GetSpans().Snapshots()
	fmt.Println(banner("SPAN TREE"))
	printSpanTree(spans)
	fmt.Println(banner("SPAN EVENTS"))
	printSpanEvents(spans)
	fmt.Println(banner("ALL METRICS (every attribute set)"))
	printMetricsFull(ctx, reader)
	fmt.Println(banner("CARDINALITY CHECK"))
	printCardinalityCheck(ctx, reader)

	if stdoutMetricReader != nil {
		// The periodic reader's interval is longer than the probe, so the one
		// emission is the one Shutdown forces — below, exactly once.
		fmt.Println(banner("OTLP-SHAPED METRIC JSON"))
	}
	for _, sd := range shutdowns {
		_ = sd(ctx)
	}
	_ = tp.Shutdown(ctx)
	_ = mp.Shutdown(ctx)
}

// namedGenerator gives an injected provider the model identity the builder
// looks for.
//
// It exists because of a measured gap, not for tidiness. Builder.WithLLM asks
// the generator for GetModelName/GetBaseURL, and providers.OpenAILLMProvider
// implements neither — so a service built this way reports Info().Model == "",
// which is the model name the runtime prices every turn with and the one the
// bridge puts on every metric. Without the wrapper the first probe run
// produced `agentgo.model=""`, two unpriced turns, and EstimatedCostUSD 0.00
// on a run that spent real money. Run with -model-identity=false to see it.
type namedGenerator struct {
	domain.Generator
	model   string
	baseURL string
}

func (g namedGenerator) GetModelName() string { return g.model }
func (g namedGenerator) GetBaseURL() string   { return g.baseURL }

// registerProbeTools adds two sandbox-free tools: arithmetic the model is bad
// at, and a lookup it cannot possibly know. Both are deterministic, so a
// repeat of the probe compares cleanly against the last one.
func registerProbeTools(svc *agent.Service) {
	svc.AddTool("calculate", "Multiply two integers. Always use this for arithmetic.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"a": map[string]interface{}{"type": "integer", "description": "first factor"},
				"b": map[string]interface{}{"type": "integer", "description": "second factor"},
			},
			"required": []string{"a", "b"},
		},
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"product": toInt(args["a"]) * toInt(args["b"])}, nil
		})

	svc.AddTool("lookup_employee", "Look up an employee record by id.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "string", "description": "employee id, e.g. E-42"},
			},
			"required": []string{"id"},
		},
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			id, _ := args["id"].(string)
			return map[string]interface{}{
				"id":         id,
				"name":       "Wen Li",
				"department": "Observability",
				"desk":       "3076",
			}, nil
		})
}

func toInt(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case string:
		var out int64
		_, _ = fmt.Sscanf(n, "%d", &out)
		return out
	}
	return 0
}

// ---------- reporting ----------

func banner(s string) string {
	return "\n" + strings.Repeat("=", 74) + "\n== " + s + "\n" + strings.Repeat("=", 74)
}

func reportRun(res *agent.ExecutionResult) {
	if res == nil {
		fmt.Println("(no result)")
		return
	}
	fmt.Printf("success=%v stop_reason=%s tool_calls=%d tools=%v\n",
		res.Success, res.StopReason, res.ToolCalls, res.ToolsUsed)
	fmt.Printf("ExecutionResult.EstimatedCostUSD = %.8f\n", res.EstimatedCostUSD)
	if res.Usage == nil {
		fmt.Println("ExecutionResult.Usage            = nil (no round reported provider usage)")
		return
	}
	fmt.Printf("ExecutionResult.Usage            = prompt=%d completion=%d cached=%d cache_write=%d\n",
		res.Usage.PromptTokens, res.Usage.CompletionTokens,
		res.Usage.CachedPromptTokens, res.Usage.CacheWriteTokens)
}

func reportLongRun(lr *agent.LongRunResult) {
	if lr == nil {
		fmt.Println("(no result)")
		return
	}
	fmt.Printf("stop=%v done=%v segments=%d duration=%s\n",
		lr.Stop, lr.Done(), len(lr.Segments), lr.Duration.Round(time.Millisecond))
	for _, s := range lr.Segments {
		fmt.Printf("  segment %d stop=%s productive=%v rounds=%d err=%q\n",
			s.Index, s.StopReason, s.Productive, s.Rounds, s.Error)
	}
	fmt.Printf("LongRunResult.TotalCostUSD = %.8f\n", lr.TotalCostUSD)
	if lr.TotalUsage == nil {
		fmt.Println("LongRunResult.TotalUsage   = nil")
		return
	}
	fmt.Printf("LongRunResult.TotalUsage   = prompt=%d completion=%d cached=%d cache_write=%d\n",
		lr.TotalUsage.PromptTokens, lr.TotalUsage.CompletionTokens,
		lr.TotalUsage.CachedPromptTokens, lr.TotalUsage.CacheWriteTokens)
}

// metricSnapshot flattens a collection into name -> summed value, plus
// name+".count" / name+".sum" for histograms.
type metricSnapshot map[string]float64

func snapshot(ctx context.Context, r *sdkmetric.ManualReader) metricSnapshot {
	var rm metricdata.ResourceMetrics
	if err := r.Collect(ctx, &rm); err != nil {
		log.Printf("collect: %v", err)
		return metricSnapshot{}
	}
	out := metricSnapshot{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					out[m.Name] += float64(dp.Value)
				}
			case metricdata.Sum[float64]:
				for _, dp := range d.DataPoints {
					out[m.Name] += dp.Value
				}
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					out[m.Name+".count"] += float64(dp.Count)
					out[m.Name+".sum"] += dp.Sum
				}
			}
		}
	}
	return out
}

func (s metricSnapshot) delta(other metricSnapshot, key string) float64 {
	return s[key] - other[key]
}

func reportMetricDelta(before, after metricSnapshot) {
	names := map[string]struct{}{}
	for k := range before {
		names[k] = struct{}{}
	}
	for k := range after {
		names[k] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	fmt.Println("metrics recorded during this call:")
	for _, n := range sorted {
		if d := after[n] - before[n]; d != 0 {
			fmt.Printf("  %-38s %.8f\n", n, d)
		}
	}
}

// crossCheckRun is the assertion the whole probe exists for: the counters and
// the run's own accounting are two independent paths to the same numbers.
func crossCheckRun(res *agent.ExecutionResult, before, after metricSnapshot) {
	if res == nil {
		return
	}
	fmt.Println("cross-check (metric delta vs ExecutionResult):")
	if res.Usage != nil {
		checkInt("tokens.prompt", after.delta(before, "agentgo.tokens.prompt"), float64(res.Usage.PromptTokens))
		checkInt("tokens.completion", after.delta(before, "agentgo.tokens.completion"), float64(res.Usage.CompletionTokens))
		checkInt("tokens.cached", after.delta(before, "agentgo.tokens.cached"), float64(res.Usage.CachedPromptTokens))
	} else {
		fmt.Println("  SKIP  token comparison: run reported no provider usage")
	}
	checkFloat("cost.usd", after.delta(before, "agentgo.cost.usd"), res.EstimatedCostUSD)
	checkInt("tool.duration count vs model.calls",
		after.delta(before, "agentgo.tool.duration.count"),
		after.delta(before, "agentgo.tool.calls"))
	checkInt("model.duration count vs model.calls",
		after.delta(before, "agentgo.model.duration.count"),
		after.delta(before, "agentgo.model.calls"))
}

func crossCheckLongRun(lr *agent.LongRunResult, before, after metricSnapshot) {
	if lr == nil {
		return
	}
	fmt.Println("cross-check (metric delta vs LongRunResult):")
	if lr.TotalUsage != nil {
		checkInt("tokens.prompt", after.delta(before, "agentgo.tokens.prompt"), float64(lr.TotalUsage.PromptTokens))
		checkInt("tokens.completion", after.delta(before, "agentgo.tokens.completion"), float64(lr.TotalUsage.CompletionTokens))
		checkInt("tokens.cached", after.delta(before, "agentgo.tokens.cached"), float64(lr.TotalUsage.CachedPromptTokens))
	} else {
		fmt.Println("  SKIP  token comparison: no segment reported provider usage")
	}
	checkFloat("cost.usd", after.delta(before, "agentgo.cost.usd"), lr.TotalCostUSD)
	checkInt("model.duration count vs model.calls",
		after.delta(before, "agentgo.model.duration.count"),
		after.delta(before, "agentgo.model.calls"))
}

func checkInt(label string, got, want float64) {
	verdict := "OK  "
	if got != want {
		verdict = "FAIL"
	}
	fmt.Printf("  %s  %-38s metric=%.0f result=%.0f\n", verdict, label, got, want)
}

func checkFloat(label string, got, want float64) {
	verdict := "OK  "
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	// Both sides are the same float additions in the same order, so anything
	// beyond rounding noise is a real disagreement.
	if diff > 1e-9 {
		verdict = "FAIL"
	}
	fmt.Printf("  %s  %-38s metric=%.8f result=%.8f diff=%.10f\n", verdict, label, got, want, got-want)
}

// printMetricsFull dumps every metric with every attribute set.
func printMetricsFull(ctx context.Context, r *sdkmetric.ManualReader) {
	var rm metricdata.ResourceMetrics
	if err := r.Collect(ctx, &rm); err != nil {
		log.Printf("collect: %v", err)
		return
	}
	for _, sm := range rm.ScopeMetrics {
		fmt.Printf("scope %s\n", sm.Scope.Name)
		metrics := append([]metricdata.Metrics(nil), sm.Metrics...)
		sort.Slice(metrics, func(i, j int) bool { return metrics[i].Name < metrics[j].Name })
		for _, m := range metrics {
			fmt.Printf("  %s [%s] %s\n", m.Name, m.Unit, m.Description)
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					fmt.Printf("      %-12d %s\n", dp.Value, attrString(dp.Attributes))
				}
			case metricdata.Sum[float64]:
				for _, dp := range d.DataPoints {
					fmt.Printf("      %-12.8f %s\n", dp.Value, attrString(dp.Attributes))
				}
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					fmt.Printf("      count=%d sum=%.3f %s\n", dp.Count, dp.Sum, attrString(dp.Attributes))
				}
			}
		}
	}
}

func attrString(set attribute.Set) string {
	kvs := set.ToSlice()
	parts := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		parts = append(parts, fmt.Sprintf("%s=%v", kv.Key, kv.Value.AsInterface()))
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, " ") + "}"
}

// forbiddenMetricAttrs are the per-run dimensions that would turn one time
// series into one per run — a trace, badly, at a hundred times the storage.
var forbiddenMetricAttrs = []string{"task", "task_id", "taskid", "session", "session_id", "sessionid", "run", "run_id", "runid", "span", "span_id", "spanid", "goal"}

func printCardinalityCheck(ctx context.Context, r *sdkmetric.ManualReader) {
	var rm metricdata.ResourceMetrics
	if err := r.Collect(ctx, &rm); err != nil {
		return
	}
	bad := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, set := range attrSets(m) {
				for _, kv := range set.ToSlice() {
					key := strings.ToLower(string(kv.Key))
					short := key
					if i := strings.LastIndex(key, "."); i >= 0 {
						short = key[i+1:]
					}
					for _, f := range forbiddenMetricAttrs {
						if key == f || short == f {
							fmt.Printf("  FAIL %s carries %s\n", m.Name, kv.Key)
							bad++
						}
					}
				}
			}
		}
	}
	if bad == 0 {
		fmt.Println("  ok: no metric carries a task / session / run / span id attribute")
	}
}

func attrSets(m metricdata.Metrics) []attribute.Set {
	var out []attribute.Set
	switch d := m.Data.(type) {
	case metricdata.Sum[int64]:
		for _, dp := range d.DataPoints {
			out = append(out, dp.Attributes)
		}
	case metricdata.Sum[float64]:
		for _, dp := range d.DataPoints {
			out = append(out, dp.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, dp := range d.DataPoints {
			out = append(out, dp.Attributes)
		}
	}
	return out
}

// printSpanTree renders parent/child nesting by span id.
func printSpanTree(spans []sdktrace.ReadOnlySpan) {
	children := map[trace.SpanID][]sdktrace.ReadOnlySpan{}
	known := map[trace.SpanID]bool{}
	for _, s := range spans {
		known[s.SpanContext().SpanID()] = true
	}
	var roots []sdktrace.ReadOnlySpan
	for _, s := range spans {
		parent := s.Parent().SpanID()
		if !s.Parent().IsValid() || !known[parent] {
			roots = append(roots, s)
			continue
		}
		children[parent] = append(children[parent], s)
	}
	sortByStart(roots)
	for _, r := range roots {
		printSpan(r, children, 0)
	}
}

func sortByStart(s []sdktrace.ReadOnlySpan) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].StartTime().Before(s[j].StartTime()) })
}

// keyAttrs are the ones worth printing inline; the rest would drown the tree.
var keyAttrs = []string{
	"openinference.span.kind", "llm.model_name", "agentgo.round", "tool.name",
	"llm.token_count.prompt", "llm.token_count.completion", "llm.token_count.prompt_details.cache_read",
	"agentgo.checkpoint.reason", "agentgo.segment.index", "agentgo.segment.stop_reason",
}

func printSpan(s sdktrace.ReadOnlySpan, children map[trace.SpanID][]sdktrace.ReadOnlySpan, depth int) {
	attrs := map[string]string{}
	for _, kv := range s.Attributes() {
		attrs[string(kv.Key)] = fmt.Sprintf("%v", kv.Value.AsInterface())
	}
	parts := make([]string, 0, len(keyAttrs))
	for _, k := range keyAttrs {
		if v, ok := attrs[k]; ok && v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	fmt.Printf("%s%s  [%s] (%s) %s\n",
		strings.Repeat("  ", depth), s.Name(), s.Status().Code,
		s.EndTime().Sub(s.StartTime()).Round(time.Millisecond), strings.Join(parts, " "))
	kids := children[s.SpanContext().SpanID()]
	sortByStart(kids)
	for _, k := range kids {
		printSpan(k, children, depth+1)
	}
}

func printSpanEvents(spans []sdktrace.ReadOnlySpan) {
	ordered := append([]sdktrace.ReadOnlySpan(nil), spans...)
	sortByStart(ordered)
	deltas := 0
	for _, s := range ordered {
		for _, e := range s.Events() {
			if e.Name == "model.delta" {
				deltas++ // one per streamed fragment; too many to read
				continue
			}
			fmt.Printf("  %-22s on %-30s %s\n", e.Name, s.Name(), attrString(attribute.NewSet(e.Attributes...)))
		}
	}
	fmt.Printf("  (%d model.delta events suppressed)\n", deltas)
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

// redactHost keeps the endpoint's host in the output and drops its path, which
// on some gateways carries a key-shaped segment.
func redactHost(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return u[:i+3] + rest[:j] + "/..."
		}
	}
	return u
}
