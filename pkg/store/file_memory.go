package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"gopkg.in/yaml.v3"
)

// FileMemoryStore implements domain.MemoryStore using Markdown files with YAML frontmatter
type FileMemoryStore struct {
	baseDir    string
	mu         sync.RWMutex
	indexDirty bool // true when index needs rebuild
	llm        domain.Generator
}

const (
	FileMemoryEntrypointName     = "MEMORY.md"
	fileMemoryEntrypointMaxLines = 120
	FileSessionMemoryDirName     = "_session"
)

type FileMemoryHeader struct {
	ID         string
	Type       domain.MemoryType
	ScopeType  domain.MemoryScopeType
	ScopeID    string
	Importance float64
	Summary    string
	UpdatedAt  time.Time
}

// WithLLM injects an LLM generator used for Reflect() consolidation.
func (s *FileMemoryStore) WithLLM(llm domain.Generator) {
	s.llm = llm
}

// MemoryIndex is the parsed representation of _index.md
type MemoryIndex struct {
	Total     int       `yaml:"total"`
	UpdatedAt time.Time `yaml:"updated_at"`
	Entries   []MemoryIndexEntry
}

// MemoryIndexEntry is one line in the index
type MemoryIndexEntry struct {
	ID         string
	Type       domain.MemoryType
	ScopeType  domain.MemoryScopeType
	ScopeID    string
	Importance float64
	Summary    string // first 60 chars of content
	IsStale    bool
	Archived   bool
}

// MemoryFrontmatter represents the YAML header in the markdown file
type MemoryFrontmatter struct {
	ID           string                 `yaml:"id"`
	Type         string                 `yaml:"type"`
	ScopeType    domain.MemoryScopeType `yaml:"scope_type,omitempty"`
	ScopeID      string                 `yaml:"scope_id,omitempty"`
	Importance   float64                `yaml:"importance"`
	SessionID    string                 `yaml:"session_id,omitempty"`
	Keywords     []string               `yaml:"keywords,omitempty"`
	Tags         []string               `yaml:"tags,omitempty"`
	AccessCount  int                    `yaml:"access_count,omitempty"`
	LastAccessed time.Time              `yaml:"last_accessed,omitempty"`
	CreatedAt    time.Time              `yaml:"created_at"`
	UpdatedAt    time.Time              `yaml:"updated_at"`
	Metadata     map[string]interface{} `yaml:"metadata,omitempty"`

	// Hindsight: temporal, evidence, and provenance fields
	EvidenceIDs     []string                `yaml:"evidence_ids,omitempty"`
	Confidence      float64                 `yaml:"confidence,omitempty"`
	ValidFrom       time.Time               `yaml:"valid_from,omitempty"`
	ValidTo         *time.Time              `yaml:"valid_to,omitempty"`
	SupersededBy    string                  `yaml:"superseded_by,omitempty"`
	SourceType      domain.MemorySourceType `yaml:"source_type,omitempty"`
	Conflicting     bool                    `yaml:"conflicting,omitempty"`
	RevisionHistory []domain.MemoryRevision `yaml:"revision_history,omitempty"`
	Archived        bool                    `yaml:"archived,omitempty"`
	ArchivedAt      *time.Time              `yaml:"archived_at,omitempty"`
	ArchiveReason   string                  `yaml:"archive_reason,omitempty"`
}

// NewFileMemoryStore creates a new markdown-based memory store
func NewFileMemoryStore(baseDir string) (*FileMemoryStore, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create memory directory: %w", err)
	}

	// Create subdirectories for OpenClaw/Mem0 style
	for _, dir := range []string{"streams", "entities"} {
		if err := os.MkdirAll(filepath.Join(baseDir, dir), 0755); err != nil {
			return nil, err
		}
	}

	return &FileMemoryStore{baseDir: baseDir}, nil
}

// Store saves a memory as a markdown file
func (s *FileMemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalizeMemoryForStore(memory)

	// Determine category (stream or entity)
	category := "entities"
	if memory.Type == domain.MemoryTypeContext {
		category = "streams"
	}

	// Use ID as filename, ensure it's safe
	fileName := fmt.Sprintf("%s.md", memory.ID)
	path := filepath.Join(s.baseDir, category, fileName)

	fm := MemoryFrontmatter{
		ID:              memory.ID,
		Type:            string(memory.Type),
		ScopeType:       memory.ScopeType,
		ScopeID:         memory.ScopeID,
		Importance:      memory.Importance,
		SessionID:       memory.SessionID,
		Keywords:        append([]string(nil), memory.Keywords...),
		Tags:            append([]string(nil), memory.Tags...),
		AccessCount:     memory.AccessCount,
		LastAccessed:    memory.LastAccessed,
		CreatedAt:       memory.CreatedAt,
		UpdatedAt:       time.Now(),
		Metadata:        memory.Metadata,
		EvidenceIDs:     memory.EvidenceIDs,
		Confidence:      memory.Confidence,
		ValidFrom:       memory.ValidFrom,
		ValidTo:         memory.ValidTo,
		SupersededBy:    memory.SupersededBy,
		SourceType:      memory.SourceType,
		Conflicting:     memory.Conflicting,
		RevisionHistory: memory.RevisionHistory,
		Archived:        memory.Archived,
		ArchivedAt:      memory.ArchivedAt,
		ArchiveReason:   memory.ArchiveReason,
	}

	// Keep tags and keywords compatible with older metadata-only callers.
	if len(fm.Tags) == 0 && memory.Metadata != nil {
		fm.Tags = stringSliceFromAny(memory.Metadata["tags"])
	}
	if len(fm.Keywords) == 0 && memory.Metadata != nil {
		fm.Keywords = stringSliceFromAny(memory.Metadata["keywords"])
	}

	frontmatter, err := yaml.Marshal(fm)
	if err != nil {
		return err
	}

	// Double check content isn't empty
	content := fmt.Sprintf("---\n%s---\n\n%s", string(frontmatter), memory.Content)

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return err
	}

	// Incremental index update: upsert this single entry into type and scope index files.
	// This avoids a full rebuild on every Store() call.
	if !memory.Archived {
		entry := MemoryIndexEntry{
			ID:         memory.ID,
			Type:       memory.Type,
			ScopeType:  memory.ScopeType,
			ScopeID:    memory.ScopeID,
			Importance: memory.Importance,
			Summary:    truncate(memory.Content, 60),
			IsStale:    memory.ValidTo != nil || memory.SupersededBy != "",
		}
		s.upsertIndexEntry(entry)
	}

	_ = s.rebuildEntrypointLocked()
	s.indexDirty = false
	return nil
}

// Search performs a simplified keyword search (since we're embedding-free)
func (s *FileMemoryStore) Search(ctx context.Context, vector []float64, topK int, minScore float64) ([]*domain.MemoryWithScore, error) {
	// In the file-based store, we prioritize "Intent-based reading".
	// We'll return the most important/recent memories.
	all, _, err := s.List(ctx, 100, 0)
	if err != nil {
		return nil, err
	}

	var results []*domain.MemoryWithScore
	for _, m := range all {
		if m.Archived {
			continue
		}
		results = append(results, &domain.MemoryWithScore{
			Memory: m,
			Score:  1.0,
		})
	}

	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

func (s *FileMemoryStore) SearchBySession(ctx context.Context, sessionID string, vector []float64, topK int) ([]*domain.MemoryWithScore, error) {
	all, _, err := s.List(ctx, 1000, 0)
	if err != nil {
		return nil, err
	}

	var results []*domain.MemoryWithScore
	for _, m := range all {
		scope := inferMemoryScope(m.ScopeType, m.ScopeID, m.SessionID)
		if m.Archived {
			continue
		}
		if (scope.Type == domain.MemoryScopeSession && scope.ID == sessionID) || m.SessionID == sessionID {
			results = append(results, &domain.MemoryWithScore{
				Memory: m,
				Score:  1.0,
			})
		}
	}
	return results, nil
}

// SearchByScope searches memories within specific scopes
func (s *FileMemoryStore) SearchByScope(ctx context.Context, vector []float64, scopes []domain.MemoryScope, topK int) ([]*domain.MemoryWithScore, error) {
	all, _, err := s.List(ctx, 1000, 0)
	if err != nil {
		return nil, err
	}

	// Build a map of scope bank IDs for quick lookup
	scopeMap := make(map[string]domain.MemoryScope)
	for _, scope := range scopes {
		scopeMap[scopeToBankIDFile(scope)] = normalizeScope(scope)
	}

	var results []*domain.MemoryWithScore
	for _, m := range all {
		if m.Archived {
			continue
		}

		memScope := inferMemoryScope(m.ScopeType, m.ScopeID, m.SessionID)
		for _, searchScope := range scopeMap {
			if sameScope(memScope, searchScope) {
				results = append(results, &domain.MemoryWithScore{
					Memory: m,
					Score:  1.0,
				})
				break
			}
		}
	}

	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

// StoreWithScope stores a memory with a specific scope
func (s *FileMemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	scope = normalizeScope(scope)
	memory.ScopeType = scope.Type
	memory.ScopeID = scope.ID
	memory.SessionID = compatibilitySessionIDForScope(scope)
	return s.Store(ctx, memory)
}

// SearchByText performs full-text search on file-based memories
func (s *FileMemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	all, _, err := s.List(ctx, 1000, 0)
	if err != nil {
		return nil, err
	}

	tokens := tokenizeText(query)
	if len(tokens) == 0 {
		return nil, nil
	}

	now := time.Now()
	var results []*domain.MemoryWithScore
	for _, m := range all {
		if m.Archived {
			continue
		}
		textScore := ngramMatchScore(tokens, m.Content)
		if textScore == 0 {
			continue
		}
		score := applyMemoryBoosts(textScore, m.Importance, m.CreatedAt, now)
		results = append(results, &domain.MemoryWithScore{Memory: m, Score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

// tokenizeText splits text into tokens supporting both space-separated words and
// CJK character n-grams (bigrams) for Chinese/Japanese/Korean text without a dictionary.
func tokenizeText(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	seen := make(map[string]struct{})
	var tokens []string

	add := func(t string) {
		if t == "" {
			return
		}
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			tokens = append(tokens, t)
		}
	}

	runes := []rune(text)
	var asciiBuf strings.Builder
	for i, r := range runes {
		if isCJK(r) {
			// Flush any pending ASCII word
			if w := strings.TrimSpace(asciiBuf.String()); w != "" {
				for _, f := range strings.Fields(w) {
					add(f)
				}
				asciiBuf.Reset()
			}
			// Add single CJK character
			add(string(r))
			// Add CJK bigram
			if i+1 < len(runes) && isCJK(runes[i+1]) {
				add(string(runes[i : i+2]))
			}
		} else {
			asciiBuf.WriteRune(r)
		}
	}
	if w := strings.TrimSpace(asciiBuf.String()); w != "" {
		for _, f := range strings.Fields(w) {
			add(f)
		}
	}
	return tokens
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x20000 && r <= 0x2A6DF) // CJK Extension B
}

// ngramMatchScore returns a [0,1] score based on how many query tokens appear in content.
func ngramMatchScore(queryTokens []string, content string) float64 {
	contentLower := strings.ToLower(content)
	matched := 0
	for _, tok := range queryTokens {
		if strings.Contains(contentLower, tok) {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	score := float64(matched) / float64(len(queryTokens))
	// Boost for full query phrase match
	if strings.Contains(contentLower, strings.Join(queryTokens, "")) {
		score = min(score*1.5, 1.0)
	}
	return score
}

// applyMemoryBoosts multiplies textScore by importance and time-decay factors.
// importance: user-assigned weight [0,1], defaults to 0.5
// decay: memories older than ~100 days lose ~63% weight (λ=0.01/day)
func applyMemoryBoosts(textScore, importance float64, createdAt, now time.Time) float64 {
	if importance <= 0 {
		importance = 0.5
	}
	importanceBoost := 0.5 + importance*0.5 // maps [0,1] → [0.5, 1.0]

	days := now.Sub(createdAt).Hours() / 24
	decay := math.Exp(-0.007 * days) // half-life ≈ 99 days

	return textScore * importanceBoost * decay
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// scopeToBankIDFile converts MemoryScope to bank ID for file store
func scopeToBankIDFile(scope domain.MemoryScope) string {
	scope = normalizeScope(scope)
	if scope.Type == domain.MemoryScopeGlobal {
		return "global"
	}
	if scope.Type == domain.MemoryScopeSession {
		if scope.ID == "" {
			return "global"
		}
		return scope.ID
	}
	if scope.ID == "" {
		return string(scope.Type)
	}
	return fmt.Sprintf("%s:%s", scope.Type, scope.ID)
}

func normalizeScope(scope domain.MemoryScope) domain.MemoryScope {
	scope.Type = normalizeScopeType(scope.Type)
	if scope.Type == "" {
		scope.Type = domain.MemoryScopeGlobal
	}
	return scope
}

func normalizeScopeType(scopeType domain.MemoryScopeType) domain.MemoryScopeType {
	switch scopeType {
	case "":
		return domain.MemoryScopeGlobal
	case domain.MemoryScopeProject:
		return domain.MemoryScopeTeam
	default:
		return scopeType
	}
}

func inferMemoryScope(scopeType domain.MemoryScopeType, scopeID, sessionID string) domain.MemoryScope {
	if normalized := normalizeScopeType(scopeType); normalized != domain.MemoryScopeGlobal || scopeID != "" {
		return normalizeScope(domain.MemoryScope{Type: normalized, ID: scopeID})
	}

	switch {
	case sessionID == "", sessionID == "global", sessionID == "default":
		return domain.MemoryScope{Type: domain.MemoryScopeGlobal}
	case strings.HasPrefix(sessionID, "session:"):
		return domain.MemoryScope{Type: domain.MemoryScopeSession, ID: strings.TrimPrefix(sessionID, "session:")}
	case strings.HasPrefix(sessionID, "agent:"):
		return domain.MemoryScope{Type: domain.MemoryScopeAgent, ID: strings.TrimPrefix(sessionID, "agent:")}
	case strings.HasPrefix(sessionID, "team:"):
		return domain.MemoryScope{Type: domain.MemoryScopeTeam, ID: strings.TrimPrefix(sessionID, "team:")}
	case strings.HasPrefix(sessionID, "project:"):
		return domain.MemoryScope{Type: domain.MemoryScopeTeam, ID: strings.TrimPrefix(sessionID, "project:")}
	case strings.HasPrefix(sessionID, "user:"):
		return domain.MemoryScope{Type: domain.MemoryScopeUser, ID: strings.TrimPrefix(sessionID, "user:")}
	default:
		// Legacy file memories often store plain session IDs without a "session:" prefix.
		return domain.MemoryScope{Type: domain.MemoryScopeSession, ID: sessionID}
	}
}

func normalizeMemoryForStore(memory *domain.Memory) {
	scope := inferMemoryScope(memory.ScopeType, memory.ScopeID, memory.SessionID)
	memory.ScopeType = scope.Type
	memory.ScopeID = scope.ID
	if memory.SessionID == "" {
		memory.SessionID = compatibilitySessionIDForScope(scope)
	}
	if len(memory.Tags) == 0 && memory.Metadata != nil {
		memory.Tags = stringSliceFromAny(memory.Metadata["tags"])
	}
	if len(memory.Keywords) == 0 && memory.Metadata != nil {
		memory.Keywords = stringSliceFromAny(memory.Metadata["keywords"])
	}
}

func compatibilitySessionIDForScope(scope domain.MemoryScope) string {
	scope = normalizeScope(scope)
	switch scope.Type {
	case domain.MemoryScopeGlobal:
		return "global"
	case domain.MemoryScopeSession:
		return scope.ID
	default:
		return scopeToBankIDFile(scope)
	}
}

func sameScope(a, b domain.MemoryScope) bool {
	a = normalizeScope(a)
	b = normalizeScope(b)
	return a.Type == b.Type && a.ID == b.ID
}

func stringSliceFromAny(value interface{}) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), v...)
	case domain.FlexibleStringArray:
		return append([]string(nil), v.Strings()...)
	case []interface{}:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				result = append(result, s)
			}
		}
		return result
	default:
		return nil
	}
}

func (s *FileMemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	for _, cat := range []string{"streams", "entities"} {
		path := filepath.Join(s.baseDir, cat, id+".md")
		if _, err := os.Stat(path); err == nil {
			return s.readFile(path)
		}
	}
	return nil, fmt.Errorf("memory %s not found", id)
}

func (s *FileMemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	return s.Store(ctx, memory)
}

func (s *FileMemoryStore) IncrementAccess(ctx context.Context, id string) error {
	m, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	m.AccessCount++
	m.LastAccessed = time.Now()
	return s.Store(ctx, m)
}

func (s *FileMemoryStore) GetByType(ctx context.Context, memoryType domain.MemoryType, limit int) ([]*domain.Memory, error) {
	all, _, _ := s.List(ctx, 1000, 0)
	var filtered []*domain.Memory
	for _, m := range all {
		if m.Type == memoryType {
			filtered = append(filtered, m)
		}
		if len(filtered) >= limit {
			break
		}
	}
	return filtered, nil
}

func (s *FileMemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	var all []*domain.Memory

	for _, cat := range []string{"streams", "entities"} {
		files, _ := filepath.Glob(filepath.Join(s.baseDir, cat, "*.md"))
		for _, f := range files {
			m, err := s.readFile(f)
			if err == nil {
				all = append(all, m)
			}
		}
	}

	total := len(all)
	if offset >= total {
		return nil, total, nil
	}

	end := offset + limit
	if end > total {
		end = total
	}

	return all[offset:end], total, nil
}

func (s *FileMemoryStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cat := range []string{"streams", "entities"} {
		_ = os.Remove(filepath.Join(s.baseDir, cat, id+".md"))
	}
	_ = s.rebuildEntrypointLocked()
	s.indexDirty = true
	return nil
}

func (s *FileMemoryStore) Clear(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, dir := range []string{"streams", "entities", "_index", "_views"} {
		if err := os.RemoveAll(filepath.Join(s.baseDir, dir)); err != nil {
			return err
		}
	}
	for _, dir := range []string{"streams", "entities"} {
		if err := os.MkdirAll(filepath.Join(s.baseDir, dir), 0755); err != nil {
			return err
		}
	}
	if err := os.Remove(s.entrypointPath()); err != nil && !os.IsNotExist(err) {
		return err
	}

	s.indexDirty = true
	return nil
}

func (s *FileMemoryStore) DeleteBySession(ctx context.Context, sessionID string) error {
	all, _, _ := s.List(ctx, 1000, 0)
	for _, m := range all {
		scope := inferMemoryScope(m.ScopeType, m.ScopeID, m.SessionID)
		if m.SessionID == sessionID || (scope.Type == domain.MemoryScopeSession && scope.ID == sessionID) {
			_ = s.Delete(ctx, m.ID)
		}
	}
	return nil
}

func (s *FileMemoryStore) InitSchema(ctx context.Context) error {
	return nil
}

func (s *FileMemoryStore) ConfigureBank(ctx context.Context, sessionID string, config *domain.MemoryBankConfig) error {
	return nil
}

func (s *FileMemoryStore) Reflect(ctx context.Context, sessionID string) (string, error) {
	if s.llm == nil {
		return "LLM not configured; skipping reflection.", nil
	}

	// 1. Collect active (non-stale) facts and existing observations for this session
	all, _, err := s.List(ctx, 1000, 0)
	if err != nil {
		return "", err
	}

	var facts []*domain.Memory
	var existingObs []*domain.Memory
	for _, m := range all {
		if sessionID != "" && m.SessionID != sessionID {
			continue
		}
		if IsStale(m) {
			continue
		}
		switch m.Type {
		case domain.MemoryTypeFact:
			facts = append(facts, m)
		case domain.MemoryTypeObservation:
			existingObs = append(existingObs, m)
		}
	}

	if len(facts) < 3 {
		return "Not enough facts to consolidate (need at least 3).", nil
	}

	// 2. Build sets of already-used evidence IDs to avoid double-counting
	usedIDs := make(map[string]bool)
	for _, obs := range existingObs {
		for _, id := range obs.EvidenceIDs {
			usedIDs[id] = true
		}
	}

	// 3. Collect only facts not yet captured in an observation
	var newFacts []*domain.Memory
	for _, f := range facts {
		if !usedIDs[f.ID] {
			newFacts = append(newFacts, f)
		}
	}
	if len(newFacts) < 2 {
		return "All facts are already covered by existing observations.", nil
	}

	// 4. Build prompt with existing observations for recursive merging + conflict detection
	var factLines strings.Builder
	for _, f := range newFacts {
		factLines.WriteString(fmt.Sprintf("- [%s] %s\n", f.ID, f.Content))
	}

	var obsLines strings.Builder
	if len(existingObs) > 0 {
		obsLines.WriteString("\nExisting observations (do not duplicate; update or merge if a new fact fits):\n")
		for _, o := range existingObs {
			obsLines.WriteString(fmt.Sprintf("- [%s] %s\n", o.ID, o.Content))
		}
	}

	promptText := fmt.Sprintf(`You are a memory consolidation engine with strict anti-hallucination rules. /nothink

New facts (not yet covered by any observation):
%s
%s
Rules:
1. ONLY use information explicitly present in the facts above. Do NOT invent or infer beyond what is stated.
2. An observation must cite at least 2 fact IDs as evidence.
3. If two facts CONTRADICT each other, set "conflicting": true and include both in evidence_ids.
4. If a new fact extends an existing observation, output an "update_obs_id" field with the existing observation's ID to supersede it.
5. Do not duplicate existing observations unless you are merging/updating them.
6. Confidence: 0.9+ only if facts are highly consistent; lower if partial or ambiguous.

Output valid JSON only:
{
  "observations": [
    {
      "content": "Single sentence synthesizing the facts.",
      "confidence": 0.85,
      "evidence_ids": ["id1", "id2"],
      "conflicting": false,
      "update_obs_id": ""
    }
  ]
}`, factLines.String(), obsLines.String())

	// Use plain Generate — prompt already contains full JSON format.
	// /nothink suppresses chain-of-thought on reasoning models (MiniMax-M2.5, DeepSeek-R1).
	// 3-minute timeout: fallback to Ollama is skipped if context expires.
	reflectCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	raw, err := s.llm.Generate(reflectCtx, promptText, &domain.GenerationOptions{Temperature: 0.2, MaxTokens: 2048})
	if err != nil {
		return "", fmt.Errorf("LLM reflection failed: %w", err)
	}

	// Strip markdown fences and extract JSON
	result := &domain.StructuredResult{Raw: extractJSONFromText(raw)}

	// 5. Parse and store observations
	type obsItem struct {
		Content     string   `json:"content"`
		Confidence  float64  `json:"confidence"`
		EvidenceIDs []string `json:"evidence_ids"`
		Conflicting bool     `json:"conflicting"`
		UpdateObsID string   `json:"update_obs_id"`
	}
	type reflectResult struct {
		Observations []obsItem `json:"observations"`
	}
	var parsed reflectResult
	if err := parseJSON(result.Raw, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse reflection result: %w", err)
	}

	created, updated := 0, 0
	for _, obs := range parsed.Observations {
		if obs.Content == "" || len(obs.EvidenceIDs) < 2 {
			continue
		}

		newID := newUUID()
		obsMemory := &domain.Memory{
			ID:          newID,
			SessionID:   sessionID,
			Type:        domain.MemoryTypeObservation,
			Content:     obs.Content,
			Importance:  obs.Confidence,
			Confidence:  obs.Confidence,
			EvidenceIDs: obs.EvidenceIDs,
			Conflicting: obs.Conflicting,
			SourceType:  domain.MemorySourceConsolidated,
			ValidFrom:   time.Now(),
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		if err := s.Store(ctx, obsMemory); err != nil {
			continue
		}

		// If this supersedes an existing observation, mark it stale
		if obs.UpdateObsID != "" {
			_ = s.MarkStale(ctx, obs.UpdateObsID, newID)
			updated++
		} else {
			created++
		}
	}

	return fmt.Sprintf("Reflection complete: %d new observations, %d updated from %d facts.", created, updated, len(newFacts)), nil
}

func (s *FileMemoryStore) AddMentalModel(ctx context.Context, model *domain.MentalModel) error {
	m := &domain.Memory{
		ID:         model.ID,
		Type:       domain.MemoryTypePattern,
		Content:    fmt.Sprintf("Mental Model: %s\n%s", model.Name, model.Content),
		Importance: 1.0,
		CreatedAt:  time.Now(),
	}
	return s.Store(ctx, m)
}

func (s *FileMemoryStore) readFile(path string) (*domain.Memory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	contentStr := string(data)
	if !strings.HasPrefix(contentStr, "---") {
		return nil, fmt.Errorf("invalid markdown format in %s", path)
	}

	parts := strings.SplitN(contentStr, "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("failed to split frontmatter from content in %s", path)
	}

	var fm MemoryFrontmatter
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		return nil, err
	}

	// Migration compatibility: fill defaults for fields added in the cognitive layer.
	// Old YAML files won't have these fields; zero values are ambiguous, so we back-fill.
	if fm.SourceType == "" {
		fm.SourceType = domain.MemorySourceUserInput // pre-existing memories are treated as user-stated
	}
	if fm.Confidence == 0 && fm.Type != "observation" {
		fm.Confidence = 1.0 // facts/preferences with no recorded confidence are assumed authoritative
	}

	memory := &domain.Memory{
		ID:              fm.ID,
		SessionID:       fm.SessionID,
		ScopeType:       fm.ScopeType,
		ScopeID:         fm.ScopeID,
		Type:            domain.MemoryType(fm.Type),
		Content:         strings.TrimSpace(parts[2]),
		Keywords:        append([]string(nil), fm.Keywords...),
		Tags:            append([]string(nil), fm.Tags...),
		Importance:      fm.Importance,
		AccessCount:     fm.AccessCount,
		LastAccessed:    fm.LastAccessed,
		Metadata:        fm.Metadata,
		CreatedAt:       fm.CreatedAt,
		UpdatedAt:       fm.UpdatedAt,
		EvidenceIDs:     fm.EvidenceIDs,
		Confidence:      fm.Confidence,
		ValidFrom:       fm.ValidFrom,
		ValidTo:         fm.ValidTo,
		SupersededBy:    fm.SupersededBy,
		SourceType:      fm.SourceType,
		Conflicting:     fm.Conflicting,
		RevisionHistory: fm.RevisionHistory,
		Archived:        fm.Archived,
		ArchivedAt:      fm.ArchivedAt,
		ArchiveReason:   fm.ArchiveReason,
	}
	normalizeMemoryForStore(memory)
	return memory, nil
}

// MarkStale marks a memory as stale (superseded by a newer memory).
// Sets ValidTo to now, records the superseding ID, and appends a revision entry.
func (s *FileMemoryStore) MarkStale(ctx context.Context, id string, supersededByID string) error {
	m, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	now := time.Now()
	m.ValidTo = &now
	m.SupersededBy = supersededByID
	m.UpdatedAt = now
	m.RevisionHistory = append(m.RevisionHistory, domain.MemoryRevision{
		At:      now,
		By:      "reflect",
		Summary: fmt.Sprintf("superseded by %s", supersededByID),
	})
	return s.Store(ctx, m)
}

// upsertIndexEntry adds or updates a single entry in both the type index and scope index files.
// Caller must hold s.mu. Creates the index directories if they don't exist.
func (s *FileMemoryStore) upsertIndexEntry(entry MemoryIndexEntry) {
	_ = os.MkdirAll(s.typeIndexDir(), 0755)
	_ = os.MkdirAll(s.scopeIndexDir(), 0755)

	// Update type index
	typePath := s.indexFilePath(entry.Type)
	upsertEntryInFile(typePath, entry, func(e MemoryIndexEntry) string {
		staleTag := ""
		if e.IsStale {
			staleTag = " ~~[stale]~~"
		}
		return fmt.Sprintf("- [%s] %.2f | %s%s\n", e.ID, e.Importance, e.Summary, staleTag)
	})

	// Update scope index
	scope := normalizeScope(domain.MemoryScope{Type: entry.ScopeType, ID: entry.ScopeID})
	scopePath := s.scopeIndexFilePath(scope)
	upsertEntryInFile(scopePath, entry, func(e MemoryIndexEntry) string {
		staleTag := ""
		if e.IsStale {
			staleTag = " ~~[stale]~~"
		}
		return fmt.Sprintf("- [%s][%s] %.2f | %s%s\n", e.ID, e.Type, e.Importance, e.Summary, staleTag)
	})
}

// upsertEntryInFile reads an index file, replaces or appends the entry line, and writes it back.
func upsertEntryInFile(path string, entry MemoryIndexEntry, formatLine func(MemoryIndexEntry) string) {
	newLine := formatLine(entry)
	prefix := fmt.Sprintf("- [%s]", entry.ID)

	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist yet — create with minimal header
		content := fmt.Sprintf("---\ntotal: 1\nupdated_at: %s\n---\n\n%s",
			time.Now().Format(time.RFC3339), newLine)
		_ = os.WriteFile(path, []byte(content), 0644)
		return
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			lines[i] = strings.TrimSuffix(newLine, "\n")
			found = true
			break
		}
	}
	if !found {
		// Append before the last empty line (or at end)
		lines = append(lines, strings.TrimSuffix(newLine, "\n"))
	}

	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

// IsStale returns true if the memory has been superseded.
func IsStale(m *domain.Memory) bool {
	return m.ValidTo != nil || m.SupersededBy != ""
}

// indexDir returns the path to the _index/ directory
func (s *FileMemoryStore) indexDir() string {
	return filepath.Join(s.baseDir, "_index")
}

func (s *FileMemoryStore) typeIndexDir() string {
	return filepath.Join(s.indexDir(), "types")
}

func (s *FileMemoryStore) scopeIndexDir() string {
	return filepath.Join(s.indexDir(), "scopes")
}

func (s *FileMemoryStore) viewsDir() string {
	return filepath.Join(s.baseDir, "_views")
}

func (s *FileMemoryStore) archiveManifestDir() string {
	return filepath.Join(s.baseDir, "_archive", "manifests")
}

func (s *FileMemoryStore) entrypointPath() string {
	return filepath.Join(s.baseDir, FileMemoryEntrypointName)
}

func (s *FileMemoryStore) sessionMemoryPath(sessionID string) string {
	return filepath.Join(s.baseDir, FileSessionMemoryDirName, sanitizeScopeID(strings.TrimSpace(sessionID))+".md")
}

func (s *FileMemoryStore) ReadEntrypoint() (string, error) {
	data, err := os.ReadFile(s.entrypointPath())
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > fileMemoryEntrypointMaxLines {
		lines = append(lines[:fileMemoryEntrypointMaxLines], "... (truncated)")
	}
	return strings.Join(lines, "\n"), nil
}

func (s *FileMemoryStore) ReadSessionMemory(sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", nil
	}
	data, err := os.ReadFile(s.sessionMemoryPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *FileMemoryStore) WriteSessionMemory(sessionID, content string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	path := s.sessionMemoryPath(sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.TrimSpace(content)), 0644)
}

func (s *FileMemoryStore) ListHeaders(ctx context.Context, limit int) ([]FileMemoryHeader, error) {
	all, _, err := s.List(ctx, 1000, 0)
	if err != nil {
		return nil, err
	}
	headers := make([]FileMemoryHeader, 0, len(all))
	for _, m := range all {
		if m.Archived {
			continue
		}
		headers = append(headers, FileMemoryHeader{
			ID:         m.ID,
			Type:       m.Type,
			ScopeType:  m.ScopeType,
			ScopeID:    m.ScopeID,
			Importance: m.Importance,
			Summary:    truncate(m.Content, 140),
			UpdatedAt:  m.UpdatedAt,
		})
	}
	sort.Slice(headers, func(i, j int) bool {
		if headers[i].Importance != headers[j].Importance {
			return headers[i].Importance > headers[j].Importance
		}
		return headers[i].UpdatedAt.After(headers[j].UpdatedAt)
	})
	if limit > 0 && len(headers) > limit {
		headers = headers[:limit]
	}
	return headers, nil
}

func (s *FileMemoryStore) SelectRelevantHeaders(ctx context.Context, query string, topK int) ([]FileMemoryHeader, error) {
	headers, err := s.ListHeaders(ctx, 0)
	if err != nil {
		return nil, err
	}
	if len(headers) == 0 {
		return nil, nil
	}
	tokens := tokenizeText(query)
	if len(tokens) == 0 {
		if topK > 0 && len(headers) > topK {
			return headers[:topK], nil
		}
		return headers, nil
	}
	type scored struct {
		FileMemoryHeader
		score float64
	}
	scoredHeaders := make([]scored, 0, len(headers))
	now := time.Now()
	for _, header := range headers {
		score := ngramMatchScore(tokens, header.Summary)
		if score == 0 {
			continue
		}
		score = applyMemoryBoosts(score, header.Importance, header.UpdatedAt, now)
		scoredHeaders = append(scoredHeaders, scored{FileMemoryHeader: header, score: score})
	}
	sort.Slice(scoredHeaders, func(i, j int) bool {
		return scoredHeaders[i].score > scoredHeaders[j].score
	})
	if topK > 0 && len(scoredHeaders) > topK {
		scoredHeaders = scoredHeaders[:topK]
	}
	out := make([]FileMemoryHeader, 0, len(scoredHeaders))
	for _, item := range scoredHeaders {
		out = append(out, item.FileMemoryHeader)
	}
	return out, nil
}

// SelectRelevantHeadersWithLLM uses an LLM to select the most relevant memories based on their summaries.
// This is an advanced, high-precision retrieval mechanism that avoids vector embeddings.
func (s *FileMemoryStore) SelectRelevantHeadersWithLLM(ctx context.Context, llm domain.Generator, query string, topK int) ([]FileMemoryHeader, error) {
	headers, err := s.ListHeaders(ctx, 0)
	if err != nil {
		return nil, err
	}
	if len(headers) == 0 {
		return nil, nil
	}
	if llm == nil {
		return s.SelectRelevantHeaders(ctx, query, topK)
	}

	var manifest strings.Builder
	validFilenames := make(map[string]FileMemoryHeader)
	for i, h := range headers {
		filename := fmt.Sprintf("%s.md", h.ID)
		validFilenames[filename] = h
		
		scopeInfo := string(h.ScopeType)
		if h.ScopeID != "" {
			scopeInfo += ":" + h.ScopeID
		}
		
		manifest.WriteString(fmt.Sprintf("- %s [%s]: \"%s\"\n", filename, scopeInfo, h.Summary))
		if i > 50 { // Cap manifest size
			break
		}
	}

	systemPrompt := `You are selecting memories that will be useful to the AI as it processes a user's query. You will be given the user's query and a list of available memory files with their filenames, scopes, and descriptions.

Return a list of filenames for the memories that will clearly be useful to the AI as it processes the user's query. Only include memories that you are certain will be helpful based on their name and description.
- If you are unsure if a memory will be useful in processing the user's query, then do not include it in your list. Be selective and discerning.
- If there are no memories in the list that would clearly be useful, feel free to return an empty list.`

	prompt := fmt.Sprintf(`Query: %s

Available memories:
%s`, query, manifest.String())

	opts := &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   500,
	}

	var parsed struct {
		SelectedMemories []string `json:"selected_memories"`
	}

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"selected_memories": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
		"required":             []string{"selected_memories"},
		"additionalProperties": false,
	}

	promptWithSystem := fmt.Sprintf("%s\n\n%s", systemPrompt, prompt)
	resp, err := llm.GenerateStructured(ctx, promptWithSystem, schema, opts)
	if err != nil {
		// Fallback to n-gram keyword match
		return s.SelectRelevantHeaders(ctx, query, topK)
	}

	if err := json.Unmarshal([]byte(resp.Raw), &parsed); err != nil {
		return s.SelectRelevantHeaders(ctx, query, topK)
	}

	var selected []FileMemoryHeader
	for _, filename := range parsed.SelectedMemories {
		if header, ok := validFilenames[filename]; ok {
			selected = append(selected, header)
			if len(selected) >= topK {
				break
			}
		}
	}
	return selected, nil
}

// indexFilePath returns the per-type index file path, e.g. _index/types/observations.md.
func (s *FileMemoryStore) indexFilePath(t domain.MemoryType) string {
	return filepath.Join(s.typeIndexDir(), string(t)+"s.md")
}

func (s *FileMemoryStore) scopeIndexFilePath(scope domain.MemoryScope) string {
	scope = normalizeScope(scope)
	name := string(scope.Type)
	if scope.ID != "" {
		name = fmt.Sprintf("%s__%s", name, sanitizeScopeID(scope.ID))
	}
	return filepath.Join(s.scopeIndexDir(), name+".md")
}

func (s *FileMemoryStore) scopeViewPath(scope domain.MemoryScope) string {
	scope = normalizeScope(scope)

	switch scope.Type {
	case domain.MemoryScopeGlobal:
		return filepath.Join(s.viewsDir(), "global", "global_memory.md")
	case domain.MemoryScopeTeam:
		return filepath.Join(s.viewsDir(), "teams", sanitizeScopeID(scope.ID), "process_memory.md")
	case domain.MemoryScopeAgent:
		return filepath.Join(s.viewsDir(), "agents", sanitizeScopeID(scope.ID), "thread_memory.md")
	case domain.MemoryScopeSession:
		return filepath.Join(s.viewsDir(), "sessions", sanitizeScopeID(scope.ID), "session_memory.md")
	case domain.MemoryScopeUser:
		return filepath.Join(s.viewsDir(), "users", sanitizeScopeID(scope.ID), "user_memory.md")
	default:
		return filepath.Join(s.viewsDir(), "misc", sanitizeScopeID(scope.ID), "memory.md")
	}
}

func (s *FileMemoryStore) archiveManifestPath(scope domain.MemoryScope, at time.Time) string {
	scope = normalizeScope(scope)
	name := string(scope.Type)
	if scope.ID != "" {
		name = fmt.Sprintf("%s__%s", name, sanitizeScopeID(scope.ID))
	}
	return filepath.Join(s.archiveManifestDir(), fmt.Sprintf("%s__%s.md", name, at.UTC().Format("20060102T150405Z")))
}

// RebuildIndex forces a full rebuild of all per-type index files.
// Useful after manual edits, migrations, or corruption recovery.
func (s *FileMemoryStore) RebuildIndex(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rebuildIndex(ctx); err != nil {
		return err
	}
	s.indexDirty = false
	return nil
}

// ReadIndex returns the merged memory index across all type files.
// Rebuilds if dirty or missing.
func (s *FileMemoryStore) ReadIndex(ctx context.Context) (*MemoryIndex, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.indexDirty {
		if err := s.rebuildIndex(ctx); err != nil {
			return nil, err
		}
		s.indexDirty = false
	}

	return s.readIndexFiles()
}

// ReadScopeIndex returns the merged index entries for one scope.
func (s *FileMemoryStore) ReadScopeIndex(ctx context.Context, scope domain.MemoryScope) (*MemoryIndex, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.indexDirty {
		if err := s.rebuildIndex(ctx); err != nil {
			return nil, err
		}
		s.indexDirty = false
	}

	data, err := os.ReadFile(s.scopeIndexFilePath(scope))
	if err != nil {
		return &MemoryIndex{}, nil
	}
	idx, err := parseMemoryIndex(data, "")
	if err != nil {
		return nil, err
	}
	scope = normalizeScope(scope)
	for i := range idx.Entries {
		idx.Entries[i].ScopeType = scope.Type
		idx.Entries[i].ScopeID = scope.ID
	}
	return idx, nil
}

// BuildScopeView materializes a human-readable summary file for one scope.
func (s *FileMemoryStore) BuildScopeView(ctx context.Context, scope domain.MemoryScope) error {
	scope = normalizeScope(scope)

	all, _, err := s.List(ctx, 1000, 0)
	if err != nil {
		return err
	}

	var memories []*domain.Memory
	for _, m := range all {
		if m.Archived {
			continue
		}
		if sameScope(inferMemoryScope(m.ScopeType, m.ScopeID, m.SessionID), scope) {
			memories = append(memories, m)
		}
	}

	sort.Slice(memories, func(i, j int) bool {
		iOrder := memoryTypeRank(memories[i].Type)
		jOrder := memoryTypeRank(memories[j].Type)
		if iOrder != jOrder {
			return iOrder < jOrder
		}
		if memories[i].Importance != memories[j].Importance {
			return memories[i].Importance > memories[j].Importance
		}
		return memories[i].CreatedAt.After(memories[j].CreatedAt)
	})

	type viewFM struct {
		ScopeType   domain.MemoryScopeType `yaml:"scope_type"`
		ScopeID     string                 `yaml:"scope_id,omitempty"`
		GeneratedAt time.Time              `yaml:"generated_at"`
		ActiveCount int                    `yaml:"active_count"`
	}
	fm, _ := yaml.Marshal(viewFM{
		ScopeType:   scope.Type,
		ScopeID:     scope.ID,
		GeneratedAt: time.Now().UTC(),
		ActiveCount: len(memories),
	})

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(string(fm))
	sb.WriteString("---\n\n")
	sb.WriteString("# ")
	sb.WriteString(scopeViewTitle(scope))
	sb.WriteString("\n\n")

	currentType := domain.MemoryType("")
	for _, memory := range memories {
		if memory.Type != currentType {
			currentType = memory.Type
			sb.WriteString("## ")
			sb.WriteString(scopeViewSectionTitle(currentType))
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("- [%s] %s\n", memory.ID, truncate(memory.Content, 160)))
	}
	if len(memories) == 0 {
		sb.WriteString("_No active memories in this scope._\n")
	}

	path := s.scopeViewPath(scope)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(sb.String()), 0644)
}

// ArchiveScope marks all memories in one scope as archived and writes an archive manifest.
func (s *FileMemoryStore) ArchiveScope(ctx context.Context, scope domain.MemoryScope, reason string) error {
	scope = normalizeScope(scope)

	all, _, err := s.List(ctx, 1000, 0)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	var archivedIDs []string
	for _, memory := range all {
		if memory.Archived {
			continue
		}
		if !sameScope(inferMemoryScope(memory.ScopeType, memory.ScopeID, memory.SessionID), scope) {
			continue
		}

		memory.Archived = true
		memory.ArchivedAt = &now
		memory.ArchiveReason = reason
		memory.UpdatedAt = now
		if err := s.Store(ctx, memory); err != nil {
			return err
		}
		archivedIDs = append(archivedIDs, memory.ID)
	}

	if err := s.RebuildIndex(ctx); err != nil {
		return err
	}
	if err := s.BuildScopeView(ctx, scope); err != nil {
		return err
	}
	return s.writeArchiveManifest(scope, reason, now, archivedIDs)
}

// readIndexFiles reads all per-type index files and merges them.
// Caller must hold s.mu (at least RLock).
func (s *FileMemoryStore) readIndexFiles() (*MemoryIndex, error) {
	idx := &MemoryIndex{}
	typeOrder := []domain.MemoryType{
		domain.MemoryTypeObservation,
		domain.MemoryTypeFact,
		domain.MemoryTypePreference,
		domain.MemoryTypeSkill,
		domain.MemoryTypePattern,
		domain.MemoryTypeContext,
	}
	for _, t := range typeOrder {
		data, err := os.ReadFile(s.indexFilePath(t))
		if err != nil {
			continue // file may not exist yet
		}
		partial, err := parseMemoryIndex(data, t)
		if err != nil {
			continue
		}
		idx.Entries = append(idx.Entries, partial.Entries...)
		idx.Total += partial.Total
		if partial.UpdatedAt.After(idx.UpdatedAt) {
			idx.UpdatedAt = partial.UpdatedAt
		}
	}
	return idx, nil
}

// rebuildIndex scans all memory files and rewrites the per-type index files.
// Caller must hold s.mu.Lock().
func (s *FileMemoryStore) rebuildIndex(ctx context.Context) error {
	if err := os.MkdirAll(s.typeIndexDir(), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(s.scopeIndexDir(), 0755); err != nil {
		return err
	}
	if existingScopeIndexes, err := filepath.Glob(filepath.Join(s.scopeIndexDir(), "*.md")); err == nil {
		for _, path := range existingScopeIndexes {
			if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				return removeErr
			}
		}
	}

	// Collect all active entries grouped by type and scope.
	typeGroups := map[domain.MemoryType][]MemoryIndexEntry{}
	scopeGroups := map[string]scopeIndexGroup{}
	for _, cat := range []string{"streams", "entities"} {
		files, _ := filepath.Glob(filepath.Join(s.baseDir, cat, "*.md"))
		for _, f := range files {
			m, err := s.readFile(f)
			if err != nil {
				continue
			}
			entry := MemoryIndexEntry{
				ID:         m.ID,
				Type:       m.Type,
				ScopeType:  m.ScopeType,
				ScopeID:    m.ScopeID,
				Importance: m.Importance,
				Summary:    truncate(m.Content, 60),
				IsStale:    IsStale(m),
				Archived:   m.Archived,
			}
			if !m.Archived {
				typeGroups[m.Type] = append(typeGroups[m.Type], entry)
				scope := normalizeScope(domain.MemoryScope{Type: m.ScopeType, ID: m.ScopeID})
				key := scopeIndexKey(scope)
				group := scopeGroups[key]
				group.Scope = scope
				group.Entries = append(group.Entries, entry)
				scopeGroups[key] = group
			}
		}
	}

	typeOrder := []domain.MemoryType{
		domain.MemoryTypeObservation,
		domain.MemoryTypeFact,
		domain.MemoryTypePreference,
		domain.MemoryTypeSkill,
		domain.MemoryTypePattern,
		domain.MemoryTypeContext,
	}

	for _, t := range typeOrder {
		entries := typeGroups[t]
		// Sort: non-stale first, then by importance desc
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsStale != entries[j].IsStale {
				return !entries[i].IsStale
			}
			return entries[i].Importance > entries[j].Importance
		})

		if err := writeIndexFile(s.indexFilePath(t), t, entries); err != nil {
			return err
		}
	}

	for _, group := range scopeGroups {
		entries := group.Entries
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsStale != entries[j].IsStale {
				return !entries[i].IsStale
			}
			if entries[i].Type != entries[j].Type {
				return entries[i].Type < entries[j].Type
			}
			return entries[i].Importance > entries[j].Importance
		})

		if err := writeScopeIndexFile(s.scopeIndexFilePath(group.Scope), group.Scope, entries); err != nil {
			return err
		}
	}
	if err := s.rebuildEntrypointLocked(); err != nil {
		return err
	}
	return nil
}

func (s *FileMemoryStore) rebuildEntrypointLocked() error {
	type entry struct {
		ID         string
		Type       domain.MemoryType
		ScopeType  domain.MemoryScopeType
		ScopeID    string
		Importance float64
		Content    string
	}

	entries := make([]entry, 0)
	for _, cat := range []string{"streams", "entities"} {
		files, _ := filepath.Glob(filepath.Join(s.baseDir, cat, "*.md"))
		for _, f := range files {
			m, err := s.readFile(f)
			if err != nil || m.Archived {
				continue
			}
			entries = append(entries, entry{
				ID:         m.ID,
				Type:       m.Type,
				ScopeType:  m.ScopeType,
				ScopeID:    m.ScopeID,
				Importance: m.Importance,
				Content:    m.Content,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type < entries[j].Type
		}
		if entries[i].Importance != entries[j].Importance {
			return entries[i].Importance > entries[j].Importance
		}
		return entries[i].ID < entries[j].ID
	})

	var sb strings.Builder
	sb.WriteString("# MEMORY\n\n")
	sb.WriteString("This file is a compact index of active memories. Each line is a summary pointer, not the full memory body.\n\n")
	currentType := domain.MemoryType("")
	for _, item := range entries {
		if item.Type != currentType {
			currentType = item.Type
			sb.WriteString("## ")
			sb.WriteString(strings.Title(string(currentType)))
			sb.WriteString("\n")
		}
		scope := normalizeScope(domain.MemoryScope{Type: item.ScopeType, ID: item.ScopeID})
		scopeLabel := string(scope.Type)
		if scope.ID != "" {
			scopeLabel += ":" + scope.ID
		}
		sb.WriteString(fmt.Sprintf("- [%s] (%s, importance=%.2f) %s\n", item.ID, scopeLabel, item.Importance, truncate(item.Content, 140)))
	}
	if len(entries) == 0 {
		sb.WriteString("_No active memories._\n")
	}

	return os.WriteFile(s.entrypointPath(), []byte(sb.String()), 0644)
}

// writeIndexFile writes one per-type index file atomically (tmp + rename).
func writeIndexFile(path string, t domain.MemoryType, entries []MemoryIndexEntry) error {
	type indexFM struct {
		Type      string    `yaml:"type"`
		Total     int       `yaml:"total"`
		UpdatedAt time.Time `yaml:"updated_at"`
	}
	fm, _ := yaml.Marshal(indexFM{
		Type:      string(t),
		Total:     len(entries),
		UpdatedAt: time.Now(),
	})

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(string(fm))
	sb.WriteString("---\n\n")
	sb.WriteString(fmt.Sprintf("# %ss (%d)\n\n", strings.Title(string(t)), len(entries)))

	for _, e := range entries {
		staleTag := ""
		if e.IsStale {
			staleTag = " ~~[stale]~~"
		}
		sb.WriteString(fmt.Sprintf("- [%s] %.2f | %s%s\n", e.ID, e.Importance, e.Summary, staleTag))
	}

	// Atomic write: write to a temp file in the same directory, then rename.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(sb.String()); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func writeScopeIndexFile(path string, scope domain.MemoryScope, entries []MemoryIndexEntry) error {
	type indexFM struct {
		ScopeType domain.MemoryScopeType `yaml:"scope_type"`
		ScopeID   string                 `yaml:"scope_id,omitempty"`
		Total     int                    `yaml:"total"`
		UpdatedAt time.Time              `yaml:"updated_at"`
	}
	fm, _ := yaml.Marshal(indexFM{
		ScopeType: scope.Type,
		ScopeID:   scope.ID,
		Total:     len(entries),
		UpdatedAt: time.Now(),
	})

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(string(fm))
	sb.WriteString("---\n\n")
	sb.WriteString(fmt.Sprintf("# Scope %s (%d)\n\n", scopeIndexKey(scope), len(entries)))

	for _, e := range entries {
		staleTag := ""
		if e.IsStale {
			staleTag = " ~~[stale]~~"
		}
		sb.WriteString(fmt.Sprintf("- [%s][%s] %.2f | %s%s\n", e.ID, e.Type, e.Importance, e.Summary, staleTag))
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".scope-index-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(sb.String()); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// parseMemoryIndex parses a per-type index file.
func parseMemoryIndex(data []byte, t domain.MemoryType) (*MemoryIndex, error) {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return &MemoryIndex{}, nil
	}
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return &MemoryIndex{}, nil
	}

	type indexFM struct {
		Total     int       `yaml:"total"`
		UpdatedAt time.Time `yaml:"updated_at"`
	}
	var fm indexFM
	_ = yaml.Unmarshal([]byte(parts[1]), &fm)

	idx := &MemoryIndex{
		Total:     fm.Total,
		UpdatedAt: fm.UpdatedAt,
	}

	for _, line := range strings.Split(parts[2], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- [") {
			continue
		}
		idEnd := strings.Index(line, "]")
		if idEnd < 0 {
			continue
		}
		id := line[3:idEnd]
		rest := strings.TrimSpace(line[idEnd+1:])
		entryType := t

		if strings.HasPrefix(rest, "[") {
			typeEnd := strings.Index(rest, "]")
			if typeEnd > 0 {
				entryType = domain.MemoryType(rest[1:typeEnd])
				rest = strings.TrimSpace(rest[typeEnd+1:])
			}
		}

		var importance float64
		var summary string
		if parts2 := strings.SplitN(rest, "|", 2); len(parts2) == 2 {
			fmt.Sscanf(strings.TrimSpace(parts2[0]), "%f", &importance)
			summary = strings.TrimSpace(parts2[1])
			summary = strings.ReplaceAll(summary, " ~~[stale]~~", "")
		}

		idx.Entries = append(idx.Entries, MemoryIndexEntry{
			ID:         id,
			Type:       entryType,
			Importance: importance,
			Summary:    summary,
			IsStale:    strings.Contains(line, "~~[stale]~~"),
		})
	}

	return idx, nil
}

type scopeIndexGroup struct {
	Scope   domain.MemoryScope
	Entries []MemoryIndexEntry
}

func scopeIndexKey(scope domain.MemoryScope) string {
	scope = normalizeScope(scope)
	if scope.ID == "" {
		return string(scope.Type)
	}
	return fmt.Sprintf("%s:%s", scope.Type, scope.ID)
}

func sanitizeScopeID(id string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(id)
}

func scopeViewTitle(scope domain.MemoryScope) string {
	switch scope.Type {
	case domain.MemoryScopeGlobal:
		return "Global Memory"
	case domain.MemoryScopeTeam:
		return fmt.Sprintf("Team %s Process Memory", scope.ID)
	case domain.MemoryScopeAgent:
		return fmt.Sprintf("Agent %s Thread Memory", scope.ID)
	case domain.MemoryScopeSession:
		return fmt.Sprintf("Session %s Memory", scope.ID)
	case domain.MemoryScopeUser:
		return fmt.Sprintf("User %s Memory", scope.ID)
	default:
		return "Memory View"
	}
}

func scopeViewSectionTitle(t domain.MemoryType) string {
	switch t {
	case domain.MemoryTypeObservation:
		return "Observations"
	case domain.MemoryTypeFact:
		return "Facts"
	case domain.MemoryTypePreference:
		return "Preferences"
	case domain.MemoryTypeSkill:
		return "Skills"
	case domain.MemoryTypePattern:
		return "Patterns"
	case domain.MemoryTypeContext:
		return "Context"
	default:
		return "Other"
	}
}

func memoryTypeRank(t domain.MemoryType) int {
	switch t {
	case domain.MemoryTypeObservation:
		return 0
	case domain.MemoryTypeFact:
		return 1
	case domain.MemoryTypePreference:
		return 2
	case domain.MemoryTypeSkill:
		return 3
	case domain.MemoryTypePattern:
		return 4
	case domain.MemoryTypeContext:
		return 5
	default:
		return 6
	}
}

func (s *FileMemoryStore) writeArchiveManifest(scope domain.MemoryScope, reason string, archivedAt time.Time, ids []string) error {
	if err := os.MkdirAll(s.archiveManifestDir(), 0755); err != nil {
		return err
	}

	type manifestFM struct {
		ScopeType     domain.MemoryScopeType `yaml:"scope_type"`
		ScopeID       string                 `yaml:"scope_id,omitempty"`
		ArchivedAt    time.Time              `yaml:"archived_at"`
		ArchiveReason string                 `yaml:"archive_reason,omitempty"`
		Count         int                    `yaml:"count"`
	}
	fm, _ := yaml.Marshal(manifestFM{
		ScopeType:     scope.Type,
		ScopeID:       scope.ID,
		ArchivedAt:    archivedAt,
		ArchiveReason: reason,
		Count:         len(ids),
	})

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(string(fm))
	sb.WriteString("---\n\n")
	sb.WriteString("# Archive Manifest\n\n")
	if reason != "" {
		sb.WriteString("Reason: ")
		sb.WriteString(reason)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Memory IDs\n")
	for _, id := range ids {
		sb.WriteString("- ")
		sb.WriteString(id)
		sb.WriteString("\n")
	}

	return os.WriteFile(s.archiveManifestPath(scope, archivedAt), []byte(sb.String()), 0644)
}

// truncate shortens s to at most n runes, appending "…" if truncated.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}

// newUUID generates a new UUID string.
func newUUID() string {
	return uuid.New().String()
}

// parseJSON unmarshals raw JSON into v, stripping markdown code fences if present.
func parseJSON(raw string, v interface{}) error {
	return json.Unmarshal([]byte(extractJSONFromText(raw)), v)
}

// extractJSONFromText strips <think>...</think> reasoning blocks, markdown code
// fences, and returns the bare JSON object or array.
func extractJSONFromText(s string) string {
	s = strings.TrimSpace(s)
	// Strip <think>...</think> blocks (reasoning model output)
	for {
		start := strings.Index(s, "<think>")
		end := strings.Index(s, "</think>")
		if start == -1 || end == -1 || end <= start {
			break
		}
		s = strings.TrimSpace(s[:start] + s[end+len("</think>"):])
	}
	// Strip markdown code fences
	for _, fence := range []string{"```json", "```"} {
		if idx := strings.Index(s, fence); idx != -1 {
			s = s[idx+len(fence):]
			if end := strings.Index(s, "```"); end != -1 {
				s = s[:end]
			}
			break
		}
	}
	s = strings.TrimSpace(s)
	// Find first { or [
	for i, ch := range s {
		if ch == '{' || ch == '[' {
			return s[i:]
		}
	}
	return s
}
