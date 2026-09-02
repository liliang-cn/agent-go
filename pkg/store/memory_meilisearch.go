package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Meilisearch as an agent-go memory backend.
//
// The fastest of these to stand up: one executable, no schema, no index
// creation — the first write creates everything — and relevance ranking with
// no embedding model anywhere. For a memory store that is mostly text, that
// is the whole job done by a single binary.
//
// The one thing a client must respect is that **every write is asynchronous**.
// Storing a document returns 202 and a task id; the document is not
// searchable until that task succeeds. A backend that ignores this passes its
// own tests (which search later) and fails the moment an agent stores a fact
// and asks about it in the same breath — so Store waits for its task before
// returning, which is what every caller in this framework assumes.
type MeilisearchMemoryStore struct {
	cfg      MeilisearchConfig
	client   *http.Client
	prepared bool
}

// MeilisearchStoreType is the store_type name this backend registers under.
const MeilisearchStoreType = "meilisearch"

const (
	EnvMeilisearchEndpoint = "MEILISEARCH_ENDPOINT"
	EnvMeilisearchAPIKey   = "MEILISEARCH_API_KEY"
)

const (
	defaultMeiliIndex   = "agentgo_memories"
	defaultMeiliTimeout = 30 * time.Second
	// meiliTaskWait bounds how long Store waits for its own write to be
	// indexed. Generous: the alternative is returning before the memory
	// exists, which is a race every caller would then have to know about.
	meiliTaskWait = 20 * time.Second
	meiliListCap  = 1000
)

func init() {
	domain.MustRegisterMemoryStore(MeilisearchStoreType, func(cfg domain.MemoryStoreConfig) (domain.MemoryStore, error) {
		return NewMeilisearchMemoryStore(MeilisearchConfigFromStoreConfig(cfg))
	})
}

// MeilisearchConfig configures the Meilisearch backend.
type MeilisearchConfig struct {
	// Endpoint is the base URL, e.g. http://192.168.1.10:7700.
	Endpoint string
	// APIKey is sent as a bearer token. Falls back to $MEILISEARCH_API_KEY.
	APIKey string
	// Index is the index name; created on first write.
	Index string
	// Timeout bounds each request.
	Timeout time.Duration
	// HTTPClient overrides the client, for tests.
	HTTPClient *http.Client
}

// MeilisearchConfigFromStoreConfig maps the registry config onto this backend.
func MeilisearchConfigFromStoreConfig(cfg domain.MemoryStoreConfig) MeilisearchConfig {
	keyEnv := cfg.OptionOr("api_key_env", EnvMeilisearchAPIKey)
	out := MeilisearchConfig{
		Endpoint: firstNonBlank(cfg.DSN, cfg.Option("endpoint"), os.Getenv(EnvMeilisearchEndpoint)),
		APIKey:   firstNonBlank(cfg.Option("api_key"), os.Getenv(keyEnv)),
		Index:    cfg.Option("index"),
	}
	if d, err := time.ParseDuration(cfg.OptionOr("timeout", "")); err == nil {
		out.Timeout = d
	}
	return out
}

// NewMeilisearchMemoryStore returns a store over the Meilisearch at cfg.Endpoint.
func NewMeilisearchMemoryStore(cfg MeilisearchConfig) (*MeilisearchMemoryStore, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("meilisearch: no endpoint (set the DSN, options[\"endpoint\"] or $%s)", EnvMeilisearchEndpoint)
	}
	cfg.Endpoint = endpoint
	if cfg.Index == "" {
		cfg.Index = defaultMeiliIndex
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultMeiliTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &MeilisearchMemoryStore{cfg: cfg, client: client}, nil
}

func (s *MeilisearchMemoryStore) InitSchema(ctx context.Context) error { return s.prepare(ctx) }
func (s *MeilisearchMemoryStore) Close() error                         { return nil }

// prepare declares which fields can be filtered on. Without this, filtering
// by session is rejected outright rather than silently ignored — which is the
// good kind of failure, but it has to be done once.
func (s *MeilisearchMemoryStore) prepare(ctx context.Context) error {
	if s.prepared {
		return nil
	}
	settings := map[string]any{
		"filterableAttributes": []string{"session_id", "scope_type", "scope_id", "type"},
		"sortableAttributes":   []string{"created_at"},
		"searchableAttributes": []string{"content"},
	}
	task, err := s.request(ctx, http.MethodPatch, "/indexes/"+s.cfg.Index+"/settings", settings)
	if err != nil {
		return err
	}
	if err := s.waitTask(ctx, task); err != nil {
		return err
	}
	s.prepared = true
	return nil
}

type meiliTask struct {
	TaskUID int    `json:"taskUid"`
	UID     int    `json:"uid"`
	Status  string `json:"status"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *MeilisearchMemoryStore) request(ctx context.Context, method, path string, body any) (*meiliTask, error) {
	var out meiliTask
	if err := s.do(ctx, method, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *MeilisearchMemoryStore) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("meilisearch: encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.Endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("meilisearch: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("meilisearch: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("meilisearch: read response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return errMeiliNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(payload))
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		return fmt.Errorf("meilisearch: %s %s: %s: %s", method, path, resp.Status, detail)
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("meilisearch: decode response: %w", err)
	}
	return nil
}

var errMeiliNotFound = fmt.Errorf("meilisearch: not found")

// waitTask blocks until an enqueued write has actually been indexed.
//
// This is the whole difference between a backend that works and one that
// races: Meilisearch returns 202 immediately, and a caller that stores a
// memory and searches for it in the next breath finds nothing.
func (s *MeilisearchMemoryStore) waitTask(ctx context.Context, task *meiliTask) error {
	if task == nil {
		return nil
	}
	uid := task.TaskUID
	if uid == 0 && task.UID != 0 {
		uid = task.UID
	}
	deadline := time.Now().Add(meiliTaskWait)
	for time.Now().Before(deadline) {
		var status meiliTask
		if err := s.do(ctx, http.MethodGet, fmt.Sprintf("/tasks/%d", uid), nil, &status); err != nil {
			return err
		}
		switch status.Status {
		case "succeeded":
			return nil
		case "failed", "canceled":
			msg := status.Status
			if status.Error != nil {
				msg = status.Error.Message
			}
			return fmt.Errorf("meilisearch: task %d %s: %s", uid, status.Status, msg)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("meilisearch: task %d did not finish within %s", uid, meiliTaskWait)
}

// meiliDoc is a memory as a Meilisearch document.
type meiliDoc struct {
	ID         string   `json:"id"`
	Content    string   `json:"content"`
	Type       string   `json:"type"`
	SessionID  string   `json:"session_id"`
	ScopeType  string   `json:"scope_type"`
	ScopeID    string   `json:"scope_id"`
	Importance float64  `json:"importance"`
	Tags       []string `json:"tags"`
	Keywords   []string `json:"keywords"`
	CreatedAt  int64    `json:"created_at"`
	Score      float64  `json:"_rankingScore,omitempty"`
}

func meiliDocFrom(m *domain.Memory) meiliDoc {
	created := m.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	return meiliDoc{
		ID: meiliDocID(m.ID), Content: m.Content, Type: string(m.Type),
		SessionID: m.SessionID, ScopeType: string(m.ScopeType), ScopeID: m.ScopeID,
		Importance: m.Importance, Tags: m.Tags, Keywords: m.Keywords,
		CreatedAt: created.Unix(),
	}
}

// meiliDocID sanitises an id: Meilisearch primary keys allow only
// [a-zA-Z0-9_-], which a UUID satisfies but an arbitrary caller id may not.
func meiliDocID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return uuid.NewString()
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (d meiliDoc) toMemory() *domain.Memory {
	m := &domain.Memory{
		ID: d.ID, Content: d.Content, SessionID: d.SessionID,
		ScopeID: d.ScopeID, Importance: d.Importance,
		Tags: d.Tags, Keywords: d.Keywords,
		Type: domain.MemoryTypeFact, ScopeType: domain.MemoryScopeGlobal,
	}
	if d.Type != "" {
		m.Type = domain.MemoryType(d.Type)
	}
	if d.ScopeType != "" {
		m.ScopeType = domain.MemoryScopeType(d.ScopeType)
	}
	if d.CreatedAt > 0 {
		m.CreatedAt = time.Unix(d.CreatedAt, 0)
	}
	return m
}

func (s *MeilisearchMemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	if memory == nil {
		return fmt.Errorf("meilisearch: nil memory")
	}
	if strings.TrimSpace(memory.ID) == "" {
		memory.ID = uuid.NewString()
	}
	if err := s.prepare(ctx); err != nil {
		return err
	}
	doc := meiliDocFrom(memory)
	memory.ID = doc.ID
	task, err := s.request(ctx, http.MethodPost,
		"/indexes/"+s.cfg.Index+"/documents?primaryKey=id", []meiliDoc{doc})
	if err != nil {
		return err
	}
	return s.waitTask(ctx, task)
}

func (s *MeilisearchMemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if memory == nil {
		return fmt.Errorf("meilisearch: nil memory")
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

func (s *MeilisearchMemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if topK <= 0 {
		topK = 10
	}
	body := map[string]any{"q": query, "limit": topK, "showRankingScore": true}
	var out struct {
		Hits []meiliDoc `json:"hits"`
	}
	// No scope filter here: agent-go's own scope chain narrows the results.
	if err := s.do(ctx, http.MethodPost, "/indexes/"+s.cfg.Index+"/search", body, &out); err != nil {
		if err == errMeiliNotFound {
			return nil, nil // nothing stored yet
		}
		return nil, err
	}
	hits := make([]*domain.MemoryWithScore, 0, len(out.Hits))
	for _, d := range out.Hits {
		hits = append(hits, &domain.MemoryWithScore{Memory: d.toMemory(), Score: d.Score})
	}
	return hits, nil
}

func (s *MeilisearchMemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	var doc meiliDoc
	err := s.do(ctx, http.MethodGet, "/indexes/"+s.cfg.Index+"/documents/"+meiliDocID(id), nil, &doc)
	if err == errMeiliNotFound {
		return nil, fmt.Errorf("meilisearch: memory %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	return doc.toMemory(), nil
}

func (s *MeilisearchMemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	if memory == nil || strings.TrimSpace(memory.ID) == "" {
		return fmt.Errorf("meilisearch: update needs a memory with an id")
	}
	return s.Store(ctx, memory)
}

func (s *MeilisearchMemoryStore) Delete(ctx context.Context, id string) error {
	task, err := s.request(ctx, http.MethodDelete,
		"/indexes/"+s.cfg.Index+"/documents/"+meiliDocID(id), nil)
	if err == errMeiliNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	return s.waitTask(ctx, task)
}

func (s *MeilisearchMemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	if limit <= 0 || limit > meiliListCap {
		limit = meiliListCap
	}
	body := map[string]any{"limit": limit, "offset": offset}
	var out struct {
		Results []meiliDoc `json:"results"`
		Total   int        `json:"total"`
	}
	if err := s.do(ctx, http.MethodPost, "/indexes/"+s.cfg.Index+"/documents/fetch", body, &out); err != nil {
		if err == errMeiliNotFound {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	memories := make([]*domain.Memory, 0, len(out.Results))
	for _, d := range out.Results {
		memories = append(memories, d.toMemory())
	}
	return memories, out.Total, nil
}

func (s *MeilisearchMemoryStore) Clear(ctx context.Context) error {
	task, err := s.request(ctx, http.MethodDelete, "/indexes/"+s.cfg.Index+"/documents", nil)
	if err == errMeiliNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	return s.waitTask(ctx, task)
}

func (s *MeilisearchMemoryStore) DeleteBySession(ctx context.Context, sessionID string) error {
	if err := s.prepare(ctx); err != nil {
		return err
	}
	body := map[string]any{"filter": fmt.Sprintf("session_id = %q", sessionID)}
	task, err := s.request(ctx, http.MethodPost, "/indexes/"+s.cfg.Index+"/documents/delete", body)
	if err == errMeiliNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	return s.waitTask(ctx, task)
}

// Meilisearch can do hybrid search with an embedder configured, but this
// backend does not compute vectors: it is the lexical option, and saying so
// lets the memory service fall through to SearchByText.
func (s *MeilisearchMemoryStore) Search(context.Context, []float64, int, float64) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *MeilisearchMemoryStore) SearchBySession(context.Context, string, []float64, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *MeilisearchMemoryStore) SearchByScope(context.Context, []float64, []domain.MemoryScope, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *MeilisearchMemoryStore) IncrementAccess(context.Context, string) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *MeilisearchMemoryStore) GetByType(context.Context, domain.MemoryType, int) ([]*domain.Memory, error) {
	return nil, domain.ErrMemoryStoreUnsupported
}

func (s *MeilisearchMemoryStore) ConfigureBank(context.Context, string, *domain.MemoryBankConfig) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *MeilisearchMemoryStore) Reflect(context.Context, string) (string, error) {
	return "", domain.ErrMemoryStoreUnsupported
}

func (s *MeilisearchMemoryStore) AddMentalModel(context.Context, *domain.MentalModel) error {
	return domain.ErrMemoryStoreUnsupported
}
