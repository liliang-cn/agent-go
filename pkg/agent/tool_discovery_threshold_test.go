package agent

import (
	"context"
	"fmt"
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

	tools := svc.collectAllAvailableTools(context.Background(), svc.agent)
	if !hasToolNamed(tools, "search_available_tools") {
		t.Error("a catalogue over the threshold must offer the search tool")
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
