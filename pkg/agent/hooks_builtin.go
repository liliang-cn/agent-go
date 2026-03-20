package agent

import (
	"context"
	"log"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// RegisterAutoMemoryHook registers a hook that automatically saves important
// information to long-term memory after an agent execution.
func (s *Service) RegisterAutoMemoryHook() string {
	return s.hooks.Register(
		HookEventPostExecution,
		func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
			if s.memoryService == nil {
				return nil, nil
			}

			// 1. Analyze the goal and result for "memorability"
			// We look for facts, preferences, or specific user intents
			goalLower := strings.ToLower(data.Goal)

			isImportant := false
			memType := domain.MemoryTypeFact
			content := ""

			// Pattern matching for facts or preferences
			if strings.Contains(goalLower, "my favorite") || strings.Contains(goalLower, "i like") {
				isImportant = true
				memType = domain.MemoryTypePreference
				content = data.Goal
			} else if strings.Contains(goalLower, "remember that") || strings.Contains(goalLower, "keep in mind") {
				isImportant = true
				memType = domain.MemoryTypeFact
				content = strings.TrimPrefix(strings.TrimPrefix(goalLower, "remember that "), "keep in mind ")
			}

			// 2. If it seems important, save it
			if isImportant && content != "" {
				err := s.memoryService.Add(ctx, &domain.Memory{
					Type:       memType,
					Content:    content,
					Importance: 0.7,
					Metadata: map[string]interface{}{
						"source":     "auto_hook",
						"session_id": data.SessionID,
					},
				})
				if err == nil {
					log.Printf("[Hook] Automatically saved to memory: %s", content)
				}
			}

			return nil, nil
		},
		WithHookDescription("Automatically saves identified facts and preferences to long-term memory."),
	)
}
