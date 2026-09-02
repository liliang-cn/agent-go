package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// SurrealDB as an agent-go memory backend.
//
// One binary, embedded storage, and a full-text index with BM25 scoring — so
// like the Meilisearch and Qdrant backends, this one needs no embedding model
// and no second service.
//
// Two things about its HTTP surface are worth knowing before reading the
// code. The namespace and database are chosen by **headers**
// (`surreal-ns` / `surreal-db`), not by a path, and neither is created for
// you: a request against a namespace nobody defined fails with
// "namespace does not exist" rather than creating one. And the full-text
// index keyword changed: SurrealDB 3.x spells it `FULLTEXT`, where 2.x used
// `SEARCH`, which is a parse error rather than a graceful fallback.
type SurrealMemoryStore struct {
	cfg      SurrealConfig
	client   *http.Client
	prepared bool
}

// SurrealStoreType is the store_type name this backend registers under.
const SurrealStoreType = "surrealdb"

const (
	EnvSurrealEndpoint = "SURREALDB_ENDPOINT"
	EnvSurrealUser     = "SURREALDB_USER"
	EnvSurrealPass     = "SURREALDB_PASS"
)

const (
	defaultSurrealNS      = "agentgo"
	defaultSurrealDB      = "memories"
	defaultSurrealTable   = "memory"
	defaultSurrealTimeout = 30 * time.Second
	surrealListCap        = 1000
)

func init() {
	domain.MustRegisterMemoryStore(SurrealStoreType, func(cfg domain.MemoryStoreConfig) (domain.MemoryStore, error) {
		return NewSurrealMemoryStore(SurrealConfigFromStoreConfig(cfg))
	})
}

// SurrealConfig configures the SurrealDB backend.
type SurrealConfig struct {
	// Endpoint is the base URL, e.g. http://192.168.1.10:8000.
	Endpoint string
	// User and Pass are the root credentials. Falls back to
	// $SURREALDB_USER / $SURREALDB_PASS.
	User string
	Pass string
	// Namespace, Database and Table. Defaults agentgo / memories / memory.
	Namespace string
	Database  string
	Table     string
	// Timeout bounds each request.
	Timeout time.Duration
	// HTTPClient overrides the client, for tests.
	HTTPClient *http.Client
}

// SurrealConfigFromStoreConfig maps the registry config onto this backend.
func SurrealConfigFromStoreConfig(cfg domain.MemoryStoreConfig) SurrealConfig {
	return SurrealConfig{
		Endpoint:  firstNonBlank(cfg.DSN, cfg.Option("endpoint"), os.Getenv(EnvSurrealEndpoint)),
		User:      firstNonBlank(cfg.Option("user"), os.Getenv(EnvSurrealUser)),
		Pass:      firstNonBlank(cfg.Option("pass"), os.Getenv(EnvSurrealPass)),
		Namespace: cfg.Option("namespace"),
		Database:  cfg.Option("database"),
		Table:     cfg.Option("table"),
	}
}

// NewSurrealMemoryStore returns a store over the SurrealDB at cfg.Endpoint.
func NewSurrealMemoryStore(cfg SurrealConfig) (*SurrealMemoryStore, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("surrealdb: no endpoint (set the DSN, options[\"endpoint\"] or $%s)", EnvSurrealEndpoint)
	}
	cfg.Endpoint = endpoint
	if cfg.Namespace == "" {
		cfg.Namespace = defaultSurrealNS
	}
	if cfg.Database == "" {
		cfg.Database = defaultSurrealDB
	}
	if cfg.Table == "" {
		cfg.Table = defaultSurrealTable
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultSurrealTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &SurrealMemoryStore{cfg: cfg, client: client}, nil
}

func (s *SurrealMemoryStore) InitSchema(ctx context.Context) error { return s.prepare(ctx) }
func (s *SurrealMemoryStore) Close() error                         { return nil }

// surrealResult is one statement's result in SurrealDB's response array.
type surrealResult struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Detail json.RawMessage `json:"details"`
}

// sql runs one or more statements. withScope=false omits the namespace
// headers, which is required for the statements that CREATE the namespace.
func (s *SurrealMemoryStore) sql(ctx context.Context, statement string, withScope bool) ([]surrealResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint+"/sql", bytes.NewReader([]byte(statement)))
	if err != nil {
		return nil, fmt.Errorf("surrealdb: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "text/plain")
	if s.cfg.User != "" {
		req.SetBasicAuth(s.cfg.User, s.cfg.Pass)
	}
	if withScope {
		// Headers, not a path: this is how SurrealDB selects a namespace.
		req.Header.Set("surreal-ns", s.cfg.Namespace)
		req.Header.Set("surreal-db", s.cfg.Database)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("surrealdb: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("surrealdb: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(payload))
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		return nil, fmt.Errorf("surrealdb: %s: %s", resp.Status, detail)
	}
	var results []surrealResult
	if err := json.Unmarshal(payload, &results); err != nil {
		return nil, fmt.Errorf("surrealdb: decode response: %w", err)
	}
	for _, r := range results {
		if r.Status != "OK" {
			return results, fmt.Errorf("surrealdb: statement failed: %s", strings.TrimSpace(string(r.Result)))
		}
	}
	return results, nil
}

// prepare defines the namespace, the database and the full-text index.
func (s *SurrealMemoryStore) prepare(ctx context.Context) error {
	if s.prepared {
		return nil
	}
	// The namespace has to exist before it can be selected, so this one runs
	// without the scope headers.
	bootstrap := fmt.Sprintf("DEFINE NAMESPACE IF NOT EXISTS %s; USE NS %s; DEFINE DATABASE IF NOT EXISTS %s;",
		s.cfg.Namespace, s.cfg.Namespace, s.cfg.Database)
	if _, err := s.sql(ctx, bootstrap, false); err != nil {
		return err
	}
	// FULLTEXT, not SEARCH: the keyword changed in SurrealDB 3.x and the old
	// spelling is a parse error, not a fallback.
	schema := fmt.Sprintf(
		"DEFINE ANALYZER IF NOT EXISTS agentgo_simple TOKENIZERS blank,class FILTERS lowercase;"+
			"DEFINE INDEX IF NOT EXISTS %s_content ON %s FIELDS content FULLTEXT ANALYZER agentgo_simple BM25;",
		s.cfg.Table, s.cfg.Table)
	if _, err := s.sql(ctx, schema, true); err != nil {
		return err
	}
	s.prepared = true
	return nil
}

// surrealRecord is a memory row.
type surrealRecord struct {
	ID         string   `json:"id"`
	AgentgoID  string   `json:"agentgo_id"`
	Content    string   `json:"content"`
	Type       string   `json:"memory_type"`
	SessionID  string   `json:"session_id"`
	ScopeType  string   `json:"scope_type"`
	ScopeID    string   `json:"scope_id"`
	Importance float64  `json:"importance"`
	Tags       []string `json:"tags"`
	Keywords   []string `json:"keywords"`
	CreatedAt  string   `json:"created_at"`
	Score      float64  `json:"score"`
}

func (r surrealRecord) toMemory() *domain.Memory {
	m := &domain.Memory{
		ID:         firstNonBlank(r.AgentgoID, r.ID),
		Content:    r.Content,
		SessionID:  r.SessionID,
		ScopeID:    r.ScopeID,
		Importance: r.Importance,
		Tags:       r.Tags,
		Keywords:   r.Keywords,
		Type:       domain.MemoryTypeFact,
		ScopeType:  domain.MemoryScopeGlobal,
	}
	if r.Type != "" {
		m.Type = domain.MemoryType(r.Type)
	}
	if r.ScopeType != "" {
		m.ScopeType = domain.MemoryScopeType(r.ScopeType)
	}
	if ts, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
		m.CreatedAt = ts
	}
	return m
}

// surrealRecordID makes a record id SurrealDB accepts: a UUID's hyphens are
// not valid bare, so ids are wrapped in backticks.
func surrealRecordID(table, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		id = uuid.NewString()
	}
	return table + ":`" + strings.ReplaceAll(id, "`", "") + "`"
}

func (s *SurrealMemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	if memory == nil {
		return fmt.Errorf("surrealdb: nil memory")
	}
	if err := s.prepare(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(memory.ID) == "" {
		memory.ID = uuid.NewString()
	}
	created := memory.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	tags, _ := json.Marshal(memory.Tags)
	keywords, _ := json.Marshal(memory.Keywords)
	// UPSERT so a repeated Store replaces rather than erroring.
	stmt := fmt.Sprintf(
		"UPSERT %s SET agentgo_id = %s, content = %s, memory_type = %s, session_id = %s, "+
			"scope_type = %s, scope_id = %s, importance = %s, tags = %s, keywords = %s, created_at = %s;",
		surrealRecordID(s.cfg.Table, memory.ID),
		strconv.Quote(memory.ID), strconv.Quote(memory.Content), strconv.Quote(string(memory.Type)),
		strconv.Quote(memory.SessionID), strconv.Quote(string(memory.ScopeType)), strconv.Quote(memory.ScopeID),
		strconv.FormatFloat(memory.Importance, 'f', -1, 64), string(tags), string(keywords),
		strconv.Quote(created.Format(time.RFC3339)))
	_, err := s.sql(ctx, stmt, true)
	return err
}

func (s *SurrealMemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if memory == nil {
		return fmt.Errorf("surrealdb: nil memory")
	}
	clone := *memory
	clone.ScopeType = scope.Type
	clone.ScopeID = scope.ID
	if scope.Type == domain.MemoryScopeSession && scope.ID != "" {
		clone.SessionID = scope.ID
	}
	if err := s.Store(ctx, &clone); err != nil {
		return err
	}
	memory.ID = clone.ID
	return nil
}

func (s *SurrealMemoryStore) rows(res []surrealResult) ([]surrealRecord, error) {
	if len(res) == 0 {
		return nil, nil
	}
	var records []surrealRecord
	if err := json.Unmarshal(res[len(res)-1].Result, &records); err != nil {
		return nil, fmt.Errorf("surrealdb: decode rows: %w", err)
	}
	return records, nil
}

func (s *SurrealMemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if err := s.prepare(ctx); err != nil {
		return nil, err
	}
	if topK <= 0 {
		topK = 10
	}
	stmt := fmt.Sprintf(
		"SELECT *, search::score(1) AS score FROM %s WHERE content @1@ %s ORDER BY score DESC LIMIT %d;",
		s.cfg.Table, strconv.Quote(query), topK)
	res, err := s.sql(ctx, stmt, true)
	if err != nil {
		return nil, err
	}
	records, err := s.rows(res)
	if err != nil {
		return nil, err
	}
	hits := make([]*domain.MemoryWithScore, 0, len(records))
	for _, r := range records {
		score := r.Score
		if score == 0 {
			// A matched row with a zero BM25 score is still a match; giving
			// it zero would put it below the service's relevance floor and
			// make the whole backend look empty.
			score = 0.5
		}
		hits = append(hits, &domain.MemoryWithScore{Memory: r.toMemory(), Score: score})
	}
	return hits, nil
}

func (s *SurrealMemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	if err := s.prepare(ctx); err != nil {
		return nil, err
	}
	stmt := fmt.Sprintf("SELECT * FROM %s;", surrealRecordID(s.cfg.Table, id))
	res, err := s.sql(ctx, stmt, true)
	if err != nil {
		return nil, err
	}
	records, err := s.rows(res)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("surrealdb: memory %s not found", id)
	}
	return records[0].toMemory(), nil
}

func (s *SurrealMemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	if memory == nil || strings.TrimSpace(memory.ID) == "" {
		return fmt.Errorf("surrealdb: update needs a memory with an id")
	}
	return s.Store(ctx, memory)
}

func (s *SurrealMemoryStore) Delete(ctx context.Context, id string) error {
	if err := s.prepare(ctx); err != nil {
		return err
	}
	_, err := s.sql(ctx, fmt.Sprintf("DELETE %s;", surrealRecordID(s.cfg.Table, id)), true)
	return err
}

func (s *SurrealMemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	if err := s.prepare(ctx); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > surrealListCap {
		limit = surrealListCap
	}
	stmt := fmt.Sprintf("SELECT * FROM %s LIMIT %d START %d;", s.cfg.Table, limit, offset)
	res, err := s.sql(ctx, stmt, true)
	if err != nil {
		return nil, 0, err
	}
	records, err := s.rows(res)
	if err != nil {
		return nil, 0, err
	}
	memories := make([]*domain.Memory, 0, len(records))
	for _, r := range records {
		memories = append(memories, r.toMemory())
	}
	return memories, len(memories) + offset, nil
}

func (s *SurrealMemoryStore) Clear(ctx context.Context) error {
	if err := s.prepare(ctx); err != nil {
		return err
	}
	_, err := s.sql(ctx, fmt.Sprintf("DELETE %s;", s.cfg.Table), true)
	return err
}

func (s *SurrealMemoryStore) DeleteBySession(ctx context.Context, sessionID string) error {
	if err := s.prepare(ctx); err != nil {
		return err
	}
	stmt := fmt.Sprintf("DELETE %s WHERE session_id = %s;", s.cfg.Table, strconv.Quote(sessionID))
	_, err := s.sql(ctx, stmt, true)
	return err
}

func (s *SurrealMemoryStore) Search(context.Context, []float64, int, float64) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *SurrealMemoryStore) SearchBySession(context.Context, string, []float64, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *SurrealMemoryStore) SearchByScope(context.Context, []float64, []domain.MemoryScope, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *SurrealMemoryStore) IncrementAccess(context.Context, string) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *SurrealMemoryStore) GetByType(context.Context, domain.MemoryType, int) ([]*domain.Memory, error) {
	return nil, domain.ErrMemoryStoreUnsupported
}

func (s *SurrealMemoryStore) ConfigureBank(context.Context, string, *domain.MemoryBankConfig) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *SurrealMemoryStore) Reflect(context.Context, string) (string, error) {
	return "", domain.ErrMemoryStoreUnsupported
}

func (s *SurrealMemoryStore) AddMentalModel(context.Context, *domain.MentalModel) error {
	return domain.ErrMemoryStoreUnsupported
}
