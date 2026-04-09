package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

type compactDiscoveryTestLLM struct{}

func (c *compactDiscoveryTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "compact summary", nil
}

func (c *compactDiscoveryTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (c *compactDiscoveryTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{}, nil
}

func (c *compactDiscoveryTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (c *compactDiscoveryTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true}, nil
}

func (c *compactDiscoveryTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestExtractDiscoveredToolNames_FromToolSearchResultAndSummary(t *testing.T) {
	result := domain.ToolSearchResult{
		ToolReferences: []domain.ToolReference{
			{ToolName: "mcp_filesystem_read_file"},
			{ToolName: "skill_writer"},
		},
	}
	content := toolResultToString(result)
	names := extractDiscoveredToolNames([]domain.Message{
		{Role: "tool", Content: content},
	}, appendDiscoveredToolsSnapshot("summary", []string{"mcp_websearch_websearch_basic"}))

	if len(names) != 3 {
		t.Fatalf("expected 3 discovered tools, got %v", names)
	}
}

func TestCompactMessages_PreservesDiscoveredToolsSnapshot(t *testing.T) {
	svc, err := NewService(&compactDiscoveryTestLLM{}, nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	result := domain.ToolSearchResult{
		ToolReferences: []domain.ToolReference{
			{ToolName: "mcp_filesystem_read_file"},
		},
	}
	compacted, err := svc.CompactMessages(context.Background(), []domain.Message{
		{Role: "user", Content: "read a file"},
		{Role: "assistant", Content: "searching"},
		{Role: "tool", Content: toolResultToString(result), ToolCallID: "tc1"},
	})
	if err != nil {
		t.Fatalf("CompactMessages() error = %v", err)
	}

	names := extractDiscoveredToolNames(compacted, "")
	if len(names) != 1 || names[0] != "mcp_filesystem_read_file" {
		t.Fatalf("expected discovered tool to survive compaction, got %v", names)
	}
}

func TestSyncDiscoveredToolsFromHistory_ReactivatesDeferredTool(t *testing.T) {
	registry := NewToolRegistry()
	searchDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "search_available_tools",
			Description: "search tools",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
	deferredDef := domain.ToolDefinition{
		Type:         "function",
		DeferLoading: true,
		Function: domain.ToolFunction{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}

	registry.Register(searchDef, func(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil }, CategoryCustom)
	registry.Register(deferredDef, func(context.Context, map[string]interface{}) (interface{}, error) { return "sunny", nil }, CategoryCustom)

	router := ptc.NewAgentGoRouter()
	registry.SyncToPTCRouter(router)

	svc := &Service{
		toolRegistry:     registry,
		currentSessionID: "session-sync-1",
	}
	svc.ptcIntegration = &PTCIntegration{
		config: &PTCConfig{Enabled: true},
		router: router,
	}

	before := svc.ptcAvailableCallTools(context.Background())
	if containsToolInfoName(before, "get_weather") {
		t.Fatalf("expected deferred tool hidden before sync, got %+v", before)
	}

	searchResult := domain.ToolSearchResult{
		ToolReferences: []domain.ToolReference{{ToolName: "get_weather"}},
	}
	svc.syncDiscoveredToolsFromHistory([]domain.Message{
		{Role: "tool", Content: toolResultToString(searchResult)},
	}, "")

	after := svc.ptcAvailableCallTools(context.Background())
	if !containsToolInfoName(after, "get_weather") {
		t.Fatalf("expected deferred tool visible after sync, got %+v", after)
	}
}
