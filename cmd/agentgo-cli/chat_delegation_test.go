package main

import (
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func TestBuildDelegatedTaskInstruction(t *testing.T) {
	tasks := []delegatedTask{
		{AgentName: "Responder", Instruction: "first"},
		{AgentName: "Writer", Instruction: "second"},
	}

	if got := buildDelegatedTaskInstruction(tasks, 0, "ignored"); got != "first" {
		t.Fatalf("first task should keep original instruction, got %q", got)
	}

	got := buildDelegatedTaskInstruction(tasks, 1, "result from first")
	want := "Previous result from @Responder:\nresult from first\n\nYour task:\nsecond"
	if got != want {
		t.Fatalf("unexpected chained instruction:\nwant: %q\ngot:  %q", want, got)
	}
}

// partialEvent builds a TaskEventTypeRuntime carrying an EventTypePartial chunk.
func partialEvent(taskID, agentName, content string) *agent.TaskEvent {
	return &agent.TaskEvent{
		TaskID:    taskID,
		Type:      agent.TaskEventTypeRuntime,
		AgentName: agentName,
		Runtime: &agent.Event{
			Type:      agent.EventTypePartial,
			AgentName: agentName,
			Content:   content,
		},
	}
}

func TestChatTaskStreamRendererStreamsPartials(t *testing.T) {
	var buf strings.Builder
	renderer := &chatTaskStreamRenderer{writeStream: func(s string) { buf.WriteString(s) }}

	const taskID = "abcdef1234567890"
	renderer.Handle(partialEvent(taskID, "Operator", "hello "))
	renderer.Handle(partialEvent(taskID, "Operator", "world"))
	renderer.Flush()

	got := buf.String()
	wantPrefix := "💭 [abcdef12] @Operator ▸ hello world\n"
	if got != wantPrefix {
		t.Fatalf("streaming output mismatch:\nwant: %q\ngot:  %q", wantPrefix, got)
	}
	if !renderer.everStreamed {
		t.Fatal("everStreamed should be true after partial events")
	}
}

func TestChatTaskStreamRendererSwitchesAgents(t *testing.T) {
	var buf strings.Builder
	renderer := &chatTaskStreamRenderer{writeStream: func(s string) { buf.WriteString(s) }}

	const taskID = "11111111deadbeef"
	renderer.Handle(partialEvent(taskID, "Operator", "step 1"))
	renderer.Handle(partialEvent(taskID, "Responder", "step 2"))
	renderer.Flush()

	got := buf.String()
	want := "💭 [11111111] @Operator ▸ step 1\n💭 [11111111] @Responder ▸ step 2\n"
	if got != want {
		t.Fatalf("agent switch output mismatch:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestChatTaskStreamRendererIgnoresEmptyPartials(t *testing.T) {
	var buf strings.Builder
	renderer := &chatTaskStreamRenderer{writeStream: func(s string) { buf.WriteString(s) }}

	renderer.Handle(partialEvent("aaaaaaaa11111111", "Operator", ""))
	renderer.Flush()

	if buf.Len() != 0 {
		t.Fatalf("empty partial should not produce output, got %q", buf.String())
	}
	if renderer.everStreamed {
		t.Fatal("everStreamed should remain false after empty partial")
	}
}
