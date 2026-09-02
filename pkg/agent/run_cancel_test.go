package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// hangingLLM stays inside the streaming turn until its context is cancelled.
// That is the state a stop button actually has to interrupt: the provider call
// is the long pole of a round, so a cancel almost always lands inside it.
type hangingLLM struct {
	entered chan struct{}
	once    sync.Once
}

func newHangingLLM() *hangingLLM { return &hangingLLM{entered: make(chan struct{})} }

func (h *hangingLLM) block(ctx context.Context) error {
	h.once.Do(func() { close(h.entered) })
	<-ctx.Done()
	return ctx.Err()
}

func (h *hangingLLM) Generate(ctx context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	return "", h.block(ctx)
}

func (h *hangingLLM) Stream(ctx context.Context, _ string, _ *domain.GenerationOptions, _ func(string)) error {
	return h.block(ctx)
}

func (h *hangingLLM) GenerateWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return nil, h.block(ctx)
}

func (h *hangingLLM) StreamWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, _ domain.ToolCallCallback) error {
	return h.block(ctx)
}

// The constraint pass runs before the loop and must not be the thing that
// hangs — otherwise the test would prove nothing about the loop itself.
func (h *hangingLLM) GenerateStructured(_ context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Raw: `{"forbid_tools":false,"deliverables":[]}`, Valid: true}, nil
}

func (h *hangingLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func newCancelTestService(t *testing.T, llm domain.Generator) *Service {
	t.Helper()
	svc, err := New("cancel-test").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func drainWithin(t *testing.T, events <-chan *Event, d time.Duration) []*Event {
	t.Helper()
	var collected []*Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range events {
			collected = append(collected, evt)
		}
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("the event stream never closed after cancel — the run was not actually stopped")
	}
	return collected
}

func terminalEvent(events []*Event) *Event {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case EventTypeComplete, EventTypeBlocked, EventTypeCancelled, EventTypeError:
			return events[i]
		}
	}
	return nil
}

// Service.Cancel used to be decoration: s.cancelFunc was only ever assigned by
// a test, so in production it always returned false and stopped nothing. This
// pins the opposite — the run really ends, and it ends as *cancelled*.
func TestServiceCancel_StopsAnInFlightRun(t *testing.T) {
	t.Parallel()

	llm := newHangingLLM()
	svc := newCancelTestService(t, llm)

	events, err := svc.RunStream(context.Background(), "analyse everything, slowly")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	select {
	case <-llm.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the model call never started")
	}

	if runs := svc.ActiveRuns(); len(runs) != 1 {
		t.Fatalf("expected exactly one active run, got %d", len(runs))
	}
	if !svc.Cancel() {
		t.Fatal("Cancel reported that nothing was cancelled while a run was in flight")
	}

	collected := drainWithin(t, events, 10*time.Second)

	term := terminalEvent(collected)
	if term == nil {
		t.Fatal("the cancelled run produced no terminal event")
	}
	if term.Type != EventTypeCancelled {
		t.Fatalf("a stop must terminate as %q, got %q (%q)", EventTypeCancelled, term.Type, term.Content)
	}
	if term.StopReason != StopReasonCancelled {
		t.Errorf("stop reason = %q, want %q", term.StopReason, StopReasonCancelled)
	}
	for _, evt := range collected {
		if evt.Type == EventTypeError {
			t.Errorf("a cancelled run emitted an error event (%q); cancel is an outcome, not a failure", evt.Content)
		}
	}

	// The registration is cleaned up with the stream, so a stale id cannot
	// keep answering "ok" to a stop.
	if runs := svc.ActiveRuns(); len(runs) != 0 {
		t.Fatalf("expected the run registry to be empty after the stream closed, got %d", len(runs))
	}
	if svc.Cancel() {
		t.Fatal("Cancel reported success with nothing running")
	}
}

// Run() collects the same stream, so the outcome has to survive the collector:
// Cancelled true, Err() nil. A caller that branches on err must not see its own
// stop button as a failure.
func TestRunCancelled_IsAnOutcomeNotAnError(t *testing.T) {
	t.Parallel()

	llm := newHangingLLM()
	svc := newCancelTestService(t, llm)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		res *ExecutionResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := svc.Run(ctx, "analyse everything, slowly")
		done <- outcome{res, err}
	}()

	select {
	case <-llm.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the model call never started")
	}
	cancel()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run returned a transport error for a cancelled run: %v", got.err)
		}
		if got.res == nil {
			t.Fatal("Run returned no result")
		}
		if !got.res.Cancelled {
			t.Error("ExecutionResult.Cancelled is false for a cancelled run")
		}
		if got.res.Success {
			t.Error("a cancelled run must not report success")
		}
		if err := got.res.Err(); err != nil {
			t.Errorf("Err() = %v; a stop the caller asked for must not read as a failure", err)
		}
		if got.res.Error != "" {
			t.Errorf("Error = %q; a cancelled run must not carry an error message", got.res.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run never returned after its context was cancelled")
	}
}

// CancelRun addresses one named run, which is what a host with several turns
// in flight needs — Cancel() would take all of them down.
func TestCancelRun_TargetsOneRun(t *testing.T) {
	t.Parallel()

	llm := newHangingLLM()
	svc := newCancelTestService(t, llm)

	first, err := svc.RunStreamWithOptions(context.Background(), "first",
		WithRunID("run-a"), WithSessionID("session-a"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	second, err := svc.RunStreamWithOptions(context.Background(), "second",
		WithRunID("run-b"), WithSessionID("session-b"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	t.Cleanup(func() { svc.Cancel() })

	<-llm.entered

	deadline := time.Now().Add(10 * time.Second)
	for len(svc.ActiveRuns()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(svc.ActiveRuns()); got != 2 {
		t.Fatalf("expected two active runs, got %d", got)
	}

	if svc.CancelRun("no-such-run") {
		t.Error("CancelRun answered ok for an unknown id")
	}
	if !svc.CancelRun("run-a") {
		t.Fatal("CancelRun failed to stop the run it was given")
	}

	if term := terminalEvent(drainWithin(t, first, 10*time.Second)); term == nil || term.Type != EventTypeCancelled {
		t.Fatalf("the targeted run did not terminate as cancelled: %+v", term)
	}

	// The other run is untouched: it is still registered and still streaming.
	runs := svc.ActiveRuns()
	if len(runs) != 1 || runs[0].RunID != "run-b" {
		t.Fatalf("expected only run-b to survive, got %+v", runs)
	}
	if runs[0].SessionID != "session-b" {
		t.Errorf("ActiveRun lost its session: %+v", runs[0])
	}

	if !svc.CancelSession("session-b") {
		t.Fatal("CancelSession failed to stop the session's run")
	}
	if term := terminalEvent(drainWithin(t, second, 10*time.Second)); term == nil || term.Type != EventTypeCancelled {
		t.Fatalf("CancelSession did not terminate the run as cancelled: %+v", term)
	}
}

// Registration, cancellation and cleanup all mutate the same map from
// different goroutines. Run with -race.
func TestRunRegistry_Concurrent(t *testing.T) {
	t.Parallel()

	svc := &Service{inProgressTools: make(map[string]int)}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				runCtx, _, release, _ := svc.registerRun(context.Background(), "", "session", "task", "")
				_ = runCtx
				// Releasing twice must be harmless: the observer defers it and
				// a future caller may well add another safety net.
				release()
				release()
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				svc.Cancel()
				svc.CancelSession("session")
				svc.CancelRun("nope")
				_ = svc.ActiveRuns()
			}
		}()
	}
	wg.Wait()

	if runs := svc.ActiveRuns(); len(runs) != 0 {
		t.Fatalf("registry leaked %d runs", len(runs))
	}
}

// A caller-chosen RunID that collides with a live run must not make the older
// run unstoppable by silently evicting it from the registry.
func TestRegisterRun_DuplicateIDKeepsBothCancellable(t *testing.T) {
	t.Parallel()

	svc := &Service{inProgressTools: make(map[string]int)}

	firstCtx, _, releaseFirst, _ := svc.registerRun(context.Background(), "dup", "s", "t", "")
	defer releaseFirst()
	secondCtx, _, releaseSecond, _ := svc.registerRun(context.Background(), "dup", "s", "t", "")
	defer releaseSecond()

	if got := len(svc.ActiveRuns()); got != 2 {
		t.Fatalf("expected both runs to be registered, got %d", got)
	}
	if !svc.Cancel() {
		t.Fatal("Cancel stopped nothing")
	}
	if firstCtx.Err() == nil || secondCtx.Err() == nil {
		t.Fatal("Cancel left one of the colliding runs alive")
	}
}

// The task store has to record the run as cancelled, not failed: a task list
// that shows a deliberate stop in red teaches people to ignore it.
func TestCancelledRunPersistsAsCancelledTask(t *testing.T) {
	t.Parallel()

	llm := newHangingLLM()
	svc := newCancelTestService(t, llm)

	events, err := svc.RunStreamWithOptions(context.Background(), "long job",
		WithSessionID("cancel-session"), WithTaskID("cancel-task"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	<-llm.entered
	if !svc.Cancel() {
		t.Fatal("Cancel stopped nothing")
	}
	drainWithin(t, events, 10*time.Second)

	task, err := svc.store.GetTask("cancel-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got := strings.TrimSpace(string(task.Status)); got != "cancelled" {
		t.Fatalf("task status = %q, want %q", got, "cancelled")
	}
	if strings.TrimSpace(task.Error) != "" {
		t.Errorf("a cancelled task carries an error message: %q", task.Error)
	}
}
