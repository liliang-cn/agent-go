package agent

import (
	"path/filepath"
	"testing"
	"time"
)

// Streamed fragments stay out of the task row: each one used to rewrite the
// whole row, which made a turn cost the square of its length. The steps of
// the run and its answer are still recorded.
func TestTaskRowSkipsStreamFragments(t *testing.T) {
	svc, err := NewService(nil, nil, nil, filepath.Join(t.TempDir(), "agent.db"), nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	const id = "fragment-task"
	now := time.Now()
	svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeStart, Content: "start", Timestamp: now})
	for i := 0; i < 300; i++ {
		svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeThinking, Content: "tok", Timestamp: now})
		svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypePartial, Content: "tok", Timestamp: now})
	}
	svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeToolCall, ToolName: "look", Timestamp: now})
	svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeComplete, Content: "the answer", Timestamp: now})

	task, err := svc.store.GetTask(id)
	if err != nil || task == nil {
		t.Fatalf("GetTask: %v %v", task, err)
	}
	for _, e := range task.Events {
		if isStreamFragment(EventType(e.Type)) {
			t.Fatalf("a stream fragment was persisted: %+v", e)
		}
	}
	if len(task.Events) != 3 {
		t.Fatalf("persisted %d events, want start, tool call and complete", len(task.Events))
	}
	if task.Output != "the answer" {
		t.Fatalf("output = %q, want the answer", task.Output)
	}
}
