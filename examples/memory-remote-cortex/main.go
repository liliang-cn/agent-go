// Command memory-remote-cortex is a live connectivity check for the
// "cortex-remote" memory backend: it stores one memory in a shared CortexDB
// over gRPC, searches it back, reads it by ID, proves it survives the
// RetrieveAndInject path the agent loop walks every round, then deletes it.
//
// That last step is not decoration. A store can pass Store/Search/Get and
// still be useless, because retrieval also filters by scope chain — exactly
// how this backend once returned zero memories to the loop while its store
// layer looked perfectly healthy.
//
// Nothing is hardcoded — endpoint and token come from the environment:
//
//	export CORTEXDB_REMOTE=192.168.123.252:47821
//	export CORTEXDB_GRPC_TOKEN=<your token>
//	go run ./examples/memory-remote-cortex
//
// This talks to a real server. For the unit-test view of the same backend, see
// pkg/store/memory_cortex_remote_test.go, which uses an in-process fake.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

func main() {
	endpoint := os.Getenv(store.EnvCortexRemoteEndpoint)
	if endpoint == "" {
		log.Fatalf("set %s (host:port) and %s first", store.EnvCortexRemoteEndpoint, store.EnvCortexRemoteToken)
	}

	// Token is read from $CORTEXDB_GRPC_TOKEN by the constructor. Never pass a
	// literal here and never log it.
	remote, err := store.NewCortexRemoteMemoryStore(store.CortexRemoteConfig{
		Endpoint:  endpoint,
		Namespace: "default",
		Scope:     "global",
	})
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() { _ = remote.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// A distinctive single token, so the round trip is provable against a
	// shared brain that already holds thousands of memories.
	marker := fmt.Sprintf("agentgosmoke%d", time.Now().UnixNano())
	mem := &domain.Memory{
		ID:         "agentgo-remote-store-smoke-" + marker,
		Type:       domain.MemoryTypeFact,
		Content:    "AgentGo cortex-remote smoke test " + marker + ": written by the agent-go pluggable memory backend over gRPC.",
		Importance: 0.2,
		Tags:       []string{"agentgo", "smoke"},
		ScopeType:  domain.MemoryScopeGlobal,
	}

	fmt.Printf("endpoint     : %s\n", remote.Endpoint())

	// --- store ---
	if err := remote.Store(ctx, mem); err != nil {
		log.Fatalf("store: %v", err)
	}
	fmt.Printf("stored       : id=%s\n", mem.ID)

	// --- search (server-side retrieval; the remote owns the embedder) ---
	// Retrieval strategy is the server's call (semantic when it has an
	// embedder, lexical otherwise), so we query the distinctive marker to make
	// the round trip provable against a brain that already holds thousands of
	// unrelated memories.
	query := marker
	hits, err := remote.SearchByText(ctx, query, 5)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	fmt.Printf("searched     : %q -> %d hit(s)\n", query, len(hits))
	for _, h := range hits {
		mine := ""
		if h.ID == mem.ID {
			mine = "   <-- the record we just wrote"
		}
		fmt.Printf("  score=%.4f id=%s type=%s tags=%v%s\n", h.Score, h.ID, h.Type, h.Tags, mine)
	}

	// --- read back by ID ---
	got, err := remote.Get(ctx, mem.ID)
	if err != nil {
		log.Fatalf("get: %v", err)
	}
	fmt.Printf("read back    : type=%s tags=%v importance=%.2f len=%d\n", got.Type, got.Tags, got.Importance, len(got.Content))

	// --- list (client-side page over the remote memory_list_all tool) ---
	listed, total, err := remote.List(ctx, 3, 0)
	if err != nil {
		fmt.Printf("list         : degraded (%v)\n", err)
	} else {
		fmt.Printf("listed       : %d of %d in the shared brain\n", len(listed), total)
	}

	// --- the path the agent loop actually walks every round ---
	//
	// Store/Search/Get above only prove the store layer. What the loop calls is
	// RetrieveAndInject, which additionally filters by scope chain — and that
	// filter is where a shared brain's memories can silently vanish. No
	// embedder is passed, exactly as a shared-backend agent is built: the
	// remote server owns the embedding model.
	memSvc := memory.NewService(remote, nil, nil, memory.DefaultConfig())
	injected, recalled, err := memSvc.RetrieveAndInjectWithContext(
		ctx, marker+" — what is it?", domain.MemoryQueryContext{SessionID: "smoke-session"})
	if err != nil {
		log.Fatalf("retrieve and inject: %v", err)
	}
	fmt.Printf("auto-inject   : %d memory/ies recalled for a fresh session\n", len(recalled))
	if len(recalled) == 0 || !strings.Contains(injected, marker) {
		log.Fatalf("auto-inject FAILED: the memory is invisible to the agent loop.\ninjected context: %q", injected)
	}
	fmt.Printf("injected text : %s\n", strings.TrimSpace(strings.ReplaceAll(injected, "\n", " ")))

	// --- capabilities the gRPC surface does not cover ---
	fmt.Printf("Clear()      : %v\n", remote.Clear(ctx))
	fmt.Printf("GetByType()  : ")
	if _, err := remote.GetByType(ctx, domain.MemoryTypeFact, 1); err != nil {
		fmt.Println(err)
	}

	// --- delete ---
	if err := remote.Delete(ctx, mem.ID); err != nil {
		log.Fatalf("delete: %v", err)
	}
	fmt.Printf("deleted      : id=%s\n", mem.ID)

	if _, err := remote.Get(ctx, mem.ID); err != nil {
		fmt.Printf("verified gone: %v\n", err)
	} else {
		log.Fatal("memory still readable after delete")
	}
}
