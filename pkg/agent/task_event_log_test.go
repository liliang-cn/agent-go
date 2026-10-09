package agent

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	taskpkg "github.com/liliang-cn/agent-go/v3/pkg/task"
)

// A session's turns share one task row. Its event log keeps the recent end
// and clips what each event carries, so a long conversation does not make
// every event rewrite all of it; and an event never touches the row's frames.
func TestTaskEventLogIsBoundedAndLeavesFramesAlone(t *testing.T) {
	svc, err := NewService(nil, nil, nil, filepath.Join(t.TempDir(), "agent.db"), nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	const id = "long-session-task"
	now := time.Now()
	svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeStart, Content: "start", Timestamp: now})

	frames := []taskpkg.Frame{{SessionID: "s", Message: domain.Message{Role: "user", Content: "hello", TaskID: id}}}
	if err := svc.store.updateTask(id, func(task *UnifiedTask) *UnifiedTask { task.Frames = frames; return task }); err != nil {
		t.Fatalf("write frames: %v", err)
	}

	big := strings.Repeat("磁盘 ", 5000)
	for i := 0; i < 500; i++ {
		svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeToolCall, ToolName: "bash", ToolArgs: map[string]interface{}{"i": i}, Timestamp: now})
		svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeToolResult, ToolName: "bash", ToolResult: map[string]interface{}{"out": big}, Timestamp: now})
	}
	svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeComplete, Content: "done", Timestamp: now})

	task, err := svc.store.GetTask(id)
	if err != nil || task == nil {
		t.Fatalf("GetTask: %v %v", task, err)
	}
	if len(task.Events) != maxStoredTaskEvents {
		t.Fatalf("kept %d events, want %d", len(task.Events), maxStoredTaskEvents)
	}
	if last := task.Events[len(task.Events)-1]; last.Type != string(EventTypeComplete) {
		t.Fatalf("last event = %s, want the newest (complete)", last.Type)
	}
	for _, e := range task.Events {
		if e.Runtime == nil || e.Runtime.ToolResult == nil {
			continue
		}
		if s, ok := e.Runtime.ToolResult.(string); !ok || len(s) > maxStoredEventText+len("…") {
			t.Fatalf("a long tool result was stored whole: %T len %d", e.Runtime.ToolResult, len(s))
		}
	}
	if task.Status != taskpkg.StatusCompleted || task.Output != "done" {
		t.Fatalf("status %q output %q, want completed / done", task.Status, task.Output)
	}
	if len(task.Frames) != 1 || task.Frames[0].Message.Content != "hello" {
		t.Fatalf("frames changed under event writes: %+v", task.Frames)
	}
}

func TestClipStoredKeepsWholeRunes(t *testing.T) {
	s := clipStored(strings.Repeat("盘", maxStoredEventText))
	if !strings.HasSuffix(s, "…") || !utf8ValidString(s) {
		t.Fatalf("clip broke a rune or lost the mark: %q", s[len(s)-8:])
	}
}

func utf8ValidString(s string) bool { return strings.ToValidUTF8(s, "�") == s }

// A task written before the event log had its own table keeps the log in its
// row. The next event reads it from there and moves it out, losing nothing.
func TestTaskEventLogMovesOutOfAnOldRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent.db")
	svc, err := NewService(nil, nil, nil, dbPath, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	const id = "old-row-task"
	now := time.Now()
	svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeStart, Content: "start", Timestamp: now})
	svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeToolCall, ToolName: "look", Timestamp: now})

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE tasks SET events = (SELECT events FROM task_event_logs WHERE task_id = ?) WHERE id = ?`, id, id); err != nil {
		t.Fatalf("put log back in row: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM task_event_logs WHERE task_id = ?`, id); err != nil {
		t.Fatalf("drop side log: %v", err)
	}

	svc.persistRunTaskEvent(nil, id, &Event{Type: EventTypeComplete, Content: "done", Timestamp: now})

	task, err := svc.store.GetTask(id)
	if err != nil || task == nil {
		t.Fatalf("GetTask: %v %v", task, err)
	}
	var types []string
	for _, e := range task.Events {
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != strings.Join([]string{string(EventTypeStart), string(EventTypeToolCall), string(EventTypeComplete)}, ",") {
		t.Fatalf("events after the move = %v", types)
	}
	var inRow interface{}
	if err := db.QueryRow(`SELECT events FROM tasks WHERE id = ?`, id).Scan(&inRow); err != nil || inRow != nil {
		t.Fatalf("the row still carries a log: %v %v", inRow, err)
	}
}
