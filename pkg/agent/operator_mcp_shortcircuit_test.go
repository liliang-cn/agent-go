package agent

import (
	"context"
	"log/slog"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type operatorMCPSearchTestLLM struct{}

func (o *operatorMCPSearchTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (o *operatorMCPSearchTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (o *operatorMCPSearchTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{
			{
				ID:   "call-1",
				Type: "function",
				Function: domain.FunctionCall{
					Name:      "mcp_husky-pet_start_walking",
					Arguments: map[string]interface{}{},
				},
			},
		},
	}, nil
}

func (o *operatorMCPSearchTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (o *operatorMCPSearchTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return nil, nil
}

func (o *operatorMCPSearchTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

type operatorMCPStub struct {
	tools []domain.ToolDefinition
}

func (s *operatorMCPStub) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	return "Desktop pet started walking.", nil
}

func (s *operatorMCPStub) ListTools() []domain.ToolDefinition {
	return s.tools
}

func (s *operatorMCPStub) AddServer(ctx context.Context, name string, command string, args []string) error {
	return nil
}

func TestTryDirectOperatorMCPExecutionUsesVisibleMCPTools(t *testing.T) {
	svc := &Service{
		agent:      NewAgentWithConfig(defaultOperatorAgentName, "operator", nil),
		llmService: &operatorMCPSearchTestLLM{},
		logger:     slog.Default(),
		mcpService: &operatorMCPStub{
			tools: []domain.ToolDefinition{
				{
					Type: "function",
					Function: domain.ToolFunction{
						Name:        "mcp_husky-pet_start_walking",
						Description: "Start the husky walking/running animation",
						Parameters:  map[string]interface{}{"type": "object"},
					},
				},
			},
		},
	}
	svc.agent.SetAllowedMCPTools([]string{"*"})

	result, ok, err := svc.tryDirectOperatorMCPExecution(context.Background(), "让宠物狗跑起来")
	if err != nil {
		t.Fatalf("tryDirectOperatorMCPExecution failed: %v", err)
	}
	if !ok {
		t.Fatal("expected direct operator MCP execution to trigger")
	}
	if result == "" || result != "mcp_husky-pet_start_walking: Desktop pet started walking." {
		t.Fatalf("unexpected result: %q", result)
	}
}
