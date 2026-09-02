package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// gateLLM holds every turn until it is released, so a test can have N
// runs genuinely in flight at once rather than hoping they overlap.
type gateLLM struct {
	release  chan struct{}
	inFlight atomic.Int64
	started  chan struct{}
	once     sync.Once
}

func newGateLLM() *gateLLM {
	return &gateLLM{release: make(chan struct{}), started: make(chan struct{}, 64)}
}

// answer blocks until the test releases it OR the run's context is
// cancelled. Honouring ctx is not politeness: a fake that ignores it makes
// every cancellation test hang, because the run cannot end while its model
// turn is still outstanding — which is exactly how a real provider behaves.
func (b *gateLLM) answer(ctx context.Context) (*domain.GenerationResult, error) {
	b.inFlight.Add(1)
	defer b.inFlight.Add(-1)
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return &domain.GenerationResult{Content: "done.", FinishReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *gateLLM) releaseAll() { b.once.Do(func() { close(b.release) }) }

func (b *gateLLM) Generate(ctx context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	res, err := b.answer(ctx)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

func (b *gateLLM) GenerateWithMessages(ctx context.Context, _ []domain.Message, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return b.answer(ctx)
}

func (b *gateLLM) GenerateWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return b.answer(ctx)
}

func (b *gateLLM) StreamWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	res, err := b.answer(ctx)
	if err != nil {
		return err
	}
	return cb(res)
}

func (b *gateLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (b *gateLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// A run carries its owner, and the owner is what the operator's verbs aim at.
func TestTenantIsCarriedAndCancellable(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("tenants").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	var wg sync.WaitGroup
	for _, tenant := range []string{"acme", "acme", "globex"} {
		wg.Add(1)
		go func(tenant string) {
			defer wg.Done()
			_, _ = svc.Run(context.Background(), "Work.", WithTenant(tenant), WithSessionID(tenant+"-"+uuidish()))
		}(tenant)
	}
	waitForActiveRuns(t, svc, 3)

	cap := svc.Capacity()
	if cap.ActiveRuns != 3 || cap.PerTenant["acme"] != 2 || cap.PerTenant["globex"] != 1 {
		t.Fatalf("Capacity = %+v, want 3 runs split 2 acme / 1 globex", cap)
	}
	if len(cap.Tenants) != 2 || cap.Tenants[0] != "acme" || cap.Tenants[1] != "globex" {
		t.Errorf("Tenants = %v, want [acme globex]", cap.Tenants)
	}
	if got := svc.ActiveRunsForTenant("acme"); len(got) != 2 {
		t.Errorf("ActiveRunsForTenant(acme) = %d runs, want 2", len(got))
	}
	for _, r := range svc.ActiveRuns() {
		if r.Tenant == "" {
			t.Errorf("run %s lost its tenant", r.RunID)
		}
	}

	// Stopping one customer's work leaves the other's alone.
	if stopped := svc.CancelTenant("acme"); stopped != 2 {
		t.Fatalf("CancelTenant(acme) stopped %d runs, want 2", stopped)
	}
	waitForActiveRuns(t, svc, 1)
	if left := svc.ActiveRuns(); len(left) != 1 || left[0].Tenant != "globex" {
		t.Fatalf("after cancelling acme, in flight = %+v, want one globex run", left)
	}

	llm.releaseAll()
	wg.Wait()
}

// The service-wide cap refuses rather than queues, and says so in a way a
// caller can branch on.
func TestMaxConcurrentRunsRefusesRatherThanQueues(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("capacity").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithMaxConcurrentRuns(2).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = svc.Run(context.Background(), "Work.", WithSessionID(uuidish()))
		}(i)
	}
	waitForActiveRuns(t, svc, 2)

	_, err = svc.Run(context.Background(), "One too many.", WithSessionID(uuidish()))
	if !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("err = %v, want ErrAtCapacity", err)
	}
	var capErr *CapacityError
	if !errors.As(err, &capErr) || capErr.Limit != 2 || capErr.Active != 2 {
		t.Fatalf("CapacityError = %+v, want the numbers behind the refusal", capErr)
	}
	// A refusal must not have registered anything: a limit that leaks a run
	// handle per rejection is a limit that tightens on its own.
	if got := svc.Capacity().ActiveRuns; got != 2 {
		t.Fatalf("ActiveRuns after a refusal = %d, want 2", got)
	}

	llm.releaseAll()
	wg.Wait()
	waitForActiveRuns(t, svc, 0)
	if _, err := svc.Run(context.Background(), "Now there is room.", WithSessionID(uuidish())); err != nil {
		t.Fatalf("a service back under its cap refused a run: %v", err)
	}
}

// The per-tenant cap is the one that matters on a shared service: the global
// cap alone is satisfied by one customer filling it.
func TestPerTenantCapKeepsOneCallerFromTakingTheService(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	svc, err := New("fair").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithMaxConcurrentRuns(10).
		WithMaxRunsPerTenant(1).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = svc.Run(context.Background(), "Work.", WithTenant("loud"), WithSessionID(uuidish()))
	}()
	waitForActiveRuns(t, svc, 1)

	_, err = svc.Run(context.Background(), "More.", WithTenant("loud"), WithSessionID(uuidish()))
	if !errors.Is(err, ErrTenantAtCapacity) {
		t.Fatalf("second run for the same tenant: err = %v, want ErrTenantAtCapacity", err)
	}

	// Another tenant is unaffected — that is the entire point.
	done := make(chan error, 1)
	go func() {
		_, err := svc.Run(context.Background(), "Quiet customer.", WithTenant("quiet"), WithSessionID(uuidish()))
		done <- err
	}()
	waitForActiveRuns(t, svc, 2)

	llm.releaseAll()
	wg.Wait()
	if err := <-done; err != nil {
		t.Fatalf("the quiet tenant was refused: %v", err)
	}
}

// Admission has to be atomic with registration. Checked anywhere else, two
// callers arriving together both pass a check for the last free slot.
func TestAdmissionIsAtomicUnderConcurrentStarts(t *testing.T) {
	llm := newGateLLM()
	defer llm.releaseAll()
	const limit = 4
	svc, err := New("atomic").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithMaxConcurrentRuns(limit).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	var admitted, refused atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Run(context.Background(), "Work.", WithSessionID(uuidish()))
			if errors.Is(err, ErrAtCapacity) {
				refused.Add(1)
				return
			}
			admitted.Add(1)
		}()
	}
	close(start)

	// Let them pile up against the limit, then check nothing slipped past it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if refused.Load()+admitted.Load() >= 32-int64(limit) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := svc.Capacity().ActiveRuns; got > limit {
		t.Fatalf("%d runs in flight with a limit of %d — admission is not atomic", got, limit)
	}

	llm.releaseAll()
	wg.Wait()
	if got := admitted.Load() + refused.Load(); got != 32 {
		t.Fatalf("accounted for %d of 32 attempts", got)
	}
}

// A result says whose run it was, so a host billing many customers through
// one service does not have to keep its own run-to-tenant map.
func TestExecutionResultCarriesTheTenant(t *testing.T) {
	svc, err := New("billing").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&scriptedLLM{finishAt: 0}).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	res, err := svc.Run(context.Background(), "Work.", WithTenant("  acme  "))
	if err != nil {
		t.Fatal(err)
	}
	if res.Tenant != "acme" {
		t.Fatalf("Tenant = %q, want the trimmed label back", res.Tenant)
	}
}

// A service with no limits set behaves exactly as it did before they existed.
func TestNoLimitsMeansUnlimited(t *testing.T) {
	svc, err := New("unlimited").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&scriptedLLM{finishAt: 0}).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	c := svc.Capacity()
	if c.MaxConcurrentRuns != 0 || c.MaxRunsPerTenant != 0 {
		t.Fatalf("Capacity = %+v, want zero (unlimited) ceilings by default", c)
	}
	for i := 0; i < 3; i++ {
		if _, err := svc.Run(context.Background(), "Work."); err != nil {
			t.Fatalf("run %d refused on an unlimited service: %v", i, err)
		}
	}
}

func waitForActiveRuns(t *testing.T, svc *Service, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if svc.Capacity().ActiveRuns == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d active runs; have %+v", want, svc.Capacity())
}

// uuidish is a fresh session id per run: two runs sharing a session would
// serialise on it and never be in flight together.
func uuidish() string { return uuid.NewString() }

func (b *gateLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
