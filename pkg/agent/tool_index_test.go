package agent

import (
	"context"
	"fmt"
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
//
// It has to happen at the registry: DeferLoading is what the tool search
// filters on, so a tool held back any other way is not merely absent from the
// schema — it is unfindable, and the model searches, finds nothing and falls
// back to the wrong tool.
func TestConfiguredRegistryToolsAreDeferredAndStillFindable(t *testing.T) {
	reg := NewToolRegistry()
	add := func(name string) {
		reg.Register(def(name, "does "+name), func(context.Context, map[string]interface{}) (interface{}, error) {
			return nil, nil
		}, CategoryCustom)
	}
	add("bash")
	add("knowledge_search")
	add("memory_save")

	if got := len(reg.ListForLLM("s1")); got != 3 {
		t.Fatalf("an unconfigured registry deferred something: %d of 3 sent", got)
	}

	reg.SetDeferredPatterns([]string{"knowledge_*", "memory_save"})
	sent := names(reg.ListForLLM("s1"))
	if len(sent) != 1 || sent[0] != "bash" {
		t.Fatalf("wrong tools survived: %v", sent)
	}
	// Retroactive: tools register in an order the caller does not control.
	if got := len(reg.ListDeferredTools()); got != 2 {
		t.Errorf("already-registered tools were not deferred: %d", got)
	}
	// And a tool registered afterwards obeys the same patterns.
	add("knowledge_graph_query")
	if !slices.Contains(names(reg.ListDeferredTools()), "knowledge_graph_query") {
		t.Error("a tool registered after the patterns were set was left eager")
	}

	// Findable. This is the property the first version lacked.
	found, err := reg.ExecuteToolSearch("knowledge", "bm25")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names(found), "knowledge_search") {
		t.Fatalf("a deferred tool could not be found by search: %v", names(found))
	}

	// And once found, callable for the rest of that session — otherwise the
	// turn after the search would take it away again.
	reg.ActivateForSession("s1", "knowledge_search")
	if !slices.Contains(names(reg.ListForLLM("s1")), "knowledge_search") {
		t.Error("a tool found by search was deferred again")
	}
	if slices.Contains(names(reg.ListForLLM("s2")), "knowledge_search") {
		t.Error("one session's lookup leaked into another")
	}
}

func names(defs []domain.ToolDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

// A large catalogue — CortexDB's MCP server alone is over a hundred tools,
// and was registered twice from two config files — must not become a page of
// descriptions on every turn. Each tool is named once, grouped by family, and
// the whole index stays within its budget.
func TestALargeIndexIsGroupedDeduplicatedAndBounded(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	var all []domain.ToolDefinition
	long := strings.Repeat("A long description that would cost tokens on every turn. ", 3)
	for i := 0; i < 120; i++ {
		d := def(fmt.Sprintf("mcp_cortexdb_tool_%03d", i), long)
		all = append(all, d, d) // the same server, registered twice
	}
	for i := 0; i < 30; i++ {
		all = append(all, def(fmt.Sprintf("knowledge_graph_op_%02d", i), long))
	}
	all = append(all, def("standalone", long))

	got := renderToolIndex(s, all, nil)
	if strings.Contains(got, "A long description") {
		t.Fatal("a large index still carries every description")
	}
	// Counted once each: 120, not the 240 the double registration listed.
	for _, want := range []string{"mcp_cortexdb_* (120 tools)", "knowledge_* (30 tools)", "tool_search_tool_bm25"} {
		if !strings.Contains(got, want) {
			t.Fatalf("index lacks %q:\n%s", want, got)
		}
	}
	if len(got) > indexBudget+1000 {
		t.Fatalf("index is %d bytes, budget %d", len(got), indexBudget)
	}
}

// Past the budget even the names go: families and counts are what is left.
func TestAnIndexPastItsBudgetKeepsFamiliesAndCounts(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	var all []domain.ToolDefinition
	for i := 0; i < 900; i++ {
		all = append(all, def(fmt.Sprintf("mcp_big_a_rather_long_operation_name_%04d", i), "x"))
	}
	got := renderToolIndex(s, all, nil)
	if !strings.Contains(got, "mcp_big_* (900 tools)") || strings.Contains(got, "operation_name_0001") {
		t.Fatalf("an over-budget index should list the family and its count only:\n%s", got)
	}
}

// A family whose everyday tools are in the schema must not read as out of
// reach. "mcp_noted_* (65 tools)" under "cannot call yet" sent a model to
// search for the calendar tools it already held, and it gave up when the
// search was refused.
func TestAFamilyPartlyInTheSchemaSaysSo(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	var all, sent []domain.ToolDefinition
	for i := 0; i < 65; i++ {
		all = append(all, def(fmt.Sprintf("mcp_noted_rare_operation_with_a_long_name_%02d", i), "x"))
	}
	for i := 0; i < 900; i++ {
		all = append(all, def(fmt.Sprintf("mcp_big_a_rather_long_operation_name_%04d", i), "x"))
	}
	for _, n := range []string{"mcp_noted_list_events", "mcp_noted_list_tasks"} {
		d := def(n, "x")
		all, sent = append(all, d), append(sent, d)
	}
	got := renderToolIndex(s, all, sent)
	if !strings.Contains(got, "mcp_noted_* (65 more; the other 2 mcp_noted_* tools are in your schema — call those directly)") {
		t.Fatalf("the noted family line does not say part of it is callable:\n%s", got)
	}
	if !strings.Contains(got, "mcp_big_* (900 tools)") {
		t.Fatalf("a family with nothing in the schema changed:\n%s", got)
	}
	if strings.Contains(got, "mcp_noted_list_events") {
		t.Fatalf("a tool in the schema is listed as hidden:\n%s", got)
	}
}
