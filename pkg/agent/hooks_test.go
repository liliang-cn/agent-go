package agent

import (
	"context"
	"testing"
	"time"
)

func TestHookRegistry_Register(t *testing.T) {
	registry := NewHookRegistry()

	handler := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		return nil, nil
	}

	id := registry.Register(HookEventPreToolUse, handler)
	if id == "" {
		t.Error("Expected non-empty hook ID")
	}

	hooks := registry.List(HookEventPreToolUse)
	if len(hooks) != 1 {
		t.Errorf("Expected 1 hook, got %d", len(hooks))
	}
}

func TestHookRegistry_Unregister(t *testing.T) {
	registry := NewHookRegistry()

	handler := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		return nil, nil
	}

	id := registry.Register(HookEventPreToolUse, handler)
	if !registry.Unregister(id) {
		t.Error("Expected Unregister to return true")
	}

	hooks := registry.List(HookEventPreToolUse)
	if len(hooks) != 0 {
		t.Errorf("Expected 0 hooks after unregister, got %d", len(hooks))
	}
}

func TestHookRegistry_Emit(t *testing.T) {
	registry := NewHookRegistry()

	var called bool
	handler := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		called = true
		return nil, nil
	}

	registry.Register(HookEventPreToolUse, handler)

	data := HookData{ToolName: "test_tool"}
	registry.Emit(HookEventPreToolUse, data)

	if !called {
		t.Error("Expected handler to be called")
	}
}

func TestHookRegistry_EmitWithResult(t *testing.T) {
	registry := NewHookRegistry()

	// Handler that modifies data
	handler := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		data.ToolArgs = map[string]interface{}{"modified": true}
		return data, nil
	}

	registry.Register(HookEventPreToolUse, handler)

	data := HookData{
		ToolName: "test_tool",
		ToolArgs: map[string]interface{}{"original": true},
	}

	result, err := registry.EmitWithResult(context.Background(), HookEventPreToolUse, data)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if result.ToolArgs["modified"] != true {
		t.Error("Expected ToolArgs to be modified")
	}
}

func TestHookRegistry_EmitWithResult_Blocking(t *testing.T) {
	registry := NewHookRegistry()

	// Handler that returns error to block
	blockingHandler := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		return nil, context.Canceled
	}

	registry.Register(HookEventPreToolUse, blockingHandler)

	data := HookData{ToolName: "test_tool"}
	_, err := registry.EmitWithResult(context.Background(), HookEventPreToolUse, data)

	if err == nil {
		t.Error("Expected error from blocking handler")
	}
}

func TestHookRegistry_Priority(t *testing.T) {
	registry := NewHookRegistry()

	var order []string

	handler1 := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		order = append(order, "handler1")
		return nil, nil
	}

	handler2 := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		order = append(order, "handler2")
		return nil, nil
	}

	// Register with different priorities
	registry.Register(HookEventPreToolUse, handler1, WithHookPriority(100))
	registry.Register(HookEventPreToolUse, handler2, WithHookPriority(1)) // Higher priority

	registry.Emit(HookEventPreToolUse, HookData{})

	if len(order) != 2 {
		t.Fatalf("Expected 2 handlers called, got %d", len(order))
	}

	// handler2 should be called first (lower priority number = higher priority)
	if order[0] != "handler2" {
		t.Errorf("Expected handler2 first, got %s", order[0])
	}
}

func TestHookRegistry_GlobalHooks(t *testing.T) {
	registry := NewHookRegistry()

	var globalCalled, specificCalled bool

	globalHandler := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		globalCalled = true
		return nil, nil
	}

	specificHandler := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		specificCalled = true
		return nil, nil
	}

	// Register global hook (empty event)
	registry.Register("", globalHandler)
	// Register specific hook
	registry.Register(HookEventPreToolUse, specificHandler)

	registry.Emit(HookEventPreToolUse, HookData{})

	if !globalCalled {
		t.Error("Expected global handler to be called")
	}
	if !specificCalled {
		t.Error("Expected specific handler to be called")
	}
}

func TestToolNameMatcher(t *testing.T) {
	matcher := NewToolNameMatcher("rag_query", "memory_save")

	tests := []struct {
		toolName string
		expected bool
	}{
		{"rag_query", true},
		{"memory_save", true},
		{"other_tool", false},
		{"RAG_QUERY", true}, // Case insensitive
	}

	for _, tt := range tests {
		data := HookData{ToolName: tt.toolName}
		result := matcher.Match(HookEventPreToolUse, data)
		if result != tt.expected {
			t.Errorf("ToolName %s: expected %v, got %v", tt.toolName, tt.expected, result)
		}
	}
}

func TestCompositeMatcher_All(t *testing.T) {
	matcher := AllMatchers(
		NewToolNameMatcher("rag_query"),
		NewAgentNameMatcher("agent1"),
	)

	// Both conditions match
	data := HookData{ToolName: "rag_query", AgentID: "agent1"}
	if !matcher.Match(HookEventPreToolUse, data) {
		t.Error("Expected match when both conditions are met")
	}

	// Only one condition matches
	data = HookData{ToolName: "rag_query", AgentID: "agent2"}
	if matcher.Match(HookEventPreToolUse, data) {
		t.Error("Expected no match when only one condition is met")
	}
}

func TestCompositeMatcher_Any(t *testing.T) {
	matcher := AnyMatcher(
		NewToolNameMatcher("rag_query"),
		NewToolNameMatcher("memory_save"),
	)

	tests := []struct {
		toolName string
		expected bool
	}{
		{"rag_query", true},
		{"memory_save", true},
		{"other_tool", false},
	}

	for _, tt := range tests {
		data := HookData{ToolName: tt.toolName}
		result := matcher.Match(HookEventPreToolUse, data)
		if result != tt.expected {
			t.Errorf("ToolName %s: expected %v, got %v", tt.toolName, tt.expected, result)
		}
	}
}

func TestNotMatcher(t *testing.T) {
	innerMatcher := NewToolNameMatcher("blocked_tool")
	matcher := Not(innerMatcher)

	// Inner matcher matches, so Not should return false
	data := HookData{ToolName: "blocked_tool"}
	if matcher.Match(HookEventPreToolUse, data) {
		t.Error("Expected Not matcher to return false when inner matches")
	}

	// Inner matcher doesn't match, so Not should return true
	data = HookData{ToolName: "allowed_tool"}
	if !matcher.Match(HookEventPreToolUse, data) {
		t.Error("Expected Not matcher to return true when inner doesn't match")
	}
}

func TestHookEnableDisable(t *testing.T) {
	registry := NewHookRegistry()

	var callCount int
	handler := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		callCount++
		return nil, nil
	}

	id := registry.Register(HookEventPreToolUse, handler)

	// Initial call
	registry.Emit(HookEventPreToolUse, HookData{})
	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}

	// Disable
	registry.Disable(id)
	registry.Emit(HookEventPreToolUse, HookData{})
	if callCount != 1 {
		t.Error("Handler should not be called when disabled")
	}

	// Enable
	registry.Enable(id)
	registry.Emit(HookEventPreToolUse, HookData{})
	if callCount != 2 {
		t.Errorf("Expected 2 calls after re-enable, got %d", callCount)
	}
}

func TestHookDataTimestamp(t *testing.T) {
	registry := NewHookRegistry()

	var receivedTimestamp time.Time
	handler := func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		receivedTimestamp = data.Timestamp
		return nil, nil
	}

	registry.Register(HookEventPreToolUse, handler)

	before := time.Now()
	registry.Emit(HookEventPreToolUse, HookData{})
	after := time.Now()

	if receivedTimestamp.Before(before) || receivedTimestamp.After(after) {
		t.Error("Timestamp should be set during Emit")
	}
}
