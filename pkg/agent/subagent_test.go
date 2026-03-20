package agent

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestNewSubAgent(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 5,
	})

	if subAgent == nil {
		t.Fatal("Expected non-nil SubAgent")
	}

	if subAgent.ID() == "" {
		t.Error("Expected non-empty ID")
	}

	if subAgent.GetState() != SubAgentStatePending {
		t.Errorf("Expected initial state to be Pending, got %s", subAgent.GetState())
	}
}

func TestSubAgentOptions(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(
		SubAgentConfig{Agent: agent, Goal: "test"},
		WithSubAgentMaxTurns(15),
		WithSubAgentMode(SubAgentModeBackground),
		WithSubAgentIsolated(false),
		WithSubAgentToolAllowlist([]string{"rag_query"}),
		WithSubAgentToolDenylist([]string{"memory_save"}),
	)

	if subAgent.config.MaxTurns != 15 {
		t.Errorf("Expected MaxTurns 15, got %d", subAgent.config.MaxTurns)
	}

	if subAgent.config.Mode != SubAgentModeBackground {
		t.Errorf("Expected Mode Background, got %s", subAgent.config.Mode)
	}

	if subAgent.config.Isolated != false {
		t.Error("Expected Isolated to be false")
	}

	if len(subAgent.config.ToolAllowlist) != 1 {
		t.Errorf("Expected 1 allowlist item, got %d", len(subAgent.config.ToolAllowlist))
	}

	if len(subAgent.config.ToolDenylist) != 1 {
		t.Errorf("Expected 1 denylist item, got %d", len(subAgent.config.ToolDenylist))
	}
}

func TestSubAgentPresetReadOnly(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(
		SubAgentConfig{Agent: agent, Goal: "test"},
		SubAgentReadOnly(),
	)

	// Check that write tools are denied
	if !containsStr(subAgent.config.ToolDenylist, "memory_save") {
		t.Error("Expected memory_save to be in denylist for read-only mode")
	}
}

func TestSubAgentPresetQuick(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(
		SubAgentConfig{Agent: agent, Goal: "test"},
		SubAgentQuick(),
	)

	if subAgent.config.MaxTurns != 3 {
		t.Errorf("Expected MaxTurns 3 for quick mode, got %d", subAgent.config.MaxTurns)
	}
}

func TestSubAgentStateTransitions(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 1,
	})

	// Initial state
	if subAgent.GetState() != SubAgentStatePending {
		t.Errorf("Expected Pending, got %s", subAgent.GetState())
	}

	// Note: We can't easily test Run without a full Service setup
	// State transitions are tested in integration tests
}

func TestSubAgentGetResult(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 1,
	})

	// Before execution
	result, err := subAgent.GetResult()
	if result != nil || err != nil {
		t.Error("Expected nil result and error before execution")
	}
}

func TestSubAgentPause(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 1,
	})

	// Pause before running
	err := subAgent.Pause()
	if err == nil {
		t.Error("Expected error when pausing non-running subagent")
	}
}

func TestFilterTools(t *testing.T) {
	tools := []interface{}{
		domain.ToolDefinition{Type: "function", Function: domain.ToolFunction{Name: "tool1"}},
		domain.ToolDefinition{Type: "function", Function: domain.ToolFunction{Name: "tool2"}},
		domain.ToolDefinition{Type: "function", Function: domain.ToolFunction{Name: "tool3"}},
	}

	// Convert to proper type
	var toolDefs []domain.ToolDefinition
	for _, t := range tools {
		toolDefs = append(toolDefs, t.(domain.ToolDefinition))
	}

	tests := []struct {
		name      string
		allowlist []string
		denylist  []string
		expected  int
	}{
		{"no filter", nil, nil, 3},
		{"allowlist only", []string{"tool1", "tool2"}, nil, 2},
		{"denylist only", nil, []string{"tool3"}, 2},
		{"both", []string{"tool1", "tool2", "tool3"}, []string{"tool2"}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterTools(toolDefs, tt.allowlist, tt.denylist)
			if len(result) != tt.expected {
				t.Errorf("Expected %d tools, got %d", tt.expected, len(result))
			}
		})
	}
}

func TestCopyMap(t *testing.T) {
	original := map[string]interface{}{
		"key1": "value1",
		"key2": 123,
	}

	copied := copyMap(original)

	// Verify copy is equal
	if len(copied) != len(original) {
		t.Error("Copy should have same length")
	}

	// Verify it's a true copy
	copied["key1"] = "modified"
	if original["key1"] == "modified" {
		t.Error("Modifying copy should not affect original")
	}
}

func TestFormatContext(t *testing.T) {
	ctx := map[string]interface{}{
		"name":  "test",
		"count": 42,
	}

	result := formatContext(ctx)

	if result == "" {
		t.Error("Expected non-empty formatted context")
	}
}

func TestSubAgentGetCurrentTurn(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 5,
	})

	// Before execution
	if subAgent.GetCurrentTurn() != 0 {
		t.Errorf("Expected current turn 0 before execution, got %d", subAgent.GetCurrentTurn())
	}
}

func TestSubAgentSession(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 5,
	})

	session := subAgent.GetSession()
	if session == nil {
		t.Fatal("Expected non-nil session")
	}

	if session.GetID() == "" {
		t.Error("Expected session to have ID")
	}
}

func TestSubAgentWithParentSession(t *testing.T) {
	agent := NewAgent("test-agent")
	parentSession := NewSession(agent.ID())
	parentSession.Context = map[string]interface{}{
		"parent_key": "parent_value",
	}

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:         agent,
		Goal:          "test goal",
		MaxTurns:      5,
		Isolated:      true,
		ParentSession: parentSession,
	})

	session := subAgent.GetSession()

	// Should have copied parent context
	if session.Context["parent_key"] != "parent_value" {
		t.Error("Expected parent context to be inherited")
	}
}

func TestSubAgentTimeout(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 1,
	})

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Without a real service, Run will fail quickly
	// This tests the context handling
	_, err := subAgent.Run(ctx)

	// We expect an error since Service is nil
	if err == nil {
		t.Error("Expected error with nil service")
	}
}

func TestSubAgentRunAsync(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 1,
	})

	ctx := context.Background()
	eventChan := subAgent.RunAsync(ctx)

	// Should receive at least one event (error event since Service is nil)
	eventCount := 0
	for range eventChan {
		eventCount++
	}

	if eventCount == 0 {
		t.Error("Expected at least one event from RunAsync")
	}
}

func TestSubAgentCancel(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 10,
	})

	// Cancel before running
	err := subAgent.Cancel()
	if err == nil {
		t.Error("Expected error when cancelling non-running subagent")
	}
}

func TestSubAgentStop(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 10,
	})

	// Stop is an alias for Cancel
	err := subAgent.Stop()
	if err == nil {
		t.Error("Expected error when stopping non-running subagent")
	}
}

func TestSubAgentIsTerminal(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 1,
	})

	// Pending state is not terminal
	if subAgent.IsTerminal() {
		t.Error("Pending state should not be terminal")
	}

	// Run to completion (will fail due to no service)
	subAgent.Run(context.Background())

	// Now should be terminal
	if !subAgent.IsTerminal() {
		t.Error("Failed state should be terminal")
	}
}

func TestSubAgentGetDuration(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 1,
	})

	// Before execution, duration should be 0
	if subAgent.GetDuration() != 0 {
		t.Error("Expected 0 duration before execution")
	}

	// Run to completion
	subAgent.Run(context.Background())

	// After execution, duration should be > 0
	if subAgent.GetDuration() <= 0 {
		t.Error("Expected positive duration after execution")
	}
}

func TestSubAgentName(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent: agent,
		Goal:  "test goal",
	})

	if subAgent.Name() != "test-agent" {
		t.Errorf("Expected name 'test-agent', got '%s'", subAgent.Name())
	}
}

func TestSubAgentID(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent: agent,
		Goal:  "test goal",
	})

	if subAgent.ID() == "" {
		t.Error("Expected non-empty ID")
	}
}

func TestSubAgentProgressChan(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 1,
	})

	progressChan := subAgent.ProgressChan()
	if progressChan == nil {
		t.Error("Expected non-nil progress channel")
	}
}

func TestSubAgentWithTimeout(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(
		SubAgentConfig{Agent: agent, Goal: "test"},
		WithSubAgentTimeout(5*time.Second),
	)

	if subAgent.config.Timeout != 5*time.Second {
		t.Errorf("Expected 5s timeout, got %v", subAgent.config.Timeout)
	}
}

func TestSubAgentWithRetry(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(
		SubAgentConfig{Agent: agent, Goal: "test"},
		WithSubAgentRetry(3),
	)

	if subAgent.config.RetryOnFailure != 3 {
		t.Errorf("Expected 3 retries, got %d", subAgent.config.RetryOnFailure)
	}
}

func TestSubAgentWithProgressCallback(t *testing.T) {
	agent := NewAgent("test-agent")

	var receivedProgress []SubAgentProgress
	callback := func(p SubAgentProgress) {
		receivedProgress = append(receivedProgress, p)
	}

	subAgent := NewSubAgent(
		SubAgentConfig{Agent: agent, Goal: "test", MaxTurns: 1},
		WithSubAgentProgressCallback(callback),
	)

	if subAgent.config.ProgressCb == nil {
		t.Error("Expected progress callback to be set")
	}
}

func TestSubAgentStates(t *testing.T) {
	states := []SubAgentState{
		SubAgentStatePending,
		SubAgentStateRunning,
		SubAgentStateCompleted,
		SubAgentStateFailed,
		SubAgentStatePaused,
		SubAgentStateCancelled,
		SubAgentStateTimeout,
	}

	// Verify all states are distinct
	seen := make(map[SubAgentState]bool)
	for _, s := range states {
		if seen[s] {
			t.Errorf("Duplicate state: %s", s)
		}
		seen[s] = true
	}
}

func TestSubAgentWait(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 1,
	})

	// Run in background
	go subAgent.Run(context.Background())

	// Wait for completion
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := subAgent.Wait(ctx)

	// Should complete (with error due to no service)
	if err == nil {
		t.Log("Result:", result)
	}

	// State should be terminal
	if !subAgent.IsTerminal() {
		t.Error("Expected terminal state after Wait")
	}
}

func TestSubAgentShortTimeout(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(
		SubAgentConfig{Agent: agent, Goal: "test"},
		SubAgentShortTimeout(),
	)

	if subAgent.config.Timeout != 30*time.Second {
		t.Errorf("Expected 30s timeout, got %v", subAgent.config.Timeout)
	}
}

func TestSubAgentMediumTimeout(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(
		SubAgentConfig{Agent: agent, Goal: "test"},
		SubAgentMediumTimeout(),
	)

	if subAgent.config.Timeout != 2*time.Minute {
		t.Errorf("Expected 2m timeout, got %v", subAgent.config.Timeout)
	}
}

func TestSubAgentLongTimeout(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(
		SubAgentConfig{Agent: agent, Goal: "test"},
		SubAgentLongTimeout(),
	)

	if subAgent.config.Timeout != 10*time.Minute {
		t.Errorf("Expected 10m timeout, got %v", subAgent.config.Timeout)
	}
}

func TestSubAgentProgress(t *testing.T) {
	agent := NewAgent("test-agent")

	subAgent := NewSubAgent(SubAgentConfig{
		Agent:    agent,
		Goal:     "test goal",
		MaxTurns: 5,
	})

	progress := SubAgentProgress{
		SubagentID:   subAgent.ID(),
		SubagentName: subAgent.Name(),
		CurrentTurn:  2,
		MaxTurns:     5,
		State:        SubAgentStateRunning,
		Goal:         "test goal",
		ElapsedTime:  time.Second,
		Message:      "Test progress",
	}

	// Verify progress struct fields
	if progress.SubagentID == "" {
		t.Error("Progress SubagentID should not be empty")
	}
	if progress.CurrentTurn != 2 {
		t.Errorf("Expected CurrentTurn 2, got %d", progress.CurrentTurn)
	}
}
