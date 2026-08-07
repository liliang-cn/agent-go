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
