package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// blockThenAnswerLLM gives up on the first turn (task_blocked) and, if the
// runtime pushes back, answers the computable part on the second.
type blockThenAnswerLLM struct {
	mu       sync.Mutex
	calls    int
	sawNudge bool
	answer   string
}

func (b *blockThenAnswerLLM) next(messages []domain.Message) *domain.GenerationResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range messages {
		if strings.Contains(m.Content, "no tool that can perform") {
			b.sawNudge = true
		}
	}
	b.calls++
	if b.calls == 1 {
		return &domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID:   "call-block",
				Type: "function",
				Function: domain.FunctionCall{
					Name:      "task_blocked",
					Arguments: map[string]interface{}{"blocker": "I have no email tool."},
				},
			}},
		}
	}
	return &domain.GenerationResult{Content: b.answer}
}

func (b *blockThenAnswerLLM) Generate(ctx context.Context, p string, o *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (b *blockThenAnswerLLM) Stream(ctx context.Context, p string, o *domain.GenerationOptions, cb func(string)) error {
	return nil
}
func (b *blockThenAnswerLLM) GenerateWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return b.next(m), nil
}
func (b *blockThenAnswerLLM) StreamWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(b.next(m))
}
func (b *blockThenAnswerLLM) GenerateStructured(ctx context.Context, p string, s interface{}, o *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if strings.Contains(p, "report ONLY the constraints") {
		return &domain.StructuredResult{
			Raw:   `{"forbid_tools":false,"deliverables":[{"kind":"email","description":"email me the interest"}]}`,
			Valid: true,
		}, nil
	}
	return structuredJSON(map[string]interface{}{}), nil
}
func (b *blockThenAnswerLLM) RecognizeIntent(ctx context.Context, r string) (*domain.IntentResult, error) {
	return nil, nil
}

// "Work out the interest and email it to me", run against an agent with no mail
// tool, has a right answer and a wrong one. The right answer is the figure plus
// a plain statement that it could not be sent. The wrong answer is task_blocked,
// which scores nothing and tells the user nothing they can use — and models
// reliably picked it.
//
// The runtime knows both facts (a delivery was asked for; no tool can do it), so
// it redirects once instead of hoping the prompt covers it.
func TestBlockedRedirectsToPartialAnswerWhenDeliveryToolMissing(t *testing.T) {
	t.Parallel()

	llm := &blockThenAnswerLLM{
		answer: "The interest is $1,200. I could not email it to you: this agent has no mail tool available.",
	}
	svc, err := New("partial-answer").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	res, err := svc.Run(context.Background(),
		"Work out the interest on $10,000 at 12% for one year and email it to me.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	llm.mu.Lock()
	sawNudge := llm.sawNudge
	llm.mu.Unlock()

	if !sawNudge {
		t.Error("the model was never told the delivery tool is missing")
	}
	if res.Blocked {
		t.Errorf("run should have completed with a partial answer, got blocked: %q", res.Text())
	}
	if !strings.Contains(res.Text(), "$1,200") {
		t.Errorf("the computable part is missing from the answer: %q", res.Text())
	}
	if !strings.Contains(strings.ToLower(res.Text()), "email") {
		t.Errorf("the answer does not say what could not be delivered: %q", res.Text())
	}
}

// The nudge is once per run: a model that still has nothing to say must be able
// to stop rather than loop.
func TestDeliverableRedirectHappensAtMostOnce(t *testing.T) {
	t.Parallel()

	r := &Runtime{cfg: DefaultRunConfig()}
	constraints := RunConstraints{
		Deliverables: []DeliverableRequirement{{Kind: "email", Description: "send it"}},
	}
	r.cfg.resolvedConstraints = &constraints

	msgs := []domain.Message{}
	state := newQueryLoopState("goal", msgs, 5)

	if !r.redirectBlockedToPartialAnswer(&msgs, state) {
		t.Fatal("first block should be redirected")
	}
	if r.redirectBlockedToPartialAnswer(&msgs, state) {
		t.Fatal("second block must be allowed through, or the run cannot ever stop")
	}
}

// A run whose delivery tool IS available is not redirected — blocking there may
// be perfectly correct, and the delivery contract already covers the case.
func TestNoRedirectWhenDeliveryToolExists(t *testing.T) {
	t.Parallel()

	missing := undeliverableRequirements(
		RunConstraints{Deliverables: []DeliverableRequirement{{Kind: "email"}}},
		[]string{"send_email"},
	)
	if len(missing) != 0 {
		t.Errorf("a run with a mail tool owes nothing to the fallback, got %+v", missing)
	}
}

func TestDeliverableBlockMustCarryWork(t *testing.T) {
	lint := DeliverableBlockMustCarryWork()
	ctx := LintContext{
		Deliverables:   []DeliverableRequirement{{Kind: "email", Description: "email the result"}},
		AvailableTools: []string{"fs_read"},
	}

	if ok, reason := lint.Check("I cannot send the email.", ctx); ok {
		t.Error("a bare refusal must be rejected")
	} else if reason == "" {
		t.Error("expected a reason telling the model what to do instead")
	}

	if ok, reason := lint.Check(
		"The interest is $1,200 for the year. I was not able to email it because no mail tool is available here.",
		ctx); !ok {
		t.Errorf("a blocker carrying the computed result should pass, got %q", reason)
	}

	// Nothing owed, or the tool exists: not this lint's business.
	if ok, _ := lint.Check("nope", LintContext{}); !ok {
		t.Error("a run with no deliverables must pass")
	}
	if ok, _ := lint.Check("nope", LintContext{
		Deliverables:   []DeliverableRequirement{{Kind: "email"}},
		AvailableTools: []string{"send_email"},
	}); !ok {
		t.Error("a run whose mail tool exists must pass")
	}
}
