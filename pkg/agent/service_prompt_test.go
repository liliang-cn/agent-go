package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
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

func (promptTestMemoryService) Clear(ctx context.Context) error {
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

func TestBuildSystemPromptOmitsOperationalNotesForIntentRouter(t *testing.T) {
	routerAgent := NewAgentWithConfig(BuiltInIntentRouterAgentName, "intent router instructions", nil)
	svc := &Service{
		agent:         routerAgent,
		promptManager: prompt.NewManager(),
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{Mode: "auto"},
			},
		},
	}

	got := svc.buildSystemPrompt(context.Background(), routerAgent)
	if strings.Contains(got, "\nRules:\n") {
		t.Fatalf("expected intent router prompt to omit rules, got %q", got)
	}
	if strings.Contains(got, "Web search capability:") {
		t.Fatalf("expected intent router prompt to omit web search note, got %q", got)
	}
	if !strings.Contains(got, "intent router instructions") {
		t.Fatalf("expected intent router instructions in prompt, got %q", got)
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
		memoryStoreType: "cortex",
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

func TestBuildSystemPromptIncludesMemoryToolGuidanceInFileMode(t *testing.T) {
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
	if !strings.Contains(got, "memory_save") {
		t.Fatalf("expected file-mode prompt to reference memory_save tool, got %q", got)
	}
}

func TestBuildSystemPromptIncludesAgentMessagingGuidanceWhenMessagingToolsCallable(t *testing.T) {
	assistant := NewAgentWithConfig("Assistant", "assistant instructions", nil)
	registry := NewToolRegistry()
	registry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "send_agent_message",
			Description: "Send a short built-in message to another agent",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryCustom)
	registry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "get_agent_messages",
			Description: "Read pending mailbox messages",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryCustom)

	svc := &Service{
		agent:         assistant,
		promptManager: prompt.NewManager(),
		toolRegistry:  registry,
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{Mode: "auto"},
			},
		},
	}

	got := svc.buildSystemPrompt(context.Background(), assistant)
	if !strings.Contains(got, "Inter-agent messaging:") {
		t.Fatalf("expected messaging guidance in prompt, got %q", got)
	}
	if !strings.Contains(got, "`send_agent_message`") || !strings.Contains(got, "`get_agent_messages`") {
		t.Fatalf("expected prompt to mention messaging tools explicitly, got %q", got)
	}
	if !strings.Contains(got, "request, response, event, error, cancel, progress") {
		t.Fatalf("expected prompt to enumerate structured message types, got %q", got)
	}
}

func TestBuildSystemPrompt_InsertsDynamicBoundaryBeforeDynamicSections(t *testing.T) {
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
		memoryStoreType: "cortex",
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{Mode: "auto"},
			},
		},
	}

	got := svc.buildSystemPrompt(context.Background(), assistant)
	boundaryIdx := strings.Index(got, SystemPromptDynamicBoundary)
	if boundaryIdx < 0 {
		t.Fatalf("expected dynamic boundary in prompt, got %q", got)
	}
	memoryIdx := strings.Index(got, "Memory tool usage:")
	if memoryIdx < 0 {
		t.Fatalf("expected dynamic memory section in prompt, got %q", got)
	}
	if boundaryIdx > memoryIdx {
		t.Fatalf("expected boundary before memory section, got prompt %q", got)
	}
}

func TestBuildSystemPromptSections_StartWithBaseSection(t *testing.T) {
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

	sections := svc.buildSystemPromptSections(context.Background(), assistant, systemPromptOptions{})
	if len(sections) == 0 {
		t.Fatal("expected at least one system prompt section")
	}
	if sections[0].name != "identity" {
		t.Fatalf("expected first section to be base, got %+v", sections[0])
	}
	if strings.TrimSpace(sections[0].content) == "" {
		t.Fatalf("expected base section content, got %+v", sections[0])
	}
}

func TestBuildSystemPromptSections_SplitsBaseIntoCoreSections(t *testing.T) {
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

	sections := svc.buildSystemPromptSections(context.Background(), assistant, systemPromptOptions{})
	names := make([]string, 0, len(sections))
	for _, section := range sections {
		names = append(names, section.name)
	}

	if !containsStr(names, "identity") {
		t.Fatalf("expected identity section, got %v", names)
	}
	if !containsStr(names, "operational") {
		t.Fatalf("expected operational section, got %v", names)
	}
	if !containsStr(names, "system_context") {
		t.Fatalf("expected system_context section, got %v", names)
	}
}

func TestBuildDynamicSystemPromptSections_UsesNamedSections(t *testing.T) {
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
			Name:        "send_agent_message",
			Description: "Send message",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryCustom)

	svc := &Service{
		agent:           assistant,
		promptManager:   prompt.NewManager(),
		toolRegistry:    registry,
		memoryService:   promptTestMemoryService{},
		memoryStoreType: "cortex",
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{Mode: "auto"},
			},
		},
	}

	sections := svc.buildDynamicSystemPromptSections(context.Background(), assistant, systemPromptOptions{})
	names := make([]string, 0, len(sections))
	for _, section := range sections {
		names = append(names, section.name)
	}

	if !containsStr(names, "memory") {
		t.Fatalf("expected memory section, got %v", names)
	}
	if !containsStr(names, "messaging") {
		t.Fatalf("expected messaging section, got %v", names)
	}
	if !containsStr(names, "web_search") {
		t.Fatalf("expected web_search section, got %v", names)
	}
}

func TestBuildSystemPrompt_RegistersAndResolvesSections(t *testing.T) {
	assistant := NewAgentWithConfig("Assistant", "assistant instructions", nil)
	pm := prompt.NewManager()

	svc := &Service{
		agent:         assistant,
		promptManager: pm,
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{Mode: "off"},
			},
		},
	}

	svc.ensureSystemPromptSectionRegistry()
	sections, err := pm.ResolveSections(context.Background(), []string{"identity", "operational", "system_context"}, systemPromptSectionData{
		service: svc,
		agent:   assistant,
		options: systemPromptOptions{},
		data: map[string]interface{}{
			"AgentInstructions": assistant.Instructions(),
			"OperationalRules":  "RULES",
			"SystemContext":     "CTX",
		},
	})
	if err != nil {
		t.Fatalf("ResolveSections() error = %v", err)
	}
	if len(sections) != 3 {
		t.Fatalf("expected 3 resolved sections, got %+v", sections)
	}
	if sections[0].Name != "identity" || !strings.Contains(sections[0].Content, "assistant instructions") {
		t.Fatalf("unexpected identity section: %+v", sections[0])
	}
	if sections[1].Name != "operational" || !strings.Contains(sections[1].Content, "RULES") {
		t.Fatalf("unexpected operational section: %+v", sections[1])
	}
	if sections[2].Name != "system_context" || !strings.Contains(sections[2].Content, "CTX") {
		t.Fatalf("unexpected system_context section: %+v", sections[2])
	}
}
