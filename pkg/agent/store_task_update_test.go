package agent

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	taskpkg "github.com/liliang-cn/agent-go/v3/pkg/task"
)

// A single run has two goroutines amending the same task row: the runtime
// writes Frames (via SaveSession) while the observer writes Events and Status.
// Both used to do GetTask -> mutate -> SaveTask with no serialisation, and
// SaveTask replaces the whole row — so an interleaved pair lost one side's
// field permanently. In the field that showed up as a completed task whose
// Frames were empty forever, roughly twice in forty runs.
//
// This pins the store contract directly rather than through the timing of a
// real run: concurrent amendments to different fields must all survive.
func TestUpdateTaskConcurrentAmendmentsAllSurvive(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agentgo.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.GetAgentGoDB().GetDB().Close()

	const taskID = "task-concurrent-amend"
	if err := store.SaveTask(&taskpkg.Task{
		ID:     taskID,
		Kind:   taskpkg.KindAgent,
		Status: taskpkg.StatusRunning,
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	const writers = 8
	const perWriter = 12

	var wg sync.WaitGroup

	// Writer A: appends events, the way the observer goroutine does.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = store.updateTask(taskID, func(task *UnifiedTask) *UnifiedTask {
					if task == nil {
						return nil
					}
					task.Events = append(task.Events, taskpkg.Event{
						ID:   fmt.Sprintf("evt-%d-%d", w, i),
						Type: string(EventTypeToolCall),
					})
					return task
				})
			}
		}(w)
	}

	// Writer B: replaces frames, the way the runtime goroutine does via
	// SaveTaskFramesFromSession.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = store.updateTask(taskID, func(task *UnifiedTask) *UnifiedTask {
					if task == nil {
						return nil
					}
					task.Frames = []taskpkg.Frame{
						{SessionID: "s", Message: domain.Message{Role: "user", Content: "goal"}},
						{SessionID: "s", Message: domain.Message{Role: "assistant", Content: "answer"}},
					}
					return task
				})
			}
		}(w)
	}

	wg.Wait()

	got, err := store.GetTask(taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatal("task vanished")
	}
	if want := writers * perWriter; len(got.Events) != want {
		t.Errorf("lost events: got %d, want %d — a read-modify-write was clobbered",
			len(got.Events), want)
	}
	if len(got.Frames) != 2 {
		t.Errorf("lost frames: got %d, want 2 — the frame write was clobbered by a concurrent amendment",
			len(got.Frames))
	}
}
