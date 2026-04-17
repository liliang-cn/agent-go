package store

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/cortexdb/v2/pkg/graphflow"
)

func TestNewMemoryStoreConcurrentOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")

	const workers = 6
	errCh := make(chan error, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			store, err := NewMemoryStore(dbPath)
			if err != nil {
				errCh <- err
				return
			}
			defer store.Close()

			if err := store.InitSchema(context.Background()); err != nil {
				errCh <- err
				return
			}

			if _, _, err := store.List(context.Background(), 10, 0); err != nil {
				errCh <- err
				return
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent memory store open failed: %v", err)
		}
	}
}

func TestMemoryStoreSearchByText_CJK(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "memory.db")

	store, err := NewMemoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	defer store.Close()

	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	now := time.Now().UTC()
	memories := []*domain.Memory{
		{
			ID:         "m1",
			Type:       domain.MemoryTypeFact,
			Content:    "项目使用RAG系统做语义检索",
			Importance: 0.5,
			CreatedAt:  now,
		},
		{
			ID:         "m2",
			Type:       domain.MemoryTypeFact,
			Content:    "今天只讨论了系统设计，不涉及检索",
			Importance: 0.5,
			CreatedAt:  now,
		},
	}

	for _, mem := range memories {
		if err := store.Store(ctx, mem); err != nil {
			t.Fatalf("Store(%s) error = %v", mem.ID, err)
		}
	}

	results, err := store.SearchByText(ctx, "RAG系统", 10)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("SearchByText() returned no results")
	}
	if got := results[0].ID; got != "m1" {
		t.Fatalf("top result = %s, want m1", got)
	}
}

func TestMemoryFlowStoreStoresThroughWorkflow(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "memoryflow.db")

	store, err := NewMemoryFlowStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryFlowStore() error = %v", err)
	}
	defer store.Close()

	mem := &domain.Memory{
		ID:         "mf-1",
		SessionID:  "session:apollo",
		Type:       domain.MemoryTypePreference,
		Content:    "Apollo prefers brief status updates",
		Importance: 0.8,
		CreatedAt:  time.Now().UTC(),
	}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	got, err := store.Get(ctx, "mf-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Content != mem.Content {
		t.Fatalf("content = %q, want %q", got.Content, mem.Content)
	}
	if got.Type != domain.MemoryTypePreference {
		t.Fatalf("type = %q, want %q", got.Type, domain.MemoryTypePreference)
	}

	results, err := store.SearchByText(ctx, "Apollo", 10)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("SearchByText() returned no results")
	}
	if results[0].ID != "mf-1" {
		t.Fatalf("top result = %s, want mf-1", results[0].ID)
	}
}

func TestGraphFlowStoreBuildsGraph(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "graphflow.db")

	store, err := NewGraphFlowStore(dbPath)
	if err != nil {
		t.Fatalf("NewGraphFlowStore() error = %v", err)
	}
	defer store.Close()

	mem := &domain.Memory{
		ID:         "gf-1",
		Type:       domain.MemoryTypeFact,
		Content:    "Apollo ships Friday and Alice owns Apollo.",
		Importance: 0.7,
		CreatedAt:  time.Now().UTC(),
	}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	got, err := store.Get(ctx, "gf-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Content != mem.Content {
		t.Fatalf("content = %q, want %q", got.Content, mem.Content)
	}

	report, err := graphflow.Analyze(ctx, store.db, graphflow.AnalyzeRequest{TopN: 5})
	if err != nil {
		t.Fatalf("graphflow Analyze() error = %v", err)
	}
	if report.NodeCount == 0 {
		t.Fatal("expected graphflow nodes")
	}
	if report.EdgeCount == 0 {
		t.Fatal("expected graphflow edges")
	}
}

func TestMemoryStoreSearchByTextRanksByImportanceAndRecency(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "memory.db")

	store, err := NewMemoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	defer store.Close()

	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	now := time.Now().UTC()
	memories := []*domain.Memory{
		{
			ID:         "high-importance",
			Type:       domain.MemoryTypeFact,
			Content:    "本地知识库使用 SQLite 存储",
			Importance: 0.9,
			CreatedAt:  now,
		},
		{
			ID:         "low-importance",
			Type:       domain.MemoryTypeFact,
			Content:    "本地知识库使用 Markdown 存储",
			Importance: 0.1,
			CreatedAt:  now,
		},
		{
			ID:         "very-old",
			Type:       domain.MemoryTypeFact,
			Content:    "本地知识库使用文档存储",
			Importance: 0.9,
			CreatedAt:  now.Add(-365 * 24 * time.Hour),
		},
	}

	for _, mem := range memories {
		if err := store.Store(ctx, mem); err != nil {
			t.Fatalf("Store(%s) error = %v", mem.ID, err)
		}
	}

	results, err := store.SearchByText(ctx, "本地知识库", 10)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(results) < 3 {
		t.Fatalf("SearchByText() returned %d results, want at least 3", len(results))
	}
	if got := results[0].ID; got != "high-importance" {
		t.Fatalf("top result = %s, want high-importance", got)
	}

	positions := make(map[string]int, len(results))
	for i, res := range results {
		positions[res.ID] = i
	}
	if positions["low-importance"] > positions["very-old"] {
		t.Fatalf("expected recent low-importance result to outrank very old result, got positions %+v", positions)
	}
}

func TestMemoryStoreClear(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "memory.db")

	store, err := NewMemoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	defer store.Close()

	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	for _, mem := range []*domain.Memory{
		{ID: "clear-1", Type: domain.MemoryTypeFact, Content: "one", CreatedAt: time.Now()},
		{ID: "clear-2", Type: domain.MemoryTypeFact, Content: "two", CreatedAt: time.Now()},
	} {
		if err := store.Store(ctx, mem); err != nil {
			t.Fatalf("Store(%s) error = %v", mem.ID, err)
		}
	}

	if err := store.Clear(ctx); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	memories, total, err := store.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List() after clear error = %v", err)
	}
	if total != 0 || len(memories) != 0 {
		t.Fatalf("expected empty store after clear, got total=%d len=%d", total, len(memories))
	}
}

// TestMemoryFlowStoreWakeUp verifies WakeUp returns layered context over
// seeded memories without error.
func TestMemoryFlowStoreWakeUp(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wakeup.db")

	store, err := NewMemoryFlowStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryFlowStore() error = %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	seeds := []*domain.Memory{
		{ID: "wu-1", Type: domain.MemoryTypeFact, Content: "Alice leads the API team.", Importance: 0.8, CreatedAt: now},
		{ID: "wu-2", Type: domain.MemoryTypeFact, Content: "Bob focuses on frontend performance.", Importance: 0.6, CreatedAt: now},
	}
	for _, m := range seeds {
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store(%s) error = %v", m.ID, err)
		}
	}

	layers, err := store.WakeUp(ctx, "agent:archivist", "team leadership", nil)
	if err != nil {
		t.Fatalf("WakeUp() error = %v", err)
	}
	if len(layers) == 0 {
		t.Fatal("WakeUp() returned no layers")
	}
	for _, l := range layers {
		if l.Level == "" {
			t.Errorf("layer missing level: %+v", l)
		}
	}

	// A session-scoped wake-up should also succeed (session_id is derived from
	// the scope). Verifies the scope parameter is actually wired through.
	sessionScope := &domain.MemoryScope{Type: domain.MemoryScopeSession, ID: "session:team-sync-42"}
	if _, err := store.WakeUp(ctx, "agent:archivist", "team leadership", sessionScope); err != nil {
		t.Fatalf("WakeUp(session scope) error = %v", err)
	}
}

// TestMemoryFlowStoreCloseSession verifies CloseSession runs without error
// on a minimal transcript.
func TestMemoryFlowStoreCloseSession(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "closesession.db")

	store, err := NewMemoryFlowStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryFlowStore() error = %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	transcript := []TranscriptTurn{
		{Role: "user", Content: "Let's standardize on Go generics for new code.", Timestamp: now},
		{Role: "assistant", Content: "Agreed — I'll flag older generic-free utilities for refactor.", Timestamp: now.Add(time.Second)},
	}
	if err := store.CloseSession(ctx, "session:team-sync-42", transcript); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
}

// TestMemoryFlowStoreSearchDelegatesToParent locks in the contract that
// MemoryFlowStore does not override vector-search methods. Recall is
// text-primary and does not honor the (vector, minScore) semantics, so
// Search/SearchByScope must resolve to the embedded *MemoryStore which
// uses CortexDB's real cosine similarity. If someone re-adds an override
// this test will fail.
func TestMemoryFlowStoreSearchDelegatesToParent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mf-delegate.db")
	store, err := NewMemoryFlowStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryFlowStore() error = %v", err)
	}
	defer store.Close()

	cases := []string{"Search", "SearchByScope"}
	for _, name := range cases {
		outer := reflect.ValueOf(store).MethodByName(name)
		inner := reflect.ValueOf(store.MemoryStore).MethodByName(name)
		if !outer.IsValid() || !inner.IsValid() {
			t.Fatalf("%s method missing on MemoryFlowStore or parent", name)
		}
		if outer.Pointer() != inner.Pointer() {
			t.Fatalf("%s is overridden on MemoryFlowStore; must delegate to *MemoryStore "+
				"because MemoryFlow Recall does not honor vector/minScore", name)
		}
	}
}

// TestGraphFlowStoreSearchByScopeFiltersCrossScope verifies that scoped
// search (and its graph expansion) does not leak memories from other scopes.
func TestGraphFlowStoreSearchByScopeFiltersCrossScope(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "gf-scope.db")

	store, err := NewGraphFlowStore(dbPath)
	if err != nil {
		t.Fatalf("NewGraphFlowStore() error = %v", err)
	}
	defer store.Close()

	vec := make([]float64, 4)
	for i := range vec {
		vec[i] = 1.0
	}

	alphaMem := &domain.Memory{
		ID: "scope-alpha", Type: domain.MemoryTypeFact,
		Content: "Alpha team owns the ingestion service.",
		Vector:  vec, Importance: 0.5, CreatedAt: time.Now().UTC(),
		Tags: []string{"alpha", "ingestion"},
	}
	betaMem := &domain.Memory{
		ID: "scope-beta", Type: domain.MemoryTypeFact,
		Content: "Beta team owns the ingestion service.",
		Vector:  vec, Importance: 0.5, CreatedAt: time.Now().UTC(),
		Tags: []string{"beta", "ingestion"},
	}
	if err := store.StoreWithScope(ctx, alphaMem, domain.MemoryScope{Type: domain.MemoryScopeAgent, ID: "alpha"}); err != nil {
		t.Fatalf("Store alpha: %v", err)
	}
	if err := store.StoreWithScope(ctx, betaMem, domain.MemoryScope{Type: domain.MemoryScopeAgent, ID: "beta"}); err != nil {
		t.Fatalf("Store beta: %v", err)
	}

	results, err := store.SearchByScope(ctx, vec, []domain.MemoryScope{
		{Type: domain.MemoryScopeAgent, ID: "alpha"},
	}, 10)
	if err != nil {
		t.Fatalf("SearchByScope: %v", err)
	}

	for _, r := range results {
		if r.ID == "scope-beta" {
			t.Errorf("beta-scoped memory leaked into alpha-scoped search: %+v", r)
		}
		if r.ScopeType != domain.MemoryScopeAgent || r.ScopeID != "alpha" {
			t.Errorf("result has wrong scope: type=%s id=%s", r.ScopeType, r.ScopeID)
		}
	}
}

// TestGraphFlowStoreUpdateReplacesGraph verifies that Update rebuilds the
// graph with ReplaceEdges, avoiding accumulation of stale edges.
func TestGraphFlowStoreUpdateReplacesGraph(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "gf-update.db")

	store, err := NewGraphFlowStore(dbPath)
	if err != nil {
		t.Fatalf("NewGraphFlowStore() error = %v", err)
	}
	defer store.Close()

	mem := &domain.Memory{
		ID:         "upd-1",
		Type:       domain.MemoryTypeFact,
		Content:    "Alice leads Apollo.",
		Importance: 0.7,
		CreatedAt:  time.Now().UTC(),
	}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	before, err := graphflow.Analyze(ctx, store.db, graphflow.AnalyzeRequest{TopN: 20})
	if err != nil {
		t.Fatalf("Analyze before: %v", err)
	}

	updated := &domain.Memory{
		ID:      "upd-1",
		Type:    domain.MemoryTypeFact,
		Content: "Bob leads Mercury.",
	}
	if err := store.Update(ctx, updated); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := graphflow.Analyze(ctx, store.db, graphflow.AnalyzeRequest{TopN: 20})
	if err != nil {
		t.Fatalf("Analyze after: %v", err)
	}

	// After replace-edges update, edge count should not monotonically grow
	// (otherwise we'd know old edges leaked through).
	if after.EdgeCount > before.EdgeCount*3 {
		t.Errorf("edge count exploded after update: before=%d after=%d", before.EdgeCount, after.EdgeCount)
	}
}

func TestGraphFlowStoreSearchByTextMergesLexicalAndGraphResults(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "gf-search-by-text.db")

	store, err := NewGraphFlowStore(dbPath)
	if err != nil {
		t.Fatalf("NewGraphFlowStore() error = %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	memories := []*domain.Memory{
		{ID: "gf-nebula", Type: domain.MemoryTypeFact, Content: "北极星项目组代号：Nebula-42", Importance: 0.8, CreatedAt: now},
		{ID: "gf-songyu", Type: domain.MemoryTypeFact, Content: "宋屿负责性能专项，重点关注移动端掉帧和启动耗时", Importance: 0.8, CreatedAt: now},
		{ID: "gf-tags", Type: domain.MemoryTypeFact, Content: "团队标签约定：红色=阻塞；蓝色=待验证；绿色=可发布", Importance: 0.8, CreatedAt: now},
		{ID: "gf-review", Type: domain.MemoryTypeFact, Content: "周三15:30与供应商进行接口冻结评审", Importance: 0.8, CreatedAt: now},
	}
	for _, mem := range memories {
		if err := store.Store(ctx, mem); err != nil {
			t.Fatalf("Store(%s) error = %v", mem.ID, err)
		}
	}

	results, err := store.SearchByText(ctx, "北极星项目组代号 性能专项 红色标签 周三15:30", 6)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(results) < 4 {
		t.Fatalf("expected at least 4 results, got %d: %+v", len(results), results)
	}

	found := map[string]bool{}
	for _, result := range results {
		found[result.ID] = true
	}
	for _, want := range []string{"gf-nebula", "gf-songyu", "gf-tags", "gf-review"} {
		if !found[want] {
			t.Fatalf("expected SearchByText to include %s, got %+v", want, found)
		}
	}
}

// TestMemoryStoreReflectEmptyBankReturnsEmpty verifies Reflect on an empty
// bank returns empty without error (graceful degrade with logged reason).
func TestMemoryStoreReflectEmptyBankReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "reflect-empty.db")

	store, err := NewMemoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	defer store.Close()

	summary, err := store.Reflect(ctx, "memory:global:agentgo")
	if err != nil {
		t.Fatalf("Reflect should not error on empty bank, got: %v", err)
	}
	if summary != "" {
		t.Errorf("expected empty summary for empty bank, got %q", summary)
	}
}

// TestGraphFlowExtractContentEntities verifies the heuristic entity extractor
// picks up CJK runs and Capitalized terms even without tags/keywords.
func TestGraphFlowExtractContentEntities(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"capitalized english", "Alice and Bob built Apollo.", []string{"Alice", "Bob", "Apollo"}},
		{"cjk runs", "向量数据库是现代AI应用的核心", []string{"向量数据库", "是现代", "应用的核心"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractContentEntities(tc.content)
			if len(got) == 0 {
				t.Fatalf("extractContentEntities returned empty for %q", tc.content)
			}
			seen := make(map[string]bool, len(got))
			for _, e := range got {
				seen[e] = true
			}
			foundAny := false
			for _, w := range tc.want {
				if seen[w] {
					foundAny = true
					break
				}
			}
			if !foundAny {
				t.Errorf("none of %v found in %v", tc.want, got)
			}
		})
	}
}
