package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// TestScheduledRunOnAClosedServiceFailsInsteadOfLosingHistory is the regression
// for silent data loss: a scheduled prompt that ran, answered, and never
// managed to write a word of the conversation down.
//
// The sequence is a host's ordinary rebuild. Something in the application
// changes (settings, UI rules), so it builds a new agent Service and closes the
// old one — but the PromptScheduler it started earlier is still pointed at the
// old one. Before this was fixed the schedule went on firing against a Service
// whose store was closed: the model answered, the answer reached the UI, the
// run reported Success, and both writes failed with "sql: database is closed",
// logged as warnings and dropped. Every subsequent run of that schedule lost
// its history, and nothing told anyone.
//
// Ablation: delete the s.Closed() guard in startRun and this test fails at the
// last block — the run comes back Success with an empty session.
func TestScheduledRunOnAClosedServiceFailsInsteadOfLosingHistory(t *testing.T) {
	cfg := testAgentConfig(t.TempDir())
	svc, err := New("scheduled-agent").WithConfig(cfg).WithLLM(&scheduledTestLLM{}).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	var (
		mu   sync.Mutex
		runs []PromptRun
	)
	sch, err := svc.NewPromptScheduler(WithPromptObserver(func(r PromptRun) {
		mu.Lock()
		runs = append(runs, r)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatalf("NewPromptScheduler: %v", err)
	}
	if err := sch.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = sch.Stop() })

	task, err := sch.Schedule("统计我的股票收益并发消息给我", "0 8 * * *", "", "stocks")
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}

	// Control: while the service is alive the run is persisted. Without this
	// the assertion below would pass for the wrong reason — an empty session
	// proves nothing if the session is never written in the first place.
	if _, err := sch.RunNow(task.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	stored := svc.mustSessionMessages(t, "stocks")
	if stored == 0 {
		t.Fatal("a scheduled run on a live service must persist its conversation")
	}

	// The host rebuilds and releases this service; nobody rebinds the timers.
	if err := svc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	res, err := sch.RunNow(task.ID)
	if err == nil && (res == nil || res.Success) {
		t.Fatal("a schedule bound to a closed service reported success; its history went nowhere")
	}
	failure := ""
	if res != nil {
		failure = res.Error
	}
	if err != nil {
		failure = err.Error()
	}
	if !strings.Contains(failure, ErrServiceClosed.Error()) {
		t.Errorf("the failure must name the cause, got %q", failure)
	}

	// The observer is how a host notifies, so the error has to reach it too.
	mu.Lock()
	got := append([]PromptRun{}, runs...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("observer saw %d runs, want 2", len(got))
	}
	if got[1].Err == nil {
		t.Error("the host's observer must see the failed run, or the schedule looks healthy while losing data")
	}
	if got[1].Cancelled {
		t.Error("a closed service is not a cancellation — it is a fault the host has to fix")
	}

	// And what is on disk is still exactly what the live run wrote — read
	// through a fresh handle, since the service's own is gone.
	reopened, err := NewStore(cfg.AgentDBPath())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	sess, err := reopened.GetSession("stocks")
	if err != nil || sess == nil {
		t.Fatalf("the live run's session is not on disk: %v", err)
	}
	if len(sess.GetMessages()) != stored {
		t.Errorf("stored session has %d messages, want the %d the live run wrote", len(sess.GetMessages()), stored)
	}
}

// TestClosedServiceRefusesEveryRunEntryPoint pins the guard at the one place
// that has it — startRun — by going in through each public surface.
func TestClosedServiceRefusesEveryRunEntryPoint(t *testing.T) {
	svc, _ := newScheduledTestService(t)
	if svc.Closed() {
		t.Fatal("a fresh service must not report itself closed")
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !svc.Closed() {
		t.Fatal("Closed() must report the truth after Close()")
	}

	ctx := context.Background()
	if _, err := svc.Run(ctx, "anything"); !errors.Is(err, ErrServiceClosed) {
		t.Errorf("Run on a closed service = %v, want ErrServiceClosed", err)
	}
	if _, err := svc.RunStream(ctx, "anything"); !errors.Is(err, ErrServiceClosed) {
		t.Errorf("RunStream on a closed service = %v, want ErrServiceClosed", err)
	}
	if _, err := svc.Chat(ctx, "anything"); !errors.Is(err, ErrServiceClosed) {
		t.Errorf("Chat on a closed service = %v, want ErrServiceClosed", err)
	}
	if _, err := svc.Ask(ctx, "anything"); !errors.Is(err, ErrServiceClosed) {
		t.Errorf("Ask on a closed service = %v, want ErrServiceClosed", err)
	}
}

// TestCloseIsIdempotent: "close the service I replaced" is the kind of thing a
// host reaches twice (a rebuild path and a shutdown path), and a second close
// must not be an error it has to reason about.
func TestCloseIsIdempotent(t *testing.T) {
	svc, _ := newScheduledTestService(t)
	if err := svc.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestManagerRebuildsAServiceSomebodyClosed: the Manager owns the services it
// caches, but a host that got one from Service(name) can close it. Handing that
// corpse out again would run every later turn against a closed store, so the
// cache entry is dropped and a fresh service built.
func TestManagerRebuildsAServiceSomebodyClosed(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	manager := NewManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	manager.SetLLM(&scheduledTestLLM{})
	t.Cleanup(func() { _ = manager.Close() })

	model := &AgentModel{
		ID:           "agent-closed-cache",
		Name:         "ClosedCacheAgent",
		Kind:         AgentKindAgent,
		Instructions: "answer",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := store.SaveAgentModel(model); err != nil {
		t.Fatalf("SaveAgentModel: %v", err)
	}

	first, err := manager.Service(model.Name)
	if err != nil {
		t.Fatalf("Service: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := manager.Service(model.Name)
	if err != nil {
		t.Fatalf("Service after close: %v", err)
	}
	if second == first {
		t.Fatal("the Manager handed back the service that was closed")
	}
	if second.Closed() {
		t.Fatal("the rebuilt service must be usable")
	}
}

// blockingTestLLM stalls inside the model call until the run's context dies,
// which is what a real long turn looks like from Close's point of view.
type blockingTestLLM struct {
	entered chan struct{}
	once    sync.Once
}

func (l *blockingTestLLM) enter() {
	l.once.Do(func() { close(l.entered) })
}

func (l *blockingTestLLM) Generate(ctx context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	l.enter()
	<-ctx.Done()
	return "", ctx.Err()
}

func (l *blockingTestLLM) Stream(ctx context.Context, _ string, _ *domain.GenerationOptions, _ func(string)) error {
	l.enter()
	<-ctx.Done()
	return ctx.Err()
}

func (l *blockingTestLLM) GenerateWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.enter()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (l *blockingTestLLM) StreamWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, _ domain.ToolCallCallback) error {
	l.enter()
	<-ctx.Done()
	return ctx.Err()
}

func (l *blockingTestLLM) GenerateStructured(ctx context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	l.enter()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (l *blockingTestLLM) RecognizeIntent(_ context.Context, _ string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

// TestCloseStopsRunsInFlight: closing releases the store, so a run still
// executing against it is a run whose writes are about to fail. Close cancels
// it rather than leaving it to discover that on its own.
func TestCloseStopsRunsInFlight(t *testing.T) {
	llm := &blockingTestLLM{entered: make(chan struct{})}
	svc, err := New("closing-agent").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	events, err := svc.RunStream(context.Background(), "take your time")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range events {
		}
	}()

	select {
	case <-llm.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run never reached the model")
	}

	closed := make(chan error, 1)
	go func() { closed <- svc.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(closeDrainTimeout + 5*time.Second):
		t.Fatal("Close hung on a run in flight instead of cancelling it")
	}

	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled run never ended its event stream")
	}
	if runs := svc.ActiveRuns(); len(runs) != 0 {
		t.Errorf("%d runs still registered after Close", len(runs))
	}
}
