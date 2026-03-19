package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/pkg/config"
	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/liliang-cn/agent-go/pkg/prompt"
)

type promptTestMemoryService struct{}

func (promptTestMemoryService) RetrieveAndInject(ctx context.Context, query string, sessionID string) (string, []*domain.MemoryWithScore, error) {
	return "", nil, nil
}

func (promptTestMemoryService) RetrieveAndInjectWithLogic(ctx context.Context, query string, sessionID string) (string, []*domain.MemoryWithScore, string, error) {
	return "", nil, "", nil
}

func (promptTestMemoryService) RetrieveAndInjectWithContext(ctx context.Context, query string, queryContext domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, error) {
	return "", nil, nil
}

func (promptTestMemoryService) RetrieveAndInjectWithContextAndLogic(ctx context.Context, query string, queryContext domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, string, error) {
	return "", nil, "", nil
}

func (promptTestMemoryService) StoreIfWorthwhile(ctx context.Context, req *domain.MemoryStoreRequest) error {
	return nil
}

func (promptTestMemoryService) Add(ctx context.Context, memory *domain.Memory) error {
	return nil
}

func (promptTestMemoryService) Update(ctx context.Context, id string, content string) error {
	return nil
}

func (promptTestMemoryService) Search(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (promptTestMemoryService) Get(ctx context.Context, id string) (*domain.Memory, error) {
	return nil, nil
}

func (promptTestMemoryService) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	return nil, 0, nil
}

func (promptTestMemoryService) Delete(ctx context.Context, id string) error {
	return nil
}

func (promptTestMemoryService) ConfigureBank(ctx context.Context, sessionID string, cfg *domain.MemoryBankConfig) error {
	return nil
}

func (promptTestMemoryService) Reflect(ctx context.Context, sessionID string) (string, error) {
	return "", nil
}

func (promptTestMemoryService) AddMentalModel(ctx context.Context, model *domain.MentalModel) error {
	return nil
}

func TestBuildSystemPromptOmitsOperationalNotesForConcierge(t *testing.T) {
	concierge := NewAgentWithConfig(BuiltInConciergeAgentName, "concierge instructions", nil)
	svc := &Service{
		agent:         concierge,
		promptManager: prompt.NewManager(),
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{Mode: "auto"},
			},
		},
	}

	got := svc.buildSystemPrompt(context.Background(), concierge)
	if strings.Contains(got, "\nRules:\n") {
		t.Fatalf("expected concierge prompt to omit rules, got %q", got)
	}
	if strings.Contains(got, "Web search capability:") {
		t.Fatalf("expected concierge prompt to omit web search note, got %q", got)
	}
	if !strings.Contains(got, "concierge instructions") {
		t.Fatalf("expected concierge instructions in prompt, got %q", got)
	}
}

func TestBuildSystemPromptKeepsOperationalNotesForAssistant(t *testing.T) {
	assistant := NewAgentWithConfig("Assistant", "assistant instructions", nil)
	svc := &Service{
		agent:         assistant,
		promptManager: prompt.NewManager(),
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{Mode: "auto"},
			},
		},
	}

	got := svc.buildSystemPrompt(context.Background(), assistant)
	if !strings.Contains(got, "\nRules:\n") {
		t.Fatalf("expected assistant prompt to keep rules, got %q", got)
	}
	if !strings.Contains(got, "Web search capability:") {
		t.Fatalf("expected assistant prompt to keep web search note, got %q", got)
	}
}

func TestBuildSystemPromptIncludesMemoryToolGuidanceWhenMemoryToolsCallable(t *testing.T) {
	assistant := NewAgentWithConfig("Assistant", "assistant instructions", nil)
	registry := NewToolRegistry()
	registry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "memory_save",
			Description: "Save information to long-term memory",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryMemory)
	registry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "memory_recall",
			Description: "Recall information from long-term memory",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryMemory)

	svc := &Service{
		agent:           assistant,
		promptManager:   prompt.NewManager(),
		toolRegistry:    registry,
		memoryService:   promptTestMemoryService{},
		memoryStoreType: "vector",
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{Mode: "auto"},
			},
		},
	}

	got := svc.buildSystemPrompt(context.Background(), assistant)
	if !strings.Contains(got, "Memory tool usage:") {
		t.Fatalf("expected memory tool guidance in prompt, got %q", got)
	}
	if !strings.Contains(got, "`memory_save`") || !strings.Contains(got, "`memory_recall`") {
		t.Fatalf("expected prompt to mention memory tools explicitly, got %q", got)
	}
}

func TestBuildSystemPromptOmitsMemoryToolGuidanceInFileOnlyMode(t *testing.T) {
	assistant := NewAgentWithConfig("Assistant", "assistant instructions", nil)
	registry := NewToolRegistry()
	registry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "memory_save",
			Description: "Save information to long-term memory",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryMemory)

	svc := &Service{
		agent:           assistant,
		promptManager:   prompt.NewManager(),
		toolRegistry:    registry,
		memoryService:   promptTestMemoryService{},
		memoryStoreType: "file",
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{Mode: "auto"},
			},
		},
	}

	got := svc.buildSystemPrompt(context.Background(), assistant)
	if strings.Contains(got, "Memory tool usage:") {
		t.Fatalf("expected file-only prompt to omit memory tool guidance, got %q", got)
	}
}
