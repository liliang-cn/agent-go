package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func hasToolNamed(defs []domain.ToolDefinition, want string) bool {
	for _, d := range defs {
		if d.Function.Name == want {
			return true
		}
	}
	return false
}

func registerNTools(t *testing.T, svc *Service, n int) {
	t.Helper()
	type args struct{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("probe_tool_%02d", i)
		svc.Register(NewTool(name, "probe "+name,
			func(ctx context.Context, _ *args) (any, error) { return "ok", nil }))
	}
}

// A small catalogue goes into the schema whole, and search_available_tools is
// not offered — there is nothing behind it to find, and offering it anyway is a
// standing invitation to spend a round shopping for tools. A benchmark run made
// 35 search calls against a catalogue that would have fitted in the schema.
func TestSmallCatalogueIsFlatAndHasNoSearchTool(t *testing.T) {
	t.Parallel()

	svc, err := New("flat-catalogue").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&constraintLLM{replies: []string{"done"}}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	registerNTools(t, svc, 5)

	tools := svc.collectAllAvailableTools(context.Background(), svc.agent)

	if hasToolNamed(tools, "search_available_tools") {
		t.Error("a catalogue below the threshold must not be offered the search tool")
	}
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("probe_tool_%02d", i)
		if !hasToolNamed(tools, name) {
			t.Errorf("%s missing from the schema; small catalogues go in whole", name)
		}
	}
}

// Above the threshold the layering still works: the search tool reappears so
// the model can reach what is no longer in the schema.
func TestLargeCatalogueLayersBehindSearch(t *testing.T) {
	t.Parallel()

	cfg := testAgentConfig(t.TempDir())
	cfg.Tooling.DiscoveryThreshold = 8

	svc, err := New("layered-catalogue").
		WithConfig(cfg).
		WithLLM(&constraintLLM{replies: []string{"done"}}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	registerNTools(t, svc, 40)
	svc.toolRegistry.SetDeferredPatterns([]string{"probe_tool_3*"})

	tools := svc.collectAllAvailableTools(context.Background(), svc.agent)
	if hasToolNamed(tools, "probe_tool_31") {
		t.Error("a deferred tool went into the schema")
	}
	if !hasToolNamed(tools, "search_available_tools") {
		t.Error("a catalogue that hides tools must offer the search tool")
	}
}

// Over the threshold with nothing hidden — every tool a registry tool, which
// layering never defers — there is nothing to search for. Offering the search
// anyway sent a 100-tool assistant searching for capabilities it could see it
// lacked, and its answer told the person it had "searched the tool catalog".
func TestLargeCatalogueWithNothingHiddenHasNoSearchTool(t *testing.T) {
	t.Parallel()

	cfg := testAgentConfig(t.TempDir())
	cfg.Tooling.DiscoveryThreshold = 8

	svc, err := New("wide-catalogue").
		WithConfig(cfg).
		WithLLM(&constraintLLM{replies: []string{"done"}}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	registerNTools(t, svc, 40)

	tools := svc.collectAllAvailableTools(context.Background(), svc.agent)
	if hasToolNamed(tools, "search_available_tools") {
		t.Error("nothing is hidden, so the search tool must not be offered")
	}
	if !hasToolNamed(tools, "probe_tool_39") {
		t.Error("every registry tool must stay in the schema")
	}
	prompt := svc.buildSystemPromptForTurn(context.Background(), svc.agent, nil, !offersToolSearch(tools)) +
		svc.toolIndex(context.Background(), svc.agent, svc.buildToolPreparationPolicy(context.Background()), tools)
	for _, leak := range []string{"search_available_tools", "Search-mode tool catalog", "search for an exact callable name", "cannot call yet"} {
		if strings.Contains(prompt, leak) {
			t.Errorf("prompt describes a tool search the turn does not offer (%q):\n%s", leak, prompt)
		}
	}
}

// The threshold is configurable, and a caller can turn layering off entirely.
func TestToolDiscoveryCanBeDisabled(t *testing.T) {
	t.Parallel()

	cfg := testAgentConfig(t.TempDir())
	cfg.Tooling.DiscoveryThreshold = 4
	cfg.Tooling.DisableToolSearch = true

	svc, err := New("no-discovery").
		WithConfig(cfg).
		WithLLM(&constraintLLM{replies: []string{"done"}}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	registerNTools(t, svc, 20)

	tools := svc.collectAllAvailableTools(context.Background(), svc.agent)
	if hasToolNamed(tools, "search_available_tools") {
		t.Error("DisableToolSearch must keep the search tool out of the schema")
	}
	if !hasToolNamed(tools, "probe_tool_19") {
		t.Error("with discovery off the whole catalogue must stay in the schema")
	}
}

// Red line: static tooling must not weaken the forbid-tools gate. A run whose
// constraints forbid tools gets zero tools no matter how the catalogue is laid
// out.
func TestForbidToolsStillYieldsZeroToolsUnderStaticPolicy(t *testing.T) {
	t.Parallel()

	llm := &constraintLLM{forbidTools: true, replies: []string{"Jupiter."}}
	svc, err := New("static-forbid").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	registerNTools(t, svc, 5)

	if _, err := svc.Ask(context.Background(), "Without using any tools, name the largest planet."); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := llm.maxToolsOffered(); got != 0 {
		t.Errorf("forbid-tools run was offered %d tools under the static policy; want 0", got)
	}
}

// Over the threshold, MCP tools named in EagerTools stay in the schema while
// the rest go behind search: a worker asked about tomorrow should not spend
// two model calls finding the calendar.
func TestEagerMCPToolsStayInTheSchemaWhenTheRestAreDeferred(t *testing.T) {
	t.Parallel()

	cfg := testAgentConfig(t.TempDir())
	cfg.Tooling.DiscoveryThreshold = 8
	cfg.Tooling.EagerTools = []string{"mcp_noted_list_*", "mcp_noted_get_overview"}

	svc, err := New("eager-mcp").
		WithConfig(cfg).
		WithLLM(&constraintLLM{replies: []string{"done"}}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	registerNTools(t, svc, 20)
	mcp := func(name string) domain.ToolDefinition {
		var d domain.ToolDefinition
		d.Type = "function"
		d.Function.Name = name
		d.Function.Description = name
		return d
	}
	svc.mcpService = &stubMCP{tools: []domain.ToolDefinition{
		mcp("mcp_noted_list_events"), mcp("mcp_noted_list_tasks"), mcp("mcp_noted_get_overview"),
		mcp("mcp_noted_delete_note"), mcp("mcp_other_thing"),
	}}

	tools := svc.collectAllAvailableTools(context.Background(), svc.agent)
	if !hasToolNamed(tools, "search_available_tools") {
		t.Fatal("over the threshold the rest must still be behind search")
	}
	for _, want := range []string{"mcp_noted_list_events", "mcp_noted_list_tasks", "mcp_noted_get_overview"} {
		if !hasToolNamed(tools, want) {
			t.Errorf("%s was deferred though EagerTools names it", want)
		}
	}
	for _, hidden := range []string{"mcp_noted_delete_note", "mcp_other_thing"} {
		if hasToolNamed(tools, hidden) {
			t.Errorf("%s reached the schema though nothing names it eager", hidden)
		}
	}
}
