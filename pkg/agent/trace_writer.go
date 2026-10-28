package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// A run's trace, for something other than a person.
//
// ActivityLog (activity_log.go) narrates a run in flat columns because what
// you do with a long run's log is grep and awk it. That is the right shape
// when the reader is a human at a terminal and the wrong one the moment the
// reader is a program: an aggregator, a notebook, a regression harness asking
// "did this build get slower, and where". Those want fields, not columns.
//
// TraceWriter is the same run, same seams, same numbers, emitted as JSONL —
// one JSON object per line. It is deliberately the sibling of ActivityLog and
// not a replacement: they extract the same fields from the same Observer
// callbacks, so a discrepancy between them is a bug in one of them.
//
// Every line carries ts, event, and whichever correlation ids apply — task_id,
// run_id, session_id, agent, round, span_id, call_id. run_id is the one that
// separates two runs sharing a session, which is what makes a trace file
// collected from a busy service separable at all.
//
// And every line carries the flow: trace_id, the W3C trace every run started
// from one request shares — sub-agents, segments, resumes, workers in other
// processes — with span_id the span the line is about and parent_span_id the
// one that started it. run_start / run_end are a run's span; model_* a model
// call's, a child of its run; tool_* a tool call's, also the run's child and
// the parent of whatever the tool started. The other events (lint,
// checkpoint, compaction, error, decision, resource) are about their run and
// carry its span. cmd/agentgo-trace rebuilds the tree from these three ids
// alone, across as many files as the flow wrote.
//
// The whole schema, every event and field, is docs/trace-schema.md; a test
// keeps the two in step.

// TraceWriter writes a JSONL trace of a run to w. Safe for concurrent use: a
// run executes tools in parallel and a service can have several runs in
// flight, and each line is written in a single Write under one mutex.
type TraceWriter struct {
	BaseObserver

	mu      sync.Mutex
	w       io.Writer
	started time.Time
	modelAt map[string]time.Time
	toolAt  map[string]time.Time
	// runs is each open run's tally, by its span id; spanRun maps a tool
	// call's span to the run it belongs to, so a call made inside a tool is
	// added to the right run.
	runs    map[string]*trace.Tally
	spanRun map[string]string
	host    string
	pid     int

	// Payloads writes whole what is otherwise clipped: tool arguments and
	// results, and each server tool use's result URLs. Requests and responses
	// on model_end come with WithObserverPayloads either way, bounded by
	// MaxPayloadLen. Secrets are redacted in both modes.
	Payloads bool

	// IncludeDeltas turns on one line per streamed fragment. Off by default:
	// a reasoning model emits thousands of them per turn, and a trace that is
	// mostly deltas is a token firehose nothing will read twice.
	IncludeDeltas bool

	// MaxTextLen bounds every free-text field — a tool's result, an error, a
	// lint's reason, a final answer. A file write carries the whole file, and
	// a trace is a record of what happened, not a copy of what moved.
	MaxTextLen int

	// MaxPayloadLen bounds each message's content in the request written on a
	// model_end line, when the service was built with WithObserverPayloads.
	// 0, the default, keeps it whole: a service that asked for the request
	// asked to read it back, and MaxTextLen's 512 would leave a prompt
	// unreadable. Set it when the trace is for sizes rather than words.
	MaxPayloadLen int
}

// NewTraceWriter returns an Observer that writes a machine-readable JSONL
// trace of a run to w.
//
//	f, _ := os.Create("run.jsonl")
//	svc, _ := agent.New("worker").WithObserver(agent.NewTraceWriter(f)).Build()
func NewTraceWriter(w io.Writer) *TraceWriter {
	return &TraceWriter{
		w:          w,
		started:    time.Now(),
		modelAt:    map[string]time.Time{},
		toolAt:     map[string]time.Time{},
		runs:       map[string]*trace.Tally{},
		spanRun:    map[string]string{},
		host:       hostname(),
		pid:        os.Getpid(),
		MaxTextLen: 512,
	}
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// traceLine is one JSONL record. Every field is omitempty except the two that
// are always meaningful, so a line carries exactly what its event knows and a
// consumer can branch on `event` alone.
type traceLine struct {
	TS    string `json:"ts"`
	Event string `json:"event"`

	// The flow: W3C trace id, this line's span, the span that started it.
	TraceID      string `json:"trace_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`

	// Correlation.
	TaskID       string `json:"task_id,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Agent        string `json:"agent,omitempty"`
	Round        int    `json:"round,omitempty"`
	SpanID       string `json:"span_id,omitempty"`
	CallID       string `json:"call_id,omitempty"`
	ParentTaskID string `json:"parent_task_id,omitempty"`

	ElapsedMs  int64 `json:"elapsed_ms"`
	DurationMs int64 `json:"duration_ms,omitempty"`

	// Model turns.
	Model string `json:"model,omitempty"`
	// Purpose is what a model call was for: "turn", or one the run made on
	// its own behalf ("constraints", "memory-pick", …). TTFTMs is the wait
	// for its first output, when streamed.
	Purpose string `json:"purpose,omitempty"`
	// Streamed says whether the answer arrived as a stream; ttft_ms is only
	// written when it did.
	Streamed   *bool        `json:"streamed,omitempty"`
	TTFTMs     int64        `json:"ttft_ms,omitempty"`
	Messages   int          `json:"messages,omitempty"`
	Tools      int          `json:"tools,omitempty"`
	ToolCalls  int          `json:"tool_calls,omitempty"`
	TextLen    int          `json:"text_len,omitempty"`
	Tokens     *traceTokens `json:"tokens,omitempty"`
	FinishText string       `json:"text,omitempty"`
	// Request is what the model was sent, on model_end, only with
	// WithObserverPayloads. It sits on model_end rather than model_start so
	// the one line holds both sides of the exchange: request here, reply in
	// text_len / tool_calls / tokens beside it.
	Request *traceRequest `json:"request,omitempty"`
	// Response is what the model answered, on model_end, only with
	// WithObserverPayloads: its text, its reasoning, the tool calls it asked
	// for and why it stopped. text_len and tool_calls stay the sizes.
	Response *traceResponse `json:"response,omitempty"`
	// ServerTools is the provider's own work inside the call: each server
	// tool use (name, input, result count or error code), pause_turn
	// resumes, each HTTP request's duration, citations.
	ServerTools *traceServerTools `json:"server_tools,omitempty"`

	// Tool calls.
	Tool   string         `json:"tool,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	Inner  bool           `json:"inner,omitempty"`
	Result string         `json:"result,omitempty"`
	// Remote is the traceparents another process answered a tool call with:
	// its own span for the work, where the flow continues.
	Remote []string `json:"remote,omitempty"`

	// Permission decisions, one line per tool call that reached the gate.
	Decision    string         `json:"decision,omitempty"`
	Decider     string         `json:"decider,omitempty"`
	DecidedBy   string         `json:"decided_by,omitempty"`
	Required    bool           `json:"required,omitempty"`
	Destructive bool           `json:"destructive,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`

	// Runs: where it ran, how it ended, and the run's totals.
	Host    string         `json:"host,omitempty"`
	PID     int            `json:"pid,omitempty"`
	Outcome string         `json:"outcome,omitempty"`
	Rounds  int            `json:"rounds,omitempty"`
	Summary *trace.Summary `json:"summary,omitempty"`

	// Runtime decisions: what was chosen, why, and about what.
	What   string         `json:"what,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`

	// Sub-agents.
	SubAgent   string `json:"subagent,omitempty"`
	SubAgentID string `json:"subagent_id,omitempty"`
	Goal       string `json:"goal,omitempty"`

	// Lints, retries, compaction, errors.
	Lint          string `json:"lint,omitempty"`
	Verdict       string `json:"verdict,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
	MaxTokensFrom int    `json:"max_tokens_from,omitempty"`
	MaxTokensTo   int    `json:"max_tokens_to,omitempty"`
	DelayMs       int64  `json:"delay_ms,omitempty"`
	// Status and Bytes are a model_wire step's HTTP status and how much of
	// the answer had come.
	Status          int    `json:"status,omitempty"`
	Bytes           int64  `json:"bytes,omitempty"`
	Trigger         string `json:"trigger,omitempty"`
	MessagesBefore  int    `json:"messages_before,omitempty"`
	MessagesAfter   int    `json:"messages_after,omitempty"`
	ContextTokens   int    `json:"context_tokens,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens,omitempty"`
	ContextAfter    int    `json:"context_tokens_after,omitempty"`
	Threshold       int    `json:"threshold,omitempty"`
	ThresholdSource string `json:"threshold_source,omitempty"`
	Mode            string `json:"mode,omitempty"`
	Clipped         int    `json:"clipped_results,omitempty"`
	Trimmed         int    `json:"trimmed_results,omitempty"`
	NoProgress      bool   `json:"no_progress,omitempty"`
	SummaryCalls    int    `json:"summary_calls,omitempty"`
	Degraded        bool   `json:"degraded,omitempty"`
	Marker          string `json:"marker,omitempty"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`

	// Tool output cut for the model (event "tool_output_truncated").
	OriginalBytes int   `json:"original_bytes,omitempty"`
	KeptBytes     int   `json:"kept_bytes,omitempty"`
	Limit         int   `json:"limit,omitempty"`
	StructureKept *bool `json:"structure_kept,omitempty"`

	// Checkpoints and segments.
	CheckpointReason string `json:"checkpoint_reason,omitempty"`
	SegmentIndex     *int   `json:"segment_index,omitempty"`
	SegmentTotal     int    `json:"segment_total,omitempty"`
	StopReason       string `json:"stop_reason,omitempty"`
	Productive       *bool  `json:"productive,omitempty"`
	Delta            string `json:"delta,omitempty"`

	// Process resources, one line per round. A long task's trace is where
	// its memory curve lives — nothing else records it, and by the time a
	// process is killed the curve is the only evidence left.
	Res   *traceResources `json:"res,omitempty"`
	Final bool            `json:"final,omitempty"`
}

// traceResources is what the process looked like at a round boundary.
type traceResources struct {
	HeapBytes    uint64  `json:"heap_bytes"`
	HeapObjects  uint64  `json:"heap_objects"`
	StackBytes   uint64  `json:"stack_bytes"`
	Goroutines   int     `json:"goroutines"`
	RSSBytes     uint64  `json:"rss_bytes,omitempty"`
	PeakRSSBytes uint64  `json:"peak_rss_bytes,omitempty"`
	CPUSeconds   float64 `json:"cpu_seconds,omitempty"`
	NumGC        uint32  `json:"num_gc,omitempty"`
	UptimeMs     int64   `json:"uptime_ms,omitempty"`
}

// traceTokens is the token split for one model turn. Cached is broken out
// because it is the number that says whether the prompt cache is working —
// the same reason ActivityLog prints it.
//
// CacheWrite is the prompt tokens written to the cache on this call. Cached
// (reads) without writes means the cache works; writes every round without
// reads means it is paid for and never used.
type traceTokens struct {
	Total      int `json:"total"`
	Prompt     int `json:"prompt,omitempty"`
	Completion int `json:"completion,omitempty"`
	Cached     int `json:"cached,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
}

// traceServerTools is ModelResult.ServerTools as written to the trace.
type traceServerTools struct {
	Uses      []traceServerToolUse `json:"uses,omitempty"`
	Resumes   int                  `json:"resumes,omitempty"`
	RequestMs []int64              `json:"request_ms,omitempty"`
	Citations int                  `json:"citations,omitempty"`
}

type traceServerToolUse struct {
	Name      string         `json:"name"`
	Input     map[string]any `json:"input,omitempty"`
	Results   int            `json:"results"`
	ErrorCode string         `json:"error_code,omitempty"`
	URLs      []string       `json:"urls,omitempty"`
}

func (t *TraceWriter) serverToolsLine(r *domain.ServerToolReport) *traceServerTools {
	if r == nil {
		return nil
	}
	out := &traceServerTools{Resumes: r.Resumes, RequestMs: r.RequestMs, Citations: r.Citations}
	for _, u := range r.Uses {
		use := traceServerToolUse{Name: u.Name, Input: agentgolog.RedactMap(u.Input), Results: u.Results, ErrorCode: u.ErrorCode}
		if t.Payloads {
			use.URLs = u.URLs
		}
		out.Uses = append(out.Uses, use)
	}
	return out
}

// spans fills a line's flow ids: span is the span the line is about, or
// empty to use the context's own; the parent is then the context's span.
func (t *TraceWriter) spans(line *traceLine, ctx context.Context, span string) {
	cur, ok := trace.SpanFromContext(ctx)
	if !ok {
		line.SpanID = span
		return
	}
	line.TraceID = cur.TraceID
	if span == "" || span == cur.SpanID {
		line.SpanID, line.ParentSpanID = cur.SpanID, cur.ParentSpanID
		return
	}
	line.SpanID, line.ParentSpanID = span, cur.SpanID
}

// tallyFor is the tally of the run a callback on ctx belongs to: the run
// whose span is on ctx, or the run of the tool call whose span is.
func (t *TraceWriter) tallyFor(ctx context.Context) *trace.Tally {
	cur, ok := trace.SpanFromContext(ctx)
	if !ok {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if tl := t.runs[cur.SpanID]; tl != nil {
		return tl
	}
	if run, ok := t.spanRun[cur.SpanID]; ok {
		return t.runs[run]
	}
	return nil
}

// write emits one line. Marshalling first and writing once is what makes a
// line atomic against a concurrent tool round: a half-written object is not
// recoverable by any consumer.
func (t *TraceWriter) write(line traceLine, now time.Time) {
	if t == nil || t.w == nil {
		return
	}
	line.TS = now.Format(time.RFC3339Nano)
	line.ElapsedMs = now.Sub(t.started).Milliseconds()
	line.Args = agentgolog.RedactMap(line.Args)
	line.Metadata = agentgolog.RedactMap(line.Metadata)
	encoded, err := json.Marshal(line)
	if err != nil {
		return
	}
	encoded = append(encoded, '\n')
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.w.Write(encoded)
}

// emit fills the line's flow ids from ctx (see spans) and writes it.
func (t *TraceWriter) emit(ctx context.Context, span string, line traceLine) {
	t.spans(&line, ctx, span)
	t.write(line, time.Now())
}

// clip bounds a free-text field. Unlike ActivityLog's oneLine it leaves
// newlines alone: JSON escapes them itself, and a consumer that wants the text
// wants it as it was.
func (t *TraceWriter) clip(s string) string {
	s = agentgolog.Redact(s)
	max := t.MaxTextLen
	if max <= 0 {
		return s
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func (t *TraceWriter) OnModelStart(ctx context.Context, info ModelInfo) {
	t.mu.Lock()
	t.modelAt[info.SpanID] = time.Now()
	t.mu.Unlock()
	line := traceLine{
		Event:     "model_start",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		Round:     info.Round,
		SpanID:    info.SpanID,
		Model:     info.Model,
		Purpose:   info.Purpose,
		Messages:  info.Messages,
		Tools:     info.Tools,
	}
	t.spans(&line, ctx, info.SpanID)
	t.write(line, time.Now())
}

func (t *TraceWriter) OnModelDelta(_ context.Context, delta ModelDelta) {
	if !t.IncludeDeltas {
		return
	}
	t.write(traceLine{
		Event:  "model_delta",
		SpanID: delta.SpanID,
		Kind:   delta.Kind,
		Delta:  t.clip(delta.Text),
	}, time.Now())
}

// traceRequest is ModelRequest as written to the trace: messages with their
// tool calls, tool names rather than schemas (a schema is the same bytes on
// every round), or the prompt of a structured call.
type traceRequest struct {
	Prompt   string         `json:"prompt,omitempty"`
	Messages []traceMessage `json:"messages,omitempty"`
	Tools    []string       `json:"tools,omitempty"`
}

type traceMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []traceToolCall `json:"tool_calls,omitempty"`
}

type traceToolCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// traceResponse is ModelResult.Content and Response as written to the trace.
type traceResponse struct {
	Content   string          `json:"content,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"`
	ToolCalls []traceToolCall `json:"tool_calls,omitempty"`
	Finish    string          `json:"finish_reason,omitempty"`
}

// responseLine is the model's answer for the trace, when the service carries
// payloads: a request on the info or a response on the result says it does.
// A call made on the run's behalf has no Response but its text is the reply.
func (t *TraceWriter) responseLine(info ModelInfo, res *ModelResult) *traceResponse {
	if res == nil || (info.Request == nil && res.Response == nil) {
		return nil
	}
	out := &traceResponse{Content: t.clipPayload(res.Content)}
	if r := res.Response; r != nil {
		out.Reasoning = t.clipPayload(r.Reasoning)
		out.Finish = r.FinishReason
		for _, tc := range r.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, traceToolCall{ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments})
		}
	}
	if out.Content == "" && out.Reasoning == "" && out.Finish == "" && len(out.ToolCalls) == 0 {
		return nil
	}
	return out
}

// clipPayload bounds a payload's text by MaxPayloadLen; 0 keeps it whole.
// Secrets are redacted either way.
func (t *TraceWriter) clipPayload(s string) string {
	s = agentgolog.Redact(s)
	if t.MaxPayloadLen > 0 && len(s) > t.MaxPayloadLen {
		return s[:t.MaxPayloadLen] + "…"
	}
	return s
}

func (t *TraceWriter) requestLine(req *ModelRequest) *traceRequest {
	if req == nil {
		return nil
	}
	clip := t.clipPayload
	out := &traceRequest{Prompt: clip(req.Prompt)}
	for _, m := range req.Messages {
		tm := traceMessage{Role: m.Role, Content: clip(m.Content), ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			tm.ToolCalls = append(tm.ToolCalls, traceToolCall{ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments})
		}
		out.Messages = append(out.Messages, tm)
	}
	for _, tool := range req.Tools {
		out.Tools = append(out.Tools, tool.Function.Name)
	}
	return out
}

func (t *TraceWriter) OnModelEnd(ctx context.Context, info ModelInfo, res *ModelResult, err error) {
	now := time.Now()
	t.mu.Lock()
	started, ok := t.modelAt[info.SpanID]
	delete(t.modelAt, info.SpanID)
	t.mu.Unlock()

	line := traceLine{
		Event:     "model_end",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		Round:     info.Round,
		SpanID:    info.SpanID,
		Model:     info.Model,
		Purpose:   info.Purpose,
		Request:   t.requestLine(info.Request),
	}
	t.spans(&line, ctx, info.SpanID)
	if ok {
		line.DurationMs = now.Sub(started).Milliseconds()
	} else if res != nil {
		line.DurationMs = res.DurationMs
	}
	switch {
	case err != nil:
		line.Error = t.clip(err.Error())
	case res != nil:
		line.ToolCalls = res.ToolCalls
		line.TextLen = len(res.Content)
		line.Response = t.responseLine(info, res)
		streamed := res.Streamed
		line.Streamed = &streamed
		if streamed {
			line.TTFTMs = res.TTFTMs
		}
		line.Tokens = &traceTokens{
			Total:      res.TokensUsed,
			Prompt:     res.PromptTokens,
			Completion: res.CompletionTokens,
			Cached:     res.CachedTokens,
			CacheWrite: res.CacheWriteTokens,
		}
		line.ServerTools = t.serverToolsLine(res.ServerTools)
	}
	if tl := t.tallyFor(ctx); tl != nil {
		var tok trace.Tokens
		if res != nil {
			tok = trace.Tokens{Prompt: res.PromptTokens, Completion: res.CompletionTokens, Cached: res.CachedTokens, CacheWrite: res.CacheWriteTokens}
		}
		var ttft time.Duration
		if line.TTFTMs > 0 {
			ttft = time.Duration(line.TTFTMs) * time.Millisecond
		}
		dur := time.Duration(line.DurationMs) * time.Millisecond
		tl.Model(info.Purpose, now.Add(-dur), dur, ttft, tok)
	}
	t.write(line, now)
}

// OnStep writes a memory lookup or another timed step (StepObserver).
func (t *TraceWriter) OnStep(ctx context.Context, info StepInfo) {
	if info.Kind == "memory" {
		if tl := t.tallyFor(ctx); tl != nil {
			tl.Memory(time.Duration(info.DurationMs) * time.Millisecond)
		}
	}
	line := traceLine{
		Event:      "step",
		TaskID:     info.TaskID,
		RunID:      info.RunID,
		SessionID:  info.SessionID,
		Kind:       info.Kind + "." + info.Name,
		DurationMs: info.DurationMs,
		Error:      t.clip(info.Err),
	}
	t.spans(&line, ctx, "")
	t.write(line, time.Now())
}

func (t *TraceWriter) OnToolStart(ctx context.Context, info ToolInfo) {
	t.mu.Lock()
	t.toolAt[info.CallID] = time.Now()
	if cur, ok := trace.SpanFromContext(ctx); ok && info.SpanID != "" {
		run := cur.SpanID
		if r, ok := t.spanRun[run]; ok {
			run = r
		}
		t.spanRun[info.SpanID] = run
	}
	t.mu.Unlock()
	line := traceLine{
		Event:     "tool_start",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		CallID:    info.CallID,
		Tool:      info.Tool,
		Args:      t.clipArgs(info.Args),
		Inner:     info.Inner,
	}
	t.spans(&line, ctx, info.SpanID)
	t.write(line, time.Now())
}

// clipArgs bounds each string argument by MaxTextLen, unless Payloads.
func (t *TraceWriter) clipArgs(args map[string]any) map[string]any {
	if t.Payloads || t.MaxTextLen <= 0 || args == nil {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if s, ok := v.(string); ok && len(s) > t.MaxTextLen {
			v = s[:t.MaxTextLen] + "…"
		}
		out[k] = v
	}
	return out
}

func (t *TraceWriter) OnToolEnd(ctx context.Context, info ToolInfo, result any, err error) {
	now := time.Now()
	t.mu.Lock()
	started, ok := t.toolAt[info.CallID]
	delete(t.toolAt, info.CallID)
	t.mu.Unlock()
	text := t.clip
	if t.Payloads {
		text = agentgolog.Redact
	}

	line := traceLine{
		Event:     "tool_end",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		CallID:    info.CallID,
		Tool:      info.Tool,
		Inner:     info.Inner,
		Remote:    info.Remote,
	}
	t.spans(&line, ctx, info.SpanID)
	if ok {
		line.DurationMs = now.Sub(started).Milliseconds()
	}
	if err != nil {
		line.Error = text(err.Error())
	} else {
		line.Result = text(traceValueText(result))
	}
	if tl := t.tallyFor(ctx); tl != nil {
		tl.Tool(info.Tool, time.Duration(line.DurationMs)*time.Millisecond, err != nil)
	}
	t.mu.Lock()
	delete(t.spanRun, info.SpanID)
	t.mu.Unlock()
	t.write(line, now)
}

// OnRunBegin writes run_start: the run's span, where it ran, and what it was
// asked (RunObserver).
func (t *TraceWriter) OnRunBegin(_ context.Context, info RunStartInfo) {
	now := time.Now()
	if info.SpanID != "" {
		t.mu.Lock()
		t.runs[info.SpanID] = trace.NewTally(now)
		t.mu.Unlock()
	}
	t.write(traceLine{
		Event:        "run_start",
		TraceID:      info.TraceID,
		SpanID:       info.SpanID,
		ParentSpanID: info.ParentSpanID,
		TaskID:       info.TaskID,
		RunID:        info.RunID,
		SessionID:    info.SessionID,
		Agent:        info.AgentName,
		Model:        info.Model,
		Goal:         t.clip(info.Goal),
		Host:         t.host,
		PID:          t.pid,
	}, now)
}

// OnRunFinish writes run_end: how the run ended and its totals — wall, model
// and tool time, calls and tokens by purpose, tools by name with failures,
// lint retries and compactions — added up by the same trace.Tally the run
// timeline uses (RunObserver).
func (t *TraceWriter) OnRunFinish(_ context.Context, info RunEndInfo) {
	now := time.Now()
	t.mu.Lock()
	tl := t.runs[info.SpanID]
	delete(t.runs, info.SpanID)
	t.mu.Unlock()
	line := traceLine{
		Event:        "run_end",
		TraceID:      info.TraceID,
		SpanID:       info.SpanID,
		ParentSpanID: info.ParentSpanID,
		TaskID:       info.TaskID,
		RunID:        info.RunID,
		SessionID:    info.SessionID,
		Agent:        info.AgentName,
		Outcome:      info.Outcome,
		StopReason:   string(info.StopReason),
		DurationMs:   info.Duration.Milliseconds(),
		Rounds:       info.Rounds,
		Error:        t.clip(info.Err),
		Host:         t.host,
		PID:          t.pid,
	}
	if tl != nil {
		sum := tl.SummaryAt(now)
		if info.Duration > 0 {
			sum.TotalMs = info.Duration.Milliseconds()
		}
		line.Summary = &sum
	}
	t.write(line, now)
}

// OnRuntimeDecision writes a decision line (RuntimeDecisionObserver).
func (t *TraceWriter) OnRuntimeDecision(ctx context.Context, info RuntimeDecisionInfo) {
	line := traceLine{
		Event:     "decision",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		Round:     info.Round,
		What:      info.What,
		Reason:    t.clip(info.Reason),
		Detail:    agentgolog.RedactMap(info.Detail),
	}
	t.spans(&line, ctx, "")
	t.write(line, time.Now())
}

// OnPermissionDecision records one permission decision. Every call that
// reaches the gate gets a line, the ungated ones included, so "every
// destructive call has a record" is checkable by counting — a missing line
// is a defect, never a way of saying "no approval was needed".
func (t *TraceWriter) OnPermissionDecision(ctx context.Context, d PermissionDecisionInfo) {
	line := traceLine{
		Event:       "permission",
		TaskID:      d.TaskID,
		RunID:       d.RunID,
		SessionID:   d.SessionID,
		Agent:       d.AgentName,
		CallID:      d.CallID,
		Tool:        d.Tool,
		Args:        d.Args,
		Decision:    string(d.Decision),
		Decider:     string(d.Decider),
		DecidedBy:   d.DecidedBy,
		Required:    d.Required,
		Destructive: d.Destructive,
		Reason:      t.clip(d.Reason),
		Error:       t.clip(d.Error),
		Metadata:    d.Metadata,
		DurationMs:  d.Duration.Milliseconds(),
	}
	t.spans(&line, ctx, "")
	t.write(line, time.Now())
}

func (t *TraceWriter) OnSubAgentStart(ctx context.Context, info SubAgentInfo) {
	line := traceLine{
		Event:        "subagent_start",
		ParentTaskID: info.ParentTaskID,
		RunID:        info.RunID,
		SessionID:    info.SessionID,
		SubAgent:     info.Name,
		SubAgentID:   info.SubAgentID,
		Goal:         t.clip(info.Goal),
	}
	t.spans(&line, ctx, "")
	t.write(line, time.Now())
}

func (t *TraceWriter) OnSubAgentEnd(ctx context.Context, info SubAgentInfo, result any, err error) {
	line := traceLine{
		Event:        "subagent_end",
		ParentTaskID: info.ParentTaskID,
		RunID:        info.RunID,
		SessionID:    info.SessionID,
		SubAgent:     info.Name,
		SubAgentID:   info.SubAgentID,
	}
	t.spans(&line, ctx, "")
	if err != nil {
		line.Error = t.clip(err.Error())
	} else {
		line.Result = t.clip(traceValueText(result))
	}
	t.write(line, time.Now())
}

func (t *TraceWriter) OnCheckpoint(ctx context.Context, info CheckpointInfo) {
	t.emit(ctx, "", traceLine{
		Event:            "checkpoint",
		TaskID:           info.TaskID,
		RunID:            info.RunID,
		SessionID:        info.SessionID,
		Agent:            info.AgentName,
		Round:            info.Round,
		CheckpointReason: info.Reason,
		Messages:         info.Messages,
		TextLen:          len(info.FinalText),
	})
}

// OnResourceSample records one process reading per round.
func (t *TraceWriter) OnResourceSample(ctx context.Context, s ResourceSample) {
	t.emit(ctx, "", traceLine{
		Event:     "resource",
		TaskID:    s.TaskID,
		RunID:     s.RunID,
		SessionID: s.SessionID,
		Agent:     s.AgentName,
		Round:     s.Round,
		Final:     s.Final,
		Res: &traceResources{
			HeapBytes:    s.Stats.HeapAllocBytes,
			HeapObjects:  s.Stats.HeapObjects,
			StackBytes:   s.Stats.StackInuseBytes,
			Goroutines:   s.Stats.Goroutines,
			RSSBytes:     s.Stats.RSSBytes,
			PeakRSSBytes: s.Stats.PeakRSSBytes,
			CPUSeconds:   s.Stats.CPUSeconds(),
			NumGC:        s.Stats.NumGC,
			UptimeMs:     s.Stats.Uptime.Milliseconds(),
		},
	})
}

func (t *TraceWriter) OnLint(ctx context.Context, info LintInfo) {
	verdict := "blocked"
	if info.Retrying {
		verdict = "retry"
	}
	if tl := t.tallyFor(ctx); tl != nil {
		tl.Lint(info.Retrying)
	}
	t.emit(ctx, "", traceLine{
		Event:     "lint",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		Round:     info.Round,
		Lint:      info.Lint,
		Verdict:   verdict,
		Reason:    t.clip(info.Reason),
	})
}

func (t *TraceWriter) OnModelRetry(ctx context.Context, info ModelRetryInfo) {
	t.emit(ctx, info.SpanID, traceLine{
		Event:         "model_retry",
		TaskID:        info.TaskID,
		RunID:         info.RunID,
		SessionID:     info.SessionID,
		Agent:         info.AgentName,
		Round:         info.Round,
		SpanID:        info.SpanID,
		Kind:          info.Kind,
		Attempt:       info.Attempt,
		Reason:        t.clip(info.Reason),
		MaxTokensFrom: info.MaxTokensFrom,
		MaxTokensTo:   info.MaxTokensTo,
		DelayMs:       info.Delay.Milliseconds(),
	})
}

// OnModelWire writes a model call's progress on the wire: model_wire lines
// with the step (queued, request, response, retry, first_byte, waiting …),
// its attempt and HTTP status, and the line a person reads.
func (t *TraceWriter) OnModelWire(ctx context.Context, info ModelWireInfo) {
	e := info.Event
	t.emit(ctx, info.SpanID, traceLine{
		Event:      "model_wire",
		TaskID:     info.TaskID,
		RunID:      info.RunID,
		SessionID:  info.SessionID,
		Agent:      info.AgentName,
		Round:      info.Round,
		SpanID:     info.SpanID,
		Purpose:    info.Purpose,
		Kind:       e.Kind,
		Attempt:    e.Attempt,
		Status:     e.Status,
		DurationMs: e.ElapsedMs,
		DelayMs:    e.WaitMs,
		Bytes:      e.Bytes,
		Reason:     t.clip(e.Text),
	})
}

func (t *TraceWriter) OnCompaction(ctx context.Context, info CompactionInfo) {
	if tl := t.tallyFor(ctx); tl != nil {
		tl.Compaction()
	}
	t.emit(ctx, "", traceLine{
		Event:           "compaction",
		TaskID:          info.TaskID,
		RunID:           info.RunID,
		SessionID:       info.SessionID,
		Agent:           info.AgentName,
		Round:           info.Round,
		Trigger:         info.Trigger,
		MessagesBefore:  info.MessagesBefore,
		MessagesAfter:   info.MessagesAfter,
		ContextTokens:   info.ContextTokens,
		EstimatedTokens: info.EstimatedTokens,
		ContextAfter:    info.ContextTokensAfter,
		Threshold:       info.Threshold,
		ThresholdSource: info.ThresholdSource,
		Mode:            info.Mode,
		Clipped:         info.ClippedResults,
		Trimmed:         info.TrimmedResults,
		NoProgress:      info.NoProgress,
		SummaryCalls:    info.SummaryCalls,
		Degraded:        info.Degraded,
		Error:           info.SummaryError,
	})
}

func (t *TraceWriter) OnError(ctx context.Context, info ErrorInfo) {
	marker := info.Marker
	if marker == "" {
		marker = "error"
	}
	t.emit(ctx, "", traceLine{
		Event:     "error",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		Round:     info.Round,
		Marker:    marker,
		Message:   t.clip(info.Message),
	})
}

func (t *TraceWriter) OnSegment(ctx context.Context, info SegmentInfo) {
	index := info.Index
	line := traceLine{
		Event:        "segment_start",
		TaskID:       info.TaskID,
		SessionID:    info.SessionID,
		SegmentIndex: &index,
		SegmentTotal: info.Total,
	}
	if info.Ending {
		productive := info.Productive
		line.Event = "segment_end"
		line.StopReason = string(info.StopReason)
		line.DurationMs = info.Duration.Milliseconds()
		line.Productive = &productive
		line.Error = t.clip(info.Err)
	}
	t.emit(ctx, "", line)
}

// traceValueText renders a tool or sub-agent result for the trace. A string
// goes in as itself; anything else is JSON-encoded, and whatever cannot be
// encoded falls back to nothing rather than to a Go %v dump nobody can parse.
func traceValueText(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case error:
		return value.Error()
	}
	if encoded, err := json.Marshal(v); err == nil {
		return string(encoded)
	}
	return ""
}

// OnToolOutputTruncated records a tool result the output cap cut before the
// model saw it. Without this line a run that re-reads the same file page by
// page looks redundant; with it, the reason is on the record.
func (t *TraceWriter) OnToolOutputTruncated(ctx context.Context, info ToolOutputTruncation) {
	kept := info.StructureKept
	t.emit(ctx, "", traceLine{
		Event:         "tool_output_truncated",
		TaskID:        info.TaskID,
		RunID:         info.RunID,
		SessionID:     info.SessionID,
		Agent:         info.AgentName,
		Round:         info.Round,
		CallID:        info.ToolCallID,
		Tool:          info.ToolName,
		OriginalBytes: info.OriginalBytes,
		KeptBytes:     info.KeptBytes,
		Limit:         info.Limit,
		StructureKept: &kept,
	})
}
