package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	rpcv1 "github.com/liliang-cn/cortexdb/v2/pkg/rpc/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ============================================================
// In-process fake of the cortexdb gRPC surface (no real network)
// ============================================================

type fakeCortexServer struct {
	rpcv1.UnimplementedMemoryServiceServer
	rpcv1.UnimplementedToolsServiceServer

	records  map[string]*rpcv1.MemoryRecord
	order    []string
	seq      int
	seenAuth []string
	// searchCalls records the queries the store sent to SearchMemory.
	searchCalls []*rpcv1.SearchMemoryRequest
	listCalls   []string
}

func newFakeCortexServer() *fakeCortexServer {
	return &fakeCortexServer{records: map[string]*rpcv1.MemoryRecord{}}
}

func (f *fakeCortexServer) noteAuth(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.seenAuth = append(f.seenAuth, strings.Join(md.Get("authorization"), ","))
}

func (f *fakeCortexServer) SaveMemory(ctx context.Context, req *rpcv1.SaveMemoryRequest) (*rpcv1.SaveMemoryResponse, error) {
	f.noteAuth(ctx)
	id := req.GetMemoryId()
	if id == "" {
		f.seq++
		id = fmt.Sprintf("remote-%d", f.seq)
	}
	rec := &rpcv1.MemoryRecord{
		Id:         id,
		UserId:     req.GetUserId(),
		SessionId:  req.GetSessionId(),
		Scope:      req.GetScope(),
		Namespace:  req.GetNamespace(),
		Role:       req.GetRole(),
		Content:    req.GetContent(),
		Metadata:   req.GetMetadata(),
		Importance: req.GetImportance(),
		CreatedAt:  timestamppb.New(time.Unix(1700000000, 0).UTC()),
	}
	if _, exists := f.records[id]; !exists {
		f.order = append(f.order, id)
	}
	f.records[id] = rec
	return &rpcv1.SaveMemoryResponse{Memory: rec}, nil
}

func (f *fakeCortexServer) UpdateMemory(ctx context.Context, req *rpcv1.UpdateMemoryRequest) (*rpcv1.SaveMemoryResponse, error) {
	f.noteAuth(ctx)
	rec, ok := f.records[req.GetMemoryId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "memory not found")
	}
	if req.Content != nil {
		rec.Content = *req.Content
	}
	if req.Importance != nil {
		rec.Importance = *req.Importance
	}
	if req.GetMetadata() != nil {
		rec.Metadata = req.GetMetadata()
	}
	return &rpcv1.SaveMemoryResponse{Memory: rec}, nil
}

func (f *fakeCortexServer) GetMemory(ctx context.Context, req *rpcv1.GetMemoryRequest) (*rpcv1.GetMemoryResponse, error) {
	f.noteAuth(ctx)
	rec, ok := f.records[req.GetMemoryId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "memory not found")
	}
	return &rpcv1.GetMemoryResponse{Memory: rec}, nil
}

func (f *fakeCortexServer) DeleteMemory(ctx context.Context, req *rpcv1.DeleteMemoryRequest) (*rpcv1.DeleteMemoryResponse, error) {
	f.noteAuth(ctx)
	if _, ok := f.records[req.GetMemoryId()]; !ok {
		return &rpcv1.DeleteMemoryResponse{MemoryId: req.GetMemoryId(), Deleted: false}, nil
	}
	delete(f.records, req.GetMemoryId())
	return &rpcv1.DeleteMemoryResponse{MemoryId: req.GetMemoryId(), Deleted: true}, nil
}

func (f *fakeCortexServer) SearchMemory(ctx context.Context, req *rpcv1.SearchMemoryRequest) (*rpcv1.SearchMemoryResponse, error) {
	f.noteAuth(ctx)
	f.searchCalls = append(f.searchCalls, req)
	var hits []*rpcv1.MemorySearchHit
	for _, id := range f.order {
		rec, ok := f.records[id]
		if !ok {
			continue
		}
		if strings.Contains(strings.ToLower(rec.GetContent()), strings.ToLower(req.GetQuery())) {
			hits = append(hits, &rpcv1.MemorySearchHit{Memory: rec, Score: 0.75})
		}
	}
	return &rpcv1.SearchMemoryResponse{Query: req.GetQuery(), Results: hits}, nil
}

func (f *fakeCortexServer) ListTools(context.Context, *rpcv1.ListToolsRequest) (*rpcv1.ListToolsResponse, error) {
	return &rpcv1.ListToolsResponse{Tools: []*rpcv1.ToolDefinition{{Name: "memory_list_all"}}}, nil
}

func (f *fakeCortexServer) CallTool(ctx context.Context, req *rpcv1.CallToolRequest) (*rpcv1.CallToolResponse, error) {
	f.noteAuth(ctx)
	if req.GetName() != "memory_list_all" {
		return nil, status.Errorf(codes.NotFound, "unknown tool %q", req.GetName())
	}
	f.listCalls = append(f.listCalls, req.GetArgsJson())

	type outRec struct {
		ID        string                 `json:"id"`
		SessionID string                 `json:"session_id"`
		Scope     string                 `json:"scope"`
		Namespace string                 `json:"namespace"`
		Content   string                 `json:"content"`
		Metadata  map[string]interface{} `json:"metadata"`
		CreatedAt time.Time              `json:"created_at"`
	}
	out := struct {
		Memories []outRec `json:"memories"`
	}{}
	for _, id := range f.order {
		rec, ok := f.records[id]
		if !ok {
			continue
		}
		out.Memories = append(out.Memories, outRec{
			ID: rec.GetId(), SessionID: rec.GetSessionId(), Scope: rec.GetScope(),
			Namespace: rec.GetNamespace(), Content: rec.GetContent(),
			Metadata: rec.GetMetadata().AsMap(), CreatedAt: rec.GetCreatedAt().AsTime(),
		})
	}
	blob, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &rpcv1.CallToolResponse{ResultJson: string(blob)}, nil
}

// startFakeCortex spins the fake up over an in-memory bufconn listener, so the
// test never touches a real network or a real server.
func startFakeCortex(t *testing.T) (*fakeCortexServer, grpc.ClientConnInterface) {
	t.Helper()
	fake := newFakeCortexServer()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	rpcv1.RegisterMemoryServiceServer(srv, fake)
	rpcv1.RegisterToolsServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return fake, conn
}

func newTestRemoteStore(t *testing.T) (*CortexRemoteMemoryStore, *fakeCortexServer) {
	t.Helper()
	fake, conn := startFakeCortex(t)
	s, err := NewCortexRemoteMemoryStore(CortexRemoteConfig{
		Conn:      conn,
		Token:     "test-token",
		Namespace: "agentgo-test",
		Scope:     "global",
	})
	if err != nil {
		t.Fatalf("NewCortexRemoteMemoryStore() error = %v", err)
	}
	return s, fake
}

// ============================================================
// Tests
// ============================================================

func TestCortexRemoteStoreRoundTrip(t *testing.T) {
	s, fake := newTestRemoteStore(t)
	ctx := context.Background()

	mem := &domain.Memory{
		Type:       domain.MemoryTypePreference,
		Content:    "Liang prefers random high ports over 8080",
		Importance: 0.9,
		Tags:       []string{"preference", "ports"},
		Keywords:   []string{"port"},
		SourceType: domain.MemorySourceUserInput,
		Metadata:   map[string]interface{}{"origin": "unit-test"},
	}
	if err := s.Store(ctx, mem); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if mem.ID == "" {
		t.Fatal("Store() did not write the server-assigned ID back")
	}

	got, err := s.Get(ctx, mem.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Content != mem.Content {
		t.Errorf("Get() content = %q, want %q", got.Content, mem.Content)
	}
	if got.Type != domain.MemoryTypePreference {
		t.Errorf("Get() type = %q, want preference (metadata round-trip lost it)", got.Type)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "preference" {
		t.Errorf("Get() tags = %v, want [preference ports]", got.Tags)
	}
	if got.Metadata["origin"] != "unit-test" {
		t.Errorf("Get() metadata = %v, want origin=unit-test", got.Metadata)
	}
	if got.Importance != 0.9 {
		t.Errorf("Get() importance = %v, want 0.9", got.Importance)
	}

	// Every call must carry the bearer token, and never in any other form.
	for _, auth := range fake.seenAuth {
		if auth != "Bearer test-token" {
			t.Fatalf("authorization metadata = %q, want %q", auth, "Bearer test-token")
		}
	}

	hits, err := s.SearchByText(ctx, "kubernetes", 5)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("SearchByText() unexpectedly matched: %d", len(hits))
	}
	hits, err = s.SearchByText(ctx, "8080", 5)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != mem.ID {
		t.Fatalf("SearchByText() = %+v, want the stored memory", hits)
	}
	if hits[0].Score != 0.75 {
		t.Errorf("SearchByText() score = %v, want the server's 0.75", hits[0].Score)
	}
	if len(fake.searchCalls) == 0 || fake.searchCalls[0].GetNamespace() != "agentgo-test" {
		t.Errorf("SearchMemory namespace not propagated: %+v", fake.searchCalls)
	}

	mem.Content = "updated content"
	if err := s.Update(ctx, mem); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got, _ := s.Get(ctx, mem.ID); got.Content != "updated content" {
		t.Errorf("Update() did not stick: %q", got.Content)
	}

	if err := s.Delete(ctx, mem.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := s.Get(ctx, mem.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("Get() after delete error = %v, want ErrMemoryNotFound", err)
	}
	if err := s.Delete(ctx, mem.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("second Delete() error = %v, want ErrMemoryNotFound", err)
	}
}

func TestCortexRemoteStoreScopeMapping(t *testing.T) {
	s, fake := newTestRemoteStore(t)
	ctx := context.Background()

	cases := []struct {
		name  string
		scope domain.MemoryScope
		want  string
	}{
		{"session", domain.MemoryScope{Type: domain.MemoryScopeSession, ID: "sess-1"}, "session"},
		{"user", domain.MemoryScope{Type: domain.MemoryScopeUser, ID: "u-1"}, "user"},
		{"global", domain.MemoryScope{Type: domain.MemoryScopeGlobal}, "global"},
		// agent/team/project have no remote slot: they collapse to global and
		// keep their real identity in metadata.
		{"agent", domain.MemoryScope{Type: domain.MemoryScopeAgent, ID: "a-1"}, "global"},
		{"team", domain.MemoryScope{Type: domain.MemoryScopeTeam, ID: "t-1"}, "global"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem := &domain.Memory{Content: "scoped " + tc.name, Type: domain.MemoryTypeFact}
			if err := s.StoreWithScope(ctx, mem, tc.scope); err != nil {
				t.Fatalf("StoreWithScope() error = %v", err)
			}
			rec := fake.records[mem.ID]
			if rec.GetScope() != tc.want {
				t.Errorf("remote scope = %q, want %q", rec.GetScope(), tc.want)
			}
			got, err := s.Get(ctx, mem.ID)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if got.ScopeType != tc.scope.Type {
				t.Errorf("round-tripped scope type = %q, want %q", got.ScopeType, tc.scope.Type)
			}
			if got.ScopeID != tc.scope.ID {
				t.Errorf("round-tripped scope id = %q, want %q", got.ScopeID, tc.scope.ID)
			}
		})
	}
}

func TestCortexRemoteStoreList(t *testing.T) {
	s, _ := newTestRemoteStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := s.Store(ctx, &domain.Memory{Content: fmt.Sprintf("fact %d", i), Type: domain.MemoryTypeFact}); err != nil {
			t.Fatalf("Store() error = %v", err)
		}
	}

	mems, total, err := s.List(ctx, 2, 1)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if total != 5 {
		t.Errorf("List() total = %d, want 5", total)
	}
	if len(mems) != 2 {
		t.Fatalf("List() len = %d, want 2", len(mems))
	}
	if mems[0].Content != "fact 1" || mems[1].Content != "fact 2" {
		t.Errorf("List() offset ignored: %q, %q", mems[0].Content, mems[1].Content)
	}

	if _, _, err := s.List(ctx, 10, 99); err != nil {
		t.Errorf("List() past the end error = %v, want nil", err)
	}
}

// TestCortexRemoteStoreDegradesVectorSearch pins the honest degradation: the
// remote surface takes a query string, not a vector, so vector search returns
// nothing instead of pretending or erroring.
func TestCortexRemoteStoreDegradesVectorSearch(t *testing.T) {
	s, _ := newTestRemoteStore(t)
	ctx := context.Background()
	vec := []float64{0.1, 0.2, 0.3}

	if got, err := s.Search(ctx, vec, 5, 0); err != nil || got != nil {
		t.Errorf("Search() = %v, %v; want nil, nil", got, err)
	}
	if got, err := s.SearchBySession(ctx, "s", vec, 5); err != nil || got != nil {
		t.Errorf("SearchBySession() = %v, %v; want nil, nil", got, err)
	}
	if got, err := s.SearchByScope(ctx, vec, nil, 5); err != nil || got != nil {
		t.Errorf("SearchByScope() = %v, %v; want nil, nil", got, err)
	}
}

// TestCortexRemoteStoreUnsupportedSurface pins exactly which capabilities the
// gRPC surface does not cover, so a future cortexdb that grows them shows up
// as a failing test rather than staying silently unimplemented.
func TestCortexRemoteStoreUnsupportedSurface(t *testing.T) {
	s, _ := newTestRemoteStore(t)
	ctx := context.Background()

	checks := map[string]error{
		"IncrementAccess": s.IncrementAccess(ctx, "id"),
		"Clear":           s.Clear(ctx),
		"DeleteBySession": s.DeleteBySession(ctx, "s"),
		"ConfigureBank":   s.ConfigureBank(ctx, "s", &domain.MemoryBankConfig{}),
		"AddMentalModel":  s.AddMentalModel(ctx, &domain.MentalModel{}),
	}
	if _, err := s.GetByType(ctx, domain.MemoryTypeFact, 5); err != nil {
		checks["GetByType"] = err
	}
	if _, err := s.Reflect(ctx, "s"); err != nil {
		checks["Reflect"] = err
	}
	for name, err := range checks {
		if !errors.Is(err, domain.ErrMemoryStoreUnsupported) {
			t.Errorf("%s() error = %v, want ErrMemoryStoreUnsupported", name, err)
		}
	}

	// InitSchema is a no-op, not an error: the remote owns its schema.
	if err := s.InitSchema(ctx); err != nil {
		t.Errorf("InitSchema() error = %v, want nil", err)
	}
}

func TestCortexRemoteStoreRegistered(t *testing.T) {
	factory, ok := domain.LookupMemoryStore(CortexRemoteStoreType)
	if !ok {
		t.Fatalf("%q is not registered; package init() should have registered it", CortexRemoteStoreType)
	}
	// No endpoint anywhere -> a clear error, not a silent half-built store.
	t.Setenv(EnvCortexRemoteEndpoint, "")
	if _, err := factory(domain.MemoryStoreConfig{Name: CortexRemoteStoreType}); err == nil {
		t.Error("factory with no endpoint = nil error, want a required-endpoint error")
	}

	t.Setenv(EnvCortexRemoteEndpoint, "127.0.0.1:47821")
	t.Setenv(EnvCortexRemoteToken, "env-token")
	built, err := factory(domain.MemoryStoreConfig{Name: CortexRemoteStoreType})
	if err != nil {
		t.Fatalf("factory with env endpoint error = %v", err)
	}
	remote, ok := built.(*CortexRemoteMemoryStore)
	if !ok {
		t.Fatalf("factory returned %T, want *CortexRemoteMemoryStore", built)
	}
	defer func() { _ = remote.Close() }()
	if remote.Endpoint() != "127.0.0.1:47821" {
		t.Errorf("Endpoint() = %q, want the env fallback", remote.Endpoint())
	}
	if remote.cfg.Token != "env-token" {
		t.Error("token was not picked up from the environment")
	}
}

func TestCortexRemoteConfigFromStoreConfig(t *testing.T) {
	t.Setenv(EnvCortexRemoteToken, "")
	t.Setenv("MY_BRAIN_TOKEN", "from-custom-env")

	cfg := CortexRemoteConfigFromStoreConfig(domain.MemoryStoreConfig{
		Name: CortexRemoteStoreType,
		DSN:  "brain.local:47821",
		Options: map[string]string{
			"namespace": "team",
			"scope":     "user",
			"user_id":   "liang",
			"tls":       "true",
			"timeout":   "5s",
			"token_env": "MY_BRAIN_TOKEN",
		},
	})

	if cfg.Endpoint != "brain.local:47821" {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
	if cfg.Token != "from-custom-env" {
		t.Errorf("Token = %q, want the value from token_env", cfg.Token)
	}
	if cfg.Namespace != "team" || cfg.Scope != "user" || cfg.UserID != "liang" {
		t.Errorf("scoping options not mapped: %+v", cfg)
	}
	if !cfg.TLS {
		t.Error("tls option not mapped")
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", cfg.Timeout)
	}
}

// TestCortexRemoteMetadataFromForeignWriter checks that a record written by
// another client of the same shared brain (no agent-go metadata blob) still
// reads back as a usable memory.
func TestCortexRemoteMetadataFromForeignWriter(t *testing.T) {
	meta, err := structpb.NewStruct(map[string]interface{}{
		"kind":       "preference",
		"importance": 0.4,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := memoryFromRemoteRecord(&rpcv1.MemoryRecord{
		Id: "foreign-1", Content: "written by openclaw", Scope: "global", Metadata: meta,
	})
	if m.Content != "written by openclaw" {
		t.Errorf("content = %q", m.Content)
	}
	if m.Type != domain.MemoryTypePreference {
		t.Errorf("type = %q, want preference from the foreign `kind` field", m.Type)
	}
	if m.Importance != 0.4 {
		t.Errorf("importance = %v, want 0.4", m.Importance)
	}
	if m.ScopeType != domain.MemoryScopeGlobal {
		t.Errorf("scope type = %q, want global", m.ScopeType)
	}
}
