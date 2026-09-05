package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// An idle service still has a great deal to say about itself.
func TestStatusSnapshotDescribesAnIdleService(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("watcher").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	got := svc.StatusSnapshot()
	if got.State != ServiceIdle {
		t.Errorf("State = %q, want %q", got.State, ServiceIdle)
	}
	if got.Agent.Name != "watcher" {
		t.Errorf("Agent.Name = %q, want watcher", got.Agent.Name)
	}
	if len(got.Runs) != 0 {
		t.Errorf("Runs = %d, want none on an idle service", len(got.Runs))
	}
	if len(got.Lints) == 0 {
		t.Error("Lints is empty; every built service gets the baseline set")
	}
	if got.Process != nil {
		t.Error("Process was sampled without WithProcessStats — an unwatched service must not pay for it")
	}
	if got.At.IsZero() {
		t.Error("At is zero")
	}

	// The whole point is that a host can hand this to an HTTP handler.
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("snapshot does not marshal: %v", err)
	}

	withProcess := svc.StatusSnapshot(WithProcessStats())
	if withProcess.Process == nil {
		t.Error("WithProcessStats did not include process figures")
	}
}

// The reading a host actually wants: what is this run doing, right now, read
// from somewhere other than the event stream.
func TestStatusSnapshotReportsARunInFlight(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("watcher").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.Run(context.Background(), "Count the beans.",
			WithSessionID(uuidish()), WithTenant("acme"))
	}()
	waitForActiveRuns(t, svc, 1)

	// A concurrent reader, so -race has something to complain about if
	// publishing and reading ever share unguarded state.
	var wg sync.WaitGroup
	wg.Add(1)
	stop := make(chan struct{})
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = svc.StatusSnapshot()
			}
		}
	}()

	// The gate holds the model turn, so awaiting_model is where this run is
	// parked — wait for that reading rather than for the first one, which is
	// the honest but uninteresting "preparing context, no round yet".
	run := waitForRunStatus(t, svc, func(r RunStatus) bool {
		return r.Stage == TurnStageAwaitingModel
	})
	if run.Goal != "Count the beans." {
		t.Errorf("Goal = %q, want the run's own goal", run.Goal)
	}
	if run.Tenant != "acme" {
		t.Errorf("Tenant = %q, want acme", run.Tenant)
	}
	if run.BackgroundTaskID != "" {
		t.Errorf("an ordinary run is marked as background work: %q", run.BackgroundTaskID)
	}
	if run.RunID == "" {
		t.Error("RunID is empty; it is what CancelRun takes")
	}
	if run.Stage == "" {
		t.Error("Stage is empty on a reported run")
	}
	if run.Round < 1 {
		t.Errorf("Round = %d, want at least 1", run.Round)
	}
	if run.MaxRounds < 1 {
		t.Errorf("MaxRounds = %d, want the run's round budget", run.MaxRounds)
	}
	if run.Duration <= 0 {
		t.Error("Duration is not positive on a run in flight")
	}
	if run.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero on a reported run")
	}
	if !run.Interruptible {
		t.Error("Interruptible is false with no blocking tool in progress")
	}

	// The same reading, addressed by id.
	byID, ok := svc.RunStatus(run.RunID)
	if !ok {
		t.Fatalf("RunStatus(%q) not found while the run is in flight", run.RunID)
	}
	if byID.Goal != run.Goal {
		t.Errorf("RunStatus disagrees with the snapshot: %q vs %q", byID.Goal, run.Goal)
	}
	if snap := svc.StatusSnapshot(); snap.State != ServiceRunning {
		t.Errorf("State = %q with a run in flight, want %q", snap.State, ServiceRunning)
	}

	close(stop)
	wg.Wait()
	llm.releaseAll()
	<-done

	// A run that ended is gone, not stale. Its record is the checkpoint.
	waitForActiveRuns(t, svc, 0)
	if _, ok := svc.RunStatus(run.RunID); ok {
		t.Error("RunStatus still answers for a run that has ended")
	}
	if snap := svc.StatusSnapshot(); snap.State != ServiceIdle {
		t.Errorf("State = %q after the run ended, want %q", snap.State, ServiceIdle)
	}
}

// The bug this replaced: "running" was one bool on a Service that drives many
// runs, so the first of two to finish cleared it while the other was still
// going — and only the collecting entry points ever set it, leaving a service
// busy inside RunStream reporting "idle".
func TestStatusIsDerivedNotFlagged(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("watcher").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	// Half one: a streaming run counts.
	events, err := svc.RunStreamWithOptions(context.Background(), "Stream.", WithSessionID(uuidish()))
	if err != nil {
		t.Fatal(err)
	}
	waitForActiveRuns(t, svc, 1)
	if got := svc.Status(); got != "running" {
		t.Errorf("Status() = %q during RunStream, want running", got)
	}

	// Half two: one run ending does not clear the other.
	second := make(chan struct{})
	go func() {
		defer close(second)
		_, _ = svc.Run(context.Background(), "Collect.", WithSessionID(uuidish()))
	}()
	waitForActiveRuns(t, svc, 2)

	runs := svc.RunStatuses()
	if len(runs) != 2 {
		t.Fatalf("RunStatuses = %d, want 2", len(runs))
	}
	svc.CancelRun(runs[1].RunID)
	<-second
	waitForActiveRuns(t, svc, 1)
	if got := svc.Status(); got != "running" {
		t.Errorf("Status() = %q with one of two runs still in flight, want running", got)
	}
	if !svc.IsRunning() {
		t.Error("IsRunning() is false with a run still in flight")
	}

	llm.releaseAll()
	for range events {
	}
	waitForActiveRuns(t, svc, 0)
	if got := svc.Status(); got != "idle" {
		t.Errorf("Status() = %q with nothing in flight, want idle", got)
	}
}

// A closed service says so, rather than saying "idle" and then refusing.
func TestStatusSnapshotReportsClosed(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("watcher").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := svc.StatusSnapshot().State; got != ServiceClosed {
		t.Errorf("State = %q after Close, want %q", got, ServiceClosed)
	}
	if got := svc.Status(); got != "closed" {
		t.Errorf("Status() = %q after Close, want closed", got)
	}
}

// A nil Service must answer rather than panic: a host polling status through
// a handle it has already dropped is a bug, but not one worth a crash.
func TestStatusSnapshotOnNilService(t *testing.T) {
	var svc *Service
	if got := svc.StatusSnapshot().State; got != ServiceClosed {
		t.Errorf("nil Service State = %q, want %q", got, ServiceClosed)
	}
	if _, ok := svc.RunStatus("anything"); ok {
		t.Error("nil Service claims to know a run")
	}
	if svc.IsRunning() {
		t.Error("nil Service claims to be running")
	}
}

// waitForRunStatus waits until the service's single run publishes a reading
// that satisfies want. Registration and the first stage are separate moments —
// a status API that could not tell them apart would report "round 0 of 0" for
// a run that is simply still starting, which is why Reported exists.
func waitForRunStatus(t *testing.T, svc *Service, want func(RunStatus) bool) RunStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runs := svc.RunStatuses()
		if len(runs) == 1 && runs[0].Reported && want(runs[0]) {
			return runs[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the loop to publish a matching reading; have %+v", svc.RunStatuses())
	return RunStatus{}
}

// A background task is a run, so it has everything a run's status has — but
// only if something joins the two. BackgroundTask.RunID had been declared
// since background tasks were written and never assigned, so a host holding a
// task could not reach the run behind it.
func TestBackgroundTaskStatusCarriesTheLiveRun(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("bg-status").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithBackgroundTasks(4).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	task, err := svc.StartBackgroundTask(context.Background(), "Count the beans.")
	if err != nil {
		t.Fatal(err)
	}
	if task.RunID == "" {
		t.Fatal("StartBackgroundTask returned a task with no RunID; nothing can find its run")
	}
	waitForActiveRuns(t, svc, 1)

	st := waitForBackgroundStatus(t, svc, task.ID, func(s BackgroundTaskStatus) bool {
		return s.Run != nil && s.Run.Stage == TurnStageAwaitingModel
	})
	if st.Status != BackgroundRunning {
		t.Errorf("Status = %q, want running", st.Status)
	}
	if st.Goal != "Count the beans." {
		t.Errorf("Goal = %q", st.Goal)
	}
	if st.RunID != task.RunID {
		t.Errorf("RunID = %q, want the task's own %q", st.RunID, task.RunID)
	}
	if st.Run.RunID != task.RunID {
		t.Errorf("joined the wrong run: %q vs %q", st.Run.RunID, task.RunID)
	}
	if st.Run.Round < 1 || st.Run.MaxRounds < 1 {
		t.Errorf("live reading has no round budget: %d/%d", st.Run.Round, st.Run.MaxRounds)
	}
	if st.ResultChars != 0 {
		t.Errorf("ResultChars = %d on a task still running", st.ResultChars)
	}
	if st.EndedAt != nil {
		t.Error("EndedAt set on a task still running")
	}

	// A background task is a run on this service: it occupies a concurrency
	// slot and it spends money, so it belongs in the run registry. It is
	// marked, because a host drawing "runs" and "background tasks" as two
	// lists would otherwise count it twice.
	runs := svc.RunStatuses()
	if len(runs) != 1 {
		t.Fatalf("RunStatuses = %d, want the background task's own run", len(runs))
	}
	if runs[0].BackgroundTaskID != task.ID {
		t.Errorf("BackgroundTaskID = %q, want %q", runs[0].BackgroundTaskID, task.ID)
	}
	if svc.Capacity().ActiveRuns != 1 {
		t.Error("a background task does not count against capacity")
	}

	// The same task, reached through the whole-service snapshot.
	snap := svc.StatusSnapshot()
	if snap.Background.Running != 1 || len(snap.Background.InFlight) != 1 {
		t.Fatalf("Background = %+v, want one task in flight", snap.Background)
	}
	if snap.Background.InFlight[0].Run == nil {
		t.Error("the snapshot's in-flight task carries no live reading")
	}
	if len(snap.Background.RunningIDs) != 1 || snap.Background.RunningIDs[0] != task.ID {
		t.Errorf("RunningIDs = %v, want [%s]", snap.Background.RunningIDs, task.ID)
	}

	// The agent's own view: progress, and never a result.
	full, _ := svc.BackgroundTask(task.ID)
	payload := backgroundTaskPayload(full, svc.runStatusPtr(full.RunID))
	if _, leaked := payload["result"]; leaked {
		t.Error("background_check reported a result for a running task")
	}
	progress, _ := payload["progress"].(map[string]interface{})
	if progress == nil {
		t.Fatal("background_check reported no progress for a running task")
	}
	if progress["stage"] != TurnStageAwaitingModel {
		t.Errorf("progress stage = %v, want %q", progress["stage"], TurnStageAwaitingModel)
	}

	llm.releaseAll()
	waitForBackground(t, svc, task.ID)

	// Finished: the run is gone, the task is not — its answer may never have
	// been collected.
	done := waitForBackgroundStatus(t, svc, task.ID, func(s BackgroundTaskStatus) bool {
		return s.Status.Done()
	})
	if done.Run != nil {
		t.Error("a finished task still carries a live run reading")
	}
	if done.EndedAt == nil || done.Duration <= 0 {
		t.Error("a finished task does not know how long it took")
	}
	if done.ResultChars <= 0 {
		t.Error("ResultChars = 0 on a task that produced an answer")
	}
	if snap := svc.StatusSnapshot(); len(snap.Background.InFlight) != 0 || snap.Background.Completed != 1 {
		t.Errorf("Background = %+v, want nothing in flight and one completed", snap.Background)
	}
	if _, ok := svc.BackgroundTaskStatus("no-such-task"); ok {
		t.Error("BackgroundTaskStatus answered for an unknown id")
	}
}

// Several at once is the case the join has to get right: every task must carry
// its own run's reading, not whichever one the map happened to yield.
func TestBackgroundStatusesKeepTasksApart(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("bg-many").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithBackgroundTasks(4).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	goals := []string{"First.", "Second.", "Third."}
	ids := make(map[string]string, len(goals)) // task id -> goal
	for _, goal := range goals {
		task, err := svc.StartBackgroundTask(context.Background(), goal)
		if err != nil {
			t.Fatal(err)
		}
		ids[task.ID] = goal
	}
	waitForActiveRuns(t, svc, 3)

	deadline := time.Now().Add(10 * time.Second)
	var statuses []BackgroundTaskStatus
	for time.Now().Before(deadline) {
		statuses = svc.BackgroundStatuses()
		joined := 0
		for _, st := range statuses {
			if st.Run != nil {
				joined++
			}
		}
		if joined == len(goals) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	seen := map[string]bool{}
	for _, st := range statuses {
		if st.Run == nil {
			t.Fatalf("task %s never joined its run", st.ID)
		}
		if want := ids[st.ID]; st.Goal != want || st.Run.Goal != want {
			t.Errorf("task %s: goal %q, run goal %q, want %q", st.ID, st.Goal, st.Run.Goal, want)
		}
		if seen[st.Run.RunID] {
			t.Errorf("two tasks share run %s", st.Run.RunID)
		}
		seen[st.Run.RunID] = true
		if st.SessionID == "" {
			t.Errorf("task %s has no session", st.ID)
		}
	}
	if len(statuses) != len(goals) {
		t.Errorf("got %d statuses, want %d", len(statuses), len(goals))
	}
}

// waitForBackgroundStatus waits for one task's status to satisfy want.
func waitForBackgroundStatus(t *testing.T, svc *Service, id string, want func(BackgroundTaskStatus) bool) BackgroundTaskStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last BackgroundTaskStatus
	for time.Now().Before(deadline) {
		st, ok := svc.BackgroundTaskStatus(id)
		if ok {
			last = st
			if want(st) {
				return st
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting on background task %s; last = %+v", id, last)
	return BackgroundTaskStatus{}
}
