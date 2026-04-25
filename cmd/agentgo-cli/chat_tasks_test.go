package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	os.Stdout = original
	return <-done
}

func TestRenderChatTaskEventCompleted(t *testing.T) {
	output := captureStdout(t, func() {
		renderChatTaskEvent(&agent.TaskEvent{
			TaskID:    "12345678-task",
			Type:      agent.TaskEventTypeCompleted,
			AgentName: "Orchestrator",
			Message:   "done",
			Timestamp: time.Now(),
		})
	})

	if !strings.Contains(output, "✅ [12345678] Task completed by @Orchestrator") {
		t.Fatalf("missing completion header: %q", output)
	}
	if !strings.Contains(output, "done") {
		t.Fatalf("missing completion body: %q", output)
	}
}

func TestRenderRuntimeTaskEventToolCall(t *testing.T) {
	output := captureStdout(t, func() {
		renderRuntimeTaskEvent("task1234", &agent.Event{
			Type:      agent.EventTypeToolCall,
			AgentName: "Responder",
			ToolName:  "mcp_websearch_websearch_ai_summary",
			Timestamp: time.Now(),
		})
	})

	if !strings.Contains(output, "🛠 [task1234] @Responder using mcp_websearch_websearch_ai_summary") {
		t.Fatalf("unexpected tool call output: %q", output)
	}
}

func TestShouldRenderChatTaskRuntimeEvent(t *testing.T) {
	oldDebug, oldVerbose := debug, verbose
	t.Cleanup(func() {
		debug = oldDebug
		verbose = oldVerbose
	})

	debug = false
	verbose = false
	if shouldRenderChatTaskRuntimeEvent() {
		t.Fatal("expected runtime task events to be hidden in normal mode")
	}

	debug = true
	if !shouldRenderChatTaskRuntimeEvent() {
		t.Fatal("expected runtime task events to be shown in debug mode")
	}

	debug = false
	verbose = true
	if !shouldRenderChatTaskRuntimeEvent() {
		t.Fatal("expected runtime task events to be shown in verbose mode")
	}
}
