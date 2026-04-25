package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestCortexStore(t *testing.T) *MemoryStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_memory.db")
	s, err := NewCortexMemoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewCortexMemoryStore: %v", err)
	}
	if err := s.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func makeMem(id, content string, opts ...func(*domain.Memory)) *domain.Memory {
	m := &domain.Memory{
		ID:         id,
		Type:       domain.MemoryTypeFact,
		Content:    content,
		Importance: 0.5,
		CreatedAt:  time.Now().UTC(),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

func withImportance(v float64) func(*domain.Memory) {
	return func(m *domain.Memory) { m.Importance = v }
}

func withType(t domain.MemoryType) func(*domain.Memory) {
	return func(m *domain.Memory) { m.Type = t }
}

func withScope(st domain.MemoryScopeType, id string) func(*domain.Memory) {
	return func(m *domain.Memory) { m.ScopeType = st; m.ScopeID = id }
}

func withSession(sid string) func(*domain.Memory) {
	return func(m *domain.Memory) { m.SessionID = sid }
}

func withTags(tags ...string) func(*domain.Memory) {
	return func(m *domain.Memory) { m.Tags = tags }
}

func withKeywords(kw ...string) func(*domain.Memory) {
	return func(m *domain.Memory) { m.Keywords = kw }
}

func withCreatedAt(t time.Time) func(*domain.Memory) {
	return func(m *domain.Memory) { m.CreatedAt = t }
}

// ---------------------------------------------------------------------------
// CRUD basics
// ---------------------------------------------------------------------------

func TestCortexDB_StoreAndGet(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	mem := makeMem("crud-1", "CortexDB stores memories in SQLite",
		withImportance(0.8),
		withType(domain.MemoryTypeSkill),
		withTags("db", "sqlite"),
		withKeywords("cortexdb", "store"),
	)

	if err := s.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := s.Get(ctx, "crud-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != mem.Content {
		t.Errorf("content = %q, want %q", got.Content, mem.Content)
	}
	if got.Type != domain.MemoryTypeSkill {
		t.Errorf("type = %q, want %q", got.Type, domain.MemoryTypeSkill)
	}
	if got.Importance != 0.8 {
		t.Errorf("importance = %f, want 0.8", got.Importance)
	}
}

func TestCortexDB_GetNotFound(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	_, err := s.Get(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent memory")
	}
	if err != ErrMemoryNotFound {
		t.Fatalf("expected ErrMemoryNotFound, got %v", err)
	}
}

func TestCortexDB_StoreValidation(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	tests := []struct {
		name string
		mem  *domain.Memory
	}{
		{"nil memory", nil},
		{"empty id", makeMem("", "content")},
		{"empty content", makeMem("x", "")},
		{"whitespace id", makeMem("  ", "content")},
		{"whitespace content", makeMem("x", "   ")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.Store(ctx, tt.mem); err == nil {
				t.Error("expected error for invalid memory")
			}
		})
	}
}

func TestCortexDB_Update(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	orig := makeMem("upd-1", "original content", withImportance(0.3))
	if err := s.Store(ctx, orig); err != nil {
		t.Fatalf("Store: %v", err)
	}

	update := &domain.Memory{
		ID:         "upd-1",
		Content:    "updated content",
		Importance: 0.9,
		Type:       domain.MemoryTypePreference,
	}
	if err := s.Update(ctx, update); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(ctx, "upd-1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Content != "updated content" {
		t.Errorf("content = %q, want %q", got.Content, "updated content")
	}
	if got.Importance != 0.9 {
		t.Errorf("importance = %f, want 0.9", got.Importance)
	}
	if got.Type != domain.MemoryTypePreference {
		t.Errorf("type = %q, want %q", got.Type, domain.MemoryTypePreference)
	}
}

func TestCortexDB_UpdateNonexistent(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	if err := s.Update(ctx, &domain.Memory{ID: "ghost", Content: "boo"}); err == nil {
		t.Fatal("expected error updating nonexistent memory")
	}
}

func TestCortexDB_Delete(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	if err := s.Store(ctx, makeMem("del-1", "to be deleted")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if err := s.Delete(ctx, "del-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := s.Get(ctx, "del-1")
	if err != ErrMemoryNotFound {
		t.Fatalf("expected ErrMemoryNotFound after delete, got %v", err)
	}
}

func TestCortexDB_DeleteNonexistent(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	err := s.Delete(ctx, "never-existed")
	if err == nil {
		t.Fatal("expected error deleting nonexistent memory")
	}
}

// ---------------------------------------------------------------------------
// List / Pagination
// ---------------------------------------------------------------------------

func TestCortexDB_ListPagination(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	total := 25
	for i := 0; i < total; i++ {
		m := makeMem(fmt.Sprintf("page-%03d", i), fmt.Sprintf("memory item %d", i),
			withCreatedAt(time.Now().UTC().Add(-time.Duration(total-i)*time.Second)),
		)
		if err := s.Store(ctx, m); err != nil {
			t.Fatalf("Store(%d): %v", i, err)
		}
	}

	// first page
	page1, count, err := s.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if count != total {
		t.Errorf("total = %d, want %d", count, total)
	}
	if len(page1) != 10 {
		t.Errorf("page1 len = %d, want 10", len(page1))
	}

	// second page
	page2, _, err := s.List(ctx, 10, 10)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 10 {
		t.Errorf("page2 len = %d, want 10", len(page2))
	}

	// last page
	page3, _, err := s.List(ctx, 10, 20)
	if err != nil {
		t.Fatalf("List page3: %v", err)
	}
	if len(page3) != 5 {
		t.Errorf("page3 len = %d, want 5", len(page3))
	}

	// no overlap between pages
	seen := map[string]bool{}
	for _, m := range page1 {
		seen[m.ID] = true
	}
	for _, m := range page2 {
		if seen[m.ID] {
			t.Errorf("overlap: %s in both page1 and page2", m.ID)
		}
		seen[m.ID] = true
	}
	for _, m := range page3 {
		if seen[m.ID] {
			t.Errorf("overlap: %s in earlier page", m.ID)
		}
	}
}

func TestCortexDB_ListEmpty(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	memories, total, err := s.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(memories) != 0 {
		t.Errorf("expected empty, got total=%d len=%d", total, len(memories))
	}
}

// ---------------------------------------------------------------------------
// Clear
// ---------------------------------------------------------------------------

func TestCortexDB_ClearAll(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := s.Store(ctx, makeMem(fmt.Sprintf("clr-%d", i), fmt.Sprintf("content %d", i))); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	if err := s.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	memories, total, err := s.List(ctx, 100, 0)
	if err != nil {
		t.Fatalf("List after clear: %v", err)
	}
	if total != 0 || len(memories) != 0 {
		t.Fatalf("expected empty after clear, got total=%d len=%d", total, len(memories))
	}
}

// ---------------------------------------------------------------------------
// SearchByText (BM25 / n-gram)
// ---------------------------------------------------------------------------

func TestCortexDB_SearchByText_Basic(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	mems := []*domain.Memory{
		makeMem("txt-1", "Go is a statically typed, compiled programming language"),
		makeMem("txt-2", "Python is an interpreted high-level programming language"),
		makeMem("txt-3", "Rust emphasizes memory safety and performance"),
	}
	for _, m := range mems {
		if err := s.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	results, err := s.SearchByText(ctx, "Go programming", 10)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'Go programming'")
	}
	// The Go memory should rank higher
	if results[0].ID != "txt-1" {
		t.Errorf("top result = %s, want txt-1", results[0].ID)
	}
}

func TestCortexDB_SearchByText_CJKMixed(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	mems := []*domain.Memory{
		makeMem("cjk-1", "使用向量数据库进行语义搜索是现代AI应用的核心"),
		makeMem("cjk-2", "传统数据库依赖关键字匹配进行全文检索"),
		makeMem("cjk-3", "CortexDB combines vector search and knowledge graphs"),
	}
	for _, m := range mems {
		if err := s.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	// Chinese query
	results, err := s.SearchByText(ctx, "向量数据库", 10)
	if err != nil {
		t.Fatalf("SearchByText Chinese: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for Chinese query")
	}
	if results[0].ID != "cjk-1" {
		t.Errorf("top result = %s, want cjk-1", results[0].ID)
	}

	// English query
	results2, err := s.SearchByText(ctx, "CortexDB vector", 10)
	if err != nil {
		t.Fatalf("SearchByText English: %v", err)
	}
	if len(results2) == 0 {
		t.Fatal("expected results for English query")
	}
	if results2[0].ID != "cjk-3" {
		t.Errorf("top result = %s, want cjk-3", results2[0].ID)
	}
}

func TestCortexDB_SearchByText_NoResults(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	if err := s.Store(ctx, makeMem("nr-1", "hello world")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	results, err := s.SearchByText(ctx, "zzzznonexistent99999", 10)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestCortexDB_SearchByText_ImportanceRanking(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	mems := []*domain.Memory{
		makeMem("imp-high", "memory system architecture design", withImportance(0.95), withCreatedAt(now)),
		makeMem("imp-low", "memory system overview and basics", withImportance(0.1), withCreatedAt(now)),
	}
	for _, m := range mems {
		if err := s.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	results, err := s.SearchByText(ctx, "memory system", 10)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "imp-high" {
		t.Errorf("high importance should rank first, got %s", results[0].ID)
	}
}

func TestCortexDB_SearchByText_RecencyDecay(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	mems := []*domain.Memory{
		makeMem("recent", "agent framework design patterns", withImportance(0.5), withCreatedAt(now)),
		makeMem("ancient", "agent framework design patterns", withImportance(0.5), withCreatedAt(now.Add(-730*24*time.Hour))),
	}
	for _, m := range mems {
		if err := s.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	results, err := s.SearchByText(ctx, "agent framework", 10)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "recent" {
		t.Errorf("recent memory should rank first, got %s", results[0].ID)
	}
	if results[0].Score <= results[1].Score {
		t.Errorf("recent score (%f) should be > ancient score (%f)", results[0].Score, results[1].Score)
	}
}

func TestCortexDB_SearchByText_TopKLimit(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		if err := s.Store(ctx, makeMem(fmt.Sprintf("topk-%d", i), fmt.Sprintf("database query optimization technique %d", i))); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	results, err := s.SearchByText(ctx, "database query", 5)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(results) > 5 {
		t.Errorf("expected at most 5 results, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Scoped storage
// ---------------------------------------------------------------------------

func TestCortexDB_StoreWithScope(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	scopes := []struct {
		name  string
		scope domain.MemoryScope
	}{
		{"global", domain.MemoryScope{Type: domain.MemoryScopeGlobal}},
		{"agent", domain.MemoryScope{Type: domain.MemoryScopeAgent, ID: "planner-01"}},
		{"team", domain.MemoryScope{Type: domain.MemoryScopeTeam, ID: "engineering"}},
		{"user", domain.MemoryScope{Type: domain.MemoryScopeUser, ID: "user-42"}},
		{"session", domain.MemoryScope{Type: domain.MemoryScopeSession, ID: "session-abc"}},
	}

	for i, sc := range scopes {
		mem := makeMem(fmt.Sprintf("scope-%d", i), fmt.Sprintf("%s scoped memory", sc.name))
		if err := s.StoreWithScope(ctx, mem, sc.scope); err != nil {
			t.Fatalf("StoreWithScope(%s): %v", sc.name, err)
		}

		got, err := s.Get(ctx, fmt.Sprintf("scope-%d", i))
		if err != nil {
			t.Fatalf("Get(%s): %v", sc.name, err)
		}
		if got.Content != mem.Content {
			t.Errorf("scope %s: content = %q, want %q", sc.name, got.Content, mem.Content)
		}
	}

	// all should be listable
	all, total, err := s.List(ctx, 100, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != len(scopes) {
		t.Errorf("total = %d, want %d", total, len(scopes))
	}
	if len(all) != len(scopes) {
		t.Errorf("listed = %d, want %d", len(all), len(scopes))
	}
}

func TestCortexDB_ScopeFromMemoryFields(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	// Memory with explicit scope fields
	mem := makeMem("sf-1", "agent-scoped memory",
		withScope(domain.MemoryScopeAgent, "researcher"),
	)
	if err := s.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := s.Get(ctx, "sf-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ScopeType != domain.MemoryScopeAgent {
		t.Errorf("scope type = %q, want %q", got.ScopeType, domain.MemoryScopeAgent)
	}
	if got.ScopeID != "researcher" {
		t.Errorf("scope id = %q, want %q", got.ScopeID, "researcher")
	}
}

func TestCortexDB_SessionIDFallback(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	mem := makeMem("sid-1", "session-based memory",
		withSession("my-session-xyz"),
	)
	if err := s.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := s.Get(ctx, "sid-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Session ID should be preserved via bank_id
	if got.SessionID == "" {
		t.Error("expected non-empty session ID")
	}
}

// ---------------------------------------------------------------------------
// DeleteBySession
// ---------------------------------------------------------------------------

func TestCortexDB_DeleteBySession(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	// Store memories in different sessions
	for i := 0; i < 3; i++ {
		mem := makeMem(fmt.Sprintf("sess-a-%d", i), fmt.Sprintf("session A memory %d", i),
			withSession("session-A"),
		)
		if err := s.Store(ctx, mem); err != nil {
			t.Fatalf("Store session-A: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		mem := makeMem(fmt.Sprintf("sess-b-%d", i), fmt.Sprintf("session B memory %d", i),
			withSession("session-B"),
		)
		if err := s.Store(ctx, mem); err != nil {
			t.Fatalf("Store session-B: %v", err)
		}
	}

	// Delete session A
	if err := s.DeleteBySession(ctx, "session-A"); err != nil {
		t.Fatalf("DeleteBySession: %v", err)
	}

	// Session A memories should be gone
	for i := 0; i < 3; i++ {
		_, err := s.Get(ctx, fmt.Sprintf("sess-a-%d", i))
		if err != ErrMemoryNotFound {
			t.Errorf("sess-a-%d should be deleted, got err=%v", i, err)
		}
	}

	// Session B memories should remain
	for i := 0; i < 2; i++ {
		got, err := s.Get(ctx, fmt.Sprintf("sess-b-%d", i))
		if err != nil {
			t.Errorf("sess-b-%d should still exist: %v", i, err)
		}
		if got != nil && got.Content == "" {
			t.Errorf("sess-b-%d should have content", i)
		}
	}
}

// ---------------------------------------------------------------------------
// IncrementAccess
// ---------------------------------------------------------------------------

func TestCortexDB_IncrementAccess(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	if err := s.Store(ctx, makeMem("acc-1", "frequently accessed memory")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	for i := 0; i < 5; i++ {
		if err := s.IncrementAccess(ctx, "acc-1"); err != nil {
			t.Fatalf("IncrementAccess(%d): %v", i, err)
		}
	}

	got, err := s.Get(ctx, "acc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessCount != 5 {
		t.Errorf("access count = %d, want 5", got.AccessCount)
	}
	if got.LastAccessed.IsZero() {
		t.Error("last_accessed should be set")
	}
}

// ---------------------------------------------------------------------------
// GetByType
// ---------------------------------------------------------------------------

func TestCortexDB_GetByType(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	mems := []*domain.Memory{
		makeMem("type-1", "fact one", withType(domain.MemoryTypeFact)),
		makeMem("type-2", "fact two", withType(domain.MemoryTypeFact)),
		makeMem("type-3", "skill one", withType(domain.MemoryTypeSkill)),
		makeMem("type-4", "preference one", withType(domain.MemoryTypePreference)),
		makeMem("type-5", "pattern one", withType(domain.MemoryTypePattern)),
	}
	for _, m := range mems {
		if err := s.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	facts, err := s.GetByType(ctx, domain.MemoryTypeFact, 10)
	if err != nil {
		t.Fatalf("GetByType(fact): %v", err)
	}
	if len(facts) != 2 {
		t.Errorf("facts = %d, want 2", len(facts))
	}

	skills, err := s.GetByType(ctx, domain.MemoryTypeSkill, 10)
	if err != nil {
		t.Fatalf("GetByType(skill): %v", err)
	}
	if len(skills) != 1 {
		t.Errorf("skills = %d, want 1", len(skills))
	}

	observations, err := s.GetByType(ctx, domain.MemoryTypeObservation, 10)
	if err != nil {
		t.Fatalf("GetByType(observation): %v", err)
	}
	if len(observations) != 0 {
		t.Errorf("observations = %d, want 0", len(observations))
	}
}

// ---------------------------------------------------------------------------
// ConfigureBank
// ---------------------------------------------------------------------------

func TestCortexDB_ConfigureBank(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	// Store a memory first to create the bucket
	if err := s.Store(ctx, makeMem("bank-1", "memory in bank", withSession("research-bank"))); err != nil {
		t.Fatalf("Store: %v", err)
	}

	config := &domain.MemoryBankConfig{
		Mission:    "Research assistant memory bank",
		Directives: []string{"Always cite sources", "Prefer recent data"},
		Skepticism: 3,
		Literalism: 2,
		Empathy:    4,
	}

	if err := s.ConfigureBank(ctx, "research-bank", config); err != nil {
		t.Fatalf("ConfigureBank: %v", err)
	}

	// Memory should still be accessible
	got, err := s.Get(ctx, "bank-1")
	if err != nil {
		t.Fatalf("Get after ConfigureBank: %v", err)
	}
	if got.Content != "memory in bank" {
		t.Errorf("content = %q, want %q", got.Content, "memory in bank")
	}
}

// ---------------------------------------------------------------------------
// AddMentalModel
// ---------------------------------------------------------------------------

func TestCortexDB_AddMentalModel(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	model := &domain.MentalModel{
		ID:          "mm-1",
		Name:        "OODA Loop",
		Description: "Decision-making framework",
		Content:     "Observe → Orient → Decide → Act",
		Tags:        []string{"decision-making", "strategy"},
	}

	if err := s.AddMentalModel(ctx, model); err != nil {
		t.Fatalf("AddMentalModel: %v", err)
	}

	got, err := s.Get(ctx, "mm-1")
	if err != nil {
		t.Fatalf("Get mental model: %v", err)
	}
	if got.Type != domain.MemoryTypeObservation {
		t.Errorf("type = %q, want observation", got.Type)
	}
	if got.Importance != 1.0 {
		t.Errorf("importance = %f, want 1.0", got.Importance)
	}
}

func TestCortexDB_AddMentalModelNil(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	if err := s.AddMentalModel(ctx, nil); err != nil {
		t.Fatalf("AddMentalModel(nil) should not error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Upsert (Store same ID twice)
// ---------------------------------------------------------------------------

func TestCortexDB_UpsertOverwrite(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	v1 := makeMem("upsert-1", "version one", withImportance(0.3))
	if err := s.Store(ctx, v1); err != nil {
		t.Fatalf("Store v1: %v", err)
	}

	v2 := makeMem("upsert-1", "version two", withImportance(0.7))
	if err := s.Store(ctx, v2); err != nil {
		t.Fatalf("Store v2: %v", err)
	}

	got, err := s.Get(ctx, "upsert-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "version two" {
		t.Errorf("content = %q, want 'version two'", got.Content)
	}

	_, total, err := s.List(ctx, 100, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (upsert should not duplicate)", total)
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestCortexDB_ConcurrentStoreAndSearch(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	const workers = 4
	const memoriesPerWorker = 5

	var wg sync.WaitGroup
	errCh := make(chan error, workers*memoriesPerWorker*2)

	// Concurrent writes
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < memoriesPerWorker; i++ {
				id := fmt.Sprintf("conc-%d-%d", worker, i)
				mem := makeMem(id, fmt.Sprintf("concurrent memory from worker %d item %d about database storage", worker, i))
				if err := s.Store(ctx, mem); err != nil {
					errCh <- fmt.Errorf("Store(%s): %w", id, err)
				}
			}
		}(w)
	}

	// Concurrent reads while writing
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				if _, err := s.SearchByText(ctx, "database storage", 5); err != nil {
					errCh <- fmt.Errorf("SearchByText: %w", err)
				}
				if _, _, err := s.List(ctx, 10, 0); err != nil {
					errCh <- fmt.Errorf("List: %w", err)
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent op: %v", err)
		}
	}

	// Verify all memories stored
	_, total, err := s.List(ctx, 1, 0)
	if err != nil {
		t.Fatalf("final List: %v", err)
	}
	expected := workers * memoriesPerWorker
	if total != expected {
		t.Errorf("total = %d, want %d", total, expected)
	}
}

// ---------------------------------------------------------------------------
// Metadata / Tags / Keywords round-trip
// ---------------------------------------------------------------------------

func TestCortexDB_MetadataRoundTrip(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	mem := makeMem("meta-1", "memory with rich metadata",
		withTags("important", "architecture", "v2"),
		withKeywords("design", "patterns"),
		withImportance(0.85),
	)
	mem.Metadata = map[string]interface{}{
		"source":  "user_input",
		"project": "cortexdb",
	}

	if err := s.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := s.Get(ctx, "meta-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Tags
	if len(got.Tags) == 0 {
		t.Error("expected tags to be preserved")
	}

	// Keywords
	if len(got.Keywords) == 0 {
		t.Error("expected keywords to be preserved")
	}

	// Custom metadata
	if got.Metadata == nil {
		t.Fatal("expected metadata")
	}
	if got.Metadata["source"] != "user_input" {
		t.Errorf("metadata.source = %v, want user_input", got.Metadata["source"])
	}
}

// ---------------------------------------------------------------------------
// Vector search
// ---------------------------------------------------------------------------

func TestCortexDB_VectorSearch(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	dim := 8
	vec1 := make([]float64, dim)
	vec2 := make([]float64, dim)
	vec3 := make([]float64, dim)
	for i := 0; i < dim; i++ {
		vec1[i] = 1.0  // identical to query
		vec2[i] = 0.5  // same direction but mixed
		vec3[i] = -1.0 // opposite
	}
	// Make vec2 clearly different from query by adding some orthogonal noise
	vec2[0] = 0.1
	vec2[1] = -0.3

	mems := []*domain.Memory{
		{ID: "vec-1", Type: domain.MemoryTypeFact, Content: "similar to query", Vector: vec1, Importance: 0.5, CreatedAt: time.Now().UTC()},
		{ID: "vec-2", Type: domain.MemoryTypeFact, Content: "somewhat similar", Vector: vec2, Importance: 0.5, CreatedAt: time.Now().UTC()},
		{ID: "vec-3", Type: domain.MemoryTypeFact, Content: "opposite direction", Vector: vec3, Importance: 0.5, CreatedAt: time.Now().UTC()},
	}

	for _, m := range mems {
		if err := s.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	queryVec := make([]float64, dim)
	for i := 0; i < dim; i++ {
		queryVec[i] = 1.0
	}

	results, err := s.Search(ctx, queryVec, 10, 0.5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results above minScore 0.5, got %d", len(results))
	}
	if results[0].ID != "vec-1" {
		t.Errorf("top result = %s, want vec-1", results[0].ID)
	}
	// The opposite vector should not appear (cosine sim < 0.5)
	for _, r := range results {
		if r.ID == "vec-3" {
			t.Errorf("opposite vector should be filtered by minScore, score=%f", r.Score)
		}
	}
}

func TestCortexDB_VectorSearchEmptyQuery(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	results, err := s.Search(ctx, nil, 10, 0)
	if err != nil {
		t.Fatalf("Search with nil vector: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for nil vector, got %d", len(results))
	}

	results2, err := s.Search(ctx, []float64{}, 10, 0)
	if err != nil {
		t.Fatalf("Search with empty vector: %v", err)
	}
	if len(results2) != 0 {
		t.Errorf("expected 0 results for empty vector, got %d", len(results2))
	}
}

// ---------------------------------------------------------------------------
// SearchByScope
// ---------------------------------------------------------------------------

func TestCortexDB_SearchByScope(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	dim := 4
	vec := make([]float64, dim)
	for i := range vec {
		vec[i] = 1.0
	}

	// Store in agent scope
	mem := &domain.Memory{
		ID: "scoped-vec-1", Type: domain.MemoryTypeFact,
		Content: "agent memory with vector", Vector: vec,
		Importance: 0.5, CreatedAt: time.Now().UTC(),
	}
	if err := s.StoreWithScope(ctx, mem, domain.MemoryScope{Type: domain.MemoryScopeAgent, ID: "alpha"}); err != nil {
		t.Fatalf("StoreWithScope: %v", err)
	}

	// Search only within that scope
	results, err := s.SearchByScope(ctx, vec, []domain.MemoryScope{
		{Type: domain.MemoryScopeAgent, ID: "alpha"},
	}, 10)
	if err != nil {
		t.Fatalf("SearchByScope: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from agent scope")
	}
	if results[0].ID != "scoped-vec-1" {
		t.Errorf("result = %s, want scoped-vec-1", results[0].ID)
	}

	// Search in a different scope should be empty
	results2, err := s.SearchByScope(ctx, vec, []domain.MemoryScope{
		{Type: domain.MemoryScopeAgent, ID: "beta"},
	}, 10)
	if err != nil {
		t.Fatalf("SearchByScope(beta): %v", err)
	}
	if len(results2) != 0 {
		t.Errorf("expected 0 results from beta scope, got %d", len(results2))
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestCortexDB_LargeContent(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	// ~100KB content
	bigContent := ""
	for i := 0; i < 1000; i++ {
		bigContent += fmt.Sprintf("Line %d: This is a test line with some meaningful content about knowledge management.\n", i)
	}

	mem := makeMem("big-1", bigContent)
	if err := s.Store(ctx, mem); err != nil {
		t.Fatalf("Store large content: %v", err)
	}

	got, err := s.Get(ctx, "big-1")
	if err != nil {
		t.Fatalf("Get large content: %v", err)
	}
	if len(got.Content) != len(bigContent) {
		t.Errorf("content length = %d, want %d", len(got.Content), len(bigContent))
	}
}

func TestCortexDB_SpecialCharacters(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	special := `Content with "quotes", 'apostrophes', \backslashes\, % percent, _ underscore, and emoji 🧠`
	mem := makeMem("special-1", special)
	if err := s.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := s.Get(ctx, "special-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != special {
		t.Errorf("content mismatch:\ngot:  %q\nwant: %q", got.Content, special)
	}
}

func TestCortexDB_StoreDeleteStore(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	mem := makeMem("recycle-1", "first version")
	if err := s.Store(ctx, mem); err != nil {
		t.Fatalf("Store v1: %v", err)
	}
	if err := s.Delete(ctx, "recycle-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	mem2 := makeMem("recycle-1", "second version")
	if err := s.Store(ctx, mem2); err != nil {
		t.Fatalf("Store v2: %v", err)
	}

	got, err := s.Get(ctx, "recycle-1")
	if err != nil {
		t.Fatalf("Get v2: %v", err)
	}
	if got.Content != "second version" {
		t.Errorf("content = %q, want 'second version'", got.Content)
	}
}

func TestCortexDB_Reflect(t *testing.T) {
	s := newTestCortexStore(t)
	ctx := context.Background()

	// Reflect returns empty for cortexdb store (file-store only)
	result, err := s.Reflect(ctx, "any-bank")
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if result != "" {
		t.Errorf("Reflect = %q, want empty", result)
	}
}
