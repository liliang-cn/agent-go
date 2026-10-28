package agent

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// logObserver narrates every run into the service's slog logger, at the
// levels pkg/log documents: info for run start and end and one line per model
// and tool call; debug for decisions, retries, wire steps, lints and clipped
// tool arguments; payload for whole requests, responses and tool results.
//
// Every Service has one. It asks its logger before building a line, so at the
// default level (warn) it costs a comparison per callback. Every line is
// logged with the callback's context, and pkg/log's handler adds the
// trace_id, span_id and parent_span_id of the span on it, so a log line and
// the trace line it narrates join on them.
type logObserver struct {
	BaseObserver
	svc *Service
}

func (o *logObserver) logger() *slog.Logger {
	if o.svc != nil && o.svc.runLogger != nil {
		return o.svc.runLogger
	}
	return agentgolog.WithModule("agent.run")
}

func (o *logObserver) on(ctx context.Context, level slog.Level) (*slog.Logger, bool) {
	l := o.logger()
	if ctx == nil {
		ctx = context.Background()
	}
	return l, l.Enabled(ctx, level)
}

// spanAttrs names a child span the line is about, when it is not the span on
// ctx (a model or tool call's own).
func spanAttrs(ctx context.Context, span string) []any {
	cur, ok := trace.SpanFromContext(ctx)
	if !ok || span == "" || span == cur.SpanID {
		return nil
	}
	return []any{slog.String("call_span_id", span)}
}

const logClip = 300

func (o *logObserver) OnRunBegin(ctx context.Context, info RunStartInfo) {
	l, ok := o.on(ctx, slog.LevelInfo)
	if !ok {
		return
	}
	l.InfoContext(ctx, "run start",
		"run_id", info.RunID, "task_id", info.TaskID, "session_id", info.SessionID, "agent", info.AgentName,
		"model", info.Model, "goal", oneLine(info.Goal, logClip))
}

func (o *logObserver) OnRunFinish(ctx context.Context, info RunEndInfo) {
	level := slog.LevelInfo
	if info.Outcome == "failed" {
		level = slog.LevelError
	}
	l, ok := o.on(ctx, level)
	if !ok {
		return
	}
	args := []any{"run_id", info.RunID, "task_id", info.TaskID, "agent", info.AgentName,
		"outcome", info.Outcome, "stop_reason", string(info.StopReason), "duration_ms", info.Duration.Milliseconds(), "rounds", info.Rounds}
	if info.Err != "" {
		args = append(args, "error", oneLine(info.Err, logClip))
	}
	l.Log(ctx, level, "run end", args...)
}

func (o *logObserver) OnModelEnd(ctx context.Context, info ModelInfo, res *ModelResult, err error) {
	if err != nil {
		l, ok := o.on(ctx, slog.LevelWarn)
		if ok {
			l.WarnContext(ctx, "model call failed", append([]any{"purpose", info.Purpose, "model", info.Model,
				"round", info.Round, "run_id", info.RunID, "error", oneLine(err.Error(), logClip)}, spanAttrs(ctx, info.SpanID)...)...)
		}
		return
	}
	l, ok := o.on(ctx, slog.LevelInfo)
	if !ok || res == nil {
		return
	}
	args := []any{"purpose", info.Purpose, "model", firstNonEmptyString(res.Model, info.Model), "round", info.Round, "run_id", info.RunID,
		"duration_ms", res.DurationMs, "streamed", res.Streamed,
		"prompt_tokens", res.PromptTokens, "completion_tokens", res.CompletionTokens,
		"cached_tokens", res.CachedTokens, "cache_write_tokens", res.CacheWriteTokens, "tool_calls", res.ToolCalls}
	if res.Streamed && res.TTFTMs > 0 {
		args = append(args, "ttft_ms", res.TTFTMs)
	}
	if line := serverToolsLine(res.ServerTools); line != "" {
		args = append(args, "server_tools", line)
	}
	args = append(args, spanAttrs(ctx, info.SpanID)...)
	l.InfoContext(ctx, "model call", args...)
	if l.Enabled(ctx, agentgolog.LevelPayload) {
		pargs := []any{"purpose", info.Purpose, "round", info.Round, "content", res.Content}
		if info.Request != nil {
			pargs = append(pargs, "request", info.Request)
		}
		if res.Response != nil {
			pargs = append(pargs, "response", res.Response)
		}
		if res.ServerTools != nil {
			pargs = append(pargs, "server_tool_report", res.ServerTools)
		}
		l.Log(ctx, agentgolog.LevelPayload, "model call payload", append(pargs, spanAttrs(ctx, info.SpanID)...)...)
	}
}

func (o *logObserver) OnToolStart(ctx context.Context, info ToolInfo) {
	l, ok := o.on(ctx, slog.LevelDebug)
	if !ok {
		return
	}
	args := any(clipArgsForLog(info.Args))
	if l.Enabled(ctx, agentgolog.LevelPayload) {
		args = info.Args
	}
	l.DebugContext(ctx, "tool start", append([]any{"tool", info.Tool, "call_id", info.CallID, "run_id", info.RunID, "args", args}, spanAttrs(ctx, info.SpanID)...)...)
}

func (o *logObserver) OnToolEnd(ctx context.Context, info ToolInfo, result any, err error) {
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
	}
	l, ok := o.on(ctx, level)
	if !ok {
		return
	}
	args := []any{"tool", info.Tool, "call_id", info.CallID, "run_id", info.RunID, "ok", err == nil}
	if err != nil {
		args = append(args, "error", oneLine(err.Error(), logClip))
	}
	if len(info.Remote) > 0 {
		args = append(args, "remote", strings.Join(info.Remote, ","))
	}
	switch {
	case l.Enabled(ctx, agentgolog.LevelPayload):
		args = append(args, "result", traceValueText(result))
	case l.Enabled(ctx, slog.LevelDebug):
		args = append(args, "result", oneLine(traceValueText(result), logClip))
	}
	l.Log(ctx, level, "tool call", append(args, spanAttrs(ctx, info.SpanID)...)...)
}

func clipArgsForLog(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			v = oneLine(s, logClip)
		}
		out[k] = v
	}
	return out
}

func (o *logObserver) OnRuntimeDecision(ctx context.Context, info RuntimeDecisionInfo) {
	if l, ok := o.on(ctx, slog.LevelDebug); ok {
		l.DebugContext(ctx, "decision", "what", info.What, "reason", info.Reason, "detail", info.Detail, "round", info.Round, "run_id", info.RunID)
	}
}

func (o *logObserver) OnModelRetry(ctx context.Context, info ModelRetryInfo) {
	if l, ok := o.on(ctx, slog.LevelDebug); ok {
		l.DebugContext(ctx, "model retry", "kind", info.Kind, "attempt", info.Attempt, "reason", oneLine(info.Reason, logClip),
			"delay_ms", info.Delay.Milliseconds(), "max_tokens_from", info.MaxTokensFrom, "max_tokens_to", info.MaxTokensTo,
			"round", info.Round, "run_id", info.RunID, "call_span_id", info.SpanID)
	}
}

func (o *logObserver) OnModelWire(ctx context.Context, info ModelWireInfo) {
	if l, ok := o.on(ctx, slog.LevelDebug); ok {
		e := info.Event
		l.DebugContext(ctx, "model wire", "step", e.Kind, "purpose", info.Purpose, "attempt", e.Attempt, "status", e.Status,
			"bytes", e.Bytes, "elapsed_ms", e.ElapsedMs, "text", e.Text, "run_id", info.RunID, "call_span_id", info.SpanID)
	}
}

func (o *logObserver) OnLint(ctx context.Context, info LintInfo) {
	if l, ok := o.on(ctx, slog.LevelDebug); ok {
		l.DebugContext(ctx, "lint rejected", "lint", info.Lint, "retrying", info.Retrying, "reason", oneLine(info.Reason, logClip),
			"round", info.Round, "run_id", info.RunID)
	}
}

func (o *logObserver) OnCompaction(ctx context.Context, info CompactionInfo) {
	if l, ok := o.on(ctx, slog.LevelDebug); ok {
		l.DebugContext(ctx, "compaction", "trigger", info.Trigger, "mode", info.Mode, "messages_before", info.MessagesBefore,
			"messages_after", info.MessagesAfter, "context_tokens", info.ContextTokens, "context_tokens_after", info.ContextTokensAfter,
			"round", info.Round, "run_id", info.RunID)
	}
}

func (o *logObserver) OnPermissionDecision(ctx context.Context, d PermissionDecisionInfo) {
	if l, ok := o.on(ctx, slog.LevelDebug); ok {
		l.DebugContext(ctx, "permission", "tool", d.Tool, "call_id", d.CallID, "decision", string(d.Decision), "decider", string(d.Decider),
			"reason", oneLine(d.Reason, logClip), "run_id", d.RunID)
	}
}

func (o *logObserver) OnError(ctx context.Context, info ErrorInfo) {
	if l, ok := o.on(ctx, slog.LevelWarn); ok {
		l.WarnContext(ctx, "run error", "marker", info.Marker, "message", oneLine(info.Message, logClip), "round", info.Round,
			"run_id", info.RunID, "task_id", info.TaskID)
	}
}

func (o *logObserver) OnSubAgentStart(ctx context.Context, info SubAgentInfo) {
	if l, ok := o.on(ctx, slog.LevelInfo); ok {
		l.InfoContext(ctx, "sub-agent start", "subagent", info.Name, "subagent_id", info.SubAgentID, "kind", info.Kind,
			"goal", oneLine(info.Goal, logClip), "run_id", info.RunID)
	}
}

func (o *logObserver) OnSubAgentEnd(ctx context.Context, info SubAgentInfo, _ any, err error) {
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
	}
	if l, ok := o.on(ctx, level); ok {
		args := []any{"subagent", info.Name, "subagent_id", info.SubAgentID, "ok", err == nil, "run_id", info.RunID}
		if err != nil {
			args = append(args, "error", oneLine(err.Error(), logClip))
		}
		l.Log(ctx, level, "sub-agent end", args...)
	}
}

func firstNonEmptyString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// envTraceWriter is the TraceWriter AGENTGO_TRACE_EVENTS asked for: one per
// process, shared by every Service, appending to the named file. Payload mode
// follows AGENTGO_TRACE_BODIES, the switch pkg/trace already reads for bodies.
var envTraceWriter = sync.OnceValue(func() *TraceWriter {
	p := strings.TrimSpace(os.Getenv("AGENTGO_TRACE_EVENTS"))
	if p == "" {
		return nil
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		agentgolog.WithModule("agent.trace").Error("cannot open AGENTGO_TRACE_EVENTS", "path", p, "error", err)
		return nil
	}
	tw := NewTraceWriter(f)
	tw.Payloads = traceBodiesFromEnv()
	return tw
})

func traceBodiesFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTGO_TRACE_BODIES"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}
