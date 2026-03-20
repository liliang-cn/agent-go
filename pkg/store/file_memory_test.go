package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestFileMemoryStoreCRUDAndSearch(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file memory store failed: %v", err)
	}

	fact := &domain.Memory{
		ID:         "fact-1",
		Type:       domain.MemoryTypeFact,
		Content:    "Project status is green",
		Importance: 0.9,
		SessionID:  "session-1",
		CreatedAt:  time.Now(),
	}
	contextMem := &domain.Memory{
		ID:         "ctx-1",
		Type:       domain.MemoryTypeContext,
		Content:    "Conversation context entry",
		Importance: 0.4,
		CreatedAt:  time.Now(),
	}
	squadMem := &domain.Memory{
		ID:         "fact-squad-1",
		Type:       domain.MemoryTypeFact,
		Content:    "Shared squad constraint",
		Importance: 0.8,
		CreatedAt:  time.Now(),
	}

	if err := store.Store(ctx, fact); err != nil {
		t.Fatalf("store fact failed: %v", err)
	}
	if err := store.StoreWithScope(ctx, contextMem, domain.MemoryScope{Type: domain.MemoryScopeSession, ID: "session-1"}); err != nil {
		t.Fatalf("store context failed: %v", err)
	}
	if err := store.StoreWithScope(ctx, squadMem, domain.MemoryScope{Type: domain.MemoryScopeSquad, ID: "alpha"}); err != nil {
		t.Fatalf("store squad memory failed: %v", err)
	}

	got, err := store.Get(ctx, "fact-1")
	if err != nil || got.Content != fact.Content {
		t.Fatalf("unexpected get result: %v %+v", err, got)
	}
	if got.ScopeType != domain.MemoryScopeSession || got.ScopeID != "session-1" {
		t.Fatalf("expected legacy raw session ID to infer session scope, got %+v", got)
	}

	list, total, err := store.List(ctx, 10, 0)
	if err != nil || total != 3 || len(list) != 3 {
		t.Fatalf("unexpected list result: err=%v total=%d len=%d", err, total, len(list))
	}

	byType, err := store.GetByType(ctx, domain.MemoryTypeFact, 10)
	if err != nil || len(byType) != 2 {
		t.Fatalf("unexpected type filter result: %v %+v", err, byType)
	}

	searchHits, err := store.SearchByText(ctx, "green", 10)
	if err != nil || len(searchHits) != 1 || searchHits[0].Memory.ID != "fact-1" {
		t.Fatalf("unexpected text search result: %v %+v", err, searchHits)
	}

	sessionHits, err := store.SearchBySession(ctx, "session-1", nil, 10)
	if err != nil || len(sessionHits) != 2 {
		t.Fatalf("unexpected session search result: %v len=%d", err, len(sessionHits))
	}

	scopeHits, err := store.SearchByScope(ctx, nil, []domain.MemoryScope{{Type: domain.MemoryScopeSession, ID: "session-1"}}, 10)
	if err != nil || len(scopeHits) != 2 {
		t.Fatalf("unexpected scope search result: %v len=%d", err, len(scopeHits))
	}

	squadHits, err := store.SearchByScope(ctx, nil, []domain.MemoryScope{{Type: domain.MemoryScopeSquad, ID: "alpha"}}, 10)
	if err != nil || len(squadHits) != 1 || squadHits[0].Memory.ID != "fact-squad-1" {
		t.Fatalf("unexpected squad scope search result: %v len=%d", err, len(squadHits))
	}

	if err := store.IncrementAccess(ctx, "fact-1"); err != nil {
		t.Fatalf("increment access failed: %v", err)
	}
	got, _ = store.Get(ctx, "fact-1")
	if got.AccessCount == 0 {
		t.Fatal("expected access count to increment")
	}
}

func TestFileMemoryStoreDeleteAndStale(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file memory store failed: %v", err)
	}

	mem := &domain.Memory{
		ID:         "fact-2",
		Type:       domain.MemoryTypeFact,
		Content:    "Old fact",
		Importance: 0.8,
		SessionID:  "session-x",
		CreatedAt:  time.Now(),
	}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("store failed: %v", err)
	}

	if err := store.MarkStale(ctx, "fact-2", "fact-3"); err != nil {
		t.Fatalf("mark stale failed: %v", err)
	}
	got, err := store.Get(ctx, "fact-2")
	if err != nil {
		t.Fatalf("get after stale failed: %v", err)
	}
	if !IsStale(got) || got.SupersededBy != "fact-3" || len(got.RevisionHistory) != 1 {
		t.Fatalf("expected stale metadata, got %+v", got)
	}

	if err := store.DeleteBySession(ctx, "session-x"); err != nil {
		t.Fatalf("delete by session failed: %v", err)
	}
	if _, err := store.Get(ctx, "fact-2"); err == nil {
		t.Fatal("expected deleted session memory to be gone")
	}
}

func TestFileMemoryStoreIndexAndHelpers(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	store, err := NewFileMemoryStore(baseDir)
	if err != nil {
		t.Fatalf("new file memory store failed: %v", err)
	}

	obs := &domain.Memory{
		ID:         "obs-1",
		Type:       domain.MemoryTypeObservation,
		Content:    "Observation content line one\nline two",
		Importance: 0.7,
		ScopeType:  domain.MemoryScopeSquad,
		ScopeID:    "alpha",
		CreatedAt:  time.Now(),
	}
	fact := &domain.Memory{
		ID:         "fact-4",
		Type:       domain.MemoryTypeFact,
		Content:    "Fact content",
		Importance: 0.9,
		ScopeType:  domain.MemoryScopeSquad,
		ScopeID:    "alpha",
		CreatedAt:  time.Now(),
	}
	if err := store.Store(ctx, obs); err != nil {
		t.Fatalf("store observation failed: %v", err)
	}
	if err := store.Store(ctx, fact); err != nil {
		t.Fatalf("store fact failed: %v", err)
	}
	if err := store.MarkStale(ctx, "obs-1", "obs-2"); err != nil {
		t.Fatalf("mark stale failed: %v", err)
	}

	if err := store.RebuildIndex(ctx); err != nil {
		t.Fatalf("rebuild index failed: %v", err)
	}
	index, err := store.ReadIndex(ctx)
	if err != nil {
		t.Fatalf("read index failed: %v", err)
	}
	if index.Total < 2 || len(index.Entries) < 2 {
		t.Fatalf("unexpected index contents: %+v", index)
	}

	data, err := os.ReadFile(store.indexFilePath(domain.MemoryTypeObservation))
	if err != nil {
		t.Fatalf("read index file failed: %v", err)
	}
	parsed, err := parseMemoryIndex(data, domain.MemoryTypeObservation)
	if err != nil {
		t.Fatalf("parse memory index failed: %v", err)
	}
	if len(parsed.Entries) == 0 || !parsed.Entries[0].IsStale {
		t.Fatalf("expected stale observation entry, got %+v", parsed.Entries)
	}

	if got := scopeToBankIDFile(domain.MemoryScope{Type: domain.MemoryScopeGlobal}); got != "global" {
		t.Fatalf("unexpected global scope: %s", got)
	}
	if got := scopeToBankIDFile(domain.MemoryScope{Type: domain.MemoryScopeSession, ID: "abc"}); got != "abc" {
		t.Fatalf("unexpected scoped bank id: %s", got)
	}
	if got := scopeToBankIDFile(domain.MemoryScope{Type: domain.MemoryScopeProject, ID: "alpha"}); got != "squad:alpha" {
		t.Fatalf("unexpected project compatibility bank id: %s", got)
	}

	scopeIndex, err := store.ReadScopeIndex(ctx, domain.MemoryScope{Type: domain.MemoryScopeSquad, ID: "alpha"})
	if err != nil {
		t.Fatalf("read scope index failed: %v", err)
	}
	if len(scopeIndex.Entries) != 2 {
		t.Fatalf("expected 2 squad scope index entries, got %+v", scopeIndex.Entries)
	}
	if scopeIndex.Entries[0].Type == "" {
		t.Fatalf("expected scope index to preserve entry types, got %+v", scopeIndex.Entries[0])
	}

	if got := truncate("abcdef", 3); got != "abc…" {
		t.Fatalf("unexpected truncate result: %s", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Fatalf("unexpected untruncated result: %s", got)
	}

	raw := "prefix <think>reasoning</think> ```json\n{\"items\":[1,2]}\n``` suffix"
	var parsedJSON struct {
		Items []int `json:"items"`
	}
	if err := parseJSON(raw, &parsedJSON); err != nil {
		t.Fatalf("parse json failed: %v", err)
	}
	if len(parsedJSON.Items) != 2 || parsedJSON.Items[1] != 2 {
		t.Fatalf("unexpected parsed json: %+v", parsedJSON)
	}
	if extracted := extractJSONFromText(raw); !strings.HasPrefix(extracted, "{") {
		t.Fatalf("expected bare json extraction, got %q", extracted)
	}

	if _, err := store.readFile(filepath.Join(baseDir, "entities", "missing.md")); err == nil {
		t.Fatal("expected readFile to fail for missing file")
	}
	badPath := filepath.Join(baseDir, "entities", "bad.md")
	if err := os.WriteFile(badPath, []byte("invalid"), 0644); err != nil {
		t.Fatalf("write bad markdown failed: %v", err)
	}
	if _, err := store.readFile(badPath); err == nil {
		t.Fatal("expected invalid markdown format error")
	}
}

func TestFileMemoryStoreReadFileScopeCompatibility(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	store, err := NewFileMemoryStore(baseDir)
	if err != nil {
		t.Fatalf("new file memory store failed: %v", err)
	}

	legacyPath := filepath.Join(baseDir, "entities", "legacy-project.md")
	content := `---
id: legacy-project
type: fact
session_id: project:legacy-alpha
importance: 0.9
created_at: 2026-03-16T10:30:00Z
updated_at: 2026-03-16T10:30:00Z
---

Legacy project-scoped memory
`
	if err := os.WriteFile(legacyPath, []byte(content), 0644); err != nil {
		t.Fatalf("write legacy memory failed: %v", err)
	}

	got, err := store.Get(ctx, "legacy-project")
	if err != nil {
		t.Fatalf("get legacy memory failed: %v", err)
	}
	if got.ScopeType != domain.MemoryScopeSquad || got.ScopeID != "legacy-alpha" {
		t.Fatalf("expected project:* compatibility to map to squad scope, got %+v", got)
	}
	if got.SessionID != "project:legacy-alpha" {
		t.Fatalf("expected legacy bank id to remain readable, got %q", got.SessionID)
	}
}

func TestFileMemoryStoreScopeViewsAndArchive(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	store, err := NewFileMemoryStore(baseDir)
	if err != nil {
		t.Fatalf("new file memory store failed: %v", err)
	}

	memories := []*domain.Memory{
		{
			ID:         "obs-alpha",
			Type:       domain.MemoryTypeObservation,
			Content:    "Squad alpha observation",
			Importance: 0.9,
			ScopeType:  domain.MemoryScopeSquad,
			ScopeID:    "alpha",
			CreatedAt:  time.Now(),
		},
		{
			ID:         "fact-alpha",
			Type:       domain.MemoryTypeFact,
			Content:    "Squad alpha fact",
			Importance: 0.7,
			ScopeType:  domain.MemoryScopeSquad,
			ScopeID:    "alpha",
			CreatedAt:  time.Now(),
		},
	}
	for _, memory := range memories {
		if err := store.Store(ctx, memory); err != nil {
			t.Fatalf("store memory failed: %v", err)
		}
	}

	scope := domain.MemoryScope{Type: domain.MemoryScopeSquad, ID: "alpha"}
	if err := store.BuildScopeView(ctx, scope); err != nil {
		t.Fatalf("build scope view failed: %v", err)
	}

	viewPath := store.scopeViewPath(scope)
	viewData, err := os.ReadFile(viewPath)
	if err != nil {
		t.Fatalf("read scope view failed: %v", err)
	}
	viewContent := string(viewData)
	if !strings.Contains(viewContent, "Squad alpha Process Memory") {
		t.Fatalf("unexpected scope view title: %s", viewContent)
	}
	if !strings.Contains(viewContent, "[obs-alpha]") || !strings.Contains(viewContent, "[fact-alpha]") {
		t.Fatalf("expected memories to appear in scope view: %s", viewContent)
	}

	if err := store.ArchiveScope(ctx, scope, "workflow completed"); err != nil {
		t.Fatalf("archive scope failed: %v", err)
	}

	for _, id := range []string{"obs-alpha", "fact-alpha"} {
		memory, err := store.Get(ctx, id)
		if err != nil {
			t.Fatalf("get archived memory failed: %v", err)
		}
		if !memory.Archived || memory.ArchiveReason != "workflow completed" || memory.ArchivedAt == nil {
			t.Fatalf("expected archived metadata on %s, got %+v", id, memory)
		}
	}

	scopeHits, err := store.SearchByScope(ctx, nil, []domain.MemoryScope{scope}, 10)
	if err != nil {
		t.Fatalf("search by scope after archive failed: %v", err)
	}
	if len(scopeHits) != 0 {
		t.Fatalf("expected archived memories to be excluded from active scope search, got %+v", scopeHits)
	}

	scopeIndex, err := store.ReadScopeIndex(ctx, scope)
	if err != nil {
		t.Fatalf("read scope index after archive failed: %v", err)
	}
	if len(scopeIndex.Entries) != 0 {
		t.Fatalf("expected active scope index to be empty after archive, got %+v", scopeIndex.Entries)
	}

	manifestFiles, err := filepath.Glob(filepath.Join(baseDir, "_archive", "manifests", "squad__alpha__*.md"))
	if err != nil {
		t.Fatalf("glob archive manifests failed: %v", err)
	}
	if len(manifestFiles) != 1 {
		t.Fatalf("expected one archive manifest, got %v", manifestFiles)
	}
	manifestData, err := os.ReadFile(manifestFiles[0])
	if err != nil {
		t.Fatalf("read archive manifest failed: %v", err)
	}
	if !strings.Contains(string(manifestData), "workflow completed") {
		t.Fatalf("expected archive reason in manifest, got %s", string(manifestData))
	}
}
