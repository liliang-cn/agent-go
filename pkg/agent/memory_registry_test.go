package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
)

// ============================================================
// A backend that only implements three of the eighteen methods.
// ============================================================

// minimalStore is the whole point of memory.BaseStore: a backend that knows how
// to write, search and read, and nothing else.
type minimalStore struct {
	memory.BaseStore

	mu    sync.Mutex
	items map[string]*domain.Memory
	order []string
	seq   int
}

func newMinimalStore() *minimalStore {
	return &minimalStore{items: map[string]*domain.Memory{}}
}

func (s *minimalStore) Store(_ context.Context, m *domain.Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		s.seq++
		m.ID = fmt.Sprintf("min-%d", s.seq)
	}
	if _, ok := s.items[m.ID]; !ok {
		s.order = append(s.order, m.ID)
	}
	clone := *m
	s.items[m.ID] = &clone
	return nil
}

func (s *minimalStore) SearchByText(_ context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*domain.MemoryWithScore
	for _, id := range s.order {
		m := s.items[id]
		if strings.Contains(strings.ToLower(m.Content), strings.ToLower(query)) {
			clone := *m
			out = append(out, &domain.MemoryWithScore{Memory: &clone, Score: 0.9})
			if len(out) >= topK {
				break
			}
		}
	}
	return out, nil
}

func (s *minimalStore) Get(_ context.Context, id string) (*domain.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	clone := *m
	return &clone, nil
}

// ============================================================
// BaseStore
// ============================================================

// TestBaseStoreDegradesUnimplementedMethods proves a store that overrides only
// Store/SearchByText/Get satisfies domain.MemoryStore and completes a real
// round trip, while everything it did not implement says so honestly.
func TestBaseStoreDegradesUnimplementedMethods(t *testing.T) {
	var store domain.MemoryStore = newMinimalStore()
	ctx := context.Background()

	mem := &domain.Memory{Content: "the deploy key lives in 1Password", Type: domain.MemoryTypeFact}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if mem.ID == "" {
		t.Fatal("Store() did not assign an ID")
	}
	got, err := store.Get(ctx, mem.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Content != mem.Content {
		t.Errorf("Get() content = %q, want %q", got.Content, mem.Content)
	}
	hits, err := store.SearchByText(ctx, "1password", 5)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != mem.ID {
		t.Fatalf("SearchByText() = %+v, want the stored memory", hits)
	}

	// The fifteen methods that were never overridden all decline politely.
	unimplemented := map[string]error{
		"StoreWithScope":  store.StoreWithScope(ctx, mem, domain.MemoryScope{Type: domain.MemoryScopeGlobal}),
		"Update":          store.Update(ctx, mem),
		"IncrementAccess": store.IncrementAccess(ctx, mem.ID),
		"Delete":          store.Delete(ctx, mem.ID),
		"Clear":           store.Clear(ctx),
		"DeleteBySession": store.DeleteBySession(ctx, "s"),
		"ConfigureBank":   store.ConfigureBank(ctx, "s", &domain.MemoryBankConfig{}),
		"AddMentalModel":  store.AddMentalModel(ctx, &domain.MentalModel{}),
		"InitSchema":      store.InitSchema(ctx),
	}
	if _, err := store.Search(ctx, []float64{0.1}, 3, 0); err != nil {
		unimplemented["Search"] = err
	}
	if _, err := store.SearchBySession(ctx, "s", []float64{0.1}, 3); err != nil {
		unimplemented["SearchBySession"] = err
	}
	if _, err := store.SearchByScope(ctx, []float64{0.1}, nil, 3); err != nil {
		unimplemented["SearchByScope"] = err
	}
	if _, err := store.GetByType(ctx, domain.MemoryTypeFact, 3); err != nil {
		unimplemented["GetByType"] = err
	}
	if _, _, err := store.List(ctx, 3, 0); err != nil {
		unimplemented["List"] = err
	}
	if _, err := store.Reflect(ctx, "s"); err != nil {
		unimplemented["Reflect"] = err
	}

	if len(unimplemented) != 15 {
		t.Errorf("checked %d unimplemented methods, expected 15", len(unimplemented))
	}
	for name, err := range unimplemented {
		if !errors.Is(err, memory.ErrMemoryStoreUnsupported) {
			t.Errorf("%s() error = %v, want ErrMemoryStoreUnsupported", name, err)
		}
		if !memory.IsUnsupported(err) {
			t.Errorf("memory.IsUnsupported(%s error) = false", name)
		}
	}
}

// ============================================================
// Registry
// ============================================================

func TestRegisterMemoryStoreAndResolveByName(t *testing.T) {
	const name = "test-registry-backend"
	t.Cleanup(func() { UnregisterMemoryStore(name) })

	var gotCfg MemoryStoreConfig
	backing := newMinimalStore()
	if err := RegisterMemoryStore(name, func(cfg MemoryStoreConfig) (domain.MemoryStore, error) {
		gotCfg = cfg
		return backing, nil
	}); err != nil {
		t.Fatalf("RegisterMemoryStore() error = %v", err)
	}

	// The name resolves through the public lookup...
	if _, ok := LookupMemoryStore(name); !ok {
		t.Fatal("LookupMemoryStore() did not find the registration")
	}
	if !slices.Contains(RegisteredMemoryStores(), name) {
		t.Errorf("RegisteredMemoryStores() = %v, missing %q", RegisteredMemoryStores(), name)
	}
	// ...and config now accepts it as a store type, so agentgo.toml can select it.
	if !config.MemoryStoreType(name).Valid() {
		t.Error("a registered store type is not accepted by config.MemoryStoreType.Valid()")
	}
	if config.MemoryStoreType(name).IsBuiltin() {
		t.Error("a plugin store type must not report itself as built-in")
	}

	// ...and the builder routes store_type to the factory.
	cfg := testAgentConfig(t.TempDir())

	b := New("registry-agent").
		WithConfig(cfg).
		WithMemory(
			WithMemoryStoreType(name),
			WithMemoryDSN("brain.local:47821"),
			WithMemoryOption("namespace", "team"),
		)
	memSvc, storeType, err := b.buildMemoryService(cfg, nil, nil)
	if err != nil {
		t.Fatalf("buildMemoryService() error = %v", err)
	}
	if storeType != name {
		t.Errorf("store type = %q, want %q", storeType, name)
	}
	if memSvc == nil {
		t.Fatal("buildMemoryService() returned a nil service")
	}
	if gotCfg.Name != name {
		t.Errorf("factory config Name = %q, want %q", gotCfg.Name, name)
	}
	if gotCfg.DSN != "brain.local:47821" {
		t.Errorf("factory config DSN = %q", gotCfg.DSN)
	}
	if gotCfg.Option("namespace") != "team" {
		t.Errorf("factory config options = %v", gotCfg.Options)
	}

	// The service really talks to the registered backend.
	ctx := context.Background()
	if err := memSvc.Add(ctx, &domain.Memory{Content: "registry backend is wired", Type: domain.MemoryTypeFact}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	hits, err := memSvc.Search(ctx, "registry backend", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Search() = %d hits, want 1 from the registered backend", len(hits))
	}
}

func TestRegisterMemoryStoreRejectsBadRegistrations(t *testing.T) {
	const name = "test-registry-dupe"
	t.Cleanup(func() { UnregisterMemoryStore(name) })

	factory := func(MemoryStoreConfig) (domain.MemoryStore, error) { return newMinimalStore(), nil }

	if err := RegisterMemoryStore("  ", factory); err == nil {
		t.Error("blank name = nil error, want an error")
	}
	if err := RegisterMemoryStore(name, nil); err == nil {
		t.Error("nil factory = nil error, want an error")
	}
	for _, builtin := range []string{"file", "cortex", "memoryflow", "graphflow"} {
		if err := RegisterMemoryStore(builtin, factory); err == nil {
			t.Errorf("registering built-in %q = nil error, want a reserved-name error", builtin)
		}
	}

	if err := RegisterMemoryStore(name, factory); err != nil {
		t.Fatalf("first registration error = %v", err)
	}
	if err := RegisterMemoryStore(name, factory); err == nil {
		t.Error("duplicate registration = nil error, want an error")
	}
	// Names are case- and space-insensitive, so the duplicate check cannot be
	// dodged with whitespace.
	if err := RegisterMemoryStore("  "+strings.ToUpper(name)+" ", factory); err == nil {
		t.Error("duplicate registration under a different casing = nil error, want an error")
	}

	if !UnregisterMemoryStore(name) {
		t.Error("UnregisterMemoryStore() = false, want true")
	}
	if UnregisterMemoryStore(name) {
		t.Error("second UnregisterMemoryStore() = true, want false")
	}
	if err := RegisterMemoryStore(name, factory); err != nil {
		t.Errorf("re-registration after unregister error = %v", err)
	}
}

func TestBuildMemoryServiceRejectsUnknownStoreType(t *testing.T) {
	cfg := testAgentConfig(t.TempDir())

	b := New("unknown-agent").WithConfig(cfg).WithMemory(WithMemoryStoreType("no-such-backend"))
	_, _, err := b.buildMemoryService(cfg, nil, nil)
	if err == nil {
		t.Fatal("buildMemoryService() with an unregistered store type = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "no-such-backend") {
		t.Errorf("error = %v, want it to name the unknown store type", err)
	}
	if !strings.Contains(err.Error(), "built-in") {
		t.Errorf("error = %v, want it to list what is available", err)
	}
}

// TestRegisterMemoryStoreIsConcurrencySafe is meaningful under -race.
func TestRegisterMemoryStoreIsConcurrencySafe(t *testing.T) {
	factory := func(MemoryStoreConfig) (domain.MemoryStore, error) { return newMinimalStore(), nil }
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		name := fmt.Sprintf("test-concurrent-%d", i)
		t.Cleanup(func() { UnregisterMemoryStore(name) })
		wg.Add(3)
		go func() { defer wg.Done(); _ = RegisterMemoryStore(name, factory) }()
		go func() { defer wg.Done(); _, _ = LookupMemoryStore(name) }()
		go func() { defer wg.Done(); _ = RegisteredMemoryStores() }()
	}
	wg.Wait()
}

// TestWithMemoryStoreInjectsInstance covers seam 2: a ready-made store wins
// over any store_type resolution.
func TestWithMemoryStoreInjectsInstance(t *testing.T) {
	cfg := testAgentConfig(t.TempDir())

	injected := newMinimalStore()
	b := New("injected-agent").WithConfig(cfg).WithMemory(
		// A store type that would otherwise blow up: the instance must win.
		WithMemoryStoreType("no-such-backend"),
		WithMemoryStore(injected),
	)
	memSvc, storeType, err := b.buildMemoryService(cfg, nil, nil)
	if err != nil {
		t.Fatalf("buildMemoryService() error = %v", err)
	}
	if storeType != "no-such-backend" {
		t.Errorf("store type = %q, want the declared name to survive", storeType)
	}

	ctx := context.Background()
	if err := memSvc.Add(ctx, &domain.Memory{Content: "injected instance is wired", Type: domain.MemoryTypeFact}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	injected.mu.Lock()
	n := len(injected.items)
	injected.mu.Unlock()
	if n != 1 {
		t.Errorf("injected store received %d memories, want 1", n)
	}
}

// stubMemoryService is a minimal domain.MemoryService for the escape hatch test.
type stubMemoryService struct {
	domain.MemoryService
	added []*domain.Memory
}

func (s *stubMemoryService) Add(_ context.Context, m *domain.Memory) error {
	s.added = append(s.added, m)
	return nil
}

// TestWithMemoryServiceTakesOver covers seam 3: nothing in buildMemoryService
// runs at all.
func TestWithMemoryServiceTakesOver(t *testing.T) {
	stub := &stubMemoryService{}
	b := New("takeover-agent").WithMemoryService(stub)
	if b.memoryService != stub {
		t.Fatal("WithMemoryService() did not record the service")
	}
	if !b.enableMemory {
		t.Error("WithMemoryService() should imply memory is enabled")
	}
	if err := b.memoryService.Add(context.Background(), &domain.Memory{Content: "x"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if len(stub.added) != 1 {
		t.Errorf("stub received %d memories, want 1", len(stub.added))
	}
}
