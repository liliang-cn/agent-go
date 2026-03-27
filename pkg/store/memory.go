package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/cortexdb/v2/pkg/core"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

var (
	ErrMemoryNotFound = errors.New("memory not found")
)

const (
	memoryBucketPrefix    = "memory:"
	memoryBucketNamespace = "agentgo"
	memoryStoreRetryCount = 8
)

// Memory is a local internal structure, but we prefer using domain.Memory for interface methods.
type Memory struct {
	ID           string                 `json:"id"`
	SessionID    string                 `json:"session_id,omitempty"`
	Type         string                 `json:"type"`
	Content      string                 `json:"content"`
	Vector       []float64              `json:"vector,omitempty"`
	Importance   float64                `json:"importance"`
	AccessCount  int                    `json:"access_count"`
	LastAccessed time.Time              `json:"last_accessed"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// MemoryStore handles memory persistence using cortexdb memory buckets.
type MemoryStore struct {
	db    *cortexdb.DB
	store *core.SQLiteStore
}

type memoryBucket struct {
	BucketID      string
	UserID        string
	LogicalBankID string
	Scope         domain.MemoryScope
}

type storedMemoryRow struct {
	ID              string
	BucketID        string
	UserID          string
	Role            string
	Content         string
	Vector          []float32
	Metadata        map[string]interface{}
	CreatedAt       time.Time
	SessionMetadata map[string]interface{}
}

// NewMemoryStore creates a new memory store backed by cortexdb's dedicated memory bucket schema.
func NewMemoryStore(dbPath string) (*MemoryStore, error) {
	if dbPath == "" {
		return nil, errors.New("dbPath is required")
	}

	var db *cortexdb.DB
	err := retrySQLiteBusy(context.Background(), func() error {
		var openErr error
		db, openErr = cortexdb.Open(cortexdb.DefaultConfig(dbPath))
		return openErr
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cortexdb memory store: %w", err)
	}

	sqliteStore, ok := db.Vector().(*core.SQLiteStore)
	if !ok {
		_ = db.Close()
		return nil, fmt.Errorf("failed to get SQLiteStore from cortexdb")
	}

	return &MemoryStore{db: db, store: sqliteStore}, nil
}

// Store saves a new memory using cortexdb memory buckets.
func (s *MemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	scope := resolveMemoryScope(memory)
	return s.storeMemory(ctx, memory, scope)
}

// Search performs vector search across all memory buckets.
func (s *MemoryStore) Search(ctx context.Context, vector []float64, topK int, minScore float64) ([]*domain.MemoryWithScore, error) {
	buckets, err := s.listMemoryBucketIDs(ctx)
	if err != nil {
		return nil, err
	}
	return s.searchBuckets(ctx, buckets, vector, topK, minScore)
}

func (s *MemoryStore) SearchBySession(ctx context.Context, sessionID string, vector []float64, topK int) ([]*domain.MemoryWithScore, error) {
	bucket := memoryBucketForScope(domain.MemoryScope{Type: domain.MemoryScopeSession, ID: strings.TrimSpace(sessionID)})
	return s.searchBuckets(ctx, []string{bucket.BucketID}, vector, topK, 0)
}

// SearchByScope searches memories within specific scopes.
func (s *MemoryStore) SearchByScope(ctx context.Context, vector []float64, scopes []domain.MemoryScope, topK int) ([]*domain.MemoryWithScore, error) {
	if topK <= 0 {
		topK = 10
	}

	var bucketIDs []string
	seenBuckets := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		bucket := memoryBucketForScope(scope)
		if bucket.BucketID == "" {
			continue
		}
		if _, exists := seenBuckets[bucket.BucketID]; exists {
			continue
		}
		seenBuckets[bucket.BucketID] = struct{}{}
		bucketIDs = append(bucketIDs, bucket.BucketID)
	}

	return s.searchBuckets(ctx, bucketIDs, vector, topK, 0)
}

// StoreWithScope stores a memory with a specific scope.
func (s *MemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	return s.storeMemory(ctx, memory, scope)
}

// SearchByText performs lexical search across cortexdb memory buckets with
// CJK n-gram tokenization, importance weighting, and time decay.
func (s *MemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if topK <= 0 {
		topK = 10
	}

	tokens := tokenizeText(query)
	if len(tokens) == 0 {
		return nil, nil
	}

	// Build a LIKE pattern for the first token to narrow the SQL scan,
	// then re-score all candidates in Go for accuracy.
	searchPattern := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	rows, err := s.queryWithRetry(ctx, `
		SELECT m.id, m.session_id, s.user_id, m.role, m.content, m.vector, m.metadata, m.created_at, s.metadata
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE s.id LIKE ?
		  AND (LOWER(m.content) LIKE ? OR LOWER(m.content) LIKE ?)
		ORDER BY m.created_at DESC
		LIMIT ?
	`, memoryBucketPrefix+"%", searchPattern, "%"+tokens[0]+"%", topK*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	var results []*domain.MemoryWithScore
	for rows.Next() {
		row, err := scanStoredMemoryRow(rows)
		if err != nil {
			continue
		}
		textScore := ngramMatchScore(tokens, row.Content)
		if textScore == 0 {
			continue
		}
		mem := row.toDomainMemory()
		score := applyMemoryBoosts(textScore, mem.Importance, mem.CreatedAt, now)
		results = append(results, &domain.MemoryWithScore{Memory: mem, Score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

func (s *MemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	row, err := s.loadStoredMemoryRow(ctx, id)
	if err != nil {
		return nil, err
	}
	return row.toDomainMemory(), nil
}

func (s *MemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	if memory == nil || strings.TrimSpace(memory.ID) == "" {
		return fmt.Errorf("memory id is required")
	}

	existing, err := s.loadStoredMemoryRow(ctx, memory.ID)
	if err != nil {
		return err
	}

	merged := existing.toDomainMemory()
	if strings.TrimSpace(memory.Content) != "" {
		merged.Content = memory.Content
	}
	if memory.Type != "" {
		merged.Type = memory.Type
	}
	if len(memory.Vector) > 0 {
		merged.Vector = append([]float64(nil), memory.Vector...)
	}
	if memory.Importance != 0 {
		merged.Importance = memory.Importance
	}
	if !memory.LastAccessed.IsZero() {
		merged.LastAccessed = memory.LastAccessed
	}
	if memory.AccessCount != 0 {
		merged.AccessCount = memory.AccessCount
	}
	if memory.Metadata != nil {
		merged.Metadata = cloneMetadata(memory.Metadata)
	}
	if !memory.CreatedAt.IsZero() {
		merged.CreatedAt = memory.CreatedAt
	}
	if memory.ScopeType != "" || memory.ScopeID != "" || memory.SessionID != "" {
		merged.ScopeType = memory.ScopeType
		merged.ScopeID = memory.ScopeID
		merged.SessionID = memory.SessionID
	}
	if len(memory.Keywords) > 0 {
		merged.Keywords = append([]string(nil), memory.Keywords...)
	}
	if len(memory.Tags) > 0 {
		merged.Tags = append([]string(nil), memory.Tags...)
	}

	scope := resolveMemoryScope(merged)
	return s.storeMemory(ctx, merged, scope)
}

func (s *MemoryStore) IncrementAccess(ctx context.Context, id string) error {
	row, err := s.loadStoredMemoryRow(ctx, id)
	if err != nil {
		return err
	}

	metadata := cloneMetadata(row.Metadata)
	accessCount, _ := intFromAny(metadata["access_count"])
	accessCount++
	metadata["access_count"] = accessCount
	metadata["last_accessed"] = time.Now().UTC().Format(time.RFC3339Nano)

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}

	err = s.execWithRetry(ctx, `UPDATE messages SET metadata = ? WHERE id = ?`, metadataJSON, id)
	return err
}

func (s *MemoryStore) GetByType(ctx context.Context, memoryType domain.MemoryType, limit int) ([]*domain.Memory, error) {
	all, _, err := s.List(ctx, max(limit, 1000), 0)
	if err != nil {
		return nil, err
	}

	var filtered []*domain.Memory
	for _, memory := range all {
		if memory.Type == memoryType {
			filtered = append(filtered, memory)
		}
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}

	return filtered, nil
}

// List lists all memories across all buckets by querying cortexdb's message store directly.
func (s *MemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	if limit <= 0 {
		limit = 100
	}

	var total int
	if err := s.queryRowWithRetry(ctx, `
		SELECT COUNT(*)
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE s.id LIKE ?
	`, memoryBucketPrefix+"%").Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.queryWithRetry(ctx, `
		SELECT m.id, m.session_id, s.user_id, m.role, m.content, m.vector, m.metadata, m.created_at, s.metadata
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE s.id LIKE ?
		ORDER BY m.created_at DESC
		LIMIT ? OFFSET ?
	`, memoryBucketPrefix+"%", limit, offset)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()

	var memories []*domain.Memory
	for rows.Next() {
		row, err := scanStoredMemoryRow(rows)
		if err != nil {
			continue
		}
		memories = append(memories, row.toDomainMemory())
	}

	return memories, total, nil
}

func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	row, err := s.loadStoredMemoryRow(ctx, id)
	if err != nil {
		return err
	}

	if err := s.execWithRetry(ctx, `DELETE FROM messages WHERE id = ?`, id); err != nil {
		return err
	}

	return s.cleanupEmptyBucket(ctx, row.BucketID)
}

func (s *MemoryStore) DeleteBySession(ctx context.Context, sessionID string) error {
	scope := parseLogicalBankID(sessionID)
	scope.Type = normalizeVectorScope(scope).Type
	if scope.Type == domain.MemoryScopeGlobal && strings.TrimSpace(scope.ID) == "" && strings.TrimSpace(sessionID) != "" {
		scope = domain.MemoryScope{Type: domain.MemoryScopeSession, ID: strings.TrimSpace(sessionID)}
	}

	bucket := memoryBucketForScope(scope)
	if err := s.execWithRetry(ctx, `DELETE FROM messages WHERE session_id = ?`, bucket.BucketID); err != nil {
		return err
	}

	return s.execWithRetry(ctx, `DELETE FROM sessions WHERE id = ?`, bucket.BucketID)
}

func (s *MemoryStore) InitSchema(ctx context.Context) error {
	return nil
}

func (s *MemoryStore) ConfigureBank(ctx context.Context, bankID string, config *domain.MemoryBankConfig) error {
	scope := parseLogicalBankID(bankID)
	if scope.Type == domain.MemoryScopeGlobal && scope.ID == "" && strings.TrimSpace(bankID) != "" && bankID != "global" {
		scope = domain.MemoryScope{Type: domain.MemoryScopeSession, ID: strings.TrimSpace(bankID)}
	}

	bucket := memoryBucketForScope(scope)
	if err := s.ensureMemoryBucket(ctx, bucket); err != nil {
		return err
	}

	metadata, err := s.loadSessionMetadata(ctx, bucket.BucketID)
	if err != nil {
		return err
	}
	metadata["mission"] = config.Mission
	metadata["directives"] = config.Directives
	metadata["skepticism"] = config.Skepticism
	metadata["literalism"] = config.Literalism
	metadata["empathy"] = config.Empathy

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}

	err = s.execWithRetry(ctx, `
		UPDATE sessions
		SET metadata = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, metadataJSON, bucket.BucketID)
	return err
}

// Reflect is only implemented for the file-backed truth store.
func (s *MemoryStore) Reflect(ctx context.Context, bankID string) (string, error) {
	return "", nil
}

func (s *MemoryStore) AddMentalModel(ctx context.Context, model *domain.MentalModel) error {
	if model == nil {
		return nil
	}

	memory := &domain.Memory{
		ID:         model.ID,
		Type:       domain.MemoryTypeObservation,
		Content:    fmt.Sprintf("Mental Model: %s\n%s", model.Name, model.Content),
		Importance: 1.0,
		Metadata: map[string]interface{}{
			"name":        model.Name,
			"description": model.Description,
			"tags":        model.Tags,
		},
		CreatedAt: time.Now().UTC(),
	}

	return s.StoreWithScope(ctx, memory, domain.MemoryScope{Type: domain.MemoryScopeGlobal})
}

func (s *MemoryStore) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *MemoryStore) storeMemory(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if memory == nil {
		return fmt.Errorf("memory is required")
	}
	if strings.TrimSpace(memory.ID) == "" {
		return fmt.Errorf("memory id is required")
	}
	if strings.TrimSpace(memory.Content) == "" {
		return fmt.Errorf("memory content is required")
	}

	scope = normalizeVectorScope(scope)
	bucket := memoryBucketForScope(scope)
	if err := s.ensureMemoryBucket(ctx, bucket); err != nil {
		return err
	}

	createdAt := memory.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	metadata := buildStoredMemoryMetadata(memory, scope, bucket.LogicalBankID)
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal memory metadata: %w", err)
	}

	vectorBytes, err := encodeFloat32Vector(toFloat32(memory.Vector))
	if err != nil {
		return fmt.Errorf("encode memory vector: %w", err)
	}

	role := "memory"
	if roleValue, ok := stringFromAny(memory.Metadata["role"]); ok && strings.TrimSpace(roleValue) != "" {
		role = roleValue
	}

	err = s.execWithRetry(ctx, `
		INSERT INTO messages (id, session_id, role, content, vector, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			role = excluded.role,
			content = excluded.content,
			vector = CASE
				WHEN excluded.vector IS NOT NULL THEN excluded.vector
				ELSE messages.vector
			END,
			metadata = excluded.metadata
	`, memory.ID, bucket.BucketID, role, memory.Content, vectorBytes, metadataJSON, createdAt)
	if err != nil {
		return fmt.Errorf("store memory: %w", err)
	}

	return nil
}

func (s *MemoryStore) searchBuckets(ctx context.Context, bucketIDs []string, vector []float64, topK int, minScore float64) ([]*domain.MemoryWithScore, error) {
	if topK <= 0 {
		topK = 10
	}
	if len(vector) == 0 || len(bucketIDs) == 0 {
		return nil, nil
	}

	queryVec := toFloat32(vector)
	seen := make(map[string]*domain.MemoryWithScore)
	for _, bucketID := range bucketIDs {
		rows, err := s.queryWithRetry(ctx, `
			SELECT m.id, m.session_id, s.user_id, m.role, m.content, m.vector, m.metadata, m.created_at, s.metadata
			FROM messages m
			JOIN sessions s ON s.id = m.session_id
			WHERE m.session_id = ? AND m.vector IS NOT NULL
		`, bucketID)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			row, err := scanStoredMemoryRow(rows)
			if err != nil || len(row.Vector) == 0 {
				continue
			}

			score := cosineSimilarity(queryVec, row.Vector)
			if score < minScore {
				continue
			}

			candidate := &domain.MemoryWithScore{
				Memory: row.toDomainMemory(),
				Score:  score,
			}
			if existing, exists := seen[candidate.ID]; !exists || existing.Score < candidate.Score {
				seen[candidate.ID] = candidate
			}
		}
		_ = rows.Close()
	}

	results := make([]*domain.MemoryWithScore, 0, len(seen))
	for _, result := range seen {
		results = append(results, result)
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].CreatedAt.After(results[j].CreatedAt)
		}
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

func (s *MemoryStore) ensureMemoryBucket(ctx context.Context, bucket memoryBucket) error {
	if bucket.BucketID == "" {
		return fmt.Errorf("memory bucket id is required")
	}

	return retrySQLiteBusy(ctx, func() error {
		_, err := s.store.GetSession(ctx, bucket.BucketID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, core.ErrNotFound) {
			return err
		}

		return s.store.CreateSession(ctx, &core.Session{
			ID:     bucket.BucketID,
			UserID: bucket.UserID,
			Metadata: map[string]interface{}{
				"kind":               "memory_bucket",
				"namespace":          memoryBucketNamespace,
				"scope":              encodedScopeForBucket(bucket.Scope),
				"agentgo_scope_type": string(bucket.Scope.Type),
				"agentgo_scope_id":   bucket.Scope.ID,
				"logical_bank_id":    bucket.LogicalBankID,
			},
		})
	})
}

func (s *MemoryStore) cleanupEmptyBucket(ctx context.Context, bucketID string) error {
	var remaining int
	if err := s.queryRowWithRetry(ctx, `
		SELECT COUNT(*)
		FROM messages
		WHERE session_id = ?
	`, bucketID).Scan(&remaining); err != nil {
		return err
	}
	if remaining > 0 {
		return nil
	}

	return s.execWithRetry(ctx, `DELETE FROM sessions WHERE id = ?`, bucketID)
}

func (s *MemoryStore) listMemoryBucketIDs(ctx context.Context) ([]string, error) {
	rows, err := s.queryWithRetry(ctx, `
		SELECT id
		FROM sessions
		WHERE id LIKE ?
		ORDER BY created_at DESC
	`, memoryBucketPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bucketIDs []string
	for rows.Next() {
		var bucketID string
		if err := rows.Scan(&bucketID); err == nil {
			bucketIDs = append(bucketIDs, bucketID)
		}
	}
	return bucketIDs, nil
}

func (s *MemoryStore) loadStoredMemoryRow(ctx context.Context, id string) (*storedMemoryRow, error) {
	rows, err := s.queryWithRetry(ctx, `
		SELECT m.id, m.session_id, s.user_id, m.role, m.content, m.vector, m.metadata, m.created_at, s.metadata
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE m.id = ? AND s.id LIKE ?
	`, id, memoryBucketPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, ErrMemoryNotFound
	}

	row, err := scanStoredMemoryRow(rows)
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (s *MemoryStore) loadSessionMetadata(ctx context.Context, bucketID string) (map[string]interface{}, error) {
	var metadataJSON []byte
	err := s.queryRowWithRetry(ctx, `
		SELECT metadata
		FROM sessions
		WHERE id = ?
	`, bucketID).Scan(&metadataJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]interface{}{}, nil
	}
	if err != nil {
		return nil, err
	}

	metadata := map[string]interface{}{}
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &metadata)
	}
	return metadata, nil
}

func (s *MemoryStore) execWithRetry(ctx context.Context, query string, args ...interface{}) error {
	return retrySQLiteBusy(ctx, func() error {
		_, err := s.store.GetDB().ExecContext(ctx, query, args...)
		return err
	})
}

func (s *MemoryStore) queryWithRetry(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	var rows *sql.Rows
	err := retrySQLiteBusy(ctx, func() error {
		var queryErr error
		rows, queryErr = s.store.GetDB().QueryContext(ctx, query, args...)
		return queryErr
	})
	return rows, err
}

func (s *MemoryStore) queryRowWithRetry(ctx context.Context, query string, args ...interface{}) *retryRow {
	return &retryRow{
		scan: func(dest ...interface{}) error {
			return retrySQLiteBusy(ctx, func() error {
				return s.store.GetDB().QueryRowContext(ctx, query, args...).Scan(dest...)
			})
		},
	}
}

type retryRow struct {
	scan func(dest ...interface{}) error
}

func (r *retryRow) Scan(dest ...interface{}) error {
	return r.scan(dest...)
}

func retrySQLiteBusy(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < memoryStoreRetryCount; attempt++ {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			if !isSQLiteBusyErr(err) || attempt == memoryStoreRetryCount-1 {
				return err
			}
		}

		delay := time.Duration(attempt+1) * 75 * time.Millisecond
		if ctx == nil {
			time.Sleep(delay)
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func isSQLiteBusyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "sqlite_busy")
}

func scanStoredMemoryRow(rows *sql.Rows) (*storedMemoryRow, error) {
	var (
		row             storedMemoryRow
		vectorBytes     []byte
		metadataJSON    []byte
		sessionMetaJSON []byte
	)

	if err := rows.Scan(
		&row.ID,
		&row.BucketID,
		&row.UserID,
		&row.Role,
		&row.Content,
		&vectorBytes,
		&metadataJSON,
		&row.CreatedAt,
		&sessionMetaJSON,
	); err != nil {
		return nil, err
	}

	if len(vectorBytes) > 0 {
		vector, err := decodeFloat32Vector(vectorBytes)
		if err != nil {
			return nil, err
		}
		row.Vector = vector
	}

	row.Metadata = map[string]interface{}{}
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &row.Metadata)
	}

	row.SessionMetadata = map[string]interface{}{}
	if len(sessionMetaJSON) > 0 {
		_ = json.Unmarshal(sessionMetaJSON, &row.SessionMetadata)
	}

	return &row, nil
}

func (r *storedMemoryRow) toDomainMemory() *domain.Memory {
	metadata := cloneMetadata(r.Metadata)
	scope := storedScope(metadata, r.BucketID, r.UserID, r.SessionMetadata)
	logicalBankID, _ := stringFromAny(metadata["bank_id"])
	if strings.TrimSpace(logicalBankID) == "" {
		logicalBankID = logicalBankIDFromScope(scope)
	}

	memoryType := string(domain.MemoryTypeFact)
	if value, ok := stringFromAny(metadata["memory_type"]); ok && strings.TrimSpace(value) != "" {
		memoryType = value
	}

	importance, _ := floatFromAny(metadata["importance"])
	accessCount, _ := intFromAny(metadata["access_count"])
	lastAccessed := timeFromAny(metadata["last_accessed"])

	return &domain.Memory{
		ID:           r.ID,
		SessionID:    logicalBankID,
		ScopeType:    scope.Type,
		ScopeID:      scope.ID,
		Type:         domain.MemoryType(memoryType),
		Content:      r.Content,
		Vector:       toFloat64(r.Vector),
		Keywords:     stringSliceFromAny(metadata["keywords"]),
		Tags:         stringSliceFromAny(metadata["tags"]),
		Importance:   importance,
		AccessCount:  accessCount,
		LastAccessed: lastAccessed,
		Metadata:     metadata,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.CreatedAt,
	}
}

func resolveMemoryScope(memory *domain.Memory) domain.MemoryScope {
	if memory == nil {
		return domain.MemoryScope{Type: domain.MemoryScopeGlobal}
	}

	if normalizeScopeType(memory.ScopeType) != domain.MemoryScopeGlobal || strings.TrimSpace(memory.ScopeID) != "" {
		return normalizeVectorScope(domain.MemoryScope{Type: memory.ScopeType, ID: strings.TrimSpace(memory.ScopeID)})
	}

	if strings.TrimSpace(memory.SessionID) == "" {
		return domain.MemoryScope{Type: domain.MemoryScopeGlobal}
	}

	return normalizeVectorScope(parseLogicalBankID(memory.SessionID))
}

func memoryBucketForScope(scope domain.MemoryScope) memoryBucket {
	scope = normalizeVectorScope(scope)
	logicalBankID := logicalBankIDFromScope(scope)

	switch scope.Type {
	case domain.MemoryScopeGlobal:
		return memoryBucket{
			BucketID:      fmt.Sprintf("%s%s:%s", memoryBucketPrefix, cortexdb.MemoryScopeGlobal, memoryBucketNamespace),
			LogicalBankID: logicalBankID,
			Scope:         scope,
		}
	case domain.MemoryScopeSession:
		sessionID := strings.TrimSpace(scope.ID)
		return memoryBucket{
			BucketID:      fmt.Sprintf("%s%s:%s:%s", memoryBucketPrefix, cortexdb.MemoryScopeSession, sessionID, memoryBucketNamespace),
			LogicalBankID: logicalBankID,
			Scope:         scope,
		}
	default:
		userID := encodedUserIDForScope(scope)
		return memoryBucket{
			BucketID:      fmt.Sprintf("%s%s:%s:%s", memoryBucketPrefix, cortexdb.MemoryScopeUser, userID, memoryBucketNamespace),
			UserID:        userID,
			LogicalBankID: logicalBankID,
			Scope:         scope,
		}
	}
}

func logicalBankIDFromScope(scope domain.MemoryScope) string {
	scope = normalizeVectorScope(scope)
	switch scope.Type {
	case domain.MemoryScopeGlobal:
		return "global"
	case domain.MemoryScopeSession:
		return strings.TrimSpace(scope.ID)
	default:
		if strings.TrimSpace(scope.ID) == "" {
			return string(scope.Type)
		}
		return fmt.Sprintf("%s:%s", scope.Type, strings.TrimSpace(scope.ID))
	}
}

func parseLogicalBankID(bankID string) domain.MemoryScope {
	bankID = strings.TrimSpace(bankID)
	if bankID == "" || bankID == "global" || bankID == "default" {
		return domain.MemoryScope{Type: domain.MemoryScopeGlobal}
	}

	parts := strings.SplitN(bankID, ":", 2)
	if len(parts) == 1 {
		switch normalizeScopeType(domain.MemoryScopeType(parts[0])) {
		case domain.MemoryScopeGlobal, domain.MemoryScopeAgent, domain.MemoryScopeSquad, domain.MemoryScopeUser:
			return domain.MemoryScope{Type: normalizeScopeType(domain.MemoryScopeType(parts[0]))}
		default:
			return domain.MemoryScope{Type: domain.MemoryScopeSession, ID: bankID}
		}
	}

	return domain.MemoryScope{
		Type: normalizeScopeType(domain.MemoryScopeType(parts[0])),
		ID:   strings.TrimSpace(parts[1]),
	}
}

func normalizeVectorScope(scope domain.MemoryScope) domain.MemoryScope {
	scope.Type = normalizeScopeType(scope.Type)
	if scope.Type == "" {
		scope.Type = domain.MemoryScopeGlobal
	}
	return scope
}

func encodedScopeForBucket(scope domain.MemoryScope) string {
	scope = normalizeVectorScope(scope)
	switch scope.Type {
	case domain.MemoryScopeGlobal:
		return cortexdb.MemoryScopeGlobal
	case domain.MemoryScopeSession:
		return cortexdb.MemoryScopeSession
	default:
		return cortexdb.MemoryScopeUser
	}
}

func encodedUserIDForScope(scope domain.MemoryScope) string {
	scope = normalizeVectorScope(scope)
	switch scope.Type {
	case domain.MemoryScopeUser:
		return "user:" + strings.TrimSpace(scope.ID)
	case domain.MemoryScopeAgent:
		return "agent:" + strings.TrimSpace(scope.ID)
	case domain.MemoryScopeSquad:
		return "squad:" + strings.TrimSpace(scope.ID)
	default:
		return "scope:" + strings.TrimSpace(scope.ID)
	}
}

func storedScope(metadata map[string]interface{}, bucketID, userID string, sessionMeta map[string]interface{}) domain.MemoryScope {
	if scopeType, ok := stringFromAny(metadata["agentgo_scope_type"]); ok && strings.TrimSpace(scopeType) != "" {
		scopeID, _ := stringFromAny(metadata["agentgo_scope_id"])
		return normalizeVectorScope(domain.MemoryScope{Type: domain.MemoryScopeType(scopeType), ID: scopeID})
	}

	if scopeType, ok := stringFromAny(sessionMeta["agentgo_scope_type"]); ok && strings.TrimSpace(scopeType) != "" {
		scopeID, _ := stringFromAny(sessionMeta["agentgo_scope_id"])
		return normalizeVectorScope(domain.MemoryScope{Type: domain.MemoryScopeType(scopeType), ID: scopeID})
	}

	if bankID, ok := stringFromAny(metadata["bank_id"]); ok && strings.TrimSpace(bankID) != "" {
		return normalizeVectorScope(parseLogicalBankID(bankID))
	}

	switch {
	case strings.HasPrefix(bucketID, memoryBucketPrefix+cortexdb.MemoryScopeSession+":"):
		trimmed := strings.TrimPrefix(bucketID, memoryBucketPrefix+cortexdb.MemoryScopeSession+":")
		if idx := strings.LastIndex(trimmed, ":"+memoryBucketNamespace); idx >= 0 {
			trimmed = trimmed[:idx]
		}
		return domain.MemoryScope{Type: domain.MemoryScopeSession, ID: trimmed}
	case strings.HasPrefix(bucketID, memoryBucketPrefix+cortexdb.MemoryScopeGlobal+":"):
		return domain.MemoryScope{Type: domain.MemoryScopeGlobal}
	case strings.HasPrefix(userID, "agent:"):
		return domain.MemoryScope{Type: domain.MemoryScopeAgent, ID: strings.TrimPrefix(userID, "agent:")}
	case strings.HasPrefix(userID, "squad:"):
		return domain.MemoryScope{Type: domain.MemoryScopeSquad, ID: strings.TrimPrefix(userID, "squad:")}
	case strings.HasPrefix(userID, "user:"):
		return domain.MemoryScope{Type: domain.MemoryScopeUser, ID: strings.TrimPrefix(userID, "user:")}
	default:
		return domain.MemoryScope{Type: domain.MemoryScopeGlobal}
	}
}

func buildStoredMemoryMetadata(memory *domain.Memory, scope domain.MemoryScope, logicalBankID string) map[string]interface{} {
	metadata := cloneMetadata(memory.Metadata)
	if metadata == nil {
		metadata = make(map[string]interface{})
	}

	metadata["kind"] = "memory"
	metadata["memory_type"] = string(memory.Type)
	metadata["importance"] = memory.Importance
	metadata["bank_id"] = logicalBankID
	metadata["agentgo_scope_type"] = string(scope.Type)
	metadata["agentgo_scope_id"] = scope.ID

	if memory.AccessCount > 0 {
		metadata["access_count"] = memory.AccessCount
	}
	if !memory.LastAccessed.IsZero() {
		metadata["last_accessed"] = memory.LastAccessed.UTC().Format(time.RFC3339Nano)
	}
	if len(memory.Tags) > 0 {
		metadata["tags"] = append([]string(nil), memory.Tags...)
	}
	if len(memory.Keywords) > 0 {
		metadata["keywords"] = append([]string(nil), memory.Keywords...)
	}

	return metadata
}

func cloneMetadata(values map[string]interface{}) map[string]interface{} {
	if values == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func stringFromAny(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	default:
		return "", false
	}
}

func floatFromAny(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func intFromAny(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		number, err := typed.Int64()
		return int(number), err == nil
	default:
		return 0, false
	}
}

func timeFromAny(value interface{}) time.Time {
	switch typed := value.(type) {
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err == nil {
			return parsed
		}
	case time.Time:
		return typed
	}
	return time.Time{}
}

func encodeFloat32Vector(vector []float32) ([]byte, error) {
	if vector == nil || len(vector) == 0 {
		return nil, nil
	}

	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, int32(len(vector))); err != nil {
		return nil, err
	}
	for _, value := range vector {
		if err := binary.Write(buf, binary.LittleEndian, value); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func decodeFloat32Vector(data []byte) ([]float32, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("invalid vector encoding")
	}

	reader := bytes.NewReader(data)
	var length int32
	if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, fmt.Errorf("invalid vector length")
	}
	vector := make([]float32, length)
	for i := int32(0); i < length; i++ {
		if err := binary.Read(reader, binary.LittleEndian, &vector[i]); err != nil {
			return nil, err
		}
	}
	return vector, nil
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}


func toFloat32(v []float64) []float32 {
	if len(v) == 0 {
		return nil
	}
	res := make([]float32, len(v))
	for i, f := range v {
		res[i] = float32(f)
	}
	return res
}

func toFloat64(v []float32) []float64 {
	if len(v) == 0 {
		return nil
	}
	res := make([]float64, len(v))
	for i, f := range v {
		res[i] = float64(f)
	}
	return res
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
