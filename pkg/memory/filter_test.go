package memory

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestNoiseFilter_Filter(t *testing.T) {
	filter := NewNoiseFilter(nil)

	tests := []struct {
		name        string
		memories    []*domain.MemoryWithScore
		expectCount int
	}{
		{
			name: "filter short content",
			memories: []*domain.MemoryWithScore{
				{Memory: &domain.Memory{ID: "1", Content: "ok"}, Score: 0.8},
				{Memory: &domain.Memory{ID: "2", Content: "This is a valid memory with enough content"}, Score: 0.7},
			},
			expectCount: 1,
		},
		{
			name: "filter refusal responses",
			memories: []*domain.MemoryWithScore{
				{Memory: &domain.Memory{ID: "1", Content: "I'm sorry but I can't help with that request"}, Score: 0.9},
				{Memory: &domain.Memory{ID: "2", Content: "Here's the information you requested about the topic"}, Score: 0.8},
			},
			expectCount: 1,
		},
		{
			name: "filter meta questions",
			memories: []*domain.MemoryWithScore{
				{Memory: &domain.Memory{ID: "1", Content: "Who are you and what is your purpose"}, Score: 0.9},
				{Memory: &domain.Memory{ID: "2", Content: "The configuration file is located in the home directory"}, Score: 0.8},
			},
			expectCount: 1,
		},
		{
			name: "filter duplicates",
			memories: []*domain.MemoryWithScore{
				{Memory: &domain.Memory{ID: "1", Content: "The quick brown fox jumps over the lazy dog"}, Score: 0.9},
				{Memory: &domain.Memory{ID: "2", Content: "The quick brown fox jumps over the lazy dog"}, Score: 0.8},
				{Memory: &domain.Memory{ID: "3", Content: "Different content entirely"}, Score: 0.7},
			},
			expectCount: 2,
		},
		{
			name: "keep all valid memories",
			memories: []*domain.MemoryWithScore{
				{Memory: &domain.Memory{ID: "1", Content: "First valid memory with sufficient content"}, Score: 0.9},
				{Memory: &domain.Memory{ID: "2", Content: "Second valid memory with enough text"}, Score: 0.8},
				{Memory: &domain.Memory{ID: "3", Content: "Third valid memory with proper length"}, Score: 0.7},
			},
			expectCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filter.Filter(tt.memories)
			if len(result) != tt.expectCount {
				t.Errorf("Filter() returned %d memories, want %d", len(result), tt.expectCount)
			}
		})
	}
}

func TestNoiseFilter_IsNoisy(t *testing.T) {
	filter := NewNoiseFilter(nil)

	tests := []struct {
		content       string
		expectedNoisy bool
	}{
		{"ok", true},                     // Too short
		{"thanks!", true},                // Generic
		{"I can't help with that", true}, // Refusal
		{"Who are you?", true},           // Meta
		{"This is a valid memory about something important", false},
		{"The configuration settings are stored in a JSON file", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			result := filter.IsNoisy(tt.content)
			if result != tt.expectedNoisy {
				t.Errorf("IsNoisy(%q) = %v, want %v", tt.content, result, tt.expectedNoisy)
			}
		})
	}
}

func TestNoiseFilter_Disabled(t *testing.T) {
	config := &NoiseFilterConfig{
		Enabled: false,
	}
	filter := NewNoiseFilter(config)

	memories := []*domain.MemoryWithScore{
		{Memory: &domain.Memory{ID: "1", Content: "ok"}, Score: 0.8},
		{Memory: &domain.Memory{ID: "2", Content: "I can't help"}, Score: 0.7},
	}

	result := filter.Filter(memories)
	if len(result) != 2 {
		t.Errorf("Filter() should return all memories when disabled, got %d", len(result))
	}
}
