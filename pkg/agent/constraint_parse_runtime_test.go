package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// proseFirstLLM answers the first extraction attempt with prose (what the real
// gateway did) and only obeys on the retry.
type proseFirstLLM struct {
	constraintLLM
	structuredCalls int
}

func (p *proseFirstLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if !strings.Contains(prompt, "report ONLY the constraints") {
		return structuredJSON(map[string]interface{}{}), nil
	}
	p.mu.Lock()
	p.structuredCalls++
	n := p.structuredCalls
	p.mu.Unlock()
	if n == 1 {
		// Verbatim shape of the failure seen on the real gateway: the strict
		// parse reported `invalid character 'B' looking for beginning of value`.
		return &domain.StructuredResult{
			Raw:   "Based on the user request, the user has forbidden tool use.",
			Valid: false,
		}, nil
	}
	return &domain.StructuredResult{
		Raw:   `{"forbid_tools":true,"deliverables":[]}`,
		Valid: true,
	}, nil
}

// A prose reply used to mean the constraint was silently dropped and the gate
// never closed. It must now cost a retry, not the constraint.
func TestConstraintExtractionRetriesOnProseReply(t *testing.T) {
	t.Parallel()

	llm := &proseFirstLLM{constraintLLM: constraintLLM{replies: []string{"Jupiter."}}}
	svc, err := New("prose-retry").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if _, err := svc.Ask(context.Background(), "Without using any tools, name the largest planet."); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	llm.mu.Lock()
	calls := llm.structuredCalls
	llm.mu.Unlock()
	if calls != 2 {
		t.Errorf("expected one retry after the prose reply, got %d extraction calls", calls)
	}
	if got := llm.maxToolsOffered(); got != 0 {
		t.Errorf("the retry recovered the constraint but %d tools were still offered", got)
	}
}

type fencedLLM struct {
	constraintLLM
	structuredCalls int
}

func (f *fencedLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if !strings.Contains(prompt, "report ONLY the constraints") {
		return structuredJSON(map[string]interface{}{}), nil
	}
	f.mu.Lock()
	f.structuredCalls++
	f.mu.Unlock()
	return &domain.StructuredResult{
		Raw:   "```json\n{\"forbid_tools\":true,\"deliverables\":[]}\n```",
		Valid: false,
	}, nil
}

// A fenced reply must parse on the first attempt — no retry, no lost constraint.
func TestConstraintExtractionAcceptsFencedReply(t *testing.T) {
	t.Parallel()

	llm := &fencedLLM{constraintLLM: constraintLLM{replies: []string{"Jupiter."}}}
	svc, err := New("fenced").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if _, err := svc.Ask(context.Background(), "Without using any tools, name the largest planet."); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	llm.mu.Lock()
	calls := llm.structuredCalls
	llm.mu.Unlock()
	if calls != 1 {
		t.Errorf("a fenced reply should parse first time, got %d calls", calls)
	}
	if got := llm.maxToolsOffered(); got != 0 {
		t.Errorf("fenced constraint not honoured; %d tools offered", got)
	}
}

// catalogLLM records the extraction prompt so a test can assert on what the
// extraction was actually shown.
type catalogLLM struct {
	constraintLLM
	prompt string
}

func (c *catalogLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if !strings.Contains(prompt, "report ONLY the constraints") {
		return structuredJSON(map[string]interface{}{}), nil
	}
	c.mu.Lock()
	c.prompt = prompt
	c.mu.Unlock()
	return &domain.StructuredResult{
		Raw:   `{"forbid_tools":false,"deliverables":[],"requested_actions":[]}`,
		Valid: true,
	}, nil
}

// The whole design rests on the extraction being able to name a real tool: it
// is the only place in the system that knows what "set a reminder" means for
// THIS run's tool names. If the catalog stops reaching the prompt, every
// requested-action contract silently degrades to "no tool can do it".
func TestConstraintExtractionSeesTheToolCatalog(t *testing.T) {
	t.Parallel()

	llm := &catalogLLM{constraintLLM: constraintLLM{replies: []string{"ok"}}}
	svc, err := New("catalog").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	svc.AddTool("set_reminder", "Create a reminder for the user at a given time. Extra detail ignored.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(context.Context, map[string]interface{}) (interface{}, error) { return "ok", nil })

	if _, err := svc.Ask(context.Background(), "Set a reminder to drink water."); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	llm.mu.Lock()
	prompt := llm.prompt
	llm.mu.Unlock()

	if !strings.Contains(prompt, "AVAILABLE TOOLS") {
		t.Fatalf("extraction prompt carried no tool catalog:\n%s", prompt)
	}
	if !strings.Contains(prompt, "- set_reminder: Create a reminder for the user at a given time") {
		t.Errorf("the registered tool is missing from the catalog:\n%s", prompt)
	}
	// One line per tool: the extraction runs on every task, so a full schema
	// dump would make the precondition check cost more than the work it guards.
	if strings.Contains(prompt, "Extra detail ignored") {
		t.Errorf("catalog description was not trimmed to one line:\n%s", prompt)
	}
}

// A satisfied_by naming a tool nobody registered can never be called, so a lint
// waiting for it would turn into a guaranteed block. Clear it instead.
func TestPruneUnknownToolsDropsInventedPicks(t *testing.T) {
	t.Parallel()

	catalog := []toolCatalogEntry{{Name: "set_reminder"}}
	got := pruneUnknownTools(RunConstraints{
		Deliverables: []DeliverableRequirement{
			{Kind: "email", SatisfiedBy: "send_email"},
		},
		RequestedActions: []RequestedAction{
			{Kind: "reminder", SatisfiedBy: "set_reminder"},
			{Kind: "calendar", SatisfiedBy: "calendar_api_v2"},
		},
	}, catalog)

	if got.Deliverables[0].SatisfiedBy != "" {
		t.Errorf("unregistered mail tool survived pruning: %q", got.Deliverables[0].SatisfiedBy)
	}
	if got.RequestedActions[0].SatisfiedBy != "set_reminder" {
		t.Errorf("a real tool pick was pruned: %q", got.RequestedActions[0].SatisfiedBy)
	}
	if got.RequestedActions[1].SatisfiedBy != "" {
		t.Errorf("invented tool survived pruning: %q", got.RequestedActions[1].SatisfiedBy)
	}
}
