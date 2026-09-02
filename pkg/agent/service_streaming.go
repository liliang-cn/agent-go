package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

type StreamTurnCallbacks struct {
	OnReasoning     func(text string)
	OnPartial       func(text string)
	OnFirstToolCall func()
	OnToolCall      func(tc domain.ToolCall) error
}

func (s *Service) streamToolTurn(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callbacks StreamTurnCallbacks) (*domain.GenerationResult, string, recoveryMeta, error) {
	return s.streamToolTurnWithRecovery(ctx, messages, tools, opts, callbacks, 0)
}

// streamToolTurnWithRecovery attempts streaming, and if a withholdable error occurs,
// compacts messages and retries once.
func (s *Service) streamToolTurnWithRecovery(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callbacks StreamTurnCallbacks, attempt int) (*domain.GenerationResult, string, recoveryMeta, error) {
	var (
		fullContent      strings.Builder
		toolCalls        []domain.ToolCall
		lastResponseID   string
		lastFinishReason string
		toolCallDetected bool
		lastUsage        *domain.TokenUsage
		// Non-text output the model produced. Aggregated like everything
		// else, because this function rebuilds the turn's result from the
		// deltas and anything it does not copy is lost — which is what
		// happened to a drawn image: the provider parsed it, this dropped
		// it, and the loop saw an empty answer.
		outputParts []domain.MessagePart
	)

	llmCtx, cancel := withLLMTurnTimeout(ctx, s.cfg)
	defer cancel()
	err := s.llmService.StreamWithTools(llmCtx, sanitizeToolPairing(messages), tools, opts, func(delta *domain.GenerationResult) error {
		if delta.ID != "" {
			lastResponseID = delta.ID
		}
		if delta.FinishReason != "" {
			lastFinishReason = delta.FinishReason
		}
		if delta.Usage != nil {
			lastUsage = delta.Usage
		}
		for _, tc := range delta.ToolCalls {
			if callbacks.OnToolCall != nil {
				if err := callbacks.OnToolCall(tc); err != nil {
					toolCalls = delta.ToolCalls
					return err
				}
			}
		}
		if delta.ReasoningContent != "" && callbacks.OnReasoning != nil {
			callbacks.OnReasoning(delta.ReasoningContent)
		}
		if len(delta.Parts) > 0 {
			outputParts = append(outputParts, delta.Parts...)
		}
		if delta.Content != "" {
			fullContent.WriteString(delta.Content)
			if callbacks.OnPartial != nil {
				callbacks.OnPartial(delta.Content)
			}
		}
		if len(delta.ToolCalls) > 0 {
			if !toolCallDetected {
				toolCallDetected = true
				if callbacks.OnFirstToolCall != nil {
					callbacks.OnFirstToolCall()
				}
			}
			toolCalls = delta.ToolCalls
		}
		return nil
	})
	if err != nil && !errors.Is(err, errTaskTerminal) {
		// Check if error is withholdable and we haven't already retried
		if attempt == 0 && IsWithholdable(err) {
			// Try to compact messages and retry once
			compacted, compErr := s.CompactMessages(ctx, messages)
			if compErr == nil {
				// Retry with compacted messages
				result, responseID, meta, retryErr := s.streamToolTurnWithRecovery(ctx, compacted, tools, opts, callbacks, attempt+1)
				meta.Compacted = true
				if retryErr == nil {
					meta.Recovered = true
				}
				return result, responseID, meta, retryErr
			}
		}
		return nil, lastResponseID, recoveryMeta{}, err
	}
	// The task-terminal sentinel rides back WITH the assembled result rather
	// than instead of it. Aborting the stream on task_complete used to return
	// (nil, errTaskTerminal), which threw away everything the turn had
	// already produced — the streamed content, the tool-call list, and the
	// provider's usage report. The run recovered (the terminal handler works
	// from the callback's captured result), but every observer saw the
	// concluding turn of every task as a nil ModelResult: no tokens, no
	// content, on exactly the turn that carries the answer. A chat that
	// wraps up in one turn measured as zero model turns.
	return &domain.GenerationResult{
		ID:           lastResponseID,
		Content:      fullContent.String(),
		ToolCalls:    toolCalls,
		Parts:        outputParts,
		Usage:        lastUsage,
		FinishReason: lastFinishReason,
	}, lastResponseID, recoveryMeta{}, err
}
