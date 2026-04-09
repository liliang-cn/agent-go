package agent

import (
	"context"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
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
		toolCallDetected bool
	)

	err := s.llmService.StreamWithTools(ctx, messages, tools, opts, func(delta *domain.GenerationResult) error {
		if delta.ID != "" {
			lastResponseID = delta.ID
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
	if err != nil {
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
	return &domain.GenerationResult{
		ID:        lastResponseID,
		Content:   fullContent.String(),
		ToolCalls: toolCalls,
	}, lastResponseID, recoveryMeta{}, nil
}
