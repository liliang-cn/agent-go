package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The whole point, and the one thing that separates a background task from a
// sub-agent: it survives the turn that started it. A sub-agent runs under its
// parent's context and dies with it; a task started and then abandoned by its
// caller has to keep going.
func TestBackgroundTaskOutlivesTheCallerContext(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("background").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	// A context that is cancelled the moment the "turn" ends.
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	task, err := svc.StartBackgroundTask(callerCtx, "Do the long thing.")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != BackgroundRunning {
		t.Fatalf("status = %q, want running", task.Status)
	}
	waitForActiveRuns(t, svc, 1)

	// The turn ends. The task must not.
	cancelCaller()
	time.Sleep(150 * time.Millisecond)
	if got, _ := svc.BackgroundTask(task.ID); got == nil || got.Status != BackgroundRunning {
		t.Fatalf("the task died with its caller: %+v", got)
	}

	llm.releaseAll()
	waitForBackground(t, svc, task.ID)
	got, _ := svc.BackgroundTask(task.ID)
	if got.Status != BackgroundCompleted {
		t.Fatalf("status = %q (%s), want completed", got.Status, got.Err)
	}
	if got.Result == "" {
		t.Error("a completed task kept no result")
	}
	if got.EndedAt == nil || got.Duration() <= 0 {
		t.Error("a finished task should know how long it took")
	}
}

// It is a separate conversation. Putting its turns into the session that
// started it would put work the user never saw into the next thing they say.
func TestBackgroundTaskGetsItsOwnSession(t *testing.T) {
	svc, err := New("bg-session").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&scriptedLLM{finishAt: 0}).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	parent := "parent-session"
	ctx := withCurrentSessionID(withCurrentTenant(context.Background(), "acme"), parent)
	task, err := svc.StartBackgroundTask(ctx, "Work.")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID == "" || task.SessionID == parent {
		t.Errorf("SessionID = %q, want a fresh one", task.SessionID)
	}
	if task.ParentSessionID != parent {
		t.Errorf("ParentSessionID = %q, want %q", task.ParentSessionID, parent)
	}
	// Work started on somebody's behalf stays theirs, for limits and for
	// CancelTenant.
	if task.Tenant != "acme" {
		t.Errorf("Tenant = %q, want the caller's", task.Tenant)
	}
	waitForBackground(t, svc, task.ID)
}

// A running task can be stopped, and a stop is an outcome rather than a
// failure.
func TestBackgroundTaskCanBeCancelled(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("bg-cancel").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	task, err := svc.StartBackgroundTask(context.Background(), "Long thing.")
	if err != nil {
		t.Fatal(err)
	}
	waitForActiveRuns(t, svc, 1)

	if !svc.CancelBackgroundTask(task.ID) {
		t.Fatal("CancelBackgroundTask reported it stopped nothing")
	}
	waitForBackground(t, svc, task.ID)
	got, _ := svc.BackgroundTask(task.ID)
	if got.Status != BackgroundCancelled && got.Status != BackgroundFailed {
		t.Fatalf("status after cancel = %q", got.Status)
	}
	// Cancelling a finished task is not an error, it just stops nothing.
	if svc.CancelBackgroundTask(task.ID) {
		t.Error("cancelling a finished task reported a stop")
	}
	if svc.CancelBackgroundTask("no-such-id") {
		t.Error("cancelling an unknown id reported a stop")
	}
}

// The ceiling is a memory ceiling: each task holds a whole conversation.
func TestBackgroundTasksAreBounded(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("bg-bound").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithBackgroundTasks(2).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for i := 0; i < 2; i++ {
		if _, err := svc.StartBackgroundTask(context.Background(), "Work."); err != nil {
			t.Fatalf("task %d: %v", i, err)
		}
	}
	waitForActiveRuns(t, svc, 2)
	if _, err := svc.StartBackgroundTask(context.Background(), "One too many."); !errors.Is(err, ErrTooManyBackgroundTasks) {
		t.Fatalf("err = %v, want ErrTooManyBackgroundTasks", err)
	}

	llm.releaseAll()
	waitForActiveRuns(t, svc, 0)
	if _, err := svc.StartBackgroundTask(context.Background(), "Now there is room."); err != nil {
		t.Fatalf("a service back under its ceiling refused: %v", err)
	}
}

// The tools are off unless the author turned them on: a background task is a
// whole run, and an agent that can start them uninvited can spend in a loop.
func TestBackgroundToolsAreOptIn(t *testing.T) {
	off, err := New("no-bg").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&scriptedLLM{finishAt: 0}).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()
	for _, name := range []string{"background_start", "background_check", "background_cancel"} {
		if off.toolRegistry.Has(name) {
			t.Errorf("%s is registered on a service that did not ask for it", name)
		}
	}

	on, err := New("with-bg").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&scriptedLLM{finishAt: 0}).WithBackgroundTasks(0).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer on.Close()
	for _, name := range []string{"background_start", "background_check", "background_cancel"} {
		if !on.toolRegistry.Has(name) {
			t.Errorf("%s missing after WithBackgroundTasks", name)
		}
	}
}

// A task in flight has no result, and must not be reported as if it had one:
// a model reading a partial answer tells the user it is the answer.
func TestRunningTaskReportsNoResult(t *testing.T) {
	running := &BackgroundTask{ID: "a", Goal: "g", Status: BackgroundRunning, StartedAt: time.Now()}
	payload := backgroundTaskPayload(running, nil)
	if _, ok := payload["result"]; ok {
		t.Error("a running task reported a result")
	}
	if note, _ := payload["note"].(string); !strings.Contains(note, "no result yet") {
		t.Errorf("note = %q, want it to say there is no result yet", note)
	}

	done := &BackgroundTask{ID: "b", Goal: "g", Status: BackgroundCompleted, Result: "the answer", StartedAt: time.Now()}
	if got, _ := backgroundTaskPayload(done, nil)["result"].(string); got != "the answer" {
		t.Errorf("result = %q", got)
	}
}

// Close must not leave a task running through a store it has released.
func TestCloseStopsBackgroundTasks(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("bg-close").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartBackgroundTask(context.Background(), "Long thing."); err != nil {
		t.Fatal(err)
	}
	waitForActiveRuns(t, svc, 1)

	done := make(chan struct{})
	go func() { _ = svc.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung on a background task")
	}
	if _, err := svc.StartBackgroundTask(context.Background(), "After close."); !errors.Is(err, ErrServiceClosed) {
		t.Errorf("starting a task on a closed service: err = %v, want ErrServiceClosed", err)
	}
}

// Finished tasks are pruned, running ones never are: a handle that stops
// answering while the work is still going is worse than none.
func TestPruneKeepsRunningTasks(t *testing.T) {
	reg := &backgroundRegistry{tasks: map[string]*BackgroundTask{}, max: 100, keep: 2}
	add := func(id string, status BackgroundStatus) {
		reg.tasks[id] = &BackgroundTask{ID: id, Status: status}
		reg.order = append(reg.order, id)
	}
	add("old1", BackgroundCompleted)
	add("old2", BackgroundCompleted)
	add("running", BackgroundRunning)
	add("new1", BackgroundCompleted)
	add("new2", BackgroundCompleted)

	reg.mu.Lock()
	reg.pruneLocked()
	reg.mu.Unlock()

	if _, ok := reg.tasks["running"]; !ok {
		t.Error("prune dropped a task that was still running")
	}
	if len(reg.tasks) != 3 {
		t.Errorf("kept %d tasks, want 2 finished plus the running one", len(reg.tasks))
	}
	if _, ok := reg.tasks["old1"]; ok {
		t.Error("prune kept the oldest finished task")
	}
}

// waitForBackground waits for a task to reach a terminal status.
func waitForBackground(t *testing.T, svc *Service, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := svc.BackgroundTask(id); ok && got.Status.Done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _ := svc.BackgroundTask(id)
	t.Fatalf("timed out waiting for task %s; status %+v", id, got)
}

var _ = sync.Mutex{}
