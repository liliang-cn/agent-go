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

func (s *Service) streamToolTurn(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callbacks StreamTurnCallbacks) (*domain.GenerationResult, string, error) {
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
		return nil, lastResponseID, err
	}
	return &domain.GenerationResult{
		ID:        lastResponseID,
		Content:   fullContent.String(),
		ToolCalls: toolCalls,
	}, lastResponseID, nil
}
