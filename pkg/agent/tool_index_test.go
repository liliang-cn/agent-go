package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func def(name, desc string) domain.ToolDefinition {
	return domain.ToolDefinition{
		Type:     "function",
		Function: domain.ToolFunction{Name: name, Description: desc},
	}
}

// The index is the half of tool discovery that was missing: deferring a tool
// told the model nothing, so a hundred capabilities could sit behind a search
// the model had no reason to run.
func TestTheIndexNamesWhatIsHidden(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	sent := []domain.ToolDefinition{def("bash", "Run a command.")}
	all := []domain.ToolDefinition{
		def("bash", "Run a command."),
		def("knowledge_search", "Search durable knowledge.\nLong guidance follows here."),
		def("mcp_yfinance_get_stock_quote", "Get a quote. Use it for prices."),
	}

	got := renderToolIndex(s, all, sent)
	if !strings.Contains(got, "knowledge_search") || !strings.Contains(got, "mcp_yfinance_get_stock_quote") {
		t.Fatalf("the hidden tools are not named:\n%s", got)
	}
	if strings.Contains(got, "\n- bash ") {
		t.Error("a tool that is already in the schema was listed again")
	}
	// One line each: the paragraph of guidance belongs in the schema the
	// search returns, not in a list whose job is to say the tool exists.
	if strings.Contains(got, "Long guidance follows here") {
		t.Error("the whole description went into the index")
	}
	if !strings.Contains(got, "Search durable knowledge.") {
		t.Errorf("the opening sentence was lost:\n%s", got)
	}
	// The instruction has to say what to do about it, or the list is trivia.
	for _, want := range []string{"tool_search_tool_bm25", "not in your schema"} {
		if !strings.Contains(got, want) {
			t.Errorf("the index does not say how to reach them: missing %q", want)
		}
	}
}

// A tool on the list exists. A model that reads the list and then reports the
// capability as unavailable has been told the opposite of the truth.
func TestTheIndexSaysTheseToolsExist(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	got := renderToolIndex(s, []domain.ToolDefinition{def("a", "A."), def("b", "B.")}, []domain.ToolDefinition{def("a", "A.")})
	if !strings.Contains(got, "it exists") {
		t.Errorf("nothing warns against reporting a listed tool as missing:\n%s", got)
	}
}

// Nothing hidden, nothing said.
func TestNoIndexWhenNothingIsHidden(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	tools := []domain.ToolDefinition{def("a", "A."), def("b", "B.")}
	if got := renderToolIndex(s, tools, tools); got != "" {
		t.Errorf("an index appeared with nothing to index: %q", got)
	}
}

// The prefix has to be byte-stable across turns or every request misses the
// provider's prompt cache — which would cost more than the schemas it saves.
func TestTheIndexIsStableAcrossTurns(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	all := []domain.ToolDefinition{def("z", "Z."), def("a", "A."), def("m", "M.")}
	first := renderToolIndex(s, all, nil)
	shuffled := []domain.ToolDefinition{all[1], all[2], all[0]}
	if second := renderToolIndex(s, shuffled, nil); first != second {
		t.Error("the index changed when the map iterated in a different order")
	}
	if strings.Index(first, "- a ") > strings.Index(first, "- z ") {
		t.Error("the index is not sorted")
	}
}

// Switching it off has to restore exactly the old behaviour.
func TestTheIndexCanBeTurnedOff(t *testing.T) {
	off := false
	s := &Service{cfg: &config.Config{Tooling: config.ToolingConfig{IndexDeferredTools: &off}}}
	if got := renderToolIndex(s, []domain.ToolDefinition{def("a", "A."), def("b", "B.")}, []domain.ToolDefinition{def("a", "A.")}); got != "" {
		t.Errorf("the index was rendered with indexing off: %q", got)
	}
}

// An install says which of its own tools it would rather look up. Nothing is
// deferred by default, because that would change every existing agent.
func TestConfiguredRegistryToolsAreDeferred(t *testing.T) {
	plain := &Service{cfg: &config.Config{}, toolRegistry: NewToolRegistry()}
	tools := []domain.ToolDefinition{def("bash", "b"), def("knowledge_search", "k"), def("memory_save", "m")}
	if got := plain.deferConfiguredRegistryTools(tools, "s1"); len(got) != 3 {
		t.Fatalf("an unconfigured install deferred %d tools; it must defer none", 3-len(got))
	}

	s := &Service{
		cfg:          &config.Config{Tooling: config.ToolingConfig{DeferTools: []string{"knowledge_*", "memory_save"}}},
		toolRegistry: NewToolRegistry(),
	}
	got := s.deferConfiguredRegistryTools(tools, "s1")
	if len(got) != 1 || got[0].Function.Name != "bash" {
		t.Fatalf("wrong tools survived: %+v", names(got))
	}

	// A tool the session has already looked up stays callable, or every turn
	// after the search would take it away again.
	s.toolRegistry.ActivateForSession("s1", "knowledge_search")
	got = s.deferConfiguredRegistryTools(tools, "s1")
	if len(got) != 2 || !slices.Contains(names(got), "knowledge_search") {
		t.Fatalf("a tool found by search was deferred again: %+v", names(got))
	}
	// And only for that session.
	if other := s.deferConfiguredRegistryTools(tools, "s2"); len(other) != 1 {
		t.Errorf("one session's lookup leaked into another: %+v", names(other))
	}
}

func names(defs []domain.ToolDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}
