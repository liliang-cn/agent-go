// Command memory-mcp demonstrates the "mcp-memory" backend: any MCP server
// that exposes memory tools can be an agent's memory store, with the tool names
// and argument names supplied as configuration rather than assumed.
//
// It is self-contained. Rather than requiring a particular product to be
// installed, it starts a small MCP memory server in-process whose tool surface
// is deliberately *not* what agent-go would have guessed —
//
//	stash(ref, body, flavour, labels, extra)   instead of save(id, content, ...)
//	dig(phrase, howMany)                       instead of search(query, top_k)
//	peek(ref) / drop(ref) / everything(howMany, from)
//
// — and then bridges it with nothing but a mapping. Swapping in a real server
// (CortexDB, mem0, a homegrown one) means changing that mapping, not this code.
//
// Usage:
//
//	go run ./examples/memory-mcp
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentmcp "github.com/liliang-cn/agent-go/v3/pkg/mcp"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ------------------------------------------------------------------
	// 1. A memory server with its own vocabulary, running in this process.
	// ------------------------------------------------------------------
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	session, err := newQuirkyMemoryServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		log.Fatalf("start fake MCP memory server: %v", err)
	}
	defer func() { _ = session.Close() }()

	// ------------------------------------------------------------------
	// 2. The mapping. This is the whole integration.
	// ------------------------------------------------------------------
	//
	// In agentgo.toml, against a real server, the same thing reads:
	//
	//	[memory]
	//	store_type = "mcp-memory"
	//	dsn        = "https://memory.example.com/mcp"   # or a stdio command
	//
	//	[memory.options]
	//	"tool.store"        = "stash"
	//	"arg.store.content" = "body"
	//	...
	//
	// or, for a server someone already wrote a preset for:
	//
	//	[memory.options]
	//	profile = "cortexdb"
	options := map[string]string{
		"tool.store":  "stash",
		"tool.search": "dig",
		"tool.get":    "peek",
		"tool.delete": "drop",
		"tool.list":   "everything",

		"arg.store.id":       "ref",
		"arg.store.content":  "body",
		"arg.store.type":     "flavour",
		"arg.store.tags":     "labels",
		"arg.store.metadata": "extra",
		"arg.search.query":   "phrase",
		"arg.search.limit":   "howMany",
		"arg.get.id":         "ref",
		"arg.delete.id":      "ref",
		"arg.list.limit":     "howMany",
		"arg.list.offset":    "from",

		"result.search.items": "found",
		"result.search.hit":   "entry",
		"result.search.score": "closeness",
		"result.get.item":     "entry",
		"result.list.items":   "entries",

		"field.id":         "ref",
		"field.content":    "body",
		"field.type":       "flavour",
		"field.tags":       "labels",
		"field.created_at": "when",
		"field.metadata":   "extra",
	}

	memStore, err := store.NewMCPMemoryStore(store.MCPMemoryConfig{
		Server:        agentmcp.ServerConfig{Name: "quirky-memory"},
		ClientOptions: &agentmcp.ClientOptions{Transport: clientTransport},
		Options:       options,
		Timeout:       15 * time.Second,
	})
	if err != nil {
		log.Fatalf("build mcp-memory store: %v", err)
	}
	defer func() { _ = memStore.Close() }()

	fmt.Println("=== mcp-memory backend ===")
	fmt.Println()

	// ------------------------------------------------------------------
	// 3. The MemoryStore contract, over that server.
	// ------------------------------------------------------------------
	marker := fmt.Sprintf("zq%d", time.Now().UnixNano()%1e8)
	mem := &domain.Memory{
		Type:       domain.MemoryTypePreference,
		Content:    "Marker " + marker + ": deploys go out Tuesday 21:00 UTC, never on a Friday.",
		Tags:       []string{"ops", "deploy"},
		Importance: 0.7,
		ScopeType:  domain.MemoryScopeGlobal,
	}
	if err := memStore.Store(ctx, mem); err != nil {
		log.Fatalf("store: %v", err)
	}
	fmt.Printf("stored       : id=%s\n", mem.ID)

	got, err := memStore.Get(ctx, mem.ID)
	if err != nil {
		log.Fatalf("get: %v", err)
	}
	fmt.Printf("read back    : type=%s tags=%v\n", got.Type, got.Tags)

	hits, err := memStore.SearchByText(ctx, marker, 5)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	fmt.Printf("search       : %d hit(s), top score %.2f\n", len(hits), hits[0].Score)

	listed, total, err := memStore.List(ctx, 10, 0)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	fmt.Printf("list         : %d of %d\n", len(listed), total)

	// ------------------------------------------------------------------
	// 4. The path the agent loop actually walks every round.
	// ------------------------------------------------------------------
	//
	// Store/Search passing is not the same as the agent seeing the memory:
	// memory.Service also filters retrieved memories by scope chain. Build the
	// service with no embedder — which is how a remote brain is wired, since
	// the server owns the embedding model — and check the injected *text*.
	svc := memory.NewService(memStore, nil, nil, memory.DefaultConfig())
	defer func() { _ = svc.Close() }()

	injected, mems, err := svc.RetrieveAndInjectWithContext(ctx, marker,
		domain.MemoryQueryContext{SessionID: "example-session"})
	if err != nil {
		log.Fatalf("retrieve: %v", err)
	}
	if len(mems) == 0 || !strings.Contains(injected, marker) {
		log.Fatalf("the memory never reached the prompt: %d memories, injected=%q", len(mems), injected)
	}
	fmt.Printf("injected     : %d memory/memories reached the prompt\n", len(mems))
	fmt.Println()
	fmt.Println(strings.TrimSpace(injected))
	fmt.Println()

	// ------------------------------------------------------------------
	// 5. Honest gaps.
	// ------------------------------------------------------------------
	//
	// A vector Search cannot be served over a tool surface that takes a query
	// string, so it degrades to empty and memory.Service falls through to
	// SearchByText. Capabilities with no portable MCP equivalent say so.
	vectorHits, vectorErr := memStore.Search(ctx, []float64{0.1, 0.2}, 5, 0)
	fmt.Printf("vector search: %d hits, err=%v  (degraded on purpose)\n", len(vectorHits), vectorErr)
	fmt.Printf("Clear()      : %v\n", memStore.Clear(ctx))

	if err := memStore.Delete(ctx, mem.ID); err != nil {
		log.Fatalf("delete: %v", err)
	}
	fmt.Println("deleted      : ok")
}

// ======================================================================
// A small MCP memory server with a vocabulary of its own.
// ======================================================================

type entry struct {
	Ref     string         `json:"ref"`
	Body    string         `json:"body"`
	Flavour string         `json:"flavour"`
	Labels  []string       `json:"labels"`
	Extra   map[string]any `json:"extra"`
	When    string         `json:"when"`
}

func newQuirkyMemoryServer() *mcpsdk.Server {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "quirky-memory", Version: "1"}, nil)
	entries := map[string]*entry{}
	var order []string

	schema := func(props ...string) map[string]any {
		properties := map[string]any{}
		for _, p := range props {
			properties[p] = map[string]any{}
		}
		return map[string]any{"type": "object", "properties": properties}
	}
	args := func(req *mcpsdk.CallToolRequest) map[string]any {
		out := map[string]any{}
		_ = json.Unmarshal(req.Params.Arguments, &out)
		return out
	}
	str := func(v any) string {
		s, _ := v.(string)
		return s
	}
	reply := func(v any) (*mcpsdk.CallToolResult, error) {
		blob, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(blob)}}}, nil
	}

	srv.AddTool(&mcpsdk.Tool{Name: "stash", InputSchema: schema("ref", "body", "flavour", "labels", "extra")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			a := args(req)
			labels := []string{}
			if raw, ok := a["labels"].([]any); ok {
				for _, l := range raw {
					labels = append(labels, str(l))
				}
			}
			extra, _ := a["extra"].(map[string]any)
			e := &entry{
				Ref: str(a["ref"]), Body: str(a["body"]), Flavour: str(a["flavour"]),
				Labels: labels, Extra: extra, When: time.Now().UTC().Format(time.RFC3339),
			}
			if _, seen := entries[e.Ref]; !seen {
				order = append(order, e.Ref)
			}
			entries[e.Ref] = e
			return reply(map[string]any{"entry": e})
		})

	srv.AddTool(&mcpsdk.Tool{Name: "dig", InputSchema: schema("phrase", "howMany")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			a := args(req)
			found := []any{}
			for _, ref := range order {
				if strings.Contains(strings.ToLower(entries[ref].Body), strings.ToLower(str(a["phrase"]))) {
					found = append(found, map[string]any{"entry": entries[ref], "closeness": 0.77})
				}
			}
			return reply(map[string]any{"found": found})
		})

	srv.AddTool(&mcpsdk.Tool{Name: "peek", InputSchema: schema("ref")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			e, ok := entries[str(args(req)["ref"])]
			if !ok {
				return &mcpsdk.CallToolResult{
					IsError: true,
					Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "no such entry"}},
				}, nil
			}
			return reply(map[string]any{"entry": e})
		})

	srv.AddTool(&mcpsdk.Tool{Name: "drop", InputSchema: schema("ref")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			ref := str(args(req)["ref"])
			delete(entries, ref)
			for i, existing := range order {
				if existing == ref {
					order = append(order[:i], order[i+1:]...)
					break
				}
			}
			return reply(map[string]any{"gone": true})
		})

	srv.AddTool(&mcpsdk.Tool{Name: "everything", InputSchema: schema("howMany", "from")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			a := args(req)
			from, count := 0, len(order)
			if v, ok := a["from"].(float64); ok {
				from = int(v)
			}
			if v, ok := a["howMany"].(float64); ok {
				count = int(v)
			}
			out := []any{}
			for i := from; i < len(order) && len(out) < count; i++ {
				out = append(out, entries[order[i]])
			}
			return reply(map[string]any{"entries": out})
		})

	return srv
}
