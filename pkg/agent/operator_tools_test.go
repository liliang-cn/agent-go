package agent

import (
	"context"
	"strings"
	"testing"
)

func TestRegisterOperatorTools_StartSendAndStopPTYSession(t *testing.T) {
	globalOperatorSessions = &operatorSessionManager{sessions: make(map[string]*operatorSession)}

	svc := &Service{
		agent:        NewAgentWithConfig("Operator", "operator", nil),
		toolRegistry: NewToolRegistry(),
	}
	registerOperatorTools(svc)

	if !svc.agent.HasTool("start_pty_session") {
		t.Fatal("expected start_pty_session to be registered on Operator")
	}
	if !svc.agent.HasTool("send_pty_input") {
		t.Fatal("expected send_pty_input to be registered on Operator")
	}
	if !svc.agent.HasTool("interrupt_pty_session") {
		t.Fatal("expected interrupt_pty_session to be registered on Operator")
	}
	if !svc.agent.HasTool("start_coding_agent_session") {
		t.Fatal("expected start_coding_agent_session to be registered on Operator")
	}
	if !svc.agent.HasTool("run_coding_agent_once") {
		t.Fatal("expected run_coding_agent_once to be registered on Operator")
	}

	startedRaw, err := svc.toolRegistry.Call(context.Background(), "start_pty_session", map[string]interface{}{
		"command": "/bin/cat",
		"wait_ms": 100,
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("pty sessions are not permitted in this test environment: %v", err)
		}
		t.Fatalf("start_pty_session failed: %v", err)
	}
	started, ok := startedRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected start_pty_session result: %#v", startedRaw)
	}
	sessionID, _ := started["session_id"].(string)
	if strings.TrimSpace(sessionID) == "" {
		t.Fatalf("expected non-empty session_id, got %#v", started)
	}

	sentRaw, err := svc.toolRegistry.Call(context.Background(), "send_pty_input", map[string]interface{}{
		"session_id": sessionID,
		"input":      "hello from operator",
		"wait_ms":    150,
	})
	if err != nil {
		t.Fatalf("send_pty_input failed: %v", err)
	}
	sent, ok := sentRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected send_pty_input result: %#v", sentRaw)
	}
	output, _ := sent["output"].(string)
	if !strings.Contains(output, "hello from operator") {
		t.Fatalf("expected PTY output to contain sent text, got %q", output)
	}

	stoppedRaw, err := svc.toolRegistry.Call(context.Background(), "stop_pty_session", map[string]interface{}{
		"session_id": sessionID,
		"force":      true,
		"wait_ms":    150,
	})
	if err != nil {
		t.Fatalf("stop_pty_session failed: %v", err)
	}
	stopped, ok := stoppedRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected stop_pty_session result: %#v", stoppedRaw)
	}
	status, _ := stopped["status"].(string)
	if status == "running" {
		t.Fatalf("expected stopped PTY session to no longer be running, got %#v", stopped)
	}
}

func TestResolveCodingAgentCommand(t *testing.T) {
	tests := []struct {
		provider string
		wantCmd  string
		wantErr  bool
	}{
		{provider: "claude", wantCmd: "claude"},
		{provider: "gemini", wantCmd: "gemini"},
		{provider: "codex", wantCmd: "codex"},
		{provider: "opencode", wantCmd: "opencode"},
		{provider: "custom", wantErr: true},
	}

	for _, tt := range tests {
		got, _, err := resolveCodingAgentCommand(tt.provider, "", nil)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("expected error for provider %q", tt.provider)
			}
			continue
		}
		if err != nil {
			t.Fatalf("resolveCodingAgentCommand(%q) failed: %v", tt.provider, err)
		}
		if got != tt.wantCmd {
			t.Fatalf("resolveCodingAgentCommand(%q) = %q, want %q", tt.provider, got, tt.wantCmd)
		}
	}
}

func TestRegisterOperatorTools_Metadata(t *testing.T) {
	svc := &Service{
		agent:        NewAgentWithConfig("Operator", "operator", nil),
		toolRegistry: NewToolRegistry(),
	}
	registerOperatorTools(svc)

	readMeta := svc.toolRegistry.MetadataOf("get_pty_session")
	if !readMeta.ReadOnly || !readMeta.ConcurrencySafe || readMeta.InterruptBehavior != InterruptBehaviorCancel {
		t.Fatalf("unexpected get_pty_session metadata: %+v", readMeta)
	}

	stopMeta := svc.toolRegistry.MetadataOf("stop_pty_session")
	if !stopMeta.Destructive || stopMeta.InterruptBehavior != InterruptBehaviorBlock {
		t.Fatalf("unexpected stop_pty_session metadata: %+v", stopMeta)
	}

	runOnceMeta := svc.toolRegistry.MetadataOf("run_coding_agent_once")
	if runOnceMeta.InterruptBehavior != InterruptBehaviorBlock {
		t.Fatalf("unexpected run_coding_agent_once metadata: %+v", runOnceMeta)
	}
}
