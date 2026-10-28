// Package trace records where a run's time goes: every model call with what it
// was for, how long before its first token and how long in all; memory
// lookups; tool calls; and a summary when the run ends.
//
// Three ways to read it, all off by default:
//
//   - AGENTGO_TRACE=1 writes one line per step to stderr, prefixed
//     "agentgo-trace", into whatever log the process already has:
//
//     agentgo-trace run=3f2a91c0 model purpose=memory-pick dur=1.12s ttft=- in=2210 out=40
//     agentgo-trace run=3f2a91c0 memory recall dur=1.31s
//     agentgo-trace run=3f2a91c0 model purpose=turn tools=38 msgs=6 dur=6.48s ttft=4.90s in=21544 out=402
//     agentgo-trace run=3f2a91c0 run end total=8.02s first_model_call=1.40s first_answer_token=6.30s …
//
//   - AGENTGO_TRACE_FILE=/path/trace.jsonl appends the same records as JSON,
//     one per line, for jq or a notebook. AGENTGO_TRACE_BODIES=1 adds what was
//     sent to each model call and what came back, so a wrong answer can be
//     read back from the exact prompt that produced it.
//
//   - Timeline(ctx) hands a run's records to the program itself; the agent
//     runtime emits them as a "trace" event when a run ends, so an app can show
//     a person where the time went.
//
// Setting either variable, or calling SetEnabled(true), turns recording on.
// Off, each step costs one atomic load.
package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

var (
	enabled atomic.Bool
	bodies  atomic.Bool
	stderr  atomic.Bool
)

func init() {
	on := func(k string) bool {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
		case "1", "true", "on", "yes":
			return true
		}
		return false
	}
	if on("AGENTGO_TRACE") {
		enabled.Store(true)
		stderr.Store(true)
	}
	if p := strings.TrimSpace(os.Getenv("AGENTGO_TRACE_FILE")); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			setFile(f)
			enabled.Store(true)
		} else {
			log.Printf("agentgo-trace: cannot open %s: %v", p, err)
		}
	}
	bodies.Store(on("AGENTGO_TRACE_BODIES"))
}

// Enabled reports whether recording is on.
func Enabled() bool { return enabled.Load() }

// SetEnabled turns recording on or off for the whole process. Lines go to
// stderr only when AGENTGO_TRACE asked for them or SetLines(true) was called.
func SetEnabled(on bool) { enabled.Store(on) }

// SetLines turns the stderr lines on or off.
func SetLines(on bool) { stderr.Store(on) }

// SetBodies records what each model call sent and received.
func SetBodies(on bool) { bodies.Store(on) }

var (
	outMu sync.Mutex
	lines = log.New(os.Stderr, "agentgo-trace ", log.LstdFlags|log.Lmicroseconds)
	file  io.Writer
)

// SetOutput sends the lines somewhere other than stderr (and turns them on).
func SetOutput(w io.Writer) {
	outMu.Lock()
	defer outMu.Unlock()
	lines = log.New(w, "agentgo-trace ", log.LstdFlags|log.Lmicroseconds)
	stderr.Store(true)
}

// SetFile appends JSON records to w (and turns recording on).
func SetFile(w io.Writer) {
	setFile(w)
	enabled.Store(true)
}

func setFile(w io.Writer) {
	outMu.Lock()
	defer outMu.Unlock()
	file = w
}

// Record is one step of a run.
type Record struct {
	Time time.Time `json:"ts"`
	Run  string    `json:"run,omitempty"`
	// Kind is "model", "memory", "tool", "decision" or "run" (the summary).
	Kind string `json:"kind"`
	// Name is a model call's purpose, a tool's name, a memory step's name, or
	// a run's outcome.
	Name string `json:"name"`
	// At is when the step started, after the run started.
	AtMs   int64 `json:"at_ms"`
	DurMs  int64 `json:"dur_ms"`
	TTFTMs int64 `json:"ttft_ms,omitempty"`
	In     int   `json:"in,omitempty"`
	Out    int   `json:"out,omitempty"`
	Cached int   `json:"cached,omitempty"`
	// CacheWrite is the prompt tokens written to the provider's cache.
	CacheWrite int `json:"cache_write,omitempty"`
	Think      int `json:"thinking,omitempty"`
	// Streamed is true when a model call's answer arrived as a stream; only
	// then is TTFTMs a time to first output rather than the whole wait.
	Streamed bool   `json:"streamed,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Err      string `json:"err,omitempty"`
	Request  any    `json:"request,omitempty"`
	Reply    any    `json:"reply,omitempty"`
}

type ctxKey int

const (
	runKey ctxKey = iota
	purposeKey
	statsKey
	sinkKey
)

// Sink hears every model call and step made with a context, whether or not
// recording is on. The agent runtime installs one per run to forward the calls
// it does not make itself — constraint extraction, memory picks, compaction —
// to its observers, so an OpenTelemetry exporter or a usage meter sees them.
type Sink interface {
	ModelStart(ctx context.Context, id, purpose string)
	ModelEnd(ctx context.Context, id string, r Record)
	Step(ctx context.Context, r Record)
}

// WireSink is a Sink that also hears what happens to each model call on the
// wire (Wire): a queue, an HTTP attempt and its status, a retry, the first
// byte, and a heartbeat while it waits. The agent runtime's sink forwards
// these to its observers and its event stream.
type WireSink interface {
	Sink
	Wire(ctx context.Context, id, purpose string, e WireEvent)
}

// Heartbeat pacing for a call's Wire: every WireTick it is asked whether it
// has gone WireQuiet without progress, and says what it waits on if so.
var (
	WireTick  = 5 * time.Second
	WireQuiet = 15 * time.Second
)

// BodySink is a Sink that also wants each model call's request and reply
// in the Records it receives, whether or not AGENTGO_TRACE_BODIES is set. The
// agent runtime's sink implements it when the service was built with
// WithObserverPayloads.
type BodySink interface {
	Sink
	WantBodies() bool
}

// WithSink installs a sink for the model calls and steps made with ctx.
func WithSink(ctx context.Context, s Sink) context.Context {
	if s == nil || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, sinkKey, s)
}

func sinkOf(ctx context.Context) Sink {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(sinkKey).(Sink)
	return s
}

// WithRun names the run a context belongs to and starts its timeline.
func WithRun(ctx context.Context, id string) context.Context {
	if !Enabled() {
		return ctx
	}
	ctx = context.WithValue(ctx, runKey, id)
	now := time.Now()
	return context.WithValue(ctx, statsKey, &runStats{start: now, tally: NewTally(now)})
}

// WithPurpose says what the model calls made with this context are for.
func WithPurpose(ctx context.Context, purpose string) context.Context {
	if !Enabled() || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, purposeKey, purpose)
}

func runOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(runKey).(string)
	return s
}

func statsOf(ctx context.Context) *runStats {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(statsKey).(*runStats)
	return s
}

// runStats is a run's timeline and its running totals.
type runStats struct {
	start   time.Time
	mu      sync.Mutex
	records []Record
	tally   *Tally
}

// maxRecords bounds one run's timeline; a run that loops past it keeps its
// first steps, which are the ones a slow start is made of.
const maxRecords = 500

func emit(ctx context.Context, r Record) {
	if !Enabled() {
		return
	}
	r.Run = runOf(ctx)
	if s := statsOf(ctx); s != nil {
		r.AtMs = r.Time.Sub(s.start).Milliseconds()
		if r.Kind == "run" {
			r.AtMs = 0
		}
		s.mu.Lock()
		// The wire's own lines are for the log and the file; the timeline a
		// person is shown keeps to the steps.
		if r.Kind != "wire" && len(s.records) < maxRecords {
			keep := r
			keep.Request, keep.Reply = nil, nil // the timeline is for showing, not for bodies
			s.records = append(s.records, keep)
		}
		s.mu.Unlock()
	}
	outMu.Lock()
	l, f := lines, file
	outMu.Unlock()
	if stderr.Load() {
		run := r.Run
		if len(run) > 8 {
			run = run[:8]
		}
		if run == "" {
			run = "-"
		}
		l.Printf("run=%s %s", run, r.line())
	}
	if f != nil {
		if b, err := json.Marshal(r); err == nil {
			outMu.Lock()
			_, _ = f.Write(append(b, '\n'))
			outMu.Unlock()
		}
	}
}

func (r Record) line() string {
	var b strings.Builder
	switch r.Kind {
	case "model":
		fmt.Fprintf(&b, "model purpose=%s", r.Name)
	case "run":
		fmt.Fprintf(&b, "run %s total=%s", r.Name, ms(r.DurMs))
	case "wire":
		return fmt.Sprintf("wire purpose=%s at=%s %s", r.Name, ms(r.DurMs), r.Detail)
	default:
		fmt.Fprintf(&b, "%s %s", r.Kind, r.Name)
	}
	if r.Detail != "" {
		b.WriteString(" " + r.Detail)
	}
	if r.Kind == "run" || r.Kind == "decision" {
		return b.String()
	}
	fmt.Fprintf(&b, " dur=%s", ms(r.DurMs))
	if r.Kind == "model" {
		ttft := "-" // no output came, or the call was not streamed
		if r.TTFTMs > 0 {
			ttft = ms(r.TTFTMs)
		}
		fmt.Fprintf(&b, " ttft=%s", ttft)
		if r.In+r.Out > 0 {
			fmt.Fprintf(&b, " in=%d out=%d", r.In, r.Out)
		}
		if r.Cached > 0 {
			fmt.Fprintf(&b, " cached=%d", r.Cached)
		}
		if r.CacheWrite > 0 {
			fmt.Fprintf(&b, " cache_write=%d", r.CacheWrite)
		}
		if r.Think > 0 {
			fmt.Fprintf(&b, " thinking=%d", r.Think)
		}
	}
	if r.Err != "" {
		fmt.Fprintf(&b, " err=%q", r.Err)
	}
	return b.String()
}

func ms(v int64) string {
	return fmt.Sprintf("%.2fs", float64(v)/1000)
}

// Call times one model call. Get one from Model; it is nil when recording is
// off, and every method on a nil Call does nothing.
type Call struct {
	id      string
	sink    Sink
	ctx     context.Context
	purpose string
	detail  string
	start   time.Time
	first   atomic.Int64 // ns after start of the first output; 0 = none yet
	request any
	reply   any
	wire    *Wire
	stop    chan struct{}
	stopped sync.Once
}

// Model starts timing a model call. purpose overrides the context's; detail
// is extra key=value text for the line.
func Model(ctx context.Context, purpose string, detail ...string) *Call {
	sink := sinkOf(ctx)
	if !Enabled() && sink == nil {
		return nil
	}
	if purpose == "" && ctx != nil {
		purpose, _ = ctx.Value(purposeKey).(string)
	}
	if purpose == "" {
		purpose = "other"
	}
	id := callIDOf(ctx)
	if id == "" {
		id = NewSpanID()
	}
	c := &Call{id: id, sink: sink, ctx: ctx, purpose: purpose,
		detail: strings.Join(detail, " "), start: time.Now()}
	if sink != nil {
		sink.ModelStart(ctx, c.id, purpose)
	}
	if _, ok := sink.(WireSink); ok || Enabled() {
		c.wire = NewWire(c.onWire)
		c.stop = make(chan struct{})
		go c.wire.Heartbeat(ctx, WireTick, WireQuiet, c.stop)
	}
	if s := statsOf(ctx); s != nil {
		s.tally.ModelStarted(c.start)
	}
	return c
}

type callIDKey struct{}

// WithCallID names the span id the next model call made with ctx takes, so a
// caller that already announced the call under an id (the runtime's
// answering turn) and the call's own record agree on one.
func WithCallID(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	return context.WithValue(ctx, callIDKey{}, id)
}

func callIDOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

// ID is the call's span id.
func (c *Call) ID() string {
	if c == nil {
		return ""
	}
	return c.id
}

// Context is ctx carrying this call's Wire, for the client the call goes
// through to report to, and the call as the current span, so a traceparent
// sent with the HTTP request names this call. Pass it to the model call
// instead of ctx.
func (c *Call) Context(ctx context.Context) context.Context {
	if c == nil {
		return ctx
	}
	if parent, ok := SpanFromContext(ctx); ok {
		ctx = ContextWithSpan(ctx, SpanContext{TraceID: parent.TraceID, SpanID: c.id, ParentSpanID: parent.SpanID})
	}
	return WithWire(ctx, c.wire)
}

// Wire is this call's Wire, or nil.
func (c *Call) Wire() *Wire {
	if c == nil {
		return nil
	}
	return c.wire
}

func (c *Call) onWire(e WireEvent) {
	if ws, ok := c.sink.(WireSink); ok {
		ws.Wire(c.ctx, c.id, c.purpose, e)
	}
	emit(c.ctx, Record{Time: time.Now(), Kind: "wire", Name: c.purpose, AtMs: 0, DurMs: e.ElapsedMs,
		Detail: e.Kind + ": " + e.Text})
}

func (c *Call) stopWire() {
	if c == nil || c.stop == nil {
		return
	}
	c.stopped.Do(func() { close(c.stop) })
}

// Output marks that output arrived. Only the first call counts.
func (c *Call) Output() {
	if c == nil {
		return
	}
	c.first.CompareAndSwap(0, int64(time.Since(c.start)))
}

// FirstOutput is how long the call waited for the first of its answer: the
// first output its caller marked, else the first byte the wire saw; 0 when
// neither has happened.
func (c *Call) FirstOutput() time.Duration {
	if c == nil {
		return 0
	}
	if first := time.Duration(c.first.Load()); first > 0 {
		return first
	}
	_, first := c.wire.reported()
	return first
}

// Request keeps what was sent, when bodies are being recorded or the
// call's sink asked for them.
func (c *Call) Request(v any) {
	if c != nil && c.keepBodies() {
		c.request = v
	}
}

// Reply keeps what came back, when bodies are being recorded or the call's
// sink asked for them.
func (c *Call) Reply(v any) {
	if c != nil && c.keepBodies() {
		c.reply = v
	}
}

func (c *Call) keepBodies() bool {
	if bodies.Load() {
		return true
	}
	bs, ok := c.sink.(BodySink)
	return ok && bs.WantBodies()
}

// End records the call.
func (c *Call) End(usage *domain.TokenUsage, err error) {
	if c == nil {
		return
	}
	c.stopWire()
	dur := time.Since(c.start)
	ttft := time.Duration(c.first.Load())
	// A caller that could not say — Generate returns a string, not its cost —
	// gets what the provider told the wire.
	wireUsage, wireFirst := c.wire.reported()
	if usage == nil {
		usage = wireUsage
	}
	if ttft == 0 {
		ttft = wireFirst
	}
	// Time to first output means something only for an answer that streamed.
	// A whole JSON answer's first byte is its last: reporting it as ttft said
	// a 34-second turn took 34.35 seconds to start.
	streamed := c.wire.streamed()
	if !streamed {
		ttft = 0
	}
	r := Record{Time: c.start, Kind: "model", Name: c.purpose, Detail: c.detail,
		DurMs: dur.Milliseconds(), TTFTMs: ttft.Milliseconds(), Streamed: streamed}
	if usage != nil {
		r.In, r.Out, r.Cached, r.Think = usage.PromptTokens, usage.CompletionTokens, usage.CachedPromptTokens, usage.ReasoningTokens
		r.CacheWrite = usage.CacheWriteTokens
	}
	if err != nil {
		r.Err = trim(err.Error())
	}
	// The sink gets the bodies whenever it asked for them; the trace file
	// and the stderr lines only when AGENTGO_TRACE_BODIES did. A service
	// that turned payloads on for its observers has not thereby agreed to
	// write every prompt into a file on disk.
	if c.sink != nil {
		rs := r
		if c.keepBodies() {
			rs.Request, rs.Reply = c.request, c.reply
		}
		c.sink.ModelEnd(c.ctx, c.id, rs)
	}
	if bodies.Load() {
		r.Request, r.Reply = c.request, c.reply
	}
	emit(c.ctx, r)
	if s := statsOf(c.ctx); s != nil {
		s.tally.Model(c.purpose, c.start, dur, ttft, Tokens{Prompt: r.In, Completion: r.Out, Cached: r.Cached, CacheWrite: r.CacheWrite})
	}
}

// Streamed marks this call's answer as having arrived as a stream. A client
// that reads an event stream says so on the call's Wire (Wire.Streamed); this
// is for a caller that streams without one.
func (c *Call) Streamed() {
	if c != nil {
		c.wire.Streamed()
	}
}

// Step times something that is not a model call: kind is "memory" or "tool",
// name says which. Call the returned func when it is done.
func Step(ctx context.Context, kind, name string) func(err error) {
	sink := sinkOf(ctx)
	if !Enabled() && sink == nil {
		return func(error) {}
	}
	start := time.Now()
	return func(err error) {
		dur := time.Since(start)
		r := Record{Time: start, Kind: kind, Name: name, DurMs: dur.Milliseconds()}
		if err != nil {
			r.Err = trim(err.Error())
		}
		if sink != nil {
			sink.Step(ctx, r)
		}
		emit(ctx, r)
		if s := statsOf(ctx); s != nil {
			switch kind {
			case "memory":
				s.tally.Memory(dur)
			case "tool":
				s.tally.Tool(name, dur, err != nil)
			}
		}
	}
}

// Summary is a run's totals, as a Tally adds them up.
type Summary struct {
	TotalMs            int64 `json:"total_ms"`
	FirstModelCallMs   int64 `json:"first_model_call_ms"`
	FirstAnswerTokenMs int64 `json:"first_answer_token_ms,omitempty"`
	ModelMs            int64 `json:"model_ms"`
	ModelCalls         int   `json:"model_calls"`
	MemoryMs           int64 `json:"memory_ms"`
	ToolMs             int64 `json:"tool_ms"`
	ToolCalls          int   `json:"tool_calls"`
	ToolFailures       int   `json:"tool_failures,omitempty"`
	// Tokens is every model call's tokens added up; ByPurpose splits the
	// calls, their time and their tokens by what each was for.
	Tokens    *Tokens                   `json:"tokens,omitempty"`
	ByPurpose map[string]PurposeSummary `json:"by_purpose,omitempty"`
	// Tools is the tool calls by tool name.
	Tools       map[string]ToolSummary `json:"tools,omitempty"`
	LintRetries int                    `json:"lint_retries,omitempty"`
	LintBlocks  int                    `json:"lint_blocks,omitempty"`
	Compactions int                    `json:"compactions,omitempty"`
}

// Timeline is a run's records so far and its totals; ok is false when the
// context carries no run being recorded.
func Timeline(ctx context.Context) (records []Record, sum Summary, ok bool) {
	s := statsOf(ctx)
	if s == nil {
		return nil, Summary{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records = append([]Record(nil), s.records...)
	return records, s.tally.Summary(), true
}

// End records the run's summary. Model and tool time can overlap, so the
// parts may add up to more than the whole.
func End(ctx context.Context, outcome string) {
	_, sum, ok := Timeline(ctx)
	if !ok {
		return
	}
	emit(ctx, Record{Time: time.Now(), Kind: "run", Name: outcome, DurMs: sum.TotalMs,
		Detail: fmt.Sprintf("first_model_call=%s first_answer_token=%s model=%s/%d memory=%s tool=%s/%d",
			ms(sum.FirstModelCallMs), firstToken(sum.FirstAnswerTokenMs), ms(sum.ModelMs), sum.ModelCalls,
			ms(sum.MemoryMs), ms(sum.ToolMs), sum.ToolCalls)})
}

func firstToken(v int64) string {
	if v <= 0 {
		return "-" // the answer never streamed
	}
	return ms(v)
}

func trim(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
