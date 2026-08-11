package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	rpcv1 "github.com/liliang-cn/cortexdb/v2/pkg/rpc/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// CortexRemoteStoreType is the store_type name this backend registers under.
// Set `store_type = "cortex-remote"` (agentgo.toml) or
// WithMemory(WithMemoryStoreType(store.CortexRemoteStoreType)) to select it.
const CortexRemoteStoreType = "cortex-remote"

// Environment fallbacks. The token is deliberately only ever read from the
// environment or from caller-supplied config — never hardcoded, never logged.
const (
	EnvCortexRemoteEndpoint = "CORTEXDB_REMOTE"
	EnvCortexRemoteToken    = "CORTEXDB_GRPC_TOKEN"
)

const (
	defaultCortexRemoteNamespace = "default"
	defaultCortexRemoteScope     = "global"
	defaultCortexRemoteTimeout   = 30 * time.Second
	// cortexRemoteListCap bounds how much of a shared brain one List() will
	// pull; the remote memory_list_all tool has no offset, so paging is done
	// client-side over a capped window.
	cortexRemoteListCap = 2000
)

// metadataKeyAgentGo is where the agent-go-specific half of a domain.Memory
// (type, tags, keywords, scope, temporal/evidence fields) rides along in the
// remote record's metadata, as a JSON blob. The remote schema is cortexdb's,
// not ours, so we do not try to grow it — we round-trip through metadata and
// leave the human-readable fields (content, importance, session, scope) native
// so other clients of the same shared brain still read them.
const metadataKeyAgentGo = "agentgo"

func init() {
	domain.MustRegisterMemoryStore(CortexRemoteStoreType, func(cfg domain.MemoryStoreConfig) (domain.MemoryStore, error) {
		return NewCortexRemoteMemoryStore(CortexRemoteConfigFromStoreConfig(cfg))
	})
}

// CortexRemoteConfig configures the shared-CortexDB gRPC memory backend.
type CortexRemoteConfig struct {
	// Endpoint is host:port of the cortexdb gRPC server. Falls back to
	// $CORTEXDB_REMOTE.
	Endpoint string
	// Token is the static bearer token the server's auth interceptor expects.
	// Falls back to $CORTEXDB_GRPC_TOKEN. An empty token is allowed (the server
	// disables auth when its own token is empty).
	Token string
	// TLS dials with system TLS credentials instead of plaintext. Default is
	// plaintext, which is what a LAN cortexdb serves.
	TLS bool
	// Namespace, Scope, UserID scope every write and search. Defaults:
	// namespace "default", scope "global", no user.
	Namespace string
	Scope     string
	UserID    string
	// Timeout bounds each RPC. Default 30s.
	Timeout time.Duration
	// Conn, when set, is used verbatim and never closed by the store. This is
	// the seam tests use to dial a bufconn server.
	Conn grpc.ClientConnInterface
}

// CortexRemoteConfigFromStoreConfig maps the generic registry config onto this
// backend. Endpoint comes from DSN, then Path, then options["endpoint"], then
// $CORTEXDB_REMOTE; the token comes from options["token"] then
// $CORTEXDB_GRPC_TOKEN (options["token_env"] renames that variable).
func CortexRemoteConfigFromStoreConfig(cfg domain.MemoryStoreConfig) CortexRemoteConfig {
	endpoint := firstNonBlank(cfg.DSN, cfg.Option("endpoint"), cfg.Path)
	tokenEnv := cfg.OptionOr("token_env", EnvCortexRemoteToken)

	out := CortexRemoteConfig{
		Endpoint:  endpoint,
		Token:     firstNonBlank(cfg.Option("token"), os.Getenv(tokenEnv)),
		Namespace: cfg.Option("namespace"),
		Scope:     cfg.Option("scope"),
		UserID:    cfg.Option("user_id"),
	}
	if v, err := strconv.ParseBool(cfg.OptionOr("tls", "false")); err == nil {
		out.TLS = v
	}
	if d, err := time.ParseDuration(cfg.OptionOr("timeout", "")); err == nil {
		out.Timeout = d
	}
	return out
}

// CortexRemoteMemoryStore is a domain.MemoryStore backed by a remote CortexDB
// over gRPC — the "shared brain" backend.
//
// What the gRPC surface actually gives us, and what it does not:
//
//	Store / StoreWithScope  -> MemoryService.SaveMemory
//	Get                     -> MemoryService.GetMemory
//	Update                  -> MemoryService.UpdateMemory
//	Delete                  -> MemoryService.DeleteMemory
//	SearchByText            -> MemoryService.SearchMemory (server-side hybrid)
//	List                    -> ToolsService.CallTool("memory_list_all")
//	InitSchema              -> no-op; the remote server owns its schema
//
//	Search / SearchBySession / SearchByScope
//	    DEGRADED. SearchMemory takes a *query string*, not a vector: the remote
//	    server owns the embedding model, so a locally-computed vector has no
//	    meaning there. These return no results (not an error) so that
//	    memory.Service falls through to its SearchByText path, which is the
//	    route that actually reaches the shared brain's semantic search.
//
//	IncrementAccess, GetByType, Clear, DeleteBySession, ConfigureBank,
//	Reflect, AddMentalModel
//	    UNSUPPORTED — return domain.ErrMemoryStoreUnsupported. The gRPC memory
//	    surface has no access counter, no type filter, and no bulk delete;
//	    Clear/DeleteBySession would in any case be the wrong thing to offer
//	    against a brain shared with other agents.
type CortexRemoteMemoryStore struct {
	cfg      CortexRemoteConfig
	conn     grpc.ClientConnInterface
	owned    *grpc.ClientConn // nil when the caller supplied Conn
	memory   rpcv1.MemoryServiceClient
	tools    rpcv1.ToolsServiceClient
	warnOnce sync.Once
}

var _ domain.MemoryStore = (*CortexRemoteMemoryStore)(nil)

// NewCortexRemoteMemoryStore dials a remote CortexDB and returns a memory store
// backed by it. Dialling is lazy (grpc.NewClient), so construction does not
// require the server to be up.
func NewCortexRemoteMemoryStore(cfg CortexRemoteConfig) (*CortexRemoteMemoryStore, error) {
	cfg.Endpoint = firstNonBlank(cfg.Endpoint, os.Getenv(EnvCortexRemoteEndpoint))
	cfg.Token = firstNonBlank(cfg.Token, os.Getenv(EnvCortexRemoteToken))
	cfg.Namespace = firstNonBlank(cfg.Namespace, defaultCortexRemoteNamespace)
	cfg.Scope = firstNonBlank(cfg.Scope, defaultCortexRemoteScope)
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultCortexRemoteTimeout
	}

	s := &CortexRemoteMemoryStore{cfg: cfg}

	if cfg.Conn != nil {
		s.conn = cfg.Conn
	} else {
		if strings.TrimSpace(cfg.Endpoint) == "" {
			return nil, fmt.Errorf("cortex-remote memory store: endpoint is required (set the memory DSN or $%s)", EnvCortexRemoteEndpoint)
		}
		creds := insecure.NewCredentials()
		if cfg.TLS {
			creds = credentials.NewClientTLSFromCert(nil, "")
		}
		conn, err := grpc.NewClient(cfg.Endpoint, grpc.WithTransportCredentials(creds))
		if err != nil {
			return nil, fmt.Errorf("cortex-remote memory store: dial %s: %w", cfg.Endpoint, err)
		}
		s.conn = conn
		s.owned = conn
	}

	s.memory = rpcv1.NewMemoryServiceClient(s.conn)
	s.tools = rpcv1.NewToolsServiceClient(s.conn)
	return s, nil
}

// Close releases the gRPC connection, if this store owns it.
func (s *CortexRemoteMemoryStore) Close() error {
	if s.owned != nil {
		return s.owned.Close()
	}
	return nil
}

// Endpoint reports the configured server address (never the token).
func (s *CortexRemoteMemoryStore) Endpoint() string { return s.cfg.Endpoint }

// call wraps a context with the bearer token and the per-call timeout.
func (s *CortexRemoteMemoryStore) call(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.cfg.Token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+s.cfg.Token)
	}
	return context.WithTimeout(ctx, s.cfg.Timeout)
}

// ============================================================
// Writes
// ============================================================

func (s *CortexRemoteMemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	if memory == nil {
		return errors.New("cortex-remote memory store: memory is nil")
	}
	callCtx, cancel := s.call(ctx)
	defer cancel()

	meta, err := s.metadataFor(memory)
	if err != nil {
		return err
	}

	resp, err := s.memory.SaveMemory(callCtx, &rpcv1.SaveMemoryRequest{
		MemoryId:   memory.ID,
		UserId:     s.cfg.UserID,
		SessionId:  memory.SessionID,
		Scope:      remoteScope(memory.ScopeType, memory.SessionID, s.cfg.Scope),
		Namespace:  s.cfg.Namespace,
		Role:       "memory",
		Content:    memory.Content,
		Metadata:   meta,
		Importance: memory.Importance,
	})
	if err != nil {
		return fmt.Errorf("cortex-remote SaveMemory: %w", err)
	}
	if rec := resp.GetMemory(); rec != nil && rec.GetId() != "" {
		memory.ID = rec.GetId()
		if ts := rec.GetCreatedAt(); ts != nil {
			memory.CreatedAt = ts.AsTime()
		}
	}
	return nil
}

func (s *CortexRemoteMemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if memory == nil {
		return errors.New("cortex-remote memory store: memory is nil")
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
	memory.CreatedAt = clone.CreatedAt
	return nil
}

func (s *CortexRemoteMemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	if memory == nil || memory.ID == "" {
		return errors.New("cortex-remote memory store: update needs a memory with an ID")
	}
	callCtx, cancel := s.call(ctx)
	defer cancel()

	meta, err := s.metadataFor(memory)
	if err != nil {
		return err
	}
	content := memory.Content
	importance := memory.Importance
	if _, err := s.memory.UpdateMemory(callCtx, &rpcv1.UpdateMemoryRequest{
		MemoryId:   memory.ID,
		Content:    &content,
		Metadata:   meta,
		Importance: &importance,
	}); err != nil {
		return fmt.Errorf("cortex-remote UpdateMemory: %w", err)
	}
	memory.UpdatedAt = time.Now()
	return nil
}

func (s *CortexRemoteMemoryStore) Delete(ctx context.Context, id string) error {
	callCtx, cancel := s.call(ctx)
	defer cancel()
	resp, err := s.memory.DeleteMemory(callCtx, &rpcv1.DeleteMemoryRequest{MemoryId: id})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ErrMemoryNotFound
		}
		return fmt.Errorf("cortex-remote DeleteMemory: %w", err)
	}
	if !resp.GetDeleted() {
		return ErrMemoryNotFound
	}
	return nil
}

// ============================================================
// Reads
// ============================================================

func (s *CortexRemoteMemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	callCtx, cancel := s.call(ctx)
	defer cancel()
	resp, err := s.memory.GetMemory(callCtx, &rpcv1.GetMemoryRequest{MemoryId: id})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrMemoryNotFound
		}
		return nil, fmt.Errorf("cortex-remote GetMemory: %w", err)
	}
	rec := resp.GetMemory()
	if rec == nil {
		return nil, ErrMemoryNotFound
	}
	return memoryFromRemoteRecord(rec), nil
}

// SearchByText runs the remote server's own memory search. This is the only
// retrieval path that really reaches the shared brain: the server owns the
// embedding model, so it decides between semantic and lexical retrieval.
func (s *CortexRemoteMemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	callCtx, cancel := s.call(ctx)
	defer cancel()

	resp, err := s.memory.SearchMemory(callCtx, &rpcv1.SearchMemoryRequest{
		Query:         query,
		UserId:        s.cfg.UserID,
		Scope:         s.cfg.Scope,
		Namespace:     s.cfg.Namespace,
		TopK:          int32(topK),
		RetrievalMode: "auto",
	})
	if err != nil {
		return nil, fmt.Errorf("cortex-remote SearchMemory: %w", err)
	}

	out := make([]*domain.MemoryWithScore, 0, len(resp.GetResults()))
	for _, hit := range resp.GetResults() {
		rec := hit.GetMemory()
		if rec == nil {
			continue
		}
		out = append(out, &domain.MemoryWithScore{Memory: memoryFromRemoteRecord(rec), Score: hit.GetScore()})
	}
	return out, nil
}

// List pages client-side over the remote memory_list_all tool, which has no
// offset parameter of its own. The window is capped at cortexRemoteListCap so
// one List() cannot try to pull an entire shared brain.
//
// The returned total is the size of the fetched window. When the remote reports
// the window as truncated, that total is a lower bound on the shared brain's
// real size — the gRPC surface exposes no count.
func (s *CortexRemoteMemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	want := offset + limit
	if want > cortexRemoteListCap {
		want = cortexRemoteListCap
	}

	callCtx, cancel := s.call(ctx)
	defer cancel()

	resp, err := s.tools.CallTool(callCtx, &rpcv1.CallToolRequest{
		Name:     "memory_list_all",
		ArgsJson: fmt.Sprintf(`{"limit":%d}`, want),
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Older servers may not expose the tool; that is a capability gap,
			// not a failure of this call.
			return nil, 0, fmt.Errorf("cortex-remote List: %w", domain.ErrMemoryStoreUnsupported)
		}
		return nil, 0, fmt.Errorf("cortex-remote memory_list_all: %w", err)
	}

	var payload struct {
		Memories []remoteMemoryJSON `json:"memories"`
	}
	if err := json.Unmarshal([]byte(resp.GetResultJson()), &payload); err != nil {
		return nil, 0, fmt.Errorf("cortex-remote memory_list_all: decode: %w", err)
	}

	total := len(payload.Memories)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	out := make([]*domain.Memory, 0, end-offset)
	for _, rec := range payload.Memories[offset:end] {
		out = append(out, rec.toDomain())
	}
	return out, total, nil
}

// ============================================================
// Degraded: vector search
// ============================================================

// Search cannot be honoured: the remote gRPC surface accepts a query string,
// not a vector, because the shared server owns the embedding model. It returns
// no results rather than an error so memory.Service degrades to SearchByText
// instead of failing the whole retrieval.
func (s *CortexRemoteMemoryStore) Search(ctx context.Context, vector []float64, topK int, minScore float64) ([]*domain.MemoryWithScore, error) {
	s.warnVectorSearch()
	return nil, nil
}

// SearchBySession has the same limitation as Search.
func (s *CortexRemoteMemoryStore) SearchBySession(ctx context.Context, sessionID string, vector []float64, topK int) ([]*domain.MemoryWithScore, error) {
	s.warnVectorSearch()
	return nil, nil
}

// SearchByScope has the same limitation as Search.
func (s *CortexRemoteMemoryStore) SearchByScope(ctx context.Context, vector []float64, scopes []domain.MemoryScope, topK int) ([]*domain.MemoryWithScore, error) {
	s.warnVectorSearch()
	return nil, nil
}

func (s *CortexRemoteMemoryStore) warnVectorSearch() {
	s.warnOnce.Do(func() {
		agentgolog.Warn("cortex-remote memory store: vector search is served by the remote server, not by a client-supplied vector; retrieval falls back to SearchByText")
	})
}

// ============================================================
// Unsupported: capabilities the remote gRPC surface does not expose
// ============================================================

// InitSchema is a no-op: the remote server owns its own schema.
func (s *CortexRemoteMemoryStore) InitSchema(ctx context.Context) error { return nil }

// IncrementAccess is unsupported: the gRPC memory surface exposes no access counter.
func (s *CortexRemoteMemoryStore) IncrementAccess(ctx context.Context, id string) error {
	return domain.ErrMemoryStoreUnsupported
}

// GetByType is unsupported: there is no server-side type filter, and scanning a
// whole shared brain per call is not an acceptable substitute.
func (s *CortexRemoteMemoryStore) GetByType(ctx context.Context, memoryType domain.MemoryType, limit int) ([]*domain.Memory, error) {
	return nil, domain.ErrMemoryStoreUnsupported
}

// Clear is unsupported: there is no bulk delete on the gRPC surface, and
// wiping a brain shared with other agents is not something this store offers.
func (s *CortexRemoteMemoryStore) Clear(ctx context.Context) error {
	return domain.ErrMemoryStoreUnsupported
}

// DeleteBySession is unsupported: no bulk delete on the gRPC surface.
func (s *CortexRemoteMemoryStore) DeleteBySession(ctx context.Context, sessionID string) error {
	return domain.ErrMemoryStoreUnsupported
}

// ConfigureBank is unsupported: memory-bank disposition is a local-store concept.
func (s *CortexRemoteMemoryStore) ConfigureBank(ctx context.Context, sessionID string, config *domain.MemoryBankConfig) error {
	return domain.ErrMemoryStoreUnsupported
}

// Reflect is unsupported: the server exposes a knowledge_memory_reflect tool,
// but it is a recall-and-synthesize call driven by its own reflector, not the
// bank consolidation this interface means.
func (s *CortexRemoteMemoryStore) Reflect(ctx context.Context, sessionID string) (string, error) {
	return "", domain.ErrMemoryStoreUnsupported
}

// AddMentalModel is unsupported: no matching remote concept.
func (s *CortexRemoteMemoryStore) AddMentalModel(ctx context.Context, model *domain.MentalModel) error {
	return domain.ErrMemoryStoreUnsupported
}

// ============================================================
// Record mapping
// ============================================================

// agentGoMemoryExtras is the half of domain.Memory that cortexdb's record has
// no native column for. It rides in metadata under metadataKeyAgentGo.
type agentGoMemoryExtras struct {
	Type       domain.MemoryType       `json:"type,omitempty"`
	ScopeType  domain.MemoryScopeType  `json:"scope_type,omitempty"`
	ScopeID    string                  `json:"scope_id,omitempty"`
	Keywords   []string                `json:"keywords,omitempty"`
	Tags       []string                `json:"tags,omitempty"`
	SourceType domain.MemorySourceType `json:"source_type,omitempty"`
	Confidence float64                 `json:"confidence,omitempty"`
	Metadata   map[string]interface{}  `json:"metadata,omitempty"`
}

func (s *CortexRemoteMemoryStore) metadataFor(m *domain.Memory) (*structpb.Struct, error) {
	extras := agentGoMemoryExtras{
		Type:       m.Type,
		ScopeType:  m.ScopeType,
		ScopeID:    m.ScopeID,
		Keywords:   m.Keywords,
		Tags:       m.Tags,
		SourceType: m.SourceType,
		Confidence: m.Confidence,
		Metadata:   m.Metadata,
	}
	blob, err := json.Marshal(extras)
	if err != nil {
		return nil, fmt.Errorf("cortex-remote: encode memory metadata: %w", err)
	}

	// Mirror the two fields other clients of the same brain are likely to read
	// alongside the round-trip blob.
	fields := map[string]interface{}{
		metadataKeyAgentGo: string(blob),
		"source":           "agent-go",
	}
	if m.Type != "" {
		fields["kind"] = string(m.Type)
	}
	if len(m.Tags) > 0 {
		fields["tags"] = strings.Join(m.Tags, ",")
	}
	meta, err := structpb.NewStruct(fields)
	if err != nil {
		return nil, fmt.Errorf("cortex-remote: encode memory metadata: %w", err)
	}
	return meta, nil
}

func memoryFromRemoteRecord(rec *rpcv1.MemoryRecord) *domain.Memory {
	m := &domain.Memory{
		ID:         rec.GetId(),
		SessionID:  rec.GetSessionId(),
		Content:    rec.GetContent(),
		Importance: rec.GetImportance(),
		Type:       domain.MemoryTypeFact,
	}
	if ts := rec.GetCreatedAt(); ts != nil {
		m.CreatedAt = ts.AsTime()
		m.UpdatedAt = ts.AsTime()
	}
	applyRemoteMetadata(m, rec.GetMetadata().AsMap(), rec.GetScope())
	return m
}

// remoteMemoryJSON is the shape memory_list_all returns.
type remoteMemoryJSON struct {
	ID        string                 `json:"id"`
	SessionID string                 `json:"session_id"`
	Scope     string                 `json:"scope"`
	Namespace string                 `json:"namespace"`
	Content   string                 `json:"content"`
	Metadata  map[string]interface{} `json:"metadata"`
	CreatedAt time.Time              `json:"created_at"`
}

func (r remoteMemoryJSON) toDomain() *domain.Memory {
	m := &domain.Memory{
		ID:        r.ID,
		SessionID: r.SessionID,
		Content:   r.Content,
		Type:      domain.MemoryTypeFact,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.CreatedAt,
	}
	applyRemoteMetadata(m, r.Metadata, r.Scope)
	return m
}

// applyRemoteMetadata restores the agent-go half of a memory. A record written
// by another client of the shared brain simply has no blob, and keeps the
// defaults.
func applyRemoteMetadata(m *domain.Memory, meta map[string]interface{}, scope string) {
	if m.ScopeType == "" && scope != "" {
		m.ScopeType = domain.MemoryScopeType(scope)
	}
	if meta == nil {
		return
	}
	if imp, ok := meta["importance"].(float64); ok && m.Importance == 0 {
		m.Importance = imp
	}
	blob, ok := meta[metadataKeyAgentGo].(string)
	if !ok || blob == "" {
		if kind, ok := meta["kind"].(string); ok && kind != "" && kind != "memory" {
			m.Type = domain.MemoryType(kind)
		}
		return
	}
	var extras agentGoMemoryExtras
	if err := json.Unmarshal([]byte(blob), &extras); err != nil {
		return
	}
	if extras.Type != "" {
		m.Type = extras.Type
	}
	if extras.ScopeType != "" {
		m.ScopeType = extras.ScopeType
	}
	m.ScopeID = extras.ScopeID
	m.Keywords = extras.Keywords
	m.Tags = extras.Tags
	m.SourceType = extras.SourceType
	m.Confidence = extras.Confidence
	m.Metadata = extras.Metadata
}

// remoteScope maps an agent-go scope onto the three the remote memory surface
// accepts (global | user | session). Scopes it has no slot for (agent, team,
// project) land in "global"; the exact agent-go scope survives in metadata.
func remoteScope(scopeType domain.MemoryScopeType, sessionID, fallback string) string {
	switch scopeType {
	case domain.MemoryScopeSession:
		return "session"
	case domain.MemoryScopeUser:
		return "user"
	case domain.MemoryScopeGlobal:
		return "global"
	case "":
		if sessionID != "" {
			return "session"
		}
		return fallback
	default:
		return "global"
	}
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
