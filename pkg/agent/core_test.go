package agent

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type metadataTestMCP struct{}

func (m *metadataTestMCP) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, nil
}

func (m *metadataTestMCP) ListTools() []domain.ToolDefinition { return nil }

func (m *metadataTestMCP) AddServer(ctx context.Context, name string, command string, args []string) error {
	return nil
}

func (m *metadataTestMCP) ToolMetadata(toolName string) (ToolMetadata, bool) {
	if toolName == "mcp_custom_write" {
		return ToolMetadata{Destructive: true}, true
	}
	return ToolMetadata{}, false
}

// ── ToolRegistry ─────────────────────────────────────────────────────────────

func makeToolDef(name, desc string) domain.ToolDefinition {
	return domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        name,
			Description: desc,
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}

func TestToolRegistry_RegisterAndCall(t *testing.T) {
	reg := NewToolRegistry()

	reg.Register(
		makeToolDef("echo", "Echoes input"),
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			return args["msg"], nil
		},
		CategoryCustom,
	)

	if !reg.Has("echo") {
		t.Fatal("expected registry to contain 'echo'")
	}

	result, err := reg.Call(context.Background(), "echo", map[string]interface{}{"msg": "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello" {
		t.Errorf("expected 'hello', got %v", result)
	}
}

func TestToolRegistry_CallUnknownTool(t *testing.T) {
	reg := NewToolRegistry()
	_, err := reg.Call(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestToolRegistry_ListForLLM_NativeMode(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(
		makeToolDef("tool1", "Test tool"),
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil },
		CategoryCustom,
	)

	// ptcEnabled=false → should return tool definitions
	defs := reg.ListForLLM(false, "")
	if len(defs) == 0 {
		t.Fatal("expected non-empty tool list for native mode")
	}
	if defs[0].Function.Name != "tool1" {
		t.Errorf("unexpected tool name: %v", defs[0].Function.Name)
	}
}

func TestToolRegistry_ListForLLM_PTCMode(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(
		makeToolDef("tool1", "Test tool"),
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil },
		CategoryCustom,
	)

	// ptcEnabled=true → hidden from LLM (JS sandbox exposes them via callTool)
	defs := reg.ListForLLM(true, "")
	if defs != nil {
		t.Errorf("expected nil tool list for PTC mode, got %v", defs)
	}
}

func TestToolRegistry_CategoryOf(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(makeToolDef("rag_query", "RAG"), func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil }, CategoryRAG)
	reg.Register(makeToolDef("memory_save", "Memory"), func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil }, CategoryMemory)

	if reg.CategoryOf("rag_query") != CategoryRAG {
		t.Errorf("expected CategoryRAG, got %v", reg.CategoryOf("rag_query"))
	}
	if reg.CategoryOf("memory_save") != CategoryMemory {
		t.Errorf("expected CategoryMemory, got %v", reg.CategoryOf("memory_save"))
	}
	if reg.CategoryOf("unknown") != "" {
		t.Errorf("expected empty category for unknown tool")
	}
}

func TestToolRegistry_MetadataOf(t *testing.T) {
	reg := NewToolRegistry()
	reg.RegisterWithMetadata(
		makeToolDef("memory_recall", "Memory recall"),
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil },
		CategoryMemory,
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true},
	)

	meta := reg.MetadataOf("memory_recall")
	if !meta.ReadOnly {
		t.Fatal("expected readOnly metadata to be true")
	}
	if !meta.ConcurrencySafe {
		t.Fatal("expected concurrencySafe metadata to be true")
	}

	unknown := reg.MetadataOf("unknown")
	if unknown.ReadOnly || unknown.ConcurrencySafe {
		t.Fatalf("expected zero metadata for unknown tool, got %+v", unknown)
	}
}

func TestToolRegistry_DuplicateRegistrationOverwrites(t *testing.T) {
	reg := NewToolRegistry()

	reg.Register(makeToolDef("greet", "v1"), func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return "v1", nil }, CategoryCustom)
	reg.Register(makeToolDef("greet", "v2"), func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return "v2", nil }, CategoryCustom)

	result, err := reg.Call(context.Background(), "greet", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "v2" {
		t.Errorf("expected second registration to overwrite, got %v", result)
	}
}

func TestToolRegistry_UnregisterRemovesTool(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(makeToolDef("tmp", "temp"), func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil }, CategoryCustom)

	if !reg.Has("tmp") {
		t.Fatal("expected tmp to be registered")
	}
	reg.Unregister("tmp")
	if reg.Has("tmp") {
		t.Error("expected tmp to be unregistered")
	}
}

func TestPartitionToolCalls_GroupsConcurrencySafeBatches(t *testing.T) {
	svc := &Service{
		toolRegistry: NewToolRegistry(),
	}
	svc.toolRegistry.RegisterWithMetadata(
		makeToolDef("rag_query", "RAG"),
		nil,
		CategoryRAG,
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true},
	)
	svc.toolRegistry.RegisterWithMetadata(
		makeToolDef("memory_recall", "Memory"),
		nil,
		CategoryMemory,
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true},
	)
	svc.toolRegistry.RegisterWithMetadata(
		makeToolDef("memory_save", "Memory save"),
		nil,
		CategoryMemory,
		ToolMetadata{},
	)

	batches := svc.partitionToolCalls([]domain.ToolCall{
		{ID: "1", Function: domain.FunctionCall{Name: "rag_query"}},
		{ID: "2", Function: domain.FunctionCall{Name: "memory_recall"}},
		{ID: "3", Function: domain.FunctionCall{Name: "memory_save"}},
		{ID: "4", Function: domain.FunctionCall{Name: "rag_query"}},
	}, nil, nil)

	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	if !batches[0].isConcurrencySafe || len(batches[0].toolCalls) != 2 {
		t.Fatalf("expected first batch to be concurrent with 2 tool calls, got %+v", batches[0])
	}
	if batches[1].isConcurrencySafe || len(batches[1].toolCalls) != 1 || batches[1].toolCalls[0].Function.Name != "memory_save" {
		t.Fatalf("expected second batch to be serial memory_save, got %+v", batches[1])
	}
	if !batches[2].isConcurrencySafe || len(batches[2].toolCalls) != 1 || batches[2].toolCalls[0].Function.Name != "rag_query" {
		t.Fatalf("expected third batch to be concurrent rag_query, got %+v", batches[2])
	}
}

func TestQueryLoopState_TracksBudgetAndToolTotals(t *testing.T) {
	state := newQueryLoopState("inspect repo", []domain.Message{{Role: "user", Content: "inspect repo"}}, &IntentRecognitionResult{
		IntentType: "code",
		Transition: "tool_first",
	}, 3)

	state.setStage(TurnStagePreparingContext, "starting", 0)
	if state.Stage != TurnStagePreparingContext {
		t.Fatalf("stage = %q", state.Stage)
	}
	if state.Transition != "tool_first" {
		t.Fatalf("transition = %q", state.Transition)
	}
	if state.Budget.RemainingRounds != 3 {
		t.Fatalf("remaining rounds = %d, want 3", state.Budget.RemainingRounds)
	}

	state.beginRound()
	state.noteTokens(123)
	state.recordToolResults([]ToolExecutionResult{{ToolName: "read_file"}, {ToolName: "search_code"}})
	state.noteRoundCompleted()

	if state.CurrentRound != 1 {
		t.Fatalf("current round = %d, want 1", state.CurrentRound)
	}
	if state.TotalToolCalls != 2 {
		t.Fatalf("total tool calls = %d, want 2", state.TotalToolCalls)
	}
	if state.Budget.EstimatedTokens != 123 {
		t.Fatalf("estimated tokens = %d, want 123", state.Budget.EstimatedTokens)
	}
	if state.Budget.RemainingRounds != 2 {
		t.Fatalf("remaining rounds = %d, want 2", state.Budget.RemainingRounds)
	}
}

func TestExecuteToolCallsWithOptions_EmitsYieldedStateOnRecoverableError(t *testing.T) {
	t.Parallel()

	agent := NewAgent("Assistant")
	agent.AddToolWithMetadata("boom_tool", "fails", map[string]interface{}{}, func(context.Context, map[string]interface{}) (interface{}, error) {
		return nil, errors.New("boom")
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel})

	svc := &Service{
		agent:           agent,
		logger:          slog.Default(),
		toolRegistry:    NewToolRegistry(),
		inProgressTools: make(map[string]int),
	}

	var (
		mu     sync.Mutex
		states []string
	)
	results, err := svc.executeToolCallsWithOptions(context.Background(), agent, NewSession(agent.ID()), []domain.ToolCall{
		{ID: "1", Function: domain.FunctionCall{Name: "boom_tool"}},
	}, ToolExecutionCallbacks{
		OnToolState: func(name string, state string, interruptBehavior string) {
			if name != "boom_tool" {
				return
			}
			mu.Lock()
			states = append(states, state)
			mu.Unlock()
		},
	}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if got := results[0].Result; got != "Error: boom" {
		t.Fatalf("result = %#v, want error string", got)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"queued", "executing", "yielded"}
	if len(states) != len(want) {
		t.Fatalf("states = %#v, want %#v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("states = %#v, want %#v", states, want)
		}
	}
}

func TestExecuteToolCallsWithOptions_PreservesInputOrderAcrossConcurrentBatch(t *testing.T) {
	t.Parallel()

	agent := NewAgent("Assistant")
	agent.AddToolWithMetadata("slow_tool", "slow", map[string]interface{}{}, func(context.Context, map[string]interface{}) (interface{}, error) {
		time.Sleep(40 * time.Millisecond)
		return "slow", nil
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel})
	agent.AddToolWithMetadata("fast_tool", "fast", map[string]interface{}{}, func(context.Context, map[string]interface{}) (interface{}, error) {
		time.Sleep(5 * time.Millisecond)
		return "fast", nil
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel})

	svc := &Service{
		agent:           agent,
		logger:          slog.Default(),
		toolRegistry:    NewToolRegistry(),
		inProgressTools: make(map[string]int),
	}

	results, err := svc.executeToolCallsWithOptions(context.Background(), agent, NewSession(agent.ID()), []domain.ToolCall{
		{ID: "1", Function: domain.FunctionCall{Name: "slow_tool"}},
		{ID: "2", Function: domain.FunctionCall{Name: "fast_tool"}},
	}, ToolExecutionCallbacks{}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected two results, got %d", len(results))
	}
	if results[0].Result != "slow" || results[1].Result != "fast" {
		t.Fatalf("results out of order: %+v", results)
	}
}

func TestInferDynamicToolMetadata(t *testing.T) {
	tests := []struct {
		name                string
		toolName            string
		wantKnown           bool
		wantReadOnly        bool
		wantConcurrencySafe bool
	}{
		{
			name:                "filesystem read is safe",
			toolName:            "mcp_filesystem_read_file",
			wantKnown:           true,
			wantReadOnly:        true,
			wantConcurrencySafe: true,
		},
		{
			name:                "filesystem write is not safe",
			toolName:            "mcp_filesystem_write_file",
			wantKnown:           true,
			wantReadOnly:        false,
			wantConcurrencySafe: false,
		},
		{
			name:                "websearch is safe",
			toolName:            "mcp_websearch_fetch_page_content",
			wantKnown:           true,
			wantReadOnly:        true,
			wantConcurrencySafe: true,
		},
		{
			name:                "generic mcp query is safe",
			toolName:            "mcp_sqlite_query",
			wantKnown:           true,
			wantReadOnly:        true,
			wantConcurrencySafe: true,
		},
		{
			name:                "generic mcp update is unsafe",
			toolName:            "mcp_github_create_issue",
			wantKnown:           true,
			wantReadOnly:        false,
			wantConcurrencySafe: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, ok := inferDynamicToolMetadata(tt.toolName)
			if ok != tt.wantKnown {
				t.Fatalf("known = %v, want %v", ok, tt.wantKnown)
			}
			if meta.ReadOnly != tt.wantReadOnly {
				t.Fatalf("readOnly = %v, want %v", meta.ReadOnly, tt.wantReadOnly)
			}
			if meta.ConcurrencySafe != tt.wantConcurrencySafe {
				t.Fatalf("concurrencySafe = %v, want %v", meta.ConcurrencySafe, tt.wantConcurrencySafe)
			}
			if tt.wantReadOnly && meta.InterruptBehavior != InterruptBehaviorCancel {
				t.Fatalf("interruptBehavior = %q, want %q", meta.InterruptBehavior, InterruptBehaviorCancel)
			}
			if !tt.wantReadOnly && tt.wantKnown && meta.Destructive && meta.InterruptBehavior != InterruptBehaviorBlock {
				t.Fatalf("interruptBehavior = %q, want %q", meta.InterruptBehavior, InterruptBehaviorBlock)
			}
		})
	}
}

func TestLookupToolMetadata_UsesMCPProvider(t *testing.T) {
	svc := &Service{mcpService: &metadataTestMCP{}}
	meta := svc.lookupToolMetadata("mcp_custom_write")
	if !meta.Destructive {
		t.Fatalf("expected destructive metadata from MCP provider, got %+v", meta)
	}
}

func TestRegisterBuiltInTools_Metadata(t *testing.T) {
	svc, err := NewService(nil, nil, nil, filepath.Join(t.TempDir(), "agent.db"), nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	delegateMeta := svc.toolRegistry.MetadataOf("delegate_to_subagent")
	if delegateMeta.InterruptBehavior != InterruptBehaviorBlock {
		t.Fatalf("unexpected delegate_to_subagent metadata: %+v", delegateMeta)
	}

	completeMeta := svc.toolRegistry.MetadataOf("task_complete")
	if !completeMeta.ReadOnly || !completeMeta.ConcurrencySafe || completeMeta.InterruptBehavior != InterruptBehaviorCancel {
		t.Fatalf("unexpected task_complete metadata: %+v", completeMeta)
	}
}

func TestAgentToolMetadata_IsVisibleToServiceLookup(t *testing.T) {
	svc := &Service{}
	agent := NewAgent("Assistant")
	agent.AddToolWithMetadata(
		"local_read",
		"Local read helper",
		map[string]interface{}{"type": "object"},
		nil,
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)

	meta := svc.lookupToolMetadataForAgent("local_read", agent)
	if !meta.ReadOnly || !meta.ConcurrencySafe || meta.InterruptBehavior != InterruptBehaviorCancel {
		t.Fatalf("unexpected metadata from agent-local tool: %+v", meta)
	}
}

func TestAgentAddTool_InfersMetadata(t *testing.T) {
	agent := NewAgent("Assistant")
	agent.AddTool("read_file", "Read file", map[string]interface{}{"type": "object"}, nil)

	meta := agent.MetadataOf("read_file")
	if !meta.ReadOnly || !meta.ConcurrencySafe || meta.InterruptBehavior != InterruptBehaviorCancel {
		t.Fatalf("unexpected inferred metadata: %+v", meta)
	}
}

func TestInferGenericToolMetadata(t *testing.T) {
	meta, ok := inferGenericToolMetadata("write_report")
	if !ok {
		t.Fatal("expected generic metadata inference to succeed")
	}
	if meta.InterruptBehavior != InterruptBehaviorBlock {
		t.Fatalf("unexpected generic metadata: %+v", meta)
	}
}

func TestServiceRegisterTool_InfersMetadata(t *testing.T) {
	svc := &Service{toolRegistry: NewToolRegistry()}
	svc.RegisterTool(makeToolDef("get_status", "status"), nil)

	meta := svc.toolRegistry.MetadataOf("get_status")
	if !meta.ReadOnly || !meta.ConcurrencySafe || meta.InterruptBehavior != InterruptBehaviorCancel {
		t.Fatalf("unexpected inferred metadata on RegisterTool: %+v", meta)
	}
}

// ── ExecutionResult helpers ───────────────────────────────────────────────────

func TestExecutionResult_Text_StringValue(t *testing.T) {
	r := &ExecutionResult{FinalResult: "Hello, world!"}
	if r.Text() != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", r.Text())
	}
}

func TestExecutionResult_Text_NonStringFallback(t *testing.T) {
	r := &ExecutionResult{FinalResult: 42}
	if r.Text() != "42" {
		t.Errorf("expected '42', got %q", r.Text())
	}
}

func TestExecutionResult_Text_Nil(t *testing.T) {
	r := &ExecutionResult{FinalResult: nil}
	if r.Text() != "" {
		t.Errorf("expected empty string for nil FinalResult, got %q", r.Text())
	}
}

func TestExecutionResult_Err_NoError(t *testing.T) {
	r := &ExecutionResult{}
	if r.Err() != nil {
		t.Errorf("expected nil error, got %v", r.Err())
	}
}

func TestExecutionResult_Err_WithError(t *testing.T) {
	r := &ExecutionResult{Error: "something went wrong"}
	if r.Err() == nil {
		t.Fatal("expected non-nil error")
	}
	if r.Err().Error() != "something went wrong" {
		t.Errorf("unexpected error message: %v", r.Err())
	}
}

func TestExecutionResult_HasSources(t *testing.T) {
	empty := &ExecutionResult{}
	if empty.HasSources() {
		t.Error("expected HasSources=false for empty sources")
	}

	withSources := &ExecutionResult{Sources: []domain.Chunk{{Content: "chunk1"}}}
	if !withSources.HasSources() {
		t.Error("expected HasSources=true when sources present")
	}
}

// ── Builder ergonomics ────────────────────────────────────────────────────────

func TestBuilder_WithDebug_NoArg(t *testing.T) {
	// WithDebug() with no args should enable debug
	b := New("test-agent").WithDebug()
	if !b.debug {
		t.Error("expected debug=true when WithDebug() called with no args")
	}
}

func TestBuilder_WithDebug_FalseArg(t *testing.T) {
	b := New("test-agent").WithDebug(false)
	if b.debug {
		t.Error("expected debug=false when WithDebug(false) called")
	}
}

func TestBuilder_WithDebug_TrueArg(t *testing.T) {
	b := New("test-agent").WithDebug(true)
	if !b.debug {
		t.Error("expected debug=true when WithDebug(true) called")
	}
}

func TestBuilder_WithTool_AddedToBuilder(t *testing.T) {
	tool := BuildTool("my_tool").
		Description("A test tool").
		Handler(func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil }).
		Build()

	b := New("test-agent").WithTool(tool)
	if len(b.tools) != 1 {
		t.Errorf("expected 1 tool in builder, got %d", len(b.tools))
	}
	if b.tools[0].Name() != "my_tool" {
		t.Errorf("unexpected tool name: %v", b.tools[0].Name())
	}
}

func TestBuilder_WithTools_MultipleTools(t *testing.T) {
	mkTool := func(name string) *Tool {
		return BuildTool(name).
			Description("tool").
			Handler(func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil }).
			Build()
	}

	b := New("test-agent").WithTools(mkTool("t1"), mkTool("t2"), mkTool("t3"))
	if len(b.tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(b.tools))
	}
}

func TestBuilder_WithPrompt(t *testing.T) {
	b := New("test-agent").WithPrompt("You are a test bot.")
	if b.systemPrompt != "You are a test bot." {
		t.Errorf("unexpected system prompt: %q", b.systemPrompt)
	}
}

func TestBuilder_WithSystemPrompt_Alias(t *testing.T) {
	// WithSystemPrompt and WithPrompt should set the same field
	b1 := New("test-agent").WithSystemPrompt("sys")
	b2 := New("test-agent").WithPrompt("sys")
	if b1.systemPrompt != b2.systemPrompt {
		t.Errorf("WithSystemPrompt and WithPrompt should produce same result")
	}
}

func TestBuilder_NameSet(t *testing.T) {
	b := New("my-agent")
	if b.name != "my-agent" {
		t.Errorf("expected name='my-agent', got %q", b.name)
	}
}

// ── HookRegistry isolation ────────────────────────────────────────────────────

func TestNewService_HasIsolatedHookRegistry(t *testing.T) {
	// Two HookRegistry instances should not share handlers
	s1 := &Service{hooks: NewHookRegistry()}
	s2 := &Service{hooks: NewHookRegistry()}

	called := 0
	s1.hooks.Register(HookEventPostExecution, func(_ context.Context, _ HookEvent, _ HookData) (interface{}, error) {
		called++
		return nil, nil
	})

	// Emit on s2 should NOT trigger s1's hook
	s2.hooks.Emit(HookEventPostExecution, HookData{})
	if called != 0 {
		t.Error("hook from s1 should not fire on s2's registry")
	}

	// Emit on s1 SHOULD trigger s1's hook
	s1.hooks.Emit(HookEventPostExecution, HookData{})
	if called != 1 {
		t.Errorf("expected s1 hook to fire once, called=%d", called)
	}
}

// ── error sentinel: ExecutionResult.Err wraps string ─────────────────────────

func TestExecutionResult_Err_IsComparable(t *testing.T) {
	r := &ExecutionResult{Error: "timeout"}
	err := r.Err()
	if !errors.Is(err, err) { // basic sanity: error is comparable to itself
		t.Error("error should be comparable to itself")
	}
	if err.Error() != "timeout" {
		t.Errorf("expected 'timeout', got %q", err.Error())
	}
}
