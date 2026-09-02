package store

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Qdrant as an agent-go memory backend.
//
// Qdrant is a vector database, not a memory product, and that turns out to be
// the point: its self-hosted build does **server-side BM25**. You hand it a
// string with `"model": "qdrant/bm25"` and it tokenises and builds the sparse
// vector itself, so this backend needs no embedding endpoint, no API key and
// no second service — which is the difference between a memory store you can
// stand up in a minute and one that needs a gateway behind it.
//
// (Verified against the plain OSS binary, because the documentation for that
// feature does not say whether it is cloud-only. It is not.)
//
// Two shapes of its API are load-bearing here:
//
//   - **Ids must be a UUID or a uint64.** Arbitrary strings are rejected.
//     agent-go's ids are already UUIDs, but a caller can supply anything, so
//     a non-UUID id is hashed into one deterministically — the same string
//     always maps to the same point, which is what makes Get and Delete work
//     afterwards.
//   - **Writes are synchronous** with `?wait=true`, which returns
//     `"completed"`. No task queue, no read-your-write race.
type QdrantMemoryStore struct {
	cfg    QdrantConfig
	client *http.Client
	ready  bool
}

// QdrantStoreType is the store_type name this backend registers under.
const QdrantStoreType = "qdrant"

// Environment fallbacks.
const (
	EnvQdrantEndpoint = "QDRANT_ENDPOINT"
	EnvQdrantAPIKey   = "QDRANT_API_KEY"
)

const (
	defaultQdrantCollection = "agentgo_memories"
	defaultQdrantTimeout    = 30 * time.Second
	// qdrantSparseVector is the named sparse vector BM25 lives under.
	qdrantSparseVector = "bm25"
	// qdrantBM25Model is the server-side inference model.
	qdrantBM25Model = "qdrant/bm25"
	qdrantListCap   = 1000
)

func init() {
	domain.MustRegisterMemoryStore(QdrantStoreType, func(cfg domain.MemoryStoreConfig) (domain.MemoryStore, error) {
		return NewQdrantMemoryStore(QdrantConfigFromStoreConfig(cfg))
	})
}

// QdrantConfig configures the Qdrant backend.
type QdrantConfig struct {
	// Endpoint is the REST base URL, e.g. http://192.168.1.10:6333.
	// Falls back to $QDRANT_ENDPOINT.
	Endpoint string
	// APIKey is sent as the api-key header. Falls back to $QDRANT_API_KEY.
	// Empty is normal for a LAN instance.
	APIKey string
	// Collection is the collection name; created on first use if absent.
	Collection string
	// Timeout bounds each request. Default 30s.
	Timeout time.Duration
	// HTTPClient overrides the client, for tests.
	HTTPClient *http.Client
}

// QdrantConfigFromStoreConfig maps the registry config onto this backend.
func QdrantConfigFromStoreConfig(cfg domain.MemoryStoreConfig) QdrantConfig {
	keyEnv := cfg.OptionOr("api_key_env", EnvQdrantAPIKey)
	out := QdrantConfig{
		Endpoint:   firstNonBlank(cfg.DSN, cfg.Option("endpoint"), os.Getenv(EnvQdrantEndpoint)),
		APIKey:     firstNonBlank(cfg.Option("api_key"), os.Getenv(keyEnv)),
		Collection: cfg.Option("collection"),
	}
	if d, err := time.ParseDuration(cfg.OptionOr("timeout", "")); err == nil {
		out.Timeout = d
	}
	return out
}

// NewQdrantMemoryStore returns a store over the Qdrant at cfg.Endpoint.
func NewQdrantMemoryStore(cfg QdrantConfig) (*QdrantMemoryStore, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("qdrant: no endpoint (set the DSN, options[\"endpoint\"] or $%s)", EnvQdrantEndpoint)
	}
	cfg.Endpoint = endpoint
	if cfg.Collection == "" {
		cfg.Collection = defaultQdrantCollection
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultQdrantTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &QdrantMemoryStore{cfg: cfg, client: client}, nil
}

// InitSchema creates the collection if it is not there.
func (s *QdrantMemoryStore) InitSchema(ctx context.Context) error { return s.ensure(ctx) }

// Close releases nothing.
func (s *QdrantMemoryStore) Close() error { return nil }

// ensure creates the collection on first use. Idempotent: an existing
// collection answers 200 and nothing is created.
func (s *QdrantMemoryStore) ensure(ctx context.Context) error {
	if s.ready {
		return nil
	}
	err := s.do(ctx, http.MethodGet, "/collections/"+s.cfg.Collection, nil, nil)
	if err == nil {
		s.ready = true
		return nil
	}
	// modifier "idf" is required for BM25 to score rather than merely match.
	body := map[string]any{
		"sparse_vectors": map[string]any{
			qdrantSparseVector: map[string]any{"modifier": "idf"},
		},
	}
	if err := s.do(ctx, http.MethodPut, "/collections/"+s.cfg.Collection, body, nil); err != nil {
		return err
	}
	// Payload indexes so scope filtering is not a full scan.
	for _, field := range []string{"session_id", "scope_type", "scope_id"} {
		_ = s.do(ctx, http.MethodPut, "/collections/"+s.cfg.Collection+"/index?wait=true",
			map[string]any{"field_name": field, "field_schema": "keyword"}, nil)
	}
	s.ready = true
	return nil
}

func (s *QdrantMemoryStore) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("qdrant: encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.Endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("qdrant: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.cfg.APIKey != "" {
		req.Header.Set("api-key", s.cfg.APIKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("qdrant: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(payload))
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		return fmt.Errorf("qdrant: %s %s: %s: %s", method, path, resp.Status, detail)
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("qdrant: decode response: %w", err)
	}
	return nil
}

// qdrantPointID turns an agent-go id into an id Qdrant accepts.
//
// Qdrant takes a UUID or a uint64 and rejects anything else. agent-go's ids
// are usually UUIDs already; anything else is hashed to a stable UUID, so the
// same string always addresses the same point and Get/Delete keep working.
func qdrantPointID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return uuid.NewString()
	}
	if _, err := uuid.Parse(id); err == nil {
		return id
	}
	sum := sha1.Sum([]byte(id))
	u, err := uuid.FromBytes(sum[:16])
	if err != nil {
		return uuid.NewString()
	}
	return u.String()
}

type qdrantPoint struct {
	ID      string         `json:"id"`
	Score   float64        `json:"score,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

func (s *QdrantMemoryStore) payloadFor(m *domain.Memory) map[string]any {
	return map[string]any{
		"content":    m.Content,
		"type":       string(m.Type),
		"session_id": m.SessionID,
		"scope_type": string(m.ScopeType),
		"scope_id":   m.ScopeID,
		"importance": m.Importance,
		"tags":       m.Tags,
		"keywords":   m.Keywords,
		"created_at": m.CreatedAt.Format(time.RFC3339),
		"agentgo_id": m.ID,
	}
}

func (s *QdrantMemoryStore) toMemory(p qdrantPoint) *domain.Memory {
	get := func(k string) string {
		v, _ := p.Payload[k].(string)
		return v
	}
	m := &domain.Memory{
		ID:        firstNonBlank(get("agentgo_id"), p.ID),
		Content:   get("content"),
		SessionID: get("session_id"),
		ScopeID:   get("scope_id"),
		Type:      domain.MemoryTypeFact,
		ScopeType: domain.MemoryScopeGlobal,
	}
	if t := get("type"); t != "" {
		m.Type = domain.MemoryType(t)
	}
	if st := get("scope_type"); st != "" {
		m.ScopeType = domain.MemoryScopeType(st)
	}
	if imp, ok := p.Payload["importance"].(float64); ok {
		m.Importance = imp
	}
	m.Tags = qdrantStringSlice(p.Payload["tags"])
	m.Keywords = qdrantStringSlice(p.Payload["keywords"])
	if ts, err := time.Parse(time.RFC3339, get("created_at")); err == nil {
		m.CreatedAt = ts
	}
	return m
}

func qdrantStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Store writes a memory, letting Qdrant build the BM25 vector from the text.
func (s *QdrantMemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	if memory == nil {
		return fmt.Errorf("qdrant: nil memory")
	}
	if err := s.ensure(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(memory.ID) == "" {
		memory.ID = uuid.NewString()
	}
	body := map[string]any{
		"points": []map[string]any{{
			"id": qdrantPointID(memory.ID),
			"vector": map[string]any{
				qdrantSparseVector: map[string]any{
					"text":  memory.Content,
					"model": qdrantBM25Model,
				},
			},
			"payload": s.payloadFor(memory),
		}},
	}
	return s.do(ctx, http.MethodPut, "/collections/"+s.cfg.Collection+"/points?wait=true", body, nil)
}

// StoreWithScope writes under an explicit scope.
func (s *QdrantMemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if memory == nil {
		return fmt.Errorf("qdrant: nil memory")
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

// SearchByText runs BM25 on the server. No embedder anywhere.
func (s *QdrantMemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	if topK <= 0 {
		topK = 10
	}
	body := map[string]any{
		"query":        map[string]any{"text": query, "model": qdrantBM25Model},
		"using":        qdrantSparseVector,
		"limit":        topK,
		"with_payload": true,
	}
	var out struct {
		Result struct {
			Points []qdrantPoint `json:"points"`
		} `json:"result"`
	}
	if err := s.do(ctx, http.MethodPost, "/collections/"+s.cfg.Collection+"/points/query", body, &out); err != nil {
		return nil, err
	}
	// Every memory this store holds is a candidate; agent-go's own scope
	// chain narrows the results afterwards. Filtering by session here would
	// hide the global memories a session is entitled to see — the same
	// mistake the mem0 backend made and a live run caught.
	hits := make([]*domain.MemoryWithScore, 0, len(out.Result.Points))
	for _, p := range out.Result.Points {
		hits = append(hits, &domain.MemoryWithScore{Memory: s.toMemory(p), Score: p.Score})
	}
	return hits, nil
}

// Get reads one memory.
func (s *QdrantMemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	var out struct {
		Result *qdrantPoint `json:"result"`
	}
	path := "/collections/" + s.cfg.Collection + "/points/" + url.PathEscape(qdrantPointID(id)) + "?with_payload=true"
	if err := s.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	if out.Result == nil {
		return nil, fmt.Errorf("qdrant: memory %s not found", id)
	}
	return s.toMemory(*out.Result), nil
}

// Update rewrites the point, which re-runs BM25 over the new text.
func (s *QdrantMemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	if memory == nil || strings.TrimSpace(memory.ID) == "" {
		return fmt.Errorf("qdrant: update needs a memory with an id")
	}
	return s.Store(ctx, memory)
}

// Delete removes one point.
func (s *QdrantMemoryStore) Delete(ctx context.Context, id string) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	body := map[string]any{"points": []string{qdrantPointID(id)}}
	return s.do(ctx, http.MethodPost, "/collections/"+s.cfg.Collection+"/points/delete?wait=true", body, nil)
}

// List scrolls the collection.
func (s *QdrantMemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, 0, err
	}
	body := map[string]any{"limit": qdrantListCap, "with_payload": true, "with_vector": false}
	var out struct {
		Result struct {
			Points []qdrantPoint `json:"points"`
		} `json:"result"`
	}
	if err := s.do(ctx, http.MethodPost, "/collections/"+s.cfg.Collection+"/points/scroll", body, &out); err != nil {
		return nil, 0, err
	}
	total := len(out.Result.Points)
	if limit <= 0 || limit > total {
		limit = total
	}
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := out.Result.Points[offset:end]
	memories := make([]*domain.Memory, 0, len(page))
	for _, p := range page {
		memories = append(memories, s.toMemory(p))
	}
	return memories, total, nil
}

// Clear drops every point in this collection — which is agent-go's own
// collection, not somebody else's data, so unlike the mem0 backend this one
// is safe to offer.
func (s *QdrantMemoryStore) Clear(ctx context.Context) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	body := map[string]any{"filter": map[string]any{}}
	return s.do(ctx, http.MethodPost, "/collections/"+s.cfg.Collection+"/points/delete?wait=true", body, nil)
}

// DeleteBySession removes one conversation's memories.
func (s *QdrantMemoryStore) DeleteBySession(ctx context.Context, sessionID string) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	body := map[string]any{"filter": map[string]any{
		"must": []map[string]any{{"key": "session_id", "match": map[string]any{"value": sessionID}}},
	}}
	return s.do(ctx, http.MethodPost, "/collections/"+s.cfg.Collection+"/points/delete?wait=true", body, nil)
}

// Search takes a dense vector, which this collection does not carry: it is
// BM25-only by design, so there is nothing to compare against. Empty rather
// than an error, so the memory service falls through to SearchByText.
func (s *QdrantMemoryStore) Search(context.Context, []float64, int, float64) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *QdrantMemoryStore) SearchBySession(context.Context, string, []float64, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *QdrantMemoryStore) SearchByScope(context.Context, []float64, []domain.MemoryScope, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *QdrantMemoryStore) IncrementAccess(context.Context, string) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *QdrantMemoryStore) GetByType(context.Context, domain.MemoryType, int) ([]*domain.Memory, error) {
	return nil, domain.ErrMemoryStoreUnsupported
}

func (s *QdrantMemoryStore) ConfigureBank(context.Context, string, *domain.MemoryBankConfig) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *QdrantMemoryStore) Reflect(context.Context, string) (string, error) {
	return "", domain.ErrMemoryStoreUnsupported
}

func (s *QdrantMemoryStore) AddMentalModel(context.Context, *domain.MentalModel) error {
	return domain.ErrMemoryStoreUnsupported
}

var _ = strconv.Itoa
var _ = hex.EncodeToString
