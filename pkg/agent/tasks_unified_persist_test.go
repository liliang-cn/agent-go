package agent

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	taskpkg "github.com/liliang-cn/agent-go/v3/pkg/task"
)

func newPersistTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// The lost update behind TestTaskServiceResumeFromCheckpointReplaysMessages.
//
// updateAsyncTask mirrors the in-memory task onto its row with a bare
// `go persistUnifiedTask(id)` per mutation, and persistUnifiedTask used to take
// its snapshot, then query the message store, then write the whole row. Two of
// those in flight could invert:
//
//	P1  snapshot{running, output:""} ... still in ListMessagesForTask ...
//	P2  snapshot{completed, output:"final: 42"} -> write
//	P1                                          -> write   (stale, and final)
//
// leaving the row running with no output for good. CI saw it constantly and a
// laptop almost never, because the width of that window is the latency of a
// database read.
//
// This drives the interleaving directly rather than waiting for it: P1 is held
// at the exact point the snapshot has been taken, P2 is run to completion, and
// only then is P1 released. With the snapshot taken inside the row lock, P2
// cannot start until P1 is done and the last write is the freshest. Without it,
// this fails every time.
func TestPersistUnifiedTaskCannotWriteAStaleSnapshot(t *testing.T) {
	store := newPersistTestStore(t)
	manager := NewManager(store)

	const taskID = "task-persist-race"
	manager.upsertAsyncTask(&AsyncTask{
		ID:        taskID,
		TaskID:    taskID,
		SessionID: "session-1",
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusRunning,
		AgentName: "Operator",
		Prompt:    "what is the answer?",
		CreatedAt: time.Now(),
	})

	// Hold the FIRST writer right after it has taken its snapshot, and let
	// every later one straight through. The gate has to be non-blocking for
	// the others — a sync.Once here would make the second writer wait on the
	// first inside Do, which serialises exactly the pair the test is trying to
	// let race.
	var (
		first    int32
		held     = make(chan struct{})
		release  = make(chan struct{})
		hookDone = make(chan struct{})
	)
	persistUnifiedTaskSnapshotHook = func(string) {
		if !atomic.CompareAndSwapInt32(&first, 0, 1) {
			return
		}
		close(held)
		<-release
		close(hookDone)
	}
	t.Cleanup(func() { persistUnifiedTaskSnapshotHook = nil })

	// P1 carries the stale "running, no output" snapshot.
	p1 := make(chan struct{})
	go func() {
		manager.persistUnifiedTask(taskID)
		close(p1)
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the first writer never reached the snapshot point")
	}

	// The run finishes while P1 is parked.
	finishedAt := time.Now()
	manager.taskMu.Lock()
	task := manager.asyncTasks[taskID]
	task.Status = AsyncTaskStatusCompleted
	task.ResultText = "final: 42"
	task.FinishedAt = &finishedAt
	manager.taskMu.Unlock()

	// P2 writes the terminal state. With the fix it blocks on the row lock
	// until P1 lets go, so run it in the background and release P1 once it is
	// under way.
	p2 := make(chan struct{})
	go func() {
		manager.persistUnifiedTask(taskID)
		close(p2)
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)

	// Both writers must be all the way done before the row is read, or the
	// test races the very write it is trying to catch.
	for _, done := range []chan struct{}{p1, p2} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a writer never completed")
		}
	}
	<-hookDone

	got, err := store.GetTask(taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatal("task row missing")
	}
	if string(got.Status) != string(AsyncTaskStatusCompleted) || got.Output != "final: 42" {
		t.Fatalf("a stale snapshot overwrote the terminal state: status=%s output=%q",
			got.Status, got.Output)
	}
}

// The mirror builds its row from the AsyncTask, which knows nothing about
// stats, lineage or the runtime's frames. Before this writer went through the
// row lock it replaced the whole row, so a routine status update erased them —
// the same clobbering c848ae1 fixed for the four writers on the run path.
func TestPersistUnifiedTaskKeepsFieldsItDoesNotOwn(t *testing.T) {
	store := newPersistTestStore(t)
	manager := NewManager(store)

	const taskID = "task-persist-carryover"
	manager.upsertAsyncTask(&AsyncTask{
		ID:        taskID,
		TaskID:    taskID,
		SessionID: "session-1",
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusRunning,
		AgentName: "Operator",
		Prompt:    "what is the answer?",
		CreatedAt: time.Now(),
	})

	// Something else on the run path writes the fields the mirror never sets.
	if err := store.UpdateTask(taskID, func(existing *UnifiedTask) *UnifiedTask {
		if existing == nil {
			t.Fatal("expected the row to exist")
		}
		existing.ParentTaskID = "parent-task"
		existing.Stats = &taskpkg.TaskStats{Rounds: 3}
		return existing
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	// A routine status update from the async side.
	manager.taskMu.Lock()
	manager.asyncTasks[taskID].Status = AsyncTaskStatusCompleted
	manager.asyncTasks[taskID].ResultText = "done"
	manager.taskMu.Unlock()
	manager.persistUnifiedTask(taskID)

	got, err := store.GetTask(taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Output != "done" || string(got.Status) != string(AsyncTaskStatusCompleted) {
		t.Fatalf("the mirror did not write its own fields: status=%s output=%q", got.Status, got.Output)
	}
	if got.ParentTaskID != "parent-task" {
		t.Errorf("lineage erased by a status update: %q", got.ParentTaskID)
	}
	if got.Stats == nil || got.Stats.Rounds != 3 {
		t.Errorf("stats erased by a status update: %+v", got.Stats)
	}
}
