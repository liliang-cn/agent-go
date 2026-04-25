package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type dispatcherShortcutMemoryService struct {
	storeReq  *domain.MemoryStoreRequest
	formatted string
	memories  []*domain.MemoryWithScore
	recalls   map[string][]*domain.MemoryWithScore
}

func (m *dispatcherShortcutMemoryService) RetrieveAndInject(ctx context.Context, query string, sessionID string) (string, []*domain.MemoryWithScore, error) {
	return m.formatted, m.memories, nil
}

func (m *dispatcherShortcutMemoryService) RetrieveAndInjectWithLogic(ctx context.Context, query string, sessionID string) (string, []*domain.MemoryWithScore, string, error) {
	return m.formatted, m.memories, "", nil
}

func (m *dispatcherShortcutMemoryService) RetrieveAndInjectWithContext(ctx context.Context, query string, queryContext domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, error) {
	if len(m.recalls) > 0 {
		if memories, ok := m.recalls[query]; ok {
			return formatExplicitRecallMemories(memories), memories, nil
		}
	}
	return m.formatted, m.memories, nil
}

func (m *dispatcherShortcutMemoryService) RetrieveAndInjectWithContextAndLogic(ctx context.Context, query string, queryContext domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, string, error) {
	return m.formatted, m.memories, "", nil
}

func (m *dispatcherShortcutMemoryService) StoreIfWorthwhile(ctx context.Context, req *domain.MemoryStoreRequest) error {
	m.storeReq = req
	return nil
}

func (m *dispatcherShortcutMemoryService) Add(context.Context, *domain.Memory) error    { return nil }
func (m *dispatcherShortcutMemoryService) Update(context.Context, string, string) error { return nil }
func (m *dispatcherShortcutMemoryService) Search(context.Context, string, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}
func (m *dispatcherShortcutMemoryService) Get(context.Context, string) (*domain.Memory, error) {
	return nil, nil
}
func (m *dispatcherShortcutMemoryService) List(context.Context, int, int) ([]*domain.Memory, int, error) {
	return nil, 0, nil
}
func (m *dispatcherShortcutMemoryService) Delete(context.Context, string) error { return nil }
func (m *dispatcherShortcutMemoryService) Clear(context.Context) error          { return nil }
func (m *dispatcherShortcutMemoryService) ConfigureBank(context.Context, string, *domain.MemoryBankConfig) error {
	return nil
}
func (m *dispatcherShortcutMemoryService) Reflect(context.Context, string) (string, error) {
	return "", nil
}
func (m *dispatcherShortcutMemoryService) AddMentalModel(context.Context, *domain.MentalModel) error {
	return nil
}

type dispatcherShortcutRecallLLM struct{}

func (dispatcherShortcutRecallLLM) Generate(_ context.Context, prompt string, _ *domain.GenerationOptions) (string, error) {
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	return "Nebula-42；宋屿；阻塞；供应商接口冻结评审。", nil
}
func (dispatcherShortcutRecallLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (dispatcherShortcutRecallLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return nil, nil
}
func (dispatcherShortcutRecallLLM) StreamWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions, domain.ToolCallCallback) error {
	return nil
}
func (dispatcherShortcutRecallLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return nil, nil
}
func (dispatcherShortcutRecallLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestExecuteDirectDispatcherRouteShortCircuitsThroughToolRegistry(t *testing.T) {
	svc := &Service{
		agent:        NewAgentWithConfig(BuiltInDispatcherAgentName, "dispatcher", nil),
		toolRegistry: NewToolRegistry(),
	}
	svc.SetSessionID("session-1")

	called := false
	svc.toolRegistry.Register(toolDef("route_builtin_request"), func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		called = true
		return map[string]interface{}{
			"target_agent":        "Operator",
			"intent_type":         "tool_execution",
			"routing_reason":      "execution request",
			"optimized_prompt":    "让宠物狗跑起来",
			"result":              "Desktop pet walking started.",
			"verification_result": "VERIFIED_COMPLETE: Desktop pet walking started.",
		}, nil
	}, CategoryCustom)

	session := NewSessionWithID("session-1", svc.agent.ID())
	result, ok, err := svc.executeDirectDispatcherRoute(context.Background(), session, "让宠物狗跑起来")
	if err != nil {
		t.Fatalf("executeDirectDispatcherRoute failed: %v", err)
	}
	if !ok {
		t.Fatal("expected direct dispatcher route to trigger")
	}
	if !called {
		t.Fatal("expected route_builtin_request to be invoked")
	}
	if result.Text() != "Desktop pet walking started." {
		t.Fatalf("unexpected result text: %+v", result)
	}
	if got := metadataString(result.Metadata, "dispatch_target"); got != "Operator" {
		t.Fatalf("expected Operator dispatch target, got %q", got)
	}
}

func TestExecuteDirectDispatcherRouteShortCircuitsExplicitMemorySave(t *testing.T) {
	memSvc := &dispatcherShortcutMemoryService{}
	svc := &Service{
		agent:         NewAgentWithConfig(BuiltInDispatcherAgentName, "dispatcher", nil),
		toolRegistry:  NewToolRegistry(),
		memoryService: memSvc,
	}
	svc.SetSessionID("session-1")
	svc.toolRegistry.Register(toolDef("route_builtin_request"), nil, CategoryCustom)

	session := NewSessionWithID("session-1", svc.agent.ID())
	result, ok, err := svc.executeDirectDispatcherRoute(context.Background(), session, "请记住：北极星项目组代号是 Nebula-42。")
	if err != nil {
		t.Fatalf("executeDirectDispatcherRoute failed: %v", err)
	}
	if !ok {
		t.Fatal("expected direct dispatcher route to trigger")
	}
	if result.Text() != "已保存用于后续跨会话。" {
		t.Fatalf("unexpected result text: %+v", result)
	}
	if got := metadataString(result.Metadata, "dispatch_mode"); got != "direct_dispatcher_memory_save" {
		t.Fatalf("expected memory save short-circuit mode, got %q", got)
	}
	if memSvc.storeReq == nil {
		t.Fatal("expected StoreIfWorthwhile to be called")
	}
	if memSvc.storeReq.TaskResult != "北极星项目组代号是 Nebula-42。" {
		t.Fatalf("unexpected stored task result: %+v", memSvc.storeReq)
	}
}

func TestExecuteDirectDispatcherRouteShortCircuitsExplicitMemoryRecall(t *testing.T) {
	memSvc := &dispatcherShortcutMemoryService{
		formatted: "## Relevant Memory\n\n[1] [fact]: 北极星项目组代号：Nebula-42",
		memories: []*domain.MemoryWithScore{{
			Memory: &domain.Memory{
				ID:      "m-1",
				Type:    domain.MemoryTypeFact,
				Content: "北极星项目组代号：Nebula-42",
			},
			Score: 0.9,
		}},
	}
	svc := &Service{
		agent:         NewAgentWithConfig(BuiltInDispatcherAgentName, "dispatcher", nil),
		toolRegistry:  NewToolRegistry(),
		memoryService: memSvc,
		llmService:    dispatcherShortcutRecallLLM{},
	}
	svc.SetSessionID("session-1")
	svc.toolRegistry.Register(toolDef("route_builtin_request"), nil, CategoryCustom)

	session := NewSessionWithID("session-1", svc.agent.ID())
	result, ok, err := svc.executeDirectDispatcherRoute(context.Background(), session, "我之前让你记住的团队资料里，北极星项目组代号是什么？只用一行回答。")
	if err != nil {
		t.Fatalf("executeDirectDispatcherRoute failed: %v", err)
	}
	if !ok {
		t.Fatal("expected direct dispatcher route to trigger")
	}
	if result.Text() != "Nebula-42；宋屿；阻塞；供应商接口冻结评审。" {
		t.Fatalf("unexpected recall result text: %+v", result)
	}
	if got := metadataString(result.Metadata, "dispatch_mode"); got != "direct_dispatcher_memory_recall" {
		t.Fatalf("expected memory recall short-circuit mode, got %q", got)
	}
}

func TestExecuteDirectDispatcherRouteShortCircuitsExplicitMemoryRecallBySubqueries(t *testing.T) {
	memSvc := &dispatcherShortcutMemoryService{
		recalls: map[string][]*domain.MemoryWithScore{
			"北极星项目组代号是什么": {{
				Memory: &domain.Memory{ID: "m-1", Type: domain.MemoryTypeFact, Content: "北极星项目组代号：Nebula-42"},
				Score:  0.9,
			}},
			"谁负责性能专项": {{
				Memory: &domain.Memory{ID: "m-2", Type: domain.MemoryTypeFact, Content: "宋屿负责性能专项"},
				Score:  0.9,
			}},
			"红色标签表示什么": {{
				Memory: &domain.Memory{ID: "m-3", Type: domain.MemoryTypeFact, Content: "红色标签表示阻塞"},
				Score:  0.9,
			}},
			"周三15:30要做什么": {{
				Memory: &domain.Memory{ID: "m-4", Type: domain.MemoryTypeFact, Content: "周三15:30与供应商进行接口冻结评审"},
				Score:  0.9,
			}},
		},
	}
	svc := &Service{
		agent:         NewAgentWithConfig(BuiltInDispatcherAgentName, "dispatcher", nil),
		toolRegistry:  NewToolRegistry(),
		memoryService: memSvc,
		llmService:    dispatcherShortcutRecallLLM{},
	}
	svc.SetSessionID("session-1")
	svc.toolRegistry.Register(toolDef("route_builtin_request"), nil, CategoryCustom)

	session := NewSessionWithID("session-1", svc.agent.ID())
	result, ok, err := svc.executeDirectDispatcherRoute(context.Background(), session, "我之前让你记住的团队资料里，北极星项目组代号是什么？谁负责性能专项？只用一行回答。")
	if err != nil {
		t.Fatalf("executeDirectDispatcherRoute failed: %v", err)
	}
	if !ok {
		t.Fatal("expected direct dispatcher route to trigger")
	}
	if result.Text() != "Nebula-42；宋屿；阻塞；供应商接口冻结评审。" {
		t.Fatalf("unexpected recall result text: %+v", result)
	}
}

func toolDef(name string) domain.ToolDefinition {
	return domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:       name,
			Parameters: map[string]interface{}{"type": "object"},
		},
	}
}
