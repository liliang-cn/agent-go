package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// runtimeSink hands the observers the model calls a run makes on its own
// behalf — constraint extraction, memory picks, compaction, summaries — and
// its memory steps. The answering turn and the final synthesis are reported
// where they are made, with their message and tool counts, so they are not
// reported twice.
type runtimeSink struct{ r *Runtime }

func (s runtimeSink) info(id, purpose string) ModelInfo {
	r := s.r
	return ModelInfo{
		TaskID:    currentTaskID(r.session),
		RunID:     r.runID(),
		SessionID: r.sessionID(),
		AgentName: r.currentAgentName(),
		Round:     int(r.currentRound.Load()),
		SpanID:    id,
		Model:     r.svc.Info().Model,
		Purpose:   purpose,
	}
}

func reportedElsewhere(purpose string) bool {
	return purpose == "turn" || purpose == "final-synthesis"
}

// WantBodies asks pkg/trace to keep each call's request and reply even when
// AGENTGO_TRACE_BODIES is off, so WithObserverPayloads reaches the calls the
// runtime does not make itself. Without this, the constraint and memory calls
// were the one kind of model call whose input no observer could ever see.
func (s runtimeSink) WantBodies() bool { return s.r.svc.observerPayloads }

// modelRequest is the ModelInfo.Request for a call the runtime makes itself:
// nil unless WithObserverPayloads.
func (s *Service) modelRequest(messages []domain.Message, tools []domain.ToolDefinition) *ModelRequest {
	if s == nil || !s.observerPayloads {
		return nil
	}
	return &ModelRequest{Messages: messages, Tools: tools}
}

// modelResponse is the ModelResult.Response for a call the runtime makes
// itself: nil unless WithObserverPayloads.
func (s *Service) modelResponse(res *domain.GenerationResult) *ModelResponse {
	if s == nil || !s.observerPayloads || res == nil {
		return nil
	}
	return &ModelResponse{Reasoning: res.ReasoningContent, ToolCalls: res.ToolCalls, FinishReason: res.FinishReason}
}

// requestFromRecord turns what pkg/trace kept of a call's request into a
// ModelRequest: a structured call hands it a prompt string, a chat call the
// message list.
func requestFromRecord(v any) *ModelRequest {
	switch req := v.(type) {
	case nil:
		return nil
	case string:
		return &ModelRequest{Prompt: req}
	case []domain.Message:
		return &ModelRequest{Messages: req}
	default:
		return &ModelRequest{Prompt: fmt.Sprint(req)}
	}
}

func (s runtimeSink) ModelStart(ctx context.Context, id, purpose string) {
	if reportedElsewhere(purpose) {
		return
	}
	info := s.info(id, purpose)
	// The end repeats what the start said: a constraint extraction begun
	// before round 1 and finished during it is a round-0 call on both lines.
	if s.r.span != nil {
		s.r.span.aux.Store(id, info)
	}
	s.r.svc.emitObserver(func(o Observer) { o.OnModelStart(ctx, info) })
}

func (s runtimeSink) ModelEnd(ctx context.Context, id string, rec trace.Record) {
	if reportedElsewhere(rec.Name) {
		return
	}
	info := s.info(id, rec.Name)
	if s.r.span != nil {
		if started, ok := s.r.span.aux.LoadAndDelete(id); ok {
			info = started.(ModelInfo)
		}
	}
	if s.r.svc.observerPayloads {
		info.Request = requestFromRecord(rec.Request)
	}
	res := &ModelResult{
		DurationMs: rec.DurMs, TTFTMs: rec.TTFTMs, Streamed: rec.Streamed,
		PromptTokens: rec.In, CompletionTokens: rec.Out, TokensUsed: rec.In + rec.Out, CachedTokens: rec.Cached,
		CacheWriteTokens: rec.CacheWrite,
	}
	if reply, ok := rec.Reply.(string); ok {
		res.Content = reply
	}
	var err error
	if rec.Err != "" {
		err = errors.New(rec.Err)
	}
	s.r.svc.emitObserver(func(o Observer) { o.OnModelEnd(ctx, info, res, err) })
}

func (s runtimeSink) Step(ctx context.Context, rec trace.Record) {
	if rec.Kind == "tool" {
		return // OnToolStart/OnToolEnd already carry tool calls
	}
	r := s.r
	info := StepInfo{TaskID: currentTaskID(r.session), RunID: r.runID(), SessionID: r.sessionID(),
		Kind: rec.Kind, Name: rec.Name, DurationMs: rec.DurMs, Err: rec.Err}
	r.svc.emitObserver(func(o Observer) {
		if so, ok := o.(StepObserver); ok {
			so.OnStep(ctx, info)
		}
	})
}
