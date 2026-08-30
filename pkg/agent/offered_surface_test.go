package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// wireRecorder records what actually left for the provider — the tool list and
// the system prompt of every request — rather than what the framework believes
// it sent. The two disagreed: a proxy in front of the gateway measured 2.2x the
// input tokens of a comparable runtime on the same five questions, and the
// difference was schema and prompt bytes no caller had asked for. A test that
// asserts on the registry cannot see that; this one sits where the wire is.
type wireRecorder struct {
	mu             sync.Mutex
	toolNames      [][]string
	systemPrompts  []string
	extractedCalls int
}

func (w *wireRecorder) note(messages []domain.Message, tools []domain.ToolDefinition) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Function.Name)
	}
	system := ""
	if len(messages) > 0 && messages[0].Role == "system" {
		system = messages[0].Content
	}
	w.mu.Lock()
	w.toolNames = append(w.toolNames, names)
	w.systemPrompts = append(w.systemPrompts, system)
	w.mu.Unlock()
}

// firstRequest returns the tool names and system prompt of the first request of
// the run, which is the one every run pays for.
func (w *wireRecorder) firstRequest(t *testing.T) ([]string, string) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.toolNames) == 0 {
		t.Fatal("the run never reached the provider, so there is nothing to measure")
	}
	names := append([]string(nil), w.toolNames[0]...)
	sort.Strings(names)
	return names, w.systemPrompts[0]
}

func (w *wireRecorder) constraintExtractions() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.extractedCalls
}

func (w *wireRecorder) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "done", nil
}

func (w *wireRecorder) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (w *wireRecorder) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	w.note(messages, tools)
	return &domain.GenerationResult{Content: "done"}, nil
}

func (w *wireRecorder) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	w.note(messages, tools)
	return callback(&domain.GenerationResult{Content: "done"})
}

func (w *wireRecorder) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if strings.Contains(prompt, "report ONLY the constraints") {
		w.mu.Lock()
		w.extractedCalls++
		w.mu.Unlock()
	}
	return &domain.StructuredResult{Raw: `{"forbid_tools":false,"deliverables":[]}`, Valid: true}, nil
}

func (w *wireRecorder) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

// newFourToolService builds the service a plain caller gets: four tools of
// their own and no sub-agents.
func newFourToolService(t *testing.T, llm *wireRecorder, configure func(*Builder) *Builder) *Service {
	t.Helper()
	b := New("four-tools").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm)
	if configure != nil {
		b = configure(b)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	registerNTools(t, svc, 4)
	return svc
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// The three delegation tools were registered unconditionally at Service
// construction, so a caller who registered four tools and configured zero
// sub-agents was billed for nine tool schemas on every single request — and the
// model could call them. `delegate_to_subagent` with nothing configured re-runs
// a clone of the same agent with the same tools; that is not delegation to
// anything, it is a recursive copy paid for on every turn.
func TestAServiceWithNoSubagentsDoesNotOfferTheDelegationTools(t *testing.T) {
	t.Parallel()

	llm := &wireRecorder{}
	svc := newFourToolService(t, llm, nil)

	if _, err := svc.Run(context.Background(), "Say hello."); err != nil {
		t.Fatalf("run: %v", err)
	}

	names, _ := llm.firstRequest(t)
	for _, unwanted := range []string{"delegate_to_subagent", "delegate_async", "subagent_send_message"} {
		if containsName(names, unwanted) {
			t.Errorf("%s was offered to a service with no sub-agents; offered set: %v", unwanted, names)
		}
	}
	for i := 0; i < 4; i++ {
		want := fmt.Sprintf("probe_tool_%02d", i)
		if !containsName(names, want) {
			t.Errorf("%s missing; the caller's own tools must always be offered. offered set: %v", want, names)
		}
	}
	t.Logf("a 4-tool no-subagent service offers %d tools: %v", len(names), names)
}

// Configuring sub-agents is what makes delegation mean something, so that is
// what brings the tools back.
func TestConfiguredSubagentsBringTheDelegationToolsBack(t *testing.T) {
	t.Parallel()

	llm := &wireRecorder{}
	svc := newFourToolService(t, llm, func(b *Builder) *Builder {
		return b.WithSubagents(SubagentSpec{
			Name:         "researcher",
			Description:  "Searches and summarises.",
			Instructions: "You research topics and return a concise brief.",
		})
	})

	if _, err := svc.Run(context.Background(), "Say hello."); err != nil {
		t.Fatalf("run: %v", err)
	}

	names, _ := llm.firstRequest(t)
	if !containsName(names, "delegate_to_subagent") {
		t.Errorf("delegate_to_subagent must be offered once sub-agents exist; offered set: %v", names)
	}
	if !containsName(names, "task") {
		t.Errorf("the named sub-agent tool must be offered; offered set: %v", names)
	}
}

// Not offering a tool to the model is not the same as removing it. The handlers
// stay in the registry so an internal caller — PTC's callTool, a host driving
// dispatch by name — can still reach them, which is the same bargain
// search_available_tools already strikes.
func TestTheDelegationToolsStayInTheRegistryWhenTheyAreNotOffered(t *testing.T) {
	t.Parallel()

	llm := &wireRecorder{}
	svc := newFourToolService(t, llm, nil)

	for _, name := range []string{"delegate_to_subagent", "delegate_async", "subagent_send_message"} {
		if !svc.toolRegistry.Has(name) {
			t.Errorf("%s must stay registered even when it is not offered to the model", name)
		}
	}
}

// The length anchors cap final answers at 100 words. That is a real opinion
// about output, and a caller whose own prompt demands a citation per statement
// is fighting it without ever having opted in.
func TestTheLengthLimitsAreInTheSystemPromptByDefault(t *testing.T) {
	t.Parallel()

	llm := &wireRecorder{}
	svc := newFourToolService(t, llm, nil)

	if _, err := svc.Run(context.Background(), "Say hello."); err != nil {
		t.Fatalf("run: %v", err)
	}

	_, system := llm.firstRequest(t)
	t.Logf("default system prompt is %d bytes", len(system))
	if !strings.Contains(system, "Length limits:") {
		t.Error("the length anchors are the documented default and must still be sent")
	}
}

func TestTheLengthLimitsCanBeTurnedOffByTheCaller(t *testing.T) {
	t.Parallel()

	llm := &wireRecorder{}
	svc := newFourToolService(t, llm, func(b *Builder) *Builder {
		return b.WithLengthLimits(false)
	})

	if _, err := svc.Run(context.Background(), "Say hello."); err != nil {
		t.Fatalf("run: %v", err)
	}

	_, system := llm.firstRequest(t)
	if strings.Contains(system, "Length limits:") {
		t.Errorf("WithLengthAnchors(false) must keep the cap out of the prompt; got:\n%s", system)
	}
	if strings.TrimSpace(system) == "" {
		t.Error("turning off one section must not empty the whole system prompt")
	}
}

// Constraint extraction is a structured model call made once per run, before
// the first real turn. It is on by default and a caller can already switch it
// off per run; this pins both halves of that so neither drifts.
func TestConstraintExtractionIsOnByDefaultAndTheCallerCanTurnItOff(t *testing.T) {
	t.Parallel()

	llm := &wireRecorder{}
	svc := newFourToolService(t, llm, nil)

	if _, err := svc.Run(context.Background(), "Say hello."); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := llm.constraintExtractions(); got != 1 {
		t.Fatalf("a default run makes exactly one extraction call, got %d", got)
	}

	if _, err := svc.Run(context.Background(), "Say hello again.", WithConstraintExtraction(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := llm.constraintExtractions(); got != 1 {
		t.Errorf("WithConstraintExtraction(false) must make no extra model call, total is now %d", got)
	}
}

// The gate has a back door: constraint extraction is handed a catalogue of tool
// names and reports which one satisfies each deliverable, and
// ensureRequiredToolsVisible then puts that tool back into the schema whatever
// else hid it. A withheld tool must therefore not be in the catalogue either.
func TestTheConstraintCatalogueOmitsToolsTheRunWillNeverBeOffered(t *testing.T) {
	t.Parallel()

	llm := &wireRecorder{}
	svc := newFourToolService(t, llm, nil)

	for _, entry := range svc.constraintToolCatalog() {
		if subagentDelegationToolNames[entry.Name] {
			t.Errorf("%s is in the extraction catalogue but will never be offered", entry.Name)
		}
	}

	llmWith := &wireRecorder{}
	svcWith := newFourToolService(t, llmWith, func(b *Builder) *Builder { return b.WithDelegation(true) })
	found := false
	for _, entry := range svcWith.constraintToolCatalog() {
		if entry.Name == "delegate_to_subagent" {
			found = true
		}
	}
	if !found {
		t.Error("a service that does offer delegation must still describe it to the extraction")
	}
}
