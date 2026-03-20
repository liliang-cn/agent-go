package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

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
