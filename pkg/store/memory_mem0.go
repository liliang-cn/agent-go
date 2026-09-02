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

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// mem0 as an agent-go memory backend.
//
// mem0 (github.com/mem0ai/mem0) is the most widely used open-source memory
// server for agents, and it is a server: a REST API over Postgres+pgvector
// that owns its own embedding model and, optionally, an LLM that extracts
// facts from what you send it. This backend speaks to it over HTTP.
//
// Two things about its model are worth stating up front, because they are
// where an integration goes wrong:
//
//  1. **mem0 rewrites what you store.** By default `POST /memories` runs the
//     text through an LLM that extracts durable facts — send "I love fresh
//     vegetable pizza" and what comes back is "Loves fresh vegetable pizza",
//     under a new id. agent-go has already decided what is worth remembering
//     by the time it calls Store (storeIfWorthwhileSync), so this backend
//     sends `infer: false` by default and keeps the text verbatim. Set
//     option `infer = "true"` to hand that judgement to mem0 instead, and
//     expect ids and wording not to match what you wrote.
//  2. **Every memory needs an owner.** mem0 requires at least one of
//     user_id / agent_id / run_id and 400s without one. agent-go's identity
//     is the session UUID, so the session becomes mem0's `run_id`, a global
//     memory becomes the configured `user_id`, and searches filter on
//     whichever applies. Getting this wrong is silent: the write succeeds,
//     the search returns nothing, and the store looks healthy.
//
// What it cannot do is said honestly rather than faked: vector Search /
// SearchBySession / SearchByScope return empty (mem0 embeds server-side and
// takes a query string, not a vector — the memory service then falls through
// to SearchByText), and the rest return ErrMemoryStoreUnsupported.

// Mem0StoreType is the store_type name this backend registers under.
const Mem0StoreType = "mem0"

// Environment fallbacks. The key is only ever read from the environment or
// from caller-supplied options — never hardcoded, never logged.
const (
	EnvMem0Endpoint = "MEM0_ENDPOINT"
	EnvMem0APIKey   = "MEM0_API_KEY"
)

const (
	defaultMem0Timeout = 30 * time.Second
	// defaultMem0UserID owns memories that have no session — mem0 refuses a
	// write with no owner at all.
	defaultMem0UserID = "agentgo"
	// defaultMem0AgentID partitions one mem0 between deployments and is the
	// key every search filters on.
	defaultMem0AgentID = "agentgo"
	// mem0ListCap bounds one List(); mem0's own ceiling is 1000.
	mem0ListCap = 1000
	// metadataKeyMem0 is where the agent-go half of a domain.Memory rides in
	// mem0's free-form metadata: type, tags, keywords, importance, scope.
	// mem0's schema is mem0's, so we round-trip through metadata rather than
	// trying to grow it, and leave content and the owner ids native so the
	// mem0 dashboard still shows something a person can read.
	metadataKeyMem0 = "agentgo"
)

func init() {
	domain.MustRegisterMemoryStore(Mem0StoreType, func(cfg domain.MemoryStoreConfig) (domain.MemoryStore, error) {
		return NewMem0MemoryStore(Mem0ConfigFromStoreConfig(cfg))
	})
}

// Mem0Config configures the mem0 backend.
type Mem0Config struct {
	// Endpoint is the server's base URL, e.g. http://192.168.1.10:43917.
	// Falls back to $MEM0_ENDPOINT.
	Endpoint string
	// APIKey is sent as X-API-Key. Falls back to $MEM0_API_KEY. Empty is
	// allowed for a server started with AUTH_DISABLED.
	APIKey string
	// UserID owns memories that carry no session. Default "agentgo".
	UserID string
	// AgentID partitions one mem0 between several agent-go deployments. It is
	// attached to every write AND is what searches filter on, which is the
	// only owner key that sees everything this deployment wrote — see the
	// note on searching below. Default "agentgo".
	AgentID string
	// Infer hands fact extraction to mem0's own LLM. Default false: agent-go
	// has already decided what to keep, and mem0 would rewrite it.
	Infer bool
	// Timeout bounds each request. Default 30s.
	Timeout time.Duration
	// HTTPClient overrides the client, for tests.
	HTTPClient *http.Client
}

// Mem0ConfigFromStoreConfig maps the generic registry config onto this
// backend. The endpoint comes from DSN, then options["endpoint"], then
// $MEM0_ENDPOINT; the key from options["api_key"] then $MEM0_API_KEY
// (options["api_key_env"] renames that variable).
func Mem0ConfigFromStoreConfig(cfg domain.MemoryStoreConfig) Mem0Config {
	keyEnv := cfg.OptionOr("api_key_env", EnvMem0APIKey)
	out := Mem0Config{
		Endpoint: firstNonBlank(cfg.DSN, cfg.Option("endpoint"), os.Getenv(EnvMem0Endpoint)),
		APIKey:   firstNonBlank(cfg.Option("api_key"), os.Getenv(keyEnv)),
		UserID:   cfg.Option("user_id"),
		AgentID:  cfg.Option("agent_id"),
	}
	if v, err := strconv.ParseBool(cfg.OptionOr("infer", "false")); err == nil {
		out.Infer = v
	}
	if d, err := time.ParseDuration(cfg.OptionOr("timeout", "")); err == nil {
		out.Timeout = d
	}
	return out
}

// Mem0MemoryStore is a domain.MemoryStore backed by a mem0 server.
type Mem0MemoryStore struct {
	cfg    Mem0Config
	client *http.Client
}

// NewMem0MemoryStore returns a store talking to the mem0 server at cfg.Endpoint.
func NewMem0MemoryStore(cfg Mem0Config) (*Mem0MemoryStore, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("mem0: no endpoint (set the DSN, options[\"endpoint\"] or $%s)", EnvMem0Endpoint)
	}
	cfg.Endpoint = endpoint
	if cfg.UserID == "" {
		cfg.UserID = defaultMem0UserID
	}
	// Never empty. mem0 filters searches by owner, and a memory written under
	// a run_id is invisible to a search filtered by user_id — so a search
	// needs one key that is on every record this deployment wrote, whatever
	// its scope. That key is the agent id.
	if cfg.AgentID == "" {
		cfg.AgentID = defaultMem0AgentID
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultMem0Timeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Mem0MemoryStore{cfg: cfg, client: client}, nil
}

// InitSchema is a no-op: the server owns its schema.
func (s *Mem0MemoryStore) InitSchema(context.Context) error { return nil }

// Close releases nothing; the HTTP client is stateless.
func (s *Mem0MemoryStore) Close() error { return nil }

// --- the wire ---------------------------------------------------------------

func (s *Mem0MemoryStore) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("mem0: encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.Endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("mem0: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.cfg.APIKey != "" {
		req.Header.Set("X-API-Key", s.cfg.APIKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("mem0: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("mem0: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body carries mem0's own explanation; keep it, truncated. It
		// never contains the key — that only ever travels in a header.
		detail := strings.TrimSpace(string(payload))
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		return fmt.Errorf("mem0: %s %s: %s: %s", method, path, resp.Status, detail)
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("mem0: decode response: %w", err)
	}
	return nil
}

// mem0Item is one memory as mem0 returns it.
type mem0Item struct {
	ID        string         `json:"id"`
	Memory    string         `json:"memory"`
	Hash      string         `json:"hash,omitempty"`
	UserID    string         `json:"user_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	RunID     string         `json:"run_id,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Score     *float64       `json:"score,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
}

type mem0Results struct {
	Results []mem0Item `json:"results"`
}

// owner decides which mem0 identifier a memory belongs to. agent-go's
// identity is the session UUID, so a session-scoped memory is a mem0 run_id
// and everything else belongs to the configured user.
func (s *Mem0MemoryStore) owner(m *domain.Memory) (userID, runID string) {
	if m != nil {
		if session := strings.TrimSpace(m.SessionID); session != "" {
			return "", session
		}
		if strings.TrimSpace(m.ScopeID) != "" && m.ScopeType == domain.MemoryScopeSession {
			return "", strings.TrimSpace(m.ScopeID)
		}
	}
	return s.cfg.UserID, ""
}

// toMemory maps a mem0 item back to a domain.Memory, restoring the agent-go
// half from the metadata blob we wrote.
//
// Scope is the trap here, and it is the same one the shared-brain backend
// had: a record that comes back without a session is global, and global is
// visible to every conversation. So the session is taken from our own blob
// or from mem0's run_id — never from user_id, which is this deployment's
// name for "no session", not a session of its own.
func (s *Mem0MemoryStore) toMemory(item mem0Item) *domain.Memory {
	m := &domain.Memory{
		ID:         item.ID,
		Content:    item.Memory,
		Type:       domain.MemoryTypeFact,
		Importance: 0.5,
	}
	if session := strings.TrimSpace(item.RunID); session != "" {
		m.SessionID = session
		m.ScopeType = domain.MemoryScopeSession
		m.ScopeID = session
	} else {
		m.ScopeType = domain.MemoryScopeGlobal
	}
	if ts, err := time.Parse(time.RFC3339, item.CreatedAt); err == nil {
		m.CreatedAt = ts
	}
	if ts, err := time.Parse(time.RFC3339, item.UpdatedAt); err == nil {
		m.UpdatedAt = ts
	}
	applyMem0Blob(m, item.Metadata)
	return m
}

// applyMem0Blob restores the agent-go fields we round-tripped through mem0's
// metadata. Anything missing keeps the default set above rather than a zero
// value that would read as a real answer.
func applyMem0Blob(m *domain.Memory, metadata map[string]any) {
	if m == nil || len(metadata) == 0 {
		return
	}
	raw, ok := metadata[metadataKeyMem0]
	if !ok || raw == nil {
		return
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return
	}
	var blob struct {
		Type       string   `json:"type,omitempty"`
		Tags       []string `json:"tags,omitempty"`
		Keywords   []string `json:"keywords,omitempty"`
		Importance *float64 `json:"importance,omitempty"`
		SessionID  string   `json:"session_id,omitempty"`
		ScopeType  string   `json:"scope_type,omitempty"`
		ScopeID    string   `json:"scope_id,omitempty"`
	}
	if err := json.Unmarshal(encoded, &blob); err != nil {
		return
	}
	if blob.Type != "" {
		m.Type = domain.MemoryType(blob.Type)
	}
	m.Tags = blob.Tags
	m.Keywords = blob.Keywords
	if blob.Importance != nil {
		m.Importance = *blob.Importance
	}
	if blob.SessionID != "" {
		m.SessionID = blob.SessionID
	}
	if blob.ScopeType != "" {
		m.ScopeType = domain.MemoryScopeType(blob.ScopeType)
	}
	if blob.ScopeID != "" {
		m.ScopeID = blob.ScopeID
	}
}

// mem0Blob is the agent-go half of a memory, as mem0 metadata.
func mem0Blob(m *domain.Memory) map[string]any {
	return map[string]any{
		metadataKeyMem0: map[string]any{
			"type":       string(m.Type),
			"tags":       m.Tags,
			"keywords":   m.Keywords,
			"importance": m.Importance,
			"session_id": m.SessionID,
			"scope_type": string(m.ScopeType),
			"scope_id":   m.ScopeID,
		},
	}
}

// --- the interface ----------------------------------------------------------

// Store writes a memory.
//
// infer=false by default: agent-go already decided this is worth keeping, and
// mem0's extraction would rewrite the text and hand back a different id.
func (s *Mem0MemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	if memory == nil {
		return fmt.Errorf("mem0: nil memory")
	}
	userID, runID := s.owner(memory)
	body := map[string]any{
		"messages": []map[string]string{{"role": "user", "content": memory.Content}},
		"metadata": mem0Blob(memory),
		"infer":    s.cfg.Infer,
	}
	if userID != "" {
		body["user_id"] = userID
	}
	if runID != "" {
		body["run_id"] = runID
	}
	// Always, on every write: it is what search filters on.
	body["agent_id"] = s.cfg.AgentID

	var out mem0Results
	if err := s.do(ctx, http.MethodPost, "/memories", body, &out); err != nil {
		return err
	}
	// mem0 mints its own id. Adopt it, or every later Get and Delete for this
	// memory addresses something that does not exist.
	for _, item := range out.Results {
		if strings.TrimSpace(item.ID) != "" {
			memory.ID = item.ID
			break
		}
	}
	return nil
}

// StoreWithScope writes a memory under an explicit scope.
func (s *Mem0MemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if memory == nil {
		return fmt.Errorf("mem0: nil memory")
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

// SearchByText asks mem0 to search. It embeds the query itself.
func (s *Mem0MemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if topK <= 0 {
		topK = 10
	}
	// Filter on the agent id, and only on it.
	//
	// This is the one thing about mem0 that is easy to get wrong and silent
	// when you do. mem0 partitions by owner, and agent-go writes a
	// session-scoped memory under run_id with no user_id — so a search
	// filtered by user_id returns nothing for every memory that belongs to a
	// conversation, which is most of them. Found by running a real agent:
	// the write succeeded, the store looked healthy, and the next turn had
	// never heard of it.
	//
	// Filtering by run_id instead would be the opposite mistake: it would
	// hide the global memories a session is entitled to see. The agent id is
	// on every record this deployment writes, whatever its scope, so it is
	// the only correct filter — and agent-go's own scope chain does the
	// per-session narrowing afterwards, on the results.
	body := map[string]any{
		"query":   query,
		"top_k":   topK,
		"filters": map[string]any{"agent_id": s.cfg.AgentID},
	}

	var out mem0Results
	if err := s.do(ctx, http.MethodPost, "/search", body, &out); err != nil {
		return nil, err
	}
	return s.scored(out.Results), nil
}

func (s *Mem0MemoryStore) scored(items []mem0Item) []*domain.MemoryWithScore {
	out := make([]*domain.MemoryWithScore, 0, len(items))
	for _, item := range items {
		score := 1.0
		if item.Score != nil {
			score = *item.Score
		}
		out = append(out, &domain.MemoryWithScore{Memory: s.toMemory(item), Score: score})
	}
	return out
}

// Get reads one memory by id.
func (s *Mem0MemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	var item mem0Item
	if err := s.do(ctx, http.MethodGet, "/memories/"+id, nil, &item); err != nil {
		return nil, err
	}
	if strings.TrimSpace(item.ID) == "" {
		return nil, fmt.Errorf("mem0: memory %s not found", id)
	}
	return s.toMemory(item), nil
}

// Update replaces a memory's text.
func (s *Mem0MemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	if memory == nil || strings.TrimSpace(memory.ID) == "" {
		return fmt.Errorf("mem0: update needs a memory with an id")
	}
	body := map[string]any{"text": memory.Content, "metadata": mem0Blob(memory)}
	return s.do(ctx, http.MethodPut, "/memories/"+memory.ID, body, nil)
}

// Delete removes one memory.
func (s *Mem0MemoryStore) Delete(ctx context.Context, id string) error {
	return s.do(ctx, http.MethodDelete, "/memories/"+id, nil, nil)
}

// List pages through what this deployment owns.
func (s *Mem0MemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	if limit <= 0 || limit > mem0ListCap {
		limit = mem0ListCap
	}
	// Same reasoning as SearchByText: the agent id is the key on every
	// record, so listing by it sees session-scoped memories too.
	path := fmt.Sprintf("/memories?agent_id=%s&top_k=%d", s.cfg.AgentID, mem0ListCap)
	var out mem0Results
	if err := s.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, 0, err
	}
	total := len(out.Results)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := out.Results[offset:end]
	memories := make([]*domain.Memory, 0, len(page))
	for _, item := range page {
		memories = append(memories, s.toMemory(item))
	}
	return memories, total, nil
}

// --- what it cannot do, said plainly ----------------------------------------

// Search degrades to empty rather than pretending. mem0 owns its embedding
// model and its search takes a query string, not a vector, so a caller
// handing us a vector is asking for something this backend cannot do — and
// the memory service falls through to SearchByText, which is the right
// answer. Returning an error here would fail runs that have a perfectly good
// text path.
func (s *Mem0MemoryStore) Search(context.Context, []float64, int, float64) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

// SearchBySession degrades for the same reason.
func (s *Mem0MemoryStore) SearchBySession(context.Context, string, []float64, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

// SearchByScope degrades for the same reason.
func (s *Mem0MemoryStore) SearchByScope(context.Context, []float64, []domain.MemoryScope, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}

// IncrementAccess has no counterpart in mem0's API.
func (s *Mem0MemoryStore) IncrementAccess(context.Context, string) error {
	return domain.ErrMemoryStoreUnsupported
}

// GetByType would need a filter mem0 does not expose.
func (s *Mem0MemoryStore) GetByType(context.Context, domain.MemoryType, int) ([]*domain.Memory, error) {
	return nil, domain.ErrMemoryStoreUnsupported
}

// Clear is deliberately not wired to mem0's DELETE /memories.
//
// That endpoint wipes everything the deployment owns, and this store may be
// sharing a mem0 with other clients. An operator who means it can call the
// server directly; a framework that offers it invites a stray Clear() to
// take somebody else's memories with it.
func (s *Mem0MemoryStore) Clear(context.Context) error {
	return domain.ErrMemoryStoreUnsupported
}

// DeleteBySession is the same hazard in miniature.
func (s *Mem0MemoryStore) DeleteBySession(context.Context, string) error {
	return domain.ErrMemoryStoreUnsupported
}

// ConfigureBank, Reflect and AddMentalModel are agent-go concepts mem0 has no
// equivalent for.
func (s *Mem0MemoryStore) ConfigureBank(context.Context, string, *domain.MemoryBankConfig) error {
	return domain.ErrMemoryStoreUnsupported
}

func (s *Mem0MemoryStore) Reflect(context.Context, string) (string, error) {
	return "", domain.ErrMemoryStoreUnsupported
}

func (s *Mem0MemoryStore) AddMentalModel(context.Context, *domain.MentalModel) error {
	return domain.ErrMemoryStoreUnsupported
}
