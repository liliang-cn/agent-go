package memory

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

type decisionCapture struct {
	mu   sync.Mutex
	list []trace.Decision
}

func (c *decisionCapture) ModelStart(context.Context, string, string)     {}
func (c *decisionCapture) ModelEnd(context.Context, string, trace.Record) {}
func (c *decisionCapture) Step(context.Context, trace.Record)             {}
func (c *decisionCapture) Decision(_ context.Context, d trace.Decision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, d)
}

func (c *decisionCapture) injects() []trace.Decision {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []trace.Decision
	for _, d := range c.list {
		if d.What == "inject_memories" {
			out = append(out, d)
		}
	}
	return out
}

// roundTrip turns a detail into plain JSON types, as a trace line carries it.
func roundTrip(t *testing.T, d trace.Decision) map[string]any {
	t.Helper()
	b, err := json.Marshal(d.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func injectedIDs(m map[string]any) []string {
	var ids []string
	for _, it := range m["injected"].([]any) {
		ids = append(ids, it.(map[string]any)["id"].(string))
	}
	return ids
}

func counts(m map[string]any) map[string]float64 {
	out := map[string]float64{}
	for k, v := range m["counts"].(map[string]any) {
		out[k] = v.(float64)
	}
	return out
}

// A memory from session B must not reach session A's prompt, and the trace
// must say it was a candidate that the scope filter dropped.
func TestInjectMemoriesTraceTwoSessions(t *testing.T) {
	memStore, err := store.NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(memStore, nil, nil, nil)
	ctx := context.Background()

	mine := &domain.Memory{ID: "mem-a", Type: domain.MemoryTypeFact, Content: "the cat is called Doudou", Importance: 0.8, SessionID: "sess-a"}
	theirs := &domain.Memory{ID: "mem-b", Type: domain.MemoryTypeFact, Content: "the cat is called Mimi", Importance: 0.8, SessionID: "sess-b"}
	if err := svc.Add(ctx, mine); err != nil {
		t.Fatal(err)
	}
	if err := svc.Add(ctx, theirs); err != nil {
		t.Fatal(err)
	}

	cap := &decisionCapture{}
	tctx := trace.WithSink(ctx, cap)
	text, mems, _, err := svc.RetrieveAndInjectWithContextAndLogic(tctx, "what is the cat called", domain.MemoryQueryContext{SessionID: "sess-a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Mimi") {
		t.Fatalf("session B leaked into session A's prompt:\n%s", text)
	}
	if len(mems) != 1 || mems[0].ID != "mem-a" {
		t.Fatalf("recalled %+v", mems)
	}

	got := cap.injects()
	if len(got) != 1 {
		t.Fatalf("want exactly one inject_memories decision, got %d", len(got))
	}
	d := roundTrip(t, got[0])
	ids := injectedIDs(d)
	if len(ids) != 1 || ids[0] != "mem-a" {
		t.Fatalf("injected = %v", ids)
	}
	for _, id := range ids {
		if id == "mem-b" {
			t.Fatal("mem-b listed as injected")
		}
	}
	c := counts(d)
	if c["dropped_by_scope"] != 1 || c["injected"] != 1 || c["candidates"] != 2 {
		t.Fatalf("counts = %v", c)
	}
	item := d["injected"].([]any)[0].(map[string]any)
	if item["scope_type"] != "session" || item["scope_id"] != "sess-a" {
		t.Fatalf("scope = %v", item)
	}
	if !strings.Contains(strings.Join(toStrings(d["query_scopes"]), ","), "session:sess-a") {
		t.Fatalf("query_scopes = %v", d["query_scopes"])
	}
	if d["entrypoint_index"] != true {
		t.Fatalf("file backend index should be reported: %v", d)
	}
	// Content is user data and must never ride on the decision.
	raw, _ := json.Marshal(got[0])
	if strings.Contains(string(raw), "Doudou") || strings.Contains(string(raw), "Mimi") {
		t.Fatalf("memory content leaked into the trace: %s", raw)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestInjectMemoriesTraceCap(t *testing.T) {
	memStore, err := store.NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxMemories = 2
	svc := NewService(memStore, nil, &countingEmbedder{}, cfg)
	ctx := context.Background()
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		if err := svc.Add(ctx, &domain.Memory{ID: id, Type: domain.MemoryTypeFact, Content: "the cat likes fish number " + id, Importance: 0.5, SessionID: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	cap := &decisionCapture{}
	_, _, _, err = svc.RetrieveAndInjectWithContextAndLogic(trace.WithSink(ctx, cap), "cat fish", domain.MemoryQueryContext{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	got := cap.injects()
	if len(got) != 1 {
		t.Fatalf("decisions = %d", len(got))
	}
	c := counts(roundTrip(t, got[0]))
	if c["injected"] != 2 || c["dropped_by_cap"] != 2 {
		t.Fatalf("counts = %v", c)
	}
}

func TestInjectMemoriesTraceZeroInjected(t *testing.T) {
	memStore, err := store.NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(memStore, nil, nil, nil)
	cap := &decisionCapture{}
	_, mems, _, err := svc.RetrieveAndInjectWithContextAndLogic(trace.WithSink(context.Background(), cap), "anything", domain.MemoryQueryContext{SessionID: "s"})
	if err != nil || len(mems) != 0 {
		t.Fatalf("mems=%v err=%v", mems, err)
	}
	got := cap.injects()
	if len(got) != 1 || !strings.Contains(got[0].Reason, "no memories injected") {
		t.Fatalf("decisions = %+v", got)
	}
}

func TestInjectMemoriesTraceRetrievalOff(t *testing.T) {
	memStore, err := store.NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.DisableRetrieval = true
	svc := NewService(memStore, nil, nil, cfg)
	cap := &decisionCapture{}
	_, _, _, _ = svc.RetrieveAndInjectWithContextAndLogic(trace.WithSink(context.Background(), cap), "q", domain.MemoryQueryContext{SessionID: "s"})
	got := cap.injects()
	if len(got) != 1 || !strings.Contains(got[0].Reason, "switched off") {
		t.Fatalf("decisions = %+v", got)
	}
}
