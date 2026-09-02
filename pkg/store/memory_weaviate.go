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

// Weaviate as an agent-go memory backend.
//
// Weaviate is a vector database that also does BM25, and — the part that
// matters here — it will run with `vectorizer: "none"`, which means no
// embedding model, no API key and no second service. That is the mode this
// backend uses: memories are objects, recall is BM25.
//
// Its API is two APIs. Objects go in and out over REST (`/v1/objects`), and
// **search is GraphQL** (`/v1/graphql`) — there is no REST search endpoint.
// So this file builds a small GraphQL query by hand rather than pretending
// the whole thing is REST.
type WeaviateMemoryStore struct {
	cfg      WeaviateConfig
	client   *http.Client
	prepared bool
}

// WeaviateStoreType is the store_type name this backend registers under.
const WeaviateStoreType = "weaviate"

const (
	EnvWeaviateEndpoint = "WEAVIATE_ENDPOINT"
	EnvWeaviateAPIKey   = "WEAVIATE_API_KEY"
)

const (
	defaultWeaviateClass   = "AgentgoMemory"
	defaultWeaviateTimeout = 30 * time.Second
	weaviateListCap        = 1000
)

func init() {
	domain.MustRegisterMemoryStore(WeaviateStoreType, func(cfg domain.MemoryStoreConfig) (domain.MemoryStore, error) {
		return NewWeaviateMemoryStore(WeaviateConfigFromStoreConfig(cfg))
	})
}

// WeaviateConfig configures the Weaviate backend.
type WeaviateConfig struct {
	// Endpoint is the base URL, e.g. http://192.168.1.10:8080.
	Endpoint string
	// APIKey is sent as a bearer token when the instance requires one.
	APIKey string
	// Class is the Weaviate class holding memories. Class names must start
	// with a capital letter; one is added if the caller forgets.
	Class string
	// Timeout bounds each request.
	Timeout time.Duration
	// HTTPClient overrides the client, for tests.
	HTTPClient *http.Client
}

// WeaviateConfigFromStoreConfig maps the registry config onto this backend.
func WeaviateConfigFromStoreConfig(cfg domain.MemoryStoreConfig) WeaviateConfig {
	keyEnv := cfg.OptionOr("api_key_env", EnvWeaviateAPIKey)
	out := WeaviateConfig{
		Endpoint: firstNonBlank(cfg.DSN, cfg.Option("endpoint"), os.Getenv(EnvWeaviateEndpoint)),
		APIKey:   firstNonBlank(cfg.Option("api_key"), os.Getenv(keyEnv)),
		Class:    cfg.Option("class"),
	}
	if d, err := time.ParseDuration(cfg.OptionOr("timeout", "")); err == nil {
		out.Timeout = d
	}
	return out
}

// NewWeaviateMemoryStore returns a store over the Weaviate at cfg.Endpoint.
func NewWeaviateMemoryStore(cfg WeaviateConfig) (*WeaviateMemoryStore, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("weaviate: no endpoint (set the DSN, options[\"endpoint\"] or $%s)", EnvWeaviateEndpoint)
	}
	cfg.Endpoint = endpoint
	cfg.Class = weaviateClassName(cfg.Class)
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultWeaviateTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &WeaviateMemoryStore{cfg: cfg, client: client}, nil
}

// weaviateClassName enforces Weaviate's rule that a class starts with a
// capital letter. A lower-case name is silently a different class, which is
// the kind of thing that looks like "my writes vanished".
func weaviateClassName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return defaultWeaviateClass
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func (s *WeaviateMemoryStore) InitSchema(ctx context.Context) error { return s.prepare(ctx) }
func (s *WeaviateMemoryStore) Close() error                         { return nil }

// prepare creates the class, with no vectorizer.
func (s *WeaviateMemoryStore) prepare(ctx context.Context) error {
	if s.prepared {
		return nil
	}
	if err := s.do(ctx, http.MethodGet, "/v1/schema/"+s.cfg.Class, nil, nil); err == nil {
		s.prepared = true
		return nil
	}
	class := map[string]any{
		"class": s.cfg.Class,
		// No vectorizer: this backend does lexical recall and computes
		// nothing, so it needs no model and no key.
		"vectorizer": "none",
		"properties": []map[string]any{
			{"name": "content", "dataType": []string{"text"}},
			{"name": "memoryType", "dataType": []string{"text"}},
			{"name": "sessionId", "dataType": []string{"text"}},
			{"name": "scopeType", "dataType": []string{"text"}},
			{"name": "scopeId", "dataType": []string{"text"}},
			{"name": "importance", "dataType": []string{"number"}},
			{"name": "tags", "dataType": []string{"text[]"}},
			{"name": "keywords", "dataType": []string{"text[]"}},
			{"name": "createdAt", "dataType": []string{"text"}},
			{"name": "agentgoId", "dataType": []string{"text"}},
		},
	}
	if err := s.do(ctx, http.MethodPost, "/v1/schema", class, nil); err != nil {
		// A concurrent creator wins; that is fine.
		if !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	s.prepared = true
	return nil
}

func (s *WeaviateMemoryStore) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("weaviate: encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.Endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("weaviate: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("weaviate: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("weaviate: read response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return errWeaviateNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(payload))
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		return fmt.Errorf("weaviate: %s %s: %s: %s", method, path, resp.Status, detail)
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("weaviate: decode response: %w", err)
	}
	return nil
}

var errWeaviateNotFound = fmt.Errorf("weaviate: not found")

// weaviateID: Weaviate object ids are UUIDs. Reuse the deterministic mapping
// the Qdrant backend uses so a non-UUID caller id still addresses one object.
func weaviateID(id string) string { return qdrantPointID(id) }

func (s *WeaviateMemoryStore) propsFor(m *domain.Memory) map[string]any {
	created := m.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	return map[string]any{
		"content":    m.Content,
		"memoryType": string(m.Type),
		"sessionId":  m.SessionID,
		"scopeType":  string(m.ScopeType),
		"scopeId":    m.ScopeID,
		"importance": m.Importance,
		"tags":       m.Tags,
		"keywords":   m.Keywords,
		"createdAt":  created.Format(time.RFC3339),
		"agentgoId":  m.ID,
	}
}

func (s *WeaviateMemoryStore) toMemory(id string, props map[string]any) *domain.Memory {
	str := func(k string) string {
		v, _ := props[k].(string)
		return v
	}
	m := &domain.Memory{
		ID:        firstNonBlank(str("agentgoId"), id),
		Content:   str("content"),
		SessionID: str("sessionId"),
		ScopeID:   str("scopeId"),
		Type:      domain.MemoryTypeFact,
		ScopeType: domain.MemoryScopeGlobal,
	}
	if t := str("memoryType"); t != "" {
		m.Type = domain.MemoryType(t)
	}
	if st := str("scopeType"); st != "" {
		m.ScopeType = domain.MemoryScopeType(st)
	}
	if imp, ok := props["importance"].(float64); ok {
		m.Importance = imp
	}
	m.Tags = qdrantStringSlice(props["tags"])
	m.Keywords = qdrantStringSlice(props["keywords"])
	if ts, err := time.Parse(time.RFC3339, str("createdAt")); err == nil {
		m.CreatedAt = ts
	}
	return m
}

func (s *WeaviateMemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	if memory == nil {
		return fmt.Errorf("weaviate: nil memory")
	}
	if err := s.prepare(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(memory.ID) == "" {
		memory.ID = uuid.NewString()
	}
	body := map[string]any{
		"class":      s.cfg.Class,
		"id":         weaviateID(memory.ID),
		"properties": s.propsFor(memory),
	}
	err := s.do(ctx, http.MethodPost, "/v1/objects", body, nil)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		// Upsert semantics: the caller asked to store this memory.
		return s.do(ctx, http.MethodPut, "/v1/objects/"+s.cfg.Class+"/"+weaviateID(memory.ID), body, nil)
	}
	return err
}

func (s *WeaviateMemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if memory == nil {
		return fmt.Errorf("weaviate: nil memory")
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

// SearchByText runs BM25. Weaviate has no REST search endpoint, so this is
// GraphQL — the one place in this backend where the API is not REST.
func (s *WeaviateMemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if err := s.prepare(ctx); err != nil {
		return nil, err
	}
	if topK <= 0 {
		topK = 10
	}
	gql := fmt.Sprintf(
		`{Get{%s(bm25:{query:%s},limit:%d){content memoryType sessionId scopeType scopeId importance tags keywords createdAt agentgoId _additional{id score}}}}`,
		s.cfg.Class, strconv.Quote(query), topK)

	var out struct {
		Data struct {
			Get map[string][]map[string]any `json:"Get"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := s.do(ctx, http.MethodPost, "/v1/graphql", map[string]any{"query": gql}, &out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("weaviate: graphql: %s", out.Errors[0].Message)
	}
	rows := out.Data.Get[s.cfg.Class]
	hits := make([]*domain.MemoryWithScore, 0, len(rows))
	for _, row := range rows {
		id, score := "", 0.0
		if add, ok := row["_additional"].(map[string]any); ok {
			id, _ = add["id"].(string)
			// Weaviate returns the score as a string.
			if raw, ok := add["score"].(string); ok {
				score, _ = strconv.ParseFloat(raw, 64)
			}
		}
		hits = append(hits, &domain.MemoryWithScore{Memory: s.toMemory(id, row), Score: score})
	}
	return hits, nil
}

func (s *WeaviateMemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	if err := s.prepare(ctx); err != nil {
		return nil, err
	}
	var out struct {
		ID         string         `json:"id"`
		Properties map[string]any `json:"properties"`
	}
	err := s.do(ctx, http.MethodGet, "/v1/objects/"+s.cfg.Class+"/"+weaviateID(id), nil, &out)
	if err == errWeaviateNotFound {
		return nil, fmt.Errorf("weaviate: memory %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	return s.toMemory(out.ID, out.Properties), nil
}

func (s *WeaviateMemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	if memory == nil || strings.TrimSpace(memory.ID) == "" {
		return fmt.Errorf("weaviate: update needs a memory with an id")
	}
	if err := s.prepare(ctx); err != nil {
		return err
	}
	body := map[string]any{
		"class":      s.cfg.Class,
		"id":         weaviateID(memory.ID),
		"properties": s.propsFor(memory),
	}
	return s.do(ctx, http.MethodPut, "/v1/objects/"+s.cfg.Class+"/"+weaviateID(memory.ID), body, nil)
}

func (s *WeaviateMemoryStore) Delete(ctx context.Context, id string) error {
	err := s.do(ctx, http.MethodDelete, "/v1/objects/"+s.cfg.Class+"/"+weaviateID(id), nil, nil)
	if err == errWeaviateNotFound {
		return nil
	}
	return err
}

func (s *WeaviateMemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	if err := s.prepare(ctx); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > weaviateListCap {
		limit = weaviateListCap
	}
	var out struct {
		Objects []struct {
			ID         string         `json:"id"`
			Properties map[string]any `json:"properties"`
		} `json:"objects"`
		TotalResults int `json:"totalResults"`
	}
	path := fmt.Sprintf("/v1/objects?class=%s&limit=%d&offset=%d&include=", s.cfg.Class, limit, offset)
	if err := s.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		if err == errWeaviateNotFound {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	memories := make([]*domain.Memory, 0, len(out.Objects))
	for _, obj := range out.Objects {
		memories = append(memories, s.toMemory(obj.ID, obj.Properties))
	}
	total := out.TotalResults
	if total == 0 {
		total = len(memories)
	}
	return memories, total, nil
}

// Clear drops and recreates the class — agent-go's own class, so this is not
// somebody else's data.
func (s *WeaviateMemoryStore) Clear(ctx context.Context) error {
	err := s.do(ctx, http.MethodDelete, "/v1/schema/"+s.cfg.Class, nil, nil)
	if err != nil && err != errWeaviateNotFound {
		return err
	}
	s.prepared = false
	return s.prepare(ctx)
}

func (s *WeaviateMemoryStore) Search(context.Context, []float64, int, float64) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *WeaviateMemoryStore) SearchBySession(context.Context, string, []float64, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *WeaviateMemoryStore) SearchByScope(context.Context, []float64, []domain.MemoryScope, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

func (s *WeaviateMemoryStore) IncrementAccess(context.Context, string) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *WeaviateMemoryStore) GetByType(context.Context, domain.MemoryType, int) ([]*domain.Memory, error) {
	return nil, domain.ErrMemoryStoreUnsupported
}

func (s *WeaviateMemoryStore) DeleteBySession(context.Context, string) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *WeaviateMemoryStore) ConfigureBank(context.Context, string, *domain.MemoryBankConfig) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *WeaviateMemoryStore) Reflect(context.Context, string) (string, error) {
	return "", domain.ErrMemoryStoreUnsupported
}

func (s *WeaviateMemoryStore) AddMentalModel(context.Context, *domain.MentalModel) error {
	return domain.ErrMemoryStoreUnsupported
}
