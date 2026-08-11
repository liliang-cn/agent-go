package store_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// TestMCPMemoryLive runs the whole contract against a real MCP memory server.
//
// It is skipped unless AGENTGO_MCP_MEMORY_LIVE=1. Everything about the server
// comes from the environment — nothing is hardcoded, least of all a token:
//
//	AGENTGO_MCP_MEMORY_LIVE=1        enable
//	AGENTGO_MCP_MEMORY_COMMAND=...   stdio command  (or)
//	AGENTGO_MCP_MEMORY_URL=...       http endpoint
//	AGENTGO_MCP_MEMORY_ARGS=...      optional argv
//	AGENTGO_MCP_MEMORY_PROFILE=...   mapping profile, default "cortexdb"
//	AGENTGO_MCP_MEMORY_ENV=A,B,C     env vars forwarded to a stdio server
//	                                 (e.g. CORTEXDB_REMOTE,CORTEXDB_GRPC_TOKEN)
//
// Example:
//
//	AGENTGO_MCP_MEMORY_LIVE=1 \
//	AGENTGO_MCP_MEMORY_COMMAND=$HOME/.cortexdb/bin/cortexdb-mcp-stdio \
//	AGENTGO_MCP_MEMORY_ENV=CORTEXDB_REMOTE,CORTEXDB_GRPC_TOKEN \
//	go test ./pkg/store -run TestMCPMemoryLive -v
func TestMCPMemoryLive(t *testing.T) {
	if os.Getenv("AGENTGO_MCP_MEMORY_LIVE") != "1" {
		t.Skip("set AGENTGO_MCP_MEMORY_LIVE=1 to run against a real MCP memory server")
	}

	command := os.Getenv("AGENTGO_MCP_MEMORY_COMMAND")
	url := os.Getenv("AGENTGO_MCP_MEMORY_URL")
	if command == "" && url == "" {
		t.Skip("set AGENTGO_MCP_MEMORY_COMMAND or AGENTGO_MCP_MEMORY_URL")
	}

	profile := os.Getenv("AGENTGO_MCP_MEMORY_PROFILE")
	if profile == "" {
		profile = store.MCPMemoryProfileCortexDB
	}

	options := map[string]string{
		"profile": profile,
		"command": command,
		"url":     url,
		"args":    os.Getenv("AGENTGO_MCP_MEMORY_ARGS"),
		"timeout": "60s",
	}
	// Forward named environment variables by name, so a token is passed through
	// the process environment and never appears in the repository.
	for _, name := range strings.Split(os.Getenv("AGENTGO_MCP_MEMORY_ENV"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			options["env_from."+name] = name
		}
	}

	cfg, err := store.MCPMemoryConfigFromStoreConfig(domain.MemoryStoreConfig{
		Name:    store.MCPMemoryStoreType,
		Options: options,
	})
	if err != nil {
		t.Fatalf("MCPMemoryConfigFromStoreConfig() error = %v", err)
	}
	st, err := store.NewMCPMemoryStore(cfg)
	if err != nil {
		t.Fatalf("NewMCPMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// One token, no separators. A hyphenated marker gets split by a lexical
	// tokenizer, and on a real shared brain the common pieces ("agentgo",
	// "mcp") then match thousands of unrelated records that outrank the probe.
	marker := "zq" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	mem := &domain.Memory{
		Type:       domain.MemoryTypeFact,
		ScopeType:  domain.MemoryScopeGlobal,
		Content:    "Live probe " + marker + ": the mcp-memory backend round-trips through a real MCP server.",
		Tags:       []string{"agentgo", "live-probe"},
		Importance: 0.4,
	}
	if err := st.Store(ctx, mem); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	t.Logf("stored id=%s", mem.ID)
	// Best-effort cleanup: this is somebody's real brain.
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := st.Delete(cleanupCtx, mem.ID); err != nil {
			t.Logf("cleanup Delete(%s): %v", mem.ID, err)
		}
	})

	got, err := st.Get(ctx, mem.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !strings.Contains(got.Content, marker) {
		t.Errorf("Get().Content = %q, want it to contain %q", got.Content, marker)
	}
	if got.Type != domain.MemoryTypeFact {
		t.Errorf("Get().Type = %q, want fact (the metadata blob did not round-trip)", got.Type)
	}
	if strings.Join(got.Tags, ",") != "agentgo,live-probe" {
		t.Errorf("Get().Tags = %v", got.Tags)
	}
	if got.SessionID != "" {
		t.Errorf("Get().SessionID = %q, want empty — a server-side bucket name leaked in", got.SessionID)
	}

	hits, err := st.SearchByText(ctx, marker, 10)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("SearchByText() found nothing")
	}
	t.Logf("SearchByText(%q) -> %d hits, top score %.4f", marker, len(hits), hits[0].Score)

	if listed, total, err := st.List(ctx, 3, 0); err != nil {
		t.Errorf("List() error = %v", err)
	} else {
		t.Logf("List(3,0) -> %d of %d", len(listed), total)
	}

	// The path the agent loop walks, with no embedder — the server owns that.
	//
	// MaxMemories is raised on purpose. A real shared brain holds thousands of
	// records; the default cap of 5 is a *ranking* budget, and a freshly written
	// low-importance probe losing that contest says nothing about whether the
	// backend works. What this asserts is the plumbing: the memory written
	// through the MCP tool surface comes back out of the retrieval pipeline and
	// into the injected prompt text.
	memCfg := memory.DefaultConfig()
	memCfg.MaxMemories = 25
	svc := memory.NewService(st, nil, nil, memCfg)
	t.Cleanup(func() { _ = svc.Close() })

	injected, mems, err := svc.RetrieveAndInjectWithContext(ctx, marker,
		domain.MemoryQueryContext{SessionID: "live-probe-session"})
	if err != nil {
		t.Fatalf("RetrieveAndInjectWithContext() error = %v", err)
	}
	if len(mems) == 0 {
		t.Fatal("RetrieveAndInjectWithContext() returned no memories: the loop would see nothing")
	}
	if !strings.Contains(injected, marker) {
		t.Errorf("injected context does not carry %q:\n%s", marker, injected)
	}
	t.Logf("RetrieveAndInject -> %d memories, %d chars injected", len(mems), len(injected))
}
