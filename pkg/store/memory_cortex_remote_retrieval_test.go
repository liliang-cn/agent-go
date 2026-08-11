// This is an external test package on purpose: it drives the cortex-remote
// store through memory.Service, and pkg/memory imports pkg/store, so an
// in-package test could not import it back.
package store_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	rpcv1 "github.com/liliang-cn/cortexdb/v2/pkg/rpc/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// bucketNamingServer is a cortexdb stand-in that does the one thing that broke
// retrieval: it rewrites every record's session_id to its own bucket name
// ("memory:global:<namespace>") instead of echoing what the client sent.
type bucketNamingServer struct {
	rpcv1.UnimplementedMemoryServiceServer
	rpcv1.UnimplementedToolsServiceServer

	records map[string]*rpcv1.MemoryRecord
	order   []string
}

func (b *bucketNamingServer) SaveMemory(_ context.Context, req *rpcv1.SaveMemoryRequest) (*rpcv1.SaveMemoryResponse, error) {
	scope := req.GetScope()
	if scope == "" {
		scope = "global"
	}
	bucket := "memory:" + scope + ":" + req.GetNamespace()
	if scope == "session" {
		bucket = "memory:session:" + req.GetSessionId() + ":" + req.GetNamespace()
	}
	rec := &rpcv1.MemoryRecord{
		Id: req.GetMemoryId(), SessionId: bucket, Scope: scope,
		Namespace: req.GetNamespace(), Content: req.GetContent(),
		Metadata: req.GetMetadata(), Importance: req.GetImportance(),
		CreatedAt: timestamppb.New(time.Now()),
	}
	if _, seen := b.records[rec.Id]; !seen {
		b.order = append(b.order, rec.Id)
	}
	b.records[rec.Id] = rec
	return &rpcv1.SaveMemoryResponse{Memory: rec}, nil
}

// SearchMemory matches on any query token, the way the real server's lexical
// retrieval does — a whole-query substring match would be too dumb to find
// anything for a natural-language question.
func (b *bucketNamingServer) SearchMemory(_ context.Context, req *rpcv1.SearchMemoryRequest) (*rpcv1.SearchMemoryResponse, error) {
	tokens := strings.Fields(strings.ToLower(req.GetQuery()))
	var hits []*rpcv1.MemorySearchHit
	for _, id := range b.order {
		rec := b.records[id]
		content := strings.ToLower(rec.GetContent())
		for _, token := range tokens {
			if strings.Contains(content, token) {
				hits = append(hits, &rpcv1.MemorySearchHit{Memory: rec, Score: 0.8})
				break
			}
		}
	}
	return &rpcv1.SearchMemoryResponse{Query: req.GetQuery(), Results: hits}, nil
}

func startBucketNamingServer(t *testing.T) grpc.ClientConnInterface {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	fake := &bucketNamingServer{records: map[string]*rpcv1.MemoryRecord{}}
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
	return conn
}

// TestRemoteStoreMemoriesSurviveRetrieveAndInject covers the path the agent
// loop actually walks every round, which store-level Store/Search/Get tests
// missed entirely: a memory written to a shared brain must come back out of
// RetrieveAndInject.
//
// The store layer was healthy the whole time this was broken — SearchByText
// returned the record — and the memory still never reached the model, because
// the scope filter rejected it. Test the injected text, not just the hit count.
func TestRemoteStoreMemoriesSurviveRetrieveAndInject(t *testing.T) {
	conn := startBucketNamingServer(t)
	st, err := store.NewCortexRemoteMemoryStore(store.CortexRemoteConfig{
		Conn: conn, Namespace: "default", Scope: "global",
	})
	if err != nil {
		t.Fatalf("NewCortexRemoteMemoryStore() error = %v", err)
	}

	// No embedder and no generator: exactly how SuperAI's shared mode builds it,
	// because the remote server owns the embedding model.
	svc := memory.NewService(st, nil, nil, memory.DefaultConfig())
	ctx := context.Background()

	const marker = "zzql7788"
	if err := svc.Add(ctx, &domain.Memory{
		Type:      domain.MemoryTypeFact,
		ScopeType: domain.MemoryScopeGlobal,
		Content:   "The " + marker + " protocol runs on port 43510 and is maintained by the platform team.",
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Sanity: the store layer is fine. This is what passed while retrieval
	// returned nothing.
	hits, err := st.SearchByText(ctx, marker, 10)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("store.SearchByText() found nothing; the test setup is wrong")
	}
	if hits[0].SessionID != "" {
		t.Errorf("SessionID = %q, want empty — the remote bucket name leaked back in", hits[0].SessionID)
	}

	// The path that matters: retrieval into the prompt, under a session scope
	// chain that does not contain the memory's own scope.
	injected, mems, err := svc.RetrieveAndInjectWithContext(ctx, marker+" 是什么",
		domain.MemoryQueryContext{SessionID: "probe-session"})
	if err != nil {
		t.Fatalf("RetrieveAndInjectWithContext() error = %v", err)
	}
	if len(mems) == 0 {
		t.Fatal("RetrieveAndInjectWithContext() returned no memories: a global memory in the shared brain is invisible to the loop")
	}
	if !strings.Contains(injected, marker) {
		t.Errorf("injected context = %q, want it to carry %q", injected, marker)
	}
}

// TestRemoteSessionScopedMemoryStaysInItsSession pins the other half: fixing
// global retrieval must not make one session's memories leak into another's.
func TestRemoteSessionScopedMemoryStaysInItsSession(t *testing.T) {
	conn := startBucketNamingServer(t)
	st, err := store.NewCortexRemoteMemoryStore(store.CortexRemoteConfig{
		Conn: conn, Namespace: "default", Scope: "global",
	})
	if err != nil {
		t.Fatalf("NewCortexRemoteMemoryStore() error = %v", err)
	}
	svc := memory.NewService(st, nil, nil, memory.DefaultConfig())
	ctx := context.Background()

	const marker = "qqzz4321"
	if err := st.StoreWithScope(ctx, &domain.Memory{
		Type: domain.MemoryTypeFact, Content: "The " + marker + " secret belongs to one session only.",
	}, domain.MemoryScope{Type: domain.MemoryScopeSession, ID: "session-a"}); err != nil {
		t.Fatalf("StoreWithScope() error = %v", err)
	}

	_, mems, err := svc.RetrieveAndInjectWithContext(ctx, marker+" 是什么",
		domain.MemoryQueryContext{SessionID: "session-b"})
	if err != nil {
		t.Fatalf("RetrieveAndInjectWithContext() error = %v", err)
	}
	if len(mems) != 0 {
		t.Errorf("session-a memory reached session-b: %d memories leaked", len(mems))
	}

	_, mems, err = svc.RetrieveAndInjectWithContext(ctx, marker+" 是什么",
		domain.MemoryQueryContext{SessionID: "session-a"})
	if err != nil {
		t.Fatalf("RetrieveAndInjectWithContext() error = %v", err)
	}
	if len(mems) == 0 {
		t.Error("session-a cannot see its own memory")
	}
}
