package agent

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"
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

	// IncludeDeltas turns on one line per streamed fragment. Off by default:
	// a reasoning model emits thousands of them per turn, and a trace that is
	// mostly deltas is a token firehose nothing will read twice.
	IncludeDeltas bool

	// MaxTextLen bounds every free-text field — a tool's result, an error, a
	// lint's reason, a final answer. A file write carries the whole file, and
	// a trace is a record of what happened, not a copy of what moved.
	MaxTextLen int
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
		MaxTextLen: 512,
	}
}

// traceLine is one JSONL record. Every field is omitempty except the two that
// are always meaningful, so a line carries exactly what its event knows and a
// consumer can branch on `event` alone.
type traceLine struct {
	TS    string `json:"ts"`
	Event string `json:"event"`

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
	Model      string       `json:"model,omitempty"`
	Messages   int          `json:"messages,omitempty"`
	Tools      int          `json:"tools,omitempty"`
	ToolCalls  int          `json:"tool_calls,omitempty"`
	TextLen    int          `json:"text_len,omitempty"`
	Tokens     *traceTokens `json:"tokens,omitempty"`
	FinishText string       `json:"text,omitempty"`

	// Tool calls.
	Tool   string         `json:"tool,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	Inner  bool           `json:"inner,omitempty"`
	Result string         `json:"result,omitempty"`

	// Sub-agents.
	SubAgent   string `json:"subagent,omitempty"`
	SubAgentID string `json:"subagent_id,omitempty"`
	Goal       string `json:"goal,omitempty"`

	// Lints, retries, compaction, errors.
	Lint            string `json:"lint,omitempty"`
	Verdict         string `json:"verdict,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Kind            string `json:"kind,omitempty"`
	Attempt         int    `json:"attempt,omitempty"`
	MaxTokensFrom   int    `json:"max_tokens_from,omitempty"`
	MaxTokensTo     int    `json:"max_tokens_to,omitempty"`
	DelayMs         int64  `json:"delay_ms,omitempty"`
	Trigger         string `json:"trigger,omitempty"`
	MessagesBefore  int    `json:"messages_before,omitempty"`
	MessagesAfter   int    `json:"messages_after,omitempty"`
	ContextTokens   int    `json:"context_tokens,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens,omitempty"`
	Marker          string `json:"marker,omitempty"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`

	// Checkpoints and segments.
	CheckpointReason string  `json:"checkpoint_reason,omitempty"`
	SegmentIndex     *int    `json:"segment_index,omitempty"`
	SegmentTotal     int     `json:"segment_total,omitempty"`
	StopReason       string  `json:"stop_reason,omitempty"`
	Productive       *bool   `json:"productive,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	Delta            string  `json:"delta,omitempty"`
}

// traceTokens is the token split for one model turn. Cached is broken out
// because it is billed at a fraction of the rest, so Total alone overstates
// what the turn cost — the same reason ActivityLog prints it.
type traceTokens struct {
	Total      int `json:"total"`
	Prompt     int `json:"prompt,omitempty"`
	Completion int `json:"completion,omitempty"`
	Cached     int `json:"cached,omitempty"`
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
	encoded, err := json.Marshal(line)
	if err != nil {
		return
	}
	encoded = append(encoded, '\n')
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.w.Write(encoded)
}

// clip bounds a free-text field. Unlike ActivityLog's oneLine it leaves
// newlines alone: JSON escapes them itself, and a consumer that wants the text
// wants it as it was.
func (t *TraceWriter) clip(s string) string {
	max := t.MaxTextLen
	if max <= 0 {
		return s
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func (t *TraceWriter) OnModelStart(_ context.Context, info ModelInfo) {
	t.mu.Lock()
	t.modelAt[info.SpanID] = time.Now()
	t.mu.Unlock()
	t.write(traceLine{
		Event:     "model_start",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		Round:     info.Round,
		SpanID:    info.SpanID,
		Model:     info.Model,
		Messages:  info.Messages,
		Tools:     info.Tools,
	}, time.Now())
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

func (t *TraceWriter) OnModelEnd(_ context.Context, info ModelInfo, res *ModelResult, err error) {
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
	}
	if ok {
		line.DurationMs = now.Sub(started).Milliseconds()
	}
	switch {
	case err != nil:
		line.Error = t.clip(err.Error())
	case res != nil:
		line.ToolCalls = res.ToolCalls
		line.TextLen = len(res.Content)
		line.Tokens = &traceTokens{
			Total:      res.TokensUsed,
			Prompt:     res.PromptTokens,
			Completion: res.CompletionTokens,
			Cached:     res.CachedTokens,
		}
	}
	t.write(line, now)
}

func (t *TraceWriter) OnToolStart(_ context.Context, info ToolInfo) {
	t.mu.Lock()
	t.toolAt[info.CallID] = time.Now()
	t.mu.Unlock()
	t.write(traceLine{
		Event:     "tool_start",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		CallID:    info.CallID,
		Tool:      info.Tool,
		Args:      info.Args,
		Inner:     info.Inner,
	}, time.Now())
}

func (t *TraceWriter) OnToolEnd(_ context.Context, info ToolInfo, result any, err error) {
	now := time.Now()
	t.mu.Lock()
	started, ok := t.toolAt[info.CallID]
	delete(t.toolAt, info.CallID)
	t.mu.Unlock()

	line := traceLine{
		Event:     "tool_end",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		CallID:    info.CallID,
		Tool:      info.Tool,
		Inner:     info.Inner,
	}
	if ok {
		line.DurationMs = now.Sub(started).Milliseconds()
	}
	if err != nil {
		line.Error = t.clip(err.Error())
	} else {
		line.Result = t.clip(traceValueText(result))
	}
	t.write(line, now)
}

func (t *TraceWriter) OnSubAgentStart(_ context.Context, info SubAgentInfo) {
	t.write(traceLine{
		Event:        "subagent_start",
		ParentTaskID: info.ParentTaskID,
		RunID:        info.RunID,
		SessionID:    info.SessionID,
		SubAgent:     info.Name,
		SubAgentID:   info.SubAgentID,
		Goal:         t.clip(info.Goal),
	}, time.Now())
}

func (t *TraceWriter) OnSubAgentEnd(_ context.Context, info SubAgentInfo, result any, err error) {
	line := traceLine{
		Event:        "subagent_end",
		ParentTaskID: info.ParentTaskID,
		RunID:        info.RunID,
		SessionID:    info.SessionID,
		SubAgent:     info.Name,
		SubAgentID:   info.SubAgentID,
	}
	if err != nil {
		line.Error = t.clip(err.Error())
	} else {
		line.Result = t.clip(traceValueText(result))
	}
	t.write(line, time.Now())
}

func (t *TraceWriter) OnCheckpoint(_ context.Context, info CheckpointInfo) {
	t.write(traceLine{
		Event:            "checkpoint",
		TaskID:           info.TaskID,
		RunID:            info.RunID,
		SessionID:        info.SessionID,
		Agent:            info.AgentName,
		Round:            info.Round,
		CheckpointReason: info.Reason,
		Messages:         info.Messages,
		TextLen:          len(info.FinalText),
	}, time.Now())
}

func (t *TraceWriter) OnLint(_ context.Context, info LintInfo) {
	verdict := "blocked"
	if info.Retrying {
		verdict = "retry"
	}
	t.write(traceLine{
		Event:     "lint",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		Round:     info.Round,
		Lint:      info.Lint,
		Verdict:   verdict,
		Reason:    t.clip(info.Reason),
	}, time.Now())
}

func (t *TraceWriter) OnModelRetry(_ context.Context, info ModelRetryInfo) {
	t.write(traceLine{
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
	}, time.Now())
}

func (t *TraceWriter) OnCompaction(_ context.Context, info CompactionInfo) {
	t.write(traceLine{
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
	}, time.Now())
}

func (t *TraceWriter) OnError(_ context.Context, info ErrorInfo) {
	marker := info.Marker
	if marker == "" {
		marker = "error"
	}
	t.write(traceLine{
		Event:     "error",
		TaskID:    info.TaskID,
		RunID:     info.RunID,
		SessionID: info.SessionID,
		Agent:     info.AgentName,
		Round:     info.Round,
		Marker:    marker,
		Message:   t.clip(info.Message),
	}, time.Now())
}

func (t *TraceWriter) OnSegment(_ context.Context, info SegmentInfo) {
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
		line.CostUSD = info.CostUSD
		line.Error = t.clip(info.Err)
	}
	t.write(line, time.Now())
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
