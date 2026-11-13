package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// traceDecisionSink is a trace sink that keeps the decisions it hears.
type traceDecisionSink struct {
	mu        sync.Mutex
	decisions []trace.Decision
}

func (d *traceDecisionSink) ModelStart(context.Context, string, string)     {}
func (d *traceDecisionSink) ModelEnd(context.Context, string, trace.Record) {}
func (d *traceDecisionSink) Step(context.Context, trace.Record)             {}
func (d *traceDecisionSink) Decision(_ context.Context, dec trace.Decision) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.decisions = append(d.decisions, dec)
}

func (d *traceDecisionSink) named(what string) []trace.Decision {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []trace.Decision
	for _, dec := range d.decisions {
		if dec.What == what {
			out = append(out, dec)
		}
	}
	return out
}

func registerInstallerTool(t *testing.T, svc *Service, name string) {
	t.Helper()
	svc.AddTool(name, "Installs a thing.", map[string]interface{}{"type": "object"},
		func(context.Context, map[string]interface{}) (interface{}, error) { return "ok", nil })
}

// A live run asked for a Mermaid render, searched, was told nothing matched
// and went to bash: the install tools were deferred behind the same search
// and "mermaid" matches none of their descriptions.
func TestToolSearchOffersInstallablesWhenNothingInstalledMatches(t *testing.T) {
	t.Parallel()

	var asked string
	svc := newFourToolService(t, &wireRecorder{}, func(b *Builder) *Builder {
		return b.WithInstallableSearch(func(_ context.Context, q string) []Installable {
			asked = q
			return []Installable{{
				Kind: "mcp", Name: "mermaid-render", Description: "Renders Mermaid diagrams.",
				InstallTool: "add_thing", InstallArgs: map[string]any{"name": "mermaid-render", "command": "npx"},
				Needs: []string{"MERMAID_TOKEN"},
			}}
		})
	})
	registerInstallerTool(t, svc, "add_thing")
	svc.SetSessionID("s1")

	rec := &traceDecisionSink{}
	out, err := svc.SearchAndExecute(trace.WithSink(context.Background(), rec), "Mermaid Diagram", "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if asked != "mermaid diagram" {
		t.Errorf("host was asked %q", asked)
	}
	res, ok := out.(map[string]interface{})
	if !ok {
		t.Fatalf("want a map with the installables, got %T: %v", out, out)
	}
	if tools, _ := res["tools"].([]map[string]interface{}); tools == nil || len(tools) != 0 {
		t.Errorf("tools should be an empty list, got %#v", res["tools"])
	}
	ins, _ := res["installable"].([]Installable)
	if len(ins) != 1 || ins[0].Name != "mermaid-render" || ins[0].InstallTool != "add_thing" || ins[0].Needs[0] != "MERMAID_TOKEN" {
		t.Errorf("installable = %#v", res["installable"])
	}
	if res["note"] != toolSearchInstallableNote {
		t.Errorf("note = %v", res["note"])
	}
	if !svc.toolRegistry.IsActivatedForSession("s1", "add_thing") {
		t.Error("the install tool was not activated for the session")
	}
	got := rec.named("offer_installables")
	if len(got) != 1 {
		t.Fatalf("want one offer_installables decision, got %d", len(got))
	}
	if got[0].Detail["count"] != 1 {
		t.Errorf("decision detail = %v", got[0].Detail)
	}
}

func TestToolSearchReturnsInstalledMatchesBesideInstallables(t *testing.T) {
	t.Parallel()

	svc := newFourToolService(t, &wireRecorder{}, func(b *Builder) *Builder {
		return b.WithInstallableSearch(func(context.Context, string) []Installable {
			return []Installable{{Kind: "skill", Name: "charts", InstallTool: "add_thing"}}
		})
	})
	registerInstallerTool(t, svc, "add_thing")
	registerInstallerTool(t, svc, "zebra_counter")

	plain := newFourToolService(t, &wireRecorder{}, nil)
	registerInstallerTool(t, plain, "add_thing")
	registerInstallerTool(t, plain, "zebra_counter")
	want, err := plain.SearchAndExecute(context.Background(), "zebra_counter", "", "")
	if err != nil {
		t.Fatalf("plain search: %v", err)
	}

	out, err := svc.SearchAndExecute(context.Background(), "zebra_counter", "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	res, ok := out.(map[string]interface{})
	if !ok {
		t.Fatalf("got %T", out)
	}
	if !reflect.DeepEqual(res["tools"], want) {
		t.Errorf("installed matches changed:\n got %v\nwant %v", res["tools"], want)
	}
	if ins, _ := res["installable"].([]Installable); len(ins) != 1 {
		t.Errorf("installable = %v", res["installable"])
	}
}

// With no search set, or one that finds nothing, a search answers exactly as
// it did before there was such a thing.
func TestToolSearchIsUnchangedWithoutInstallables(t *testing.T) {
	t.Parallel()

	plain := newFourToolService(t, &wireRecorder{}, nil)
	registerInstallerTool(t, plain, "zebra_counter")
	wantHit, _ := plain.SearchAndExecute(context.Background(), "zebra_counter", "", "")
	wantMiss, _ := plain.SearchAndExecute(context.Background(), "qqqnothingqqq", "", "")
	if _, ok := wantHit.([]map[string]interface{}); !ok {
		t.Fatalf("plain hit is %T", wantHit)
	}
	if wantMiss != toolSearchNoMatches {
		t.Fatalf("plain miss = %v", wantMiss)
	}

	cases := map[string]InstallableSearch{
		"nothing found": func(context.Context, string) []Installable { return nil },
		"install tool not registered": func(context.Context, string) []Installable {
			return []Installable{{Kind: "mcp", Name: "x", InstallTool: "no_such_tool"}}
		},
		"unnamed": func(context.Context, string) []Installable {
			return []Installable{{Kind: "mcp", InstallTool: "zebra_counter"}}
		},
		"panics": func(context.Context, string) []Installable { panic("registry") },
	}
	for name, fn := range cases {
		svc := newFourToolService(t, &wireRecorder{}, nil)
		registerInstallerTool(t, svc, "zebra_counter")
		svc.SetInstallableSearch(fn)
		rec := &traceDecisionSink{}
		ctx := trace.WithSink(context.Background(), rec)
		hit, err := svc.SearchAndExecute(ctx, "zebra_counter", "", "")
		if err != nil || !reflect.DeepEqual(hit, wantHit) {
			t.Errorf("%s: hit = %v (%v), want %v", name, hit, err, wantHit)
		}
		miss, err := svc.SearchAndExecute(ctx, "qqqnothingqqq", "", "")
		if err != nil || miss != wantMiss {
			t.Errorf("%s: miss = %v (%v)", name, miss, err)
		}
		if n := len(rec.named("offer_installables")); n != 0 {
			t.Errorf("%s: %d offer_installables decisions for nothing offered", name, n)
		}
	}
}

func TestToolSearchDoesNotLookForInstallablesForAnInstructionOrAScope(t *testing.T) {
	t.Parallel()

	calls := 0
	svc := newFourToolService(t, &wireRecorder{}, func(b *Builder) *Builder {
		return b.WithInstallableSearch(func(context.Context, string) []Installable {
			calls++
			return []Installable{{Kind: "mcp", Name: "x", InstallTool: "add_thing"}}
		})
	})
	registerInstallerTool(t, svc, "add_thing")

	out, err := svc.SearchAndExecute(context.Background(), "qqqnothingqqq", "do the thing", "")
	if err != nil || out != toolSearchNoMatches {
		t.Errorf("instruction: %v (%v)", out, err)
	}
	out, err = svc.SearchAndExecute(context.Background(), "qqqnothingqqq", "", "mcp_some_server")
	if err != nil || out != toolSearchNoMatches {
		t.Errorf("scope: %v (%v)", out, err)
	}
	if calls != 0 {
		t.Errorf("the host was asked %d times", calls)
	}
}

func TestInstallableSearchIsGivenADeadline(t *testing.T) {
	t.Parallel()

	svc := newFourToolService(t, &wireRecorder{}, nil)
	svc.SetInstallableSearch(func(ctx context.Context, _ string) []Installable {
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > installableSearchTimeout {
			t.Errorf("deadline = %v, %v", dl, ok)
		}
		return nil
	})
	if _, err := svc.SearchAndExecute(context.Background(), "anything", "", ""); err != nil {
		t.Fatalf("search: %v", err)
	}

	// A caller that has already given up does not wait for the host.
	block := make(chan struct{})
	defer close(block)
	svc.SetInstallableSearch(func(context.Context, string) []Installable {
		<-block
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		_, _ = svc.SearchAndExecute(ctx, "qqqnothingqqq", "", "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("search waited on a host that ignores its context")
	}
}

// The second live run searched with tool_search_tool_bm25, not
// search_available_tools: both are the model asking for a tool by meaning.
func TestBM25ToolSearchOffersInstallables(t *testing.T) {
	t.Parallel()

	svc := newFourToolService(t, &wireRecorder{}, nil)
	registerInstallerTool(t, svc, "add_thing")
	call := func(name, query string) interface{} {
		t.Helper()
		res, err := svc.executeToolCalls(context.Background(), svc.agent, nil, []domain.ToolCall{{
			ID: "c1", Type: "function",
			Function: domain.FunctionCall{Name: name, Arguments: map[string]interface{}{"query": query}},
		}})
		if err != nil || len(res) != 1 {
			t.Fatalf("%s: %v %v", name, res, err)
		}
		return res[0].Result
	}

	before := call("tool_search_tool_bm25", "qqqnothingqqq")
	if _, ok := before.(domain.ToolSearchResult); !ok {
		t.Fatalf("without a search set the result is %T", before)
	}

	svc.SetInstallableSearch(func(context.Context, string) []Installable {
		return []Installable{{Kind: "mcp", Name: "mermaid-render", InstallTool: "add_thing", InstallArgs: map[string]any{"name": "m"}}}
	})
	if _, ok := call("tool_search_tool_regex", "qqqnothingqqq").(domain.ToolSearchResult); !ok {
		t.Error("a regex search does not look for installables")
	}
	got, ok := call("tool_search_tool_bm25", "mermaid diagram").(toolSearchWithInstallables)
	if !ok {
		t.Fatalf("bm25 result has no installables")
	}
	if len(got.Installable) != 1 || got.Note != toolSearchInstallableNote {
		t.Errorf("got %+v", got)
	}
	// It still reads as a tool search result, with the install tool in it.
	raw, _ := json.Marshal(got)
	var plain domain.ToolSearchResult
	if err := json.Unmarshal(raw, &plain); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range plain.ToolReferences {
		found = found || ref.ToolName == "add_thing"
	}
	if !found {
		t.Errorf("the install tool is not among the references: %s", raw)
	}
}
