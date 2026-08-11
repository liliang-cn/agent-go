// External test package on purpose: these tests drive the mcp-memory store
// through memory.Service, and pkg/memory imports pkg/store, so an in-package
// test could not import it back.
package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentmcp "github.com/liliang-cn/agent-go/v3/pkg/mcp"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ============================================================
// Two deliberately incompatible fake MCP memory servers
// ============================================================
//
// The point of this backend is that it is not written for one product. These
// two servers agree on nothing that a client could guess at:
//
//	                   server "alpha"                server "beta"
//	save tool          memory_save                   remember
//	content argument   content                       text
//	type argument      type                          kind
//	id on save         assigned by server            supplied by client ("key")
//	save result        {"memory":{"id":...}}         a bare JSON string id
//	search tool        memory_search                 recall
//	query argument     query                         q
//	limit argument     top_k                         n
//	search result      {"results":[{"memory":{},     a bare array of records,
//	                     "score":0.9}]}                score inline as "relevance"
//	get tool           memory_fetch(id)              lookup(key)
//	get result         {"memory":{...}}              the record at the root
//	list tool          memory_dump(limit)            dump(count, skip)
//	list result        {"memories":[...]}            {"data":[...]}  + real offset
//	record fields      id/content/type/tags/meta     key/text/kind/labels/extra
//	created field      created_at (RFC3339)          ts (unix seconds)
//
// Everything above is bridged by configuration alone.

type fakeMemoryRecord struct {
	id        string
	content   string
	memType   string
	tags      []string
	metadata  map[string]any
	createdAt time.Time
}

type fakeMemoryBackend struct {
	mu      sync.Mutex
	records map[string]*fakeMemoryRecord
	order   []string
	seq     int
	// calls records every (tool, argument-name-set) pair, so a test can assert
	// that nothing was sent that the server never declared.
	calls []fakeCall
}

type fakeCall struct {
	tool string
	args map[string]any
}

func newFakeMemoryBackend() *fakeMemoryBackend {
	return &fakeMemoryBackend{records: map[string]*fakeMemoryRecord{}}
}

func (b *fakeMemoryBackend) record(tool string, args map[string]any) {
	b.calls = append(b.calls, fakeCall{tool: tool, args: args})
}

func (b *fakeMemoryBackend) argNames(tool string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var names []string
	for _, c := range b.calls {
		if c.tool != tool {
			continue
		}
		for k := range c.args {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}

func (b *fakeMemoryBackend) put(rec *fakeMemoryRecord) {
	if _, seen := b.records[rec.id]; !seen {
		b.order = append(b.order, rec.id)
	}
	b.records[rec.id] = rec
}

// match is a deliberately simple token search, the way a lexical backend works.
func (b *fakeMemoryBackend) match(query string, limit int) []*fakeMemoryRecord {
	tokens := strings.Fields(strings.ToLower(query))
	var hits []*fakeMemoryRecord
	for _, id := range b.order {
		rec := b.records[id]
		content := strings.ToLower(rec.content)
		for _, tok := range tokens {
			if strings.Contains(content, tok) {
				hits = append(hits, rec)
				break
			}
		}
		if limit > 0 && len(hits) >= limit {
			break
		}
	}
	return hits
}

// objectSchema is the minimal input schema the SDK insists every tool declares.
func objectSchema(props ...string) map[string]any {
	properties := map[string]any{}
	for _, p := range props {
		properties[p] = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": properties}
}

func decodeArgs(t *testing.T, req *mcpsdk.CallToolRequest) map[string]any {
	t.Helper()
	args := map[string]any{}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			t.Fatalf("decode tool arguments: %v", err)
		}
	}
	return args
}

func jsonResult(t *testing.T, v any) *mcpsdk.CallToolResult {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode tool result: %v", err)
	}
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(blob)}}}
}

func errResult(text string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
	}
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toMetadata(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// ---------- server "alpha" ----------

func newAlphaServer(t *testing.T, b *fakeMemoryBackend) *mcpsdk.Server {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "alpha", Version: "1"}, nil)

	srv.AddTool(&mcpsdk.Tool{Name: "memory_save", InputSchema: objectSchema("content", "type", "tags", "meta")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("memory_save", args)
			b.seq++
			rec := &fakeMemoryRecord{
				id:        fmt.Sprintf("alpha-%03d", b.seq),
				content:   fmt.Sprint(args["content"]),
				memType:   fmt.Sprint(args["type"]),
				tags:      toStringSlice(args["tags"]),
				metadata:  toMetadata(args["meta"]),
				createdAt: time.Now().UTC(),
			}
			b.put(rec)
			return jsonResult(t, map[string]any{"memory": alphaRecord(rec)}), nil
		})

	srv.AddTool(&mcpsdk.Tool{Name: "memory_search", InputSchema: objectSchema("query", "top_k")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("memory_search", args)
			limit := 0
			if v, ok := args["top_k"].(float64); ok {
				limit = int(v)
			}
			var results []any
			for _, rec := range b.match(fmt.Sprint(args["query"]), limit) {
				results = append(results, map[string]any{"memory": alphaRecord(rec), "score": 0.9})
			}
			return jsonResult(t, map[string]any{"results": results}), nil
		})

	srv.AddTool(&mcpsdk.Tool{Name: "memory_fetch", InputSchema: objectSchema("id")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("memory_fetch", args)
			rec, ok := b.records[fmt.Sprint(args["id"])]
			if !ok {
				return jsonResult(t, map[string]any{"memory": nil}), nil
			}
			return jsonResult(t, map[string]any{"memory": alphaRecord(rec)}), nil
		})

	srv.AddTool(&mcpsdk.Tool{Name: "memory_forget", InputSchema: objectSchema("id")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("memory_forget", args)
			id := fmt.Sprint(args["id"])
			if _, ok := b.records[id]; !ok {
				return errResult("no such memory: " + id), nil
			}
			delete(b.records, id)
			for i, existing := range b.order {
				if existing == id {
					b.order = append(b.order[:i], b.order[i+1:]...)
					break
				}
			}
			return jsonResult(t, map[string]any{"deleted": true}), nil
		})

	srv.AddTool(&mcpsdk.Tool{Name: "memory_dump", InputSchema: objectSchema("limit")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("memory_dump", args)
			limit := len(b.order)
			if v, ok := args["limit"].(float64); ok && int(v) < limit {
				limit = int(v)
			}
			out := make([]any, 0, limit)
			for _, id := range b.order[:limit] {
				out = append(out, alphaRecord(b.records[id]))
			}
			return jsonResult(t, map[string]any{"memories": out}), nil
		})

	return srv
}

func alphaRecord(rec *fakeMemoryRecord) map[string]any {
	return map[string]any{
		"id":         rec.id,
		"content":    rec.content,
		"type":       rec.memType,
		"tags":       rec.tags,
		"meta":       rec.metadata,
		"created_at": rec.createdAt.Format(time.RFC3339),
	}
}

func alphaOptions() map[string]string {
	return map[string]string{
		"tool.store":  "memory_save",
		"tool.search": "memory_search",
		"tool.get":    "memory_fetch",
		"tool.delete": "memory_forget",
		"tool.list":   "memory_dump",

		"arg.store.content":  "content",
		"arg.store.type":     "type",
		"arg.store.tags":     "tags",
		"arg.store.metadata": "meta",
		"arg.search.query":   "query",
		"arg.search.limit":   "top_k",
		"arg.get.id":         "id",
		"arg.delete.id":      "id",
		"arg.list.limit":     "limit",

		"result.store.id":     "memory.id",
		"result.search.items": "results",
		"result.search.hit":   "memory",
		"result.search.score": "score",
		"result.get.item":     "memory",
		"result.list.items":   "memories",

		"field.id":         "id",
		"field.content":    "content",
		"field.type":       "type",
		"field.tags":       "tags",
		"field.created_at": "created_at",
		"field.metadata":   "meta",
	}
}

// ---------- server "beta" ----------

func newBetaServer(t *testing.T, b *fakeMemoryBackend) *mcpsdk.Server {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "beta", Version: "1"}, nil)

	srv.AddTool(&mcpsdk.Tool{Name: "remember", InputSchema: objectSchema("key", "text", "kind", "labels", "extra")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("remember", args)
			key := fmt.Sprint(args["key"])
			if key == "" || key == "<nil>" {
				return errResult("remember: key is required"), nil
			}
			rec := &fakeMemoryRecord{
				id:        key,
				content:   fmt.Sprint(args["text"]),
				memType:   fmt.Sprint(args["kind"]),
				tags:      toStringSlice(args["labels"]),
				metadata:  toMetadata(args["extra"]),
				createdAt: time.Now().UTC(),
			}
			b.put(rec)
			// A bare JSON string, not an object: the id is the whole payload.
			return jsonResult(t, key), nil
		})

	srv.AddTool(&mcpsdk.Tool{Name: "recall", InputSchema: objectSchema("q", "n")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("recall", args)
			limit := 0
			if v, ok := args["n"].(float64); ok {
				limit = int(v)
			}
			// A bare array at the root, score inline on the record.
			out := []any{}
			for _, rec := range b.match(fmt.Sprint(args["q"]), limit) {
				item := betaRecord(rec)
				item["relevance"] = 0.42
				out = append(out, item)
			}
			return jsonResult(t, out), nil
		})

	srv.AddTool(&mcpsdk.Tool{Name: "lookup", InputSchema: objectSchema("key")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("lookup", args)
			rec, ok := b.records[fmt.Sprint(args["key"])]
			if !ok {
				return errResult("lookup: not found"), nil
			}
			// The record at the root, no wrapper.
			return jsonResult(t, betaRecord(rec)), nil
		})

	srv.AddTool(&mcpsdk.Tool{Name: "forget", InputSchema: objectSchema("key")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("forget", args)
			id := fmt.Sprint(args["key"])
			delete(b.records, id)
			for i, existing := range b.order {
				if existing == id {
					b.order = append(b.order[:i], b.order[i+1:]...)
					break
				}
			}
			return jsonResult(t, map[string]any{"ok": true}), nil
		})

	srv.AddTool(&mcpsdk.Tool{Name: "dump", InputSchema: objectSchema("count", "skip")},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			args := decodeArgs(t, req)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.record("dump", args)
			skip := 0
			if v, ok := args["skip"].(float64); ok {
				skip = int(v)
			}
			count := len(b.order)
			if v, ok := args["count"].(float64); ok {
				count = int(v)
			}
			out := []any{}
			for i := skip; i < len(b.order) && len(out) < count; i++ {
				out = append(out, betaRecord(b.records[b.order[i]]))
			}
			return jsonResult(t, map[string]any{"data": out}), nil
		})

	return srv
}

func betaRecord(rec *fakeMemoryRecord) map[string]any {
	return map[string]any{
		"key":    rec.id,
		"text":   rec.content,
		"kind":   rec.memType,
		"labels": rec.tags,
		"extra":  rec.metadata,
		"ts":     rec.createdAt.Unix(),
	}
}

func betaOptions() map[string]string {
	return map[string]string{
		"tool.store":  "remember",
		"tool.search": "recall",
		"tool.get":    "lookup",
		"tool.delete": "forget",
		"tool.list":   "dump",

		"arg.store.id":       "key",
		"arg.store.content":  "text",
		"arg.store.type":     "kind",
		"arg.store.tags":     "labels",
		"arg.store.metadata": "extra",
		"arg.search.query":   "q",
		"arg.search.limit":   "n",
		"arg.get.id":         "key",
		"arg.delete.id":      "key",
		"arg.list.limit":     "count",
		"arg.list.offset":    "skip",

		// The store result is the bare id, so there is no path to walk.
		"result.search.items": "",
		"result.search.score": "relevance",
		"result.get.item":     "",
		"result.list.items":   "data",

		"field.id":         "key",
		"field.content":    "text",
		"field.type":       "kind",
		"field.tags":       "labels",
		"field.created_at": "ts",
		"field.metadata":   "extra",
	}
}

// ============================================================
// Wiring
// ============================================================

// connectFakeStore runs an MCP server over an in-memory transport pair and
// returns a store bound to it through a real pkg/mcp client — a real MCP
// session, real tools/list, real tools/call, no shortcuts.
func connectFakeStore(t *testing.T, srv *mcpsdk.Server, options map[string]string) *store.MCPMemoryStore {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	session, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	st, err := store.NewMCPMemoryStore(store.MCPMemoryConfig{
		Server:        agentmcp.ServerConfig{Name: "fake"},
		ClientOptions: &agentmcp.ClientOptions{Transport: clientTransport},
		Options:       options,
		Timeout:       10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMCPMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type fakeServerCase struct {
	name    string
	build   func(t *testing.T, b *fakeMemoryBackend) *mcpsdk.Server
	options map[string]string
	// tool names, so assertions can name what was actually called
	saveTool   string
	searchTool string
}

func fakeServerCases() []fakeServerCase {
	return []fakeServerCase{
		{name: "alpha", build: newAlphaServer, options: alphaOptions(), saveTool: "memory_save", searchTool: "memory_search"},
		{name: "beta", build: newBetaServer, options: betaOptions(), saveTool: "remember", searchTool: "recall"},
	}
}

// ============================================================
// Generality: one MemoryStore contract, two unrelated tool surfaces
// ============================================================

func TestMCPMemoryStoreSameSemanticsAcrossDifferentServers(t *testing.T) {
	for _, tc := range fakeServerCases() {
		t.Run(tc.name, func(t *testing.T) {
			backend := newFakeMemoryBackend()
			st := connectFakeStore(t, tc.build(t, backend), tc.options)
			ctx := context.Background()

			mem := &domain.Memory{
				Type:    domain.MemoryTypePreference,
				Content: "The deploy window for shipyard is Tuesday 21:00 UTC.",
				Tags:    []string{"ops", "deploy"},
			}
			if err := st.Store(ctx, mem); err != nil {
				t.Fatalf("Store() error = %v", err)
			}
			if mem.ID == "" {
				t.Fatal("Store() did not return an id")
			}

			// Get
			got, err := st.Get(ctx, mem.ID)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if got.Content != mem.Content {
				t.Errorf("Get().Content = %q, want %q", got.Content, mem.Content)
			}
			if got.Type != domain.MemoryTypePreference {
				t.Errorf("Get().Type = %q, want %q", got.Type, domain.MemoryTypePreference)
			}
			if strings.Join(got.Tags, ",") != "ops,deploy" {
				t.Errorf("Get().Tags = %v, want [ops deploy]", got.Tags)
			}
			if got.CreatedAt.IsZero() {
				t.Error("Get().CreatedAt is zero; the created-at mapping did not apply")
			}

			// SearchByText
			hits, err := st.SearchByText(ctx, "shipyard deploy window", 5)
			if err != nil {
				t.Fatalf("SearchByText() error = %v", err)
			}
			if len(hits) != 1 {
				t.Fatalf("SearchByText() returned %d hits, want 1", len(hits))
			}
			if hits[0].Score <= 0 {
				t.Errorf("SearchByText() score = %v, want the mapped score", hits[0].Score)
			}
			if hits[0].Content != mem.Content {
				t.Errorf("SearchByText() content = %q, want %q", hits[0].Content, mem.Content)
			}

			// List
			listed, total, err := st.List(ctx, 10, 0)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if total != 1 || len(listed) != 1 || listed[0].ID != mem.ID {
				t.Fatalf("List() = %d records (total %d), want the one we stored", len(listed), total)
			}

			// Delete
			if err := st.Delete(ctx, mem.ID); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			if _, _, err := st.List(ctx, 10, 0); err != nil {
				t.Fatalf("List() after delete error = %v", err)
			}
			if hits, err := st.SearchByText(ctx, "shipyard", 5); err != nil || len(hits) != 0 {
				t.Errorf("after Delete(): hits = %d, err = %v; want 0, nil", len(hits), err)
			}

			// Nothing was sent that this server did not declare.
			declared := map[string][]string{
				"memory_save":   {"content", "meta", "tags", "type"},
				"remember":      {"extra", "key", "kind", "labels", "text"},
				"memory_search": {"query", "top_k"},
				"recall":        {"n", "q"},
			}
			for _, tool := range []string{tc.saveTool, tc.searchTool} {
				allowed := map[string]bool{}
				for _, name := range declared[tool] {
					allowed[name] = true
				}
				for _, sent := range backend.argNames(tool) {
					if !allowed[sent] {
						t.Errorf("%s received undeclared argument %q", tool, sent)
					}
				}
			}
		})
	}
}

// TestMCPMemoryStoreListPaging covers both paging shapes: a server with a real
// offset parameter, and one where the window has to be sliced client-side.
func TestMCPMemoryStoreListPaging(t *testing.T) {
	for _, tc := range fakeServerCases() {
		t.Run(tc.name, func(t *testing.T) {
			backend := newFakeMemoryBackend()
			st := connectFakeStore(t, tc.build(t, backend), tc.options)
			ctx := context.Background()

			for i := 0; i < 5; i++ {
				m := &domain.Memory{Content: fmt.Sprintf("record number %d", i)}
				if err := st.Store(ctx, m); err != nil {
					t.Fatalf("Store(%d) error = %v", i, err)
				}
			}

			page, _, err := st.List(ctx, 2, 2)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(page) != 2 {
				t.Fatalf("List(limit=2, offset=2) returned %d records, want 2", len(page))
			}
			if !strings.Contains(page[0].Content, "number 2") || !strings.Contains(page[1].Content, "number 3") {
				t.Errorf("List(limit=2, offset=2) = %q / %q, want records 2 and 3", page[0].Content, page[1].Content)
			}
		})
	}
}

// TestMCPMemoryStoreScopeRoundTrip pins that scope survives a server that has
// no concept of one: it rides in the metadata blob and comes back intact.
func TestMCPMemoryStoreScopeRoundTrip(t *testing.T) {
	for _, tc := range fakeServerCases() {
		t.Run(tc.name, func(t *testing.T) {
			backend := newFakeMemoryBackend()
			st := connectFakeStore(t, tc.build(t, backend), tc.options)
			ctx := context.Background()

			mem := &domain.Memory{Content: "session-only fact about the kraken deployment"}
			scope := domain.MemoryScope{Type: domain.MemoryScopeSession, ID: "session-a"}
			if err := st.StoreWithScope(ctx, mem, scope); err != nil {
				t.Fatalf("StoreWithScope() error = %v", err)
			}

			got, err := st.Get(ctx, mem.ID)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if got.ScopeType != domain.MemoryScopeSession {
				t.Errorf("ScopeType = %q, want %q", got.ScopeType, domain.MemoryScopeSession)
			}
			if got.SessionID != "session-a" {
				t.Errorf("SessionID = %q, want %q", got.SessionID, "session-a")
			}
		})
	}
}

// ============================================================
// The path the loop actually walks
// ============================================================

// TestMCPMemoryRetrieveAndInject is the test that matters. A store-level
// Store/Search round trip can pass while the agent never sees a single memory,
// because memory.Service filters retrieved memories by scope after the store
// returns them. Assert on the injected text, not on a hit count.
//
// The service is built with embedder = nil, which is how a shared MCP brain is
// wired: the server owns the embedding model. That drives retrieval down the
// SearchByText path, which is exactly why Search/SearchByScope must degrade to
// empty rather than to an error.
func TestMCPMemoryRetrieveAndInject(t *testing.T) {
	for _, tc := range fakeServerCases() {
		t.Run(tc.name, func(t *testing.T) {
			backend := newFakeMemoryBackend()
			st := connectFakeStore(t, tc.build(t, backend), tc.options)
			svc := memory.NewService(st, nil, nil, memory.DefaultConfig())
			t.Cleanup(func() { _ = svc.Close() })
			ctx := context.Background()

			const marker = "zzql7788"
			if err := svc.Add(ctx, &domain.Memory{
				Type:      domain.MemoryTypeFact,
				ScopeType: domain.MemoryScopeGlobal,
				Content:   "The " + marker + " protocol runs on port 43510 and is maintained by the platform team.",
			}); err != nil {
				t.Fatalf("Add() error = %v", err)
			}

			injected, mems, err := svc.RetrieveAndInjectWithContext(ctx, marker+" 是什么",
				domain.MemoryQueryContext{SessionID: "probe-session"})
			if err != nil {
				t.Fatalf("RetrieveAndInjectWithContext() error = %v", err)
			}
			if len(mems) == 0 {
				t.Fatal("RetrieveAndInjectWithContext() returned no memories: a global memory is invisible to the loop")
			}
			if !strings.Contains(injected, marker) {
				t.Errorf("injected context = %q, want it to carry %q", injected, marker)
			}
		})
	}
}

// TestMCPMemorySessionScopeStaysInItsSession pins the other half: making global
// memories visible must not leak one session's memories into another's.
func TestMCPMemorySessionScopeStaysInItsSession(t *testing.T) {
	for _, tc := range fakeServerCases() {
		t.Run(tc.name, func(t *testing.T) {
			backend := newFakeMemoryBackend()
			st := connectFakeStore(t, tc.build(t, backend), tc.options)
			svc := memory.NewService(st, nil, nil, memory.DefaultConfig())
			t.Cleanup(func() { _ = svc.Close() })
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
		})
	}
}

// TestMCPMemoryForeignRecordsStayGlobal pins the d0eb578 lesson directly: a
// record written by some other client of the same server carries no agent-go
// metadata, and must come back at global scope so the retrieval filter can see
// it — never with a foreign string in SessionID.
func TestMCPMemoryForeignRecordsStayGlobal(t *testing.T) {
	backend := newFakeMemoryBackend()
	backend.put(&fakeMemoryRecord{
		id:        "alpha-999",
		content:   "Written by another client entirely: the wharf service listens on 6759.",
		createdAt: time.Now().UTC(),
	})
	st := connectFakeStore(t, newAlphaServer(t, backend), alphaOptions())
	svc := memory.NewService(st, nil, nil, memory.DefaultConfig())
	t.Cleanup(func() { _ = svc.Close() })
	ctx := context.Background()

	hits, err := st.SearchByText(ctx, "wharf", 5)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchByText() returned %d hits, want 1", len(hits))
	}
	if hits[0].SessionID != "" || hits[0].ScopeID != "" {
		t.Errorf("foreign record came back with SessionID=%q ScopeID=%q, want both empty",
			hits[0].SessionID, hits[0].ScopeID)
	}

	injected, mems, err := svc.RetrieveAndInjectWithContext(ctx, "wharf service port",
		domain.MemoryQueryContext{SessionID: "some-session"})
	if err != nil {
		t.Fatalf("RetrieveAndInjectWithContext() error = %v", err)
	}
	if len(mems) == 0 || !strings.Contains(injected, "wharf") {
		t.Fatalf("a foreign record is invisible to the loop: %d memories, injected=%q", len(mems), injected)
	}
}

// ============================================================
// Honest degradation
// ============================================================

func TestMCPMemoryVectorSearchDegradesToEmpty(t *testing.T) {
	backend := newFakeMemoryBackend()
	st := connectFakeStore(t, newAlphaServer(t, backend), alphaOptions())
	ctx := context.Background()
	vector := []float64{0.1, 0.2, 0.3}

	if hits, err := st.Search(ctx, vector, 5, 0); err != nil || hits != nil {
		t.Errorf("Search() = %v, %v; want nil, nil so memory.Service falls back to SearchByText", hits, err)
	}
	if hits, err := st.SearchBySession(ctx, "s", vector, 5); err != nil || hits != nil {
		t.Errorf("SearchBySession() = %v, %v; want nil, nil", hits, err)
	}
	if hits, err := st.SearchByScope(ctx, vector, nil, 5); err != nil || hits != nil {
		t.Errorf("SearchByScope() = %v, %v; want nil, nil", hits, err)
	}
}

func TestMCPMemoryUnsupportedOperationsAreHonest(t *testing.T) {
	backend := newFakeMemoryBackend()
	st := connectFakeStore(t, newAlphaServer(t, backend), alphaOptions())
	ctx := context.Background()

	checks := map[string]error{
		"IncrementAccess": st.IncrementAccess(ctx, "x"),
		"Clear":           st.Clear(ctx),
		"DeleteBySession": st.DeleteBySession(ctx, "s"),
		"ConfigureBank":   st.ConfigureBank(ctx, "s", nil),
		"AddMentalModel":  st.AddMentalModel(ctx, nil),
	}
	if _, err := st.GetByType(ctx, domain.MemoryTypeFact, 5); true {
		checks["GetByType"] = err
	}
	if _, err := st.Reflect(ctx, "s"); true {
		checks["Reflect"] = err
	}
	for name, err := range checks {
		if !domain.IsMemoryStoreUnsupported(err) {
			t.Errorf("%s() error = %v, want ErrMemoryStoreUnsupported", name, err)
		}
	}

	if err := st.InitSchema(ctx); err != nil {
		t.Errorf("InitSchema() error = %v, want nil (the server owns its schema)", err)
	}
}

// TestMCPMemoryUnmappedOperationIsUnsupported pins that a partial tool surface
// degrades honestly instead of pretending.
func TestMCPMemoryUnmappedOperationIsUnsupported(t *testing.T) {
	backend := newFakeMemoryBackend()
	options := alphaOptions()
	delete(options, "tool.delete")
	delete(options, "tool.list")
	st := connectFakeStore(t, newAlphaServer(t, backend), options)
	ctx := context.Background()

	if err := st.Delete(ctx, "alpha-001"); !domain.IsMemoryStoreUnsupported(err) {
		t.Errorf("Delete() with no tool.delete = %v, want ErrMemoryStoreUnsupported", err)
	}
	if _, _, err := st.List(ctx, 10, 0); !domain.IsMemoryStoreUnsupported(err) {
		t.Errorf("List() with no tool.list = %v, want ErrMemoryStoreUnsupported", err)
	}
}

// TestMCPMemoryToolErrorIsNotAPayload pins that a tool reporting its own failure
// becomes an error, not a memory id or a record.
func TestMCPMemoryToolErrorIsNotAPayload(t *testing.T) {
	backend := newFakeMemoryBackend()
	st := connectFakeStore(t, newBetaServer(t, backend), betaOptions())
	ctx := context.Background()

	if _, err := st.Get(ctx, "does-not-exist"); err == nil {
		t.Error("Get() on a missing id returned no error; the tool's error text was treated as a record")
	}
}

// ============================================================
// Configuration
// ============================================================

func TestMCPMemoryConfigFromStoreConfig(t *testing.T) {
	cfg, err := store.MCPMemoryConfigFromStoreConfig(domain.MemoryStoreConfig{
		Name: store.MCPMemoryStoreType,
		DSN:  "https://memory.example.invalid/mcp",
		Options: map[string]string{
			"header.X-Tenant": "acme",
			"tool.store":      "save",
			"timeout":         "5s",
		},
	})
	if err != nil {
		t.Fatalf("MCPMemoryConfigFromStoreConfig() error = %v", err)
	}
	if cfg.Server.Type != agentmcp.ServerTypeHTTP {
		t.Errorf("transport = %q, want http (inferred from the URL-shaped DSN)", cfg.Server.Type)
	}
	if cfg.Server.URL != "https://memory.example.invalid/mcp" {
		t.Errorf("URL = %q", cfg.Server.URL)
	}
	if cfg.Server.Headers["X-Tenant"] != "acme" {
		t.Errorf("headers = %v", cfg.Server.Headers)
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", cfg.Timeout)
	}

	stdio, err := store.MCPMemoryConfigFromStoreConfig(domain.MemoryStoreConfig{
		Options: map[string]string{
			"command":      "memory-mcp",
			"args":         `["--db","/tmp/x"]`,
			"env.MEM_HOME": "/tmp/home",
			"tool.store":   "save",
		},
	})
	if err != nil {
		t.Fatalf("MCPMemoryConfigFromStoreConfig() error = %v", err)
	}
	if stdio.Server.Type != agentmcp.ServerTypeStdio {
		t.Errorf("transport = %q, want stdio", stdio.Server.Type)
	}
	if strings.Join(stdio.Server.Args, " ") != "--db /tmp/x" {
		t.Errorf("args = %v", stdio.Server.Args)
	}
	if stdio.Server.Env["MEM_HOME"] != "/tmp/home" {
		t.Errorf("env = %v", stdio.Server.Env)
	}
}

func TestMCPMemoryTokenNeverComesFromCode(t *testing.T) {
	t.Setenv("AGENTGO_TEST_MCP_MEMORY_TOKEN", "s3cr3t")
	cfg, err := store.MCPMemoryConfigFromStoreConfig(domain.MemoryStoreConfig{
		DSN: "https://memory.example.invalid/mcp",
		Options: map[string]string{
			"token_env":  "AGENTGO_TEST_MCP_MEMORY_TOKEN",
			"tool.store": "save",
		},
	})
	if err != nil {
		t.Fatalf("MCPMemoryConfigFromStoreConfig() error = %v", err)
	}
	if got := cfg.Server.Headers["Authorization"]; got != "Bearer s3cr3t" {
		t.Errorf("Authorization = %q, want the token read from the environment", got)
	}
}

func TestMCPMemoryProfileIsExplicitAndOverridable(t *testing.T) {
	if _, err := store.NewMCPMemoryStore(store.MCPMemoryConfig{
		Server:  agentmcp.ServerConfig{Command: []string{"x"}},
		Options: map[string]string{"profile": "not-a-real-profile"},
	}); err == nil {
		t.Error("an unknown profile must be an error, not a silent fallback")
	}

	st, err := store.NewMCPMemoryStore(store.MCPMemoryConfig{
		Server: agentmcp.ServerConfig{Command: []string{"x"}},
		Options: map[string]string{
			"profile":    store.MCPMemoryProfileCortexDB,
			"tool.store": "my_own_save",
		},
	})
	if err != nil {
		t.Fatalf("NewMCPMemoryStore() error = %v", err)
	}
	if name, _ := st.Mapping().Tool("store"); name != "my_own_save" {
		t.Errorf("tool.store = %q, want the explicit option to beat the profile", name)
	}
	if name, _ := st.Mapping().Tool("search"); name != "memory_search" {
		t.Errorf("tool.search = %q, want the profile's value", name)
	}
}

func TestMCPMemoryStoreIsRegisteredAsAPlugin(t *testing.T) {
	if !domain.MemoryStoreRegistered(store.MCPMemoryStoreType) {
		t.Fatalf("%q is not in the memory store registry: %v", store.MCPMemoryStoreType, domain.RegisteredMemoryStores())
	}
	factory, _ := domain.LookupMemoryStore(store.MCPMemoryStoreType)
	if _, err := factory(domain.MemoryStoreConfig{Name: store.MCPMemoryStoreType}); err == nil {
		t.Error("a config with no server and no mapping must fail loudly")
	}
	built, err := factory(domain.MemoryStoreConfig{
		Name:    store.MCPMemoryStoreType,
		DSN:     "memory-mcp --db /tmp/x",
		Options: map[string]string{"profile": store.MCPMemoryProfileCortexDB},
	})
	if err != nil {
		t.Fatalf("factory() error = %v", err)
	}
	if built == nil {
		t.Fatal("factory() returned a nil store")
	}
}

func TestMCPMemoryConstructionDoesNotRequireAReachableServer(t *testing.T) {
	st, err := store.NewMCPMemoryStore(store.MCPMemoryConfig{
		Server:  agentmcp.ServerConfig{Command: []string{"/nonexistent/memory-server-43510"}},
		Options: map[string]string{"profile": store.MCPMemoryProfileCortexDB},
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMCPMemoryStore() must not dial: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.Store(context.Background(), &domain.Memory{Content: "x"}); err == nil {
		t.Error("Store() against an unreachable server must fail")
	}
}
