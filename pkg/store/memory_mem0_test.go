package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// fakeMem0 is a mem0 server with just enough behaviour to hold this backend
// to the real one's contract: it demands an owner, mints its own ids, and
// answers the shapes the real server answers.
type fakeMem0 struct {
	mu      sync.Mutex
	items   map[string]map[string]any
	seq     int
	key     string
	lastAdd map[string]any
}

func newFakeMem0(t *testing.T, key string) (*fakeMem0, *httptest.Server) {
	t.Helper()
	f := &fakeMem0{items: map[string]map[string]any{}, key: key}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeMem0) serve(w http.ResponseWriter, r *http.Request) {
	if f.key != "" && r.Header.Get("X-API-Key") != f.key {
		http.Error(w, `{"detail":"Authentication required"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/memories":
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.lastAdd = body
		// The real server 400s without an owner, and a backend that never
		// sends one looks fine until every write fails in production.
		if body["user_id"] == nil && body["run_id"] == nil && body["agent_id"] == nil {
			http.Error(w, `{"detail":"At least one identifier is required."}`, http.StatusBadRequest)
			return
		}
		f.seq++
		id := "mem0-" + string(rune('a'+f.seq-1))
		msgs, _ := body["messages"].([]any)
		content := ""
		if len(msgs) > 0 {
			if m, ok := msgs[0].(map[string]any); ok {
				content, _ = m["content"].(string)
			}
		}
		item := map[string]any{"id": id, "memory": content, "metadata": body["metadata"]}
		for _, k := range []string{"user_id", "run_id", "agent_id"} {
			if v, ok := body[k]; ok {
				item[k] = v
			}
		}
		f.items[id] = item
		writeJSON(w, map[string]any{"results": []any{map[string]any{"id": id, "memory": content, "event": "ADD"}}})

	case r.Method == http.MethodPost && r.URL.Path == "/search":
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		query, _ := body["query"].(string)
		var hits []any
		for _, item := range f.items {
			text, _ := item["memory"].(string)
			if query == "" || strings.Contains(strings.ToLower(text), strings.ToLower(firstWord(query))) {
				clone := map[string]any{}
				for k, v := range item {
					clone[k] = v
				}
				clone["score"] = 0.9
				hits = append(hits, clone)
			}
		}
		writeJSON(w, map[string]any{"results": hits})

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/memories/"):
		id := strings.TrimPrefix(r.URL.Path, "/memories/")
		item, ok := f.items[id]
		if !ok {
			writeJSON(w, nil)
			return
		}
		writeJSON(w, item)

	case r.Method == http.MethodGet && r.URL.Path == "/memories":
		var all []any
		for _, item := range f.items {
			all = append(all, item)
		}
		writeJSON(w, map[string]any{"results": all})

	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/memories/"):
		id := strings.TrimPrefix(r.URL.Path, "/memories/")
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if item, ok := f.items[id]; ok {
			if text, ok := body["text"].(string); ok {
				item["memory"] = text
			}
			if md, ok := body["metadata"]; ok {
				item["metadata"] = md
			}
		}
		writeJSON(w, map[string]any{"message": "Memory updated successfully"})

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/memories/"):
		delete(f.items, strings.TrimPrefix(r.URL.Path, "/memories/"))
		writeJSON(w, map[string]any{"message": "Memory deleted successfully"})

	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

func newMem0Store(t *testing.T, srv *httptest.Server, key string) *store.Mem0MemoryStore {
	t.Helper()
	st, err := store.NewMem0MemoryStore(store.Mem0Config{Endpoint: srv.URL, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// mem0 mints its own id and the caller has to adopt it — a store that keeps
// the id it invented addresses nothing on every later Get and Delete.
func TestMem0AdoptsTheServersID(t *testing.T) {
	_, srv := newFakeMem0(t, "k")
	st := newMem0Store(t, srv, "k")
	ctx := context.Background()

	mem := &domain.Memory{ID: "ours", Type: domain.MemoryTypeFact, Content: "the port is 43917"}
	if err := st.Store(ctx, mem); err != nil {
		t.Fatal(err)
	}
	if mem.ID == "ours" || mem.ID == "" {
		t.Fatalf("ID = %q, want the server's", mem.ID)
	}
	got, err := st.Get(ctx, mem.ID)
	if err != nil {
		t.Fatalf("Get after Store: %v", err)
	}
	if !strings.Contains(got.Content, "43917") {
		t.Errorf("content = %q", got.Content)
	}
}

// Every write must carry an owner, or the real server 400s.
func TestMem0AlwaysSendsAnOwner(t *testing.T) {
	f, srv := newFakeMem0(t, "")
	st := newMem0Store(t, srv, "")
	ctx := context.Background()

	if err := st.Store(ctx, &domain.Memory{Content: "global fact"}); err != nil {
		t.Fatalf("global write: %v", err)
	}
	if f.lastAdd["user_id"] == nil {
		t.Error("a memory with no session must be owned by the configured user")
	}

	if err := st.Store(ctx, &domain.Memory{Content: "session fact", SessionID: "sess-1"}); err != nil {
		t.Fatalf("session write: %v", err)
	}
	if f.lastAdd["run_id"] != "sess-1" {
		t.Errorf("run_id = %v, want the session id", f.lastAdd["run_id"])
	}
	// agent-go's identity is the session UUID; it must not become mem0's
	// user_id, which is this deployment's name for "no session".
	if f.lastAdd["user_id"] != nil {
		t.Errorf("a session memory also claimed user_id = %v", f.lastAdd["user_id"])
	}
}

// agent-go has already decided what to keep, so mem0's own extraction is off
// by default — it would rewrite the text and return a different id.
func TestMem0DoesNotLetTheServerRewriteTheText(t *testing.T) {
	f, srv := newFakeMem0(t, "")
	st := newMem0Store(t, srv, "")
	if err := st.Store(context.Background(), &domain.Memory{Content: "verbatim"}); err != nil {
		t.Fatal(err)
	}
	if infer, _ := f.lastAdd["infer"].(bool); infer {
		t.Error("infer should default to false")
	}
}

// The agent-go half of a memory survives the round trip through mem0's
// free-form metadata, and the session comes back as a session.
func TestMem0RoundTripsScopeAndFields(t *testing.T) {
	_, srv := newFakeMem0(t, "")
	st := newMem0Store(t, srv, "")
	ctx := context.Background()

	mem := &domain.Memory{
		Type: domain.MemoryTypePreference, Content: "prefers dark mode",
		SessionID: "sess-9", ScopeType: domain.MemoryScopeSession, ScopeID: "sess-9",
		Tags: []string{"ui"}, Importance: 0.77,
	}
	if err := st.Store(ctx, mem); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, mem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != domain.MemoryTypePreference {
		t.Errorf("type = %q", got.Type)
	}
	if got.Importance != 0.77 {
		t.Errorf("importance = %v", got.Importance)
	}
	if got.SessionID != "sess-9" || got.ScopeType != domain.MemoryScopeSession {
		t.Errorf("scope lost: session=%q type=%q — a scope-less record is global and every chain sees it",
			got.SessionID, got.ScopeType)
	}
}

// What it cannot do it says, rather than faking. Vector search degrades to
// empty so the service falls through to text; the rest use the sentinel.
func TestMem0IsHonestAboutWhatItCannotDo(t *testing.T) {
	_, srv := newFakeMem0(t, "")
	st := newMem0Store(t, srv, "")
	ctx := context.Background()

	hits, err := st.Search(ctx, []float64{0.1}, 5, 0)
	if err != nil || len(hits) != 0 {
		t.Errorf("vector Search = %v, %v; want empty and no error so the service falls through to text", hits, err)
	}
	for name, err := range map[string]error{
		"IncrementAccess": st.IncrementAccess(ctx, "x"),
		"Clear":           st.Clear(ctx),
		"DeleteBySession": st.DeleteBySession(ctx, "s"),
		"AddMentalModel":  st.AddMentalModel(ctx, nil),
		"ConfigureBank":   st.ConfigureBank(ctx, "s", nil),
	} {
		if !errors.Is(err, domain.ErrMemoryStoreUnsupported) {
			t.Errorf("%s = %v, want ErrMemoryStoreUnsupported", name, err)
		}
	}
}

// The key travels in a header and nowhere else — not in an error message a
// caller might log.
func TestMem0DoesNotLeakTheKey(t *testing.T) {
	_, srv := newFakeMem0(t, "the-real-key")
	st := newMem0Store(t, srv, "wrong-key")
	err := st.Store(context.Background(), &domain.Memory{Content: "x"})
	if err == nil {
		t.Fatal("a wrong key should fail")
	}
	if strings.Contains(err.Error(), "wrong-key") || strings.Contains(err.Error(), "the-real-key") {
		t.Fatalf("the error carries the key: %v", err)
	}
}

// The registry, not a new case in a switch: this is how a backend is added.
func TestMem0IsRegisteredAsAPlugin(t *testing.T) {
	factory, ok := domain.LookupMemoryStore(store.Mem0StoreType)
	if !ok {
		t.Fatalf("%q is not registered", store.Mem0StoreType)
	}
	if _, err := factory(domain.MemoryStoreConfig{Name: store.Mem0StoreType}); err == nil {
		t.Error("a factory with no endpoint should fail rather than build a store that cannot work")
	}
	st, err := factory(domain.MemoryStoreConfig{
		Name: store.Mem0StoreType, DSN: "http://127.0.0.1:43917",
		Options: map[string]string{"api_key": "k", "user_id": "u"},
	})
	if err != nil || st == nil {
		t.Fatalf("factory with a DSN: %v", err)
	}
}
