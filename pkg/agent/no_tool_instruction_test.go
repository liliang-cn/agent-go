package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func toolNamesOf(defs []domain.ToolDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

// constraintLLM answers the constraint-extraction call with whatever the test
// asks for, and records how many tools the runtime attached to every ordinary
// model call. That count is the actual contract under test: "forbidden" has to
// mean the tools were never offered, not that the model was asked nicely.
type constraintLLM struct {
	mu           sync.Mutex
	forbidTools  bool
	deliverables string // raw JSON array body, "" for none
	replies      []string
	replyIdx     int
	toolsOffered []int
	extractCalls int
}

func (c *constraintLLM) noteTools(tools []domain.ToolDefinition) {
	c.mu.Lock()
	c.toolsOffered = append(c.toolsOffered, len(tools))
	c.mu.Unlock()
}

func (c *constraintLLM) maxToolsOffered() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	max := 0
	for _, n := range c.toolsOffered {
		if n > max {
			max = n
		}
	}
	return max
}

func (c *constraintLLM) modelCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.toolsOffered)
}

func (c *constraintLLM) nextReply() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.replies) == 0 {
		return "done"
	}
	if c.replyIdx >= len(c.replies) {
		return c.replies[len(c.replies)-1]
	}
	r := c.replies[c.replyIdx]
	c.replyIdx++
	return r
}

func (c *constraintLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return c.nextReply(), nil
}

func (c *constraintLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (c *constraintLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	c.noteTools(tools)
	return &domain.GenerationResult{Content: c.nextReply()}, nil
}

func (c *constraintLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	c.noteTools(tools)
	return callback(&domain.GenerationResult{Content: c.nextReply()})
}

func (c *constraintLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if !strings.Contains(prompt, "report ONLY the constraints") {
		return structuredJSON(map[string]interface{}{}), nil
	}
	c.mu.Lock()
	c.extractCalls++
	forbid := c.forbidTools
	deliverables := c.deliverables
	c.mu.Unlock()

	if deliverables == "" {
		deliverables = "[]"
	}
	raw := `{"forbid_tools":` + boolLiteral(forbid) + `,"deliverables":` + deliverables + `}`
	return &domain.StructuredResult{Raw: raw, Valid: true}, nil
}

func boolLiteral(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func (c *constraintLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func newConstraintTestService(t *testing.T, llm *constraintLLM) *Service {
	t.Helper()
	svc, err := New("constraints").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// agentbench `personal-planet`: "Without using any tools, name the largest
// planet in our solar system." The model answered correctly (Jupiter) but got
// there through execute_javascript → execute_javascript → search_available_tools
// → execute_javascript, violating the one explicit instruction in the prompt.
//
// Telling the model again in the system prompt does not fix this — the tools are
// right there in the request. Don't offer them.
//
// The gate must hold for EVERY entry point, not just the one that happened to be
// wired: a smoke test through Ask() found tools still being called because the
// enforcement sat in a helper the run config never reached.
func TestToolsWithheldAcrossEveryEntryPoint(t *testing.T) {
	t.Parallel()

	entries := map[string]func(t *testing.T, svc *Service, goal string){
		"Run": func(t *testing.T, svc *Service, goal string) {
			if _, err := svc.Run(context.Background(), goal); err != nil {
				t.Fatalf("Run: %v", err)
			}
		},
		"RunStream": func(t *testing.T, svc *Service, goal string) {
			events, err := svc.RunStream(context.Background(), goal)
			if err != nil {
				t.Fatalf("RunStream: %v", err)
			}
			for range events {
			}
		},
		"Ask": func(t *testing.T, svc *Service, goal string) {
			if _, err := svc.Ask(context.Background(), goal); err != nil {
				t.Fatalf("Ask: %v", err)
			}
		},
		"Chat": func(t *testing.T, svc *Service, goal string) {
			if _, err := svc.Chat(context.Background(), goal); err != nil {
				t.Fatalf("Chat: %v", err)
			}
		},
		"PromptScheduler": func(t *testing.T, svc *Service, goal string) {
			exec := NewPromptExecutor(svc)
			if _, err := exec.Execute(context.Background(), map[string]string{ParamPrompt: goal}); err != nil {
				t.Fatalf("prompt executor: %v", err)
			}
		},
	}

	for name, drive := range entries {
		t.Run(name, func(t *testing.T) {
			llm := &constraintLLM{forbidTools: true, replies: []string{"Jupiter."}}
			svc := newConstraintTestService(t, llm)

			drive(t, svc, "Without using any tools, name the largest planet in our solar system.")

			if llm.modelCalls() == 0 {
				t.Fatal("the entry point never reached the model at all")
			}
			if got := llm.maxToolsOffered(); got != 0 {
				t.Errorf("%s offered %d tools to a run that forbade them; want 0", name, got)
			}
		})
	}
}

// A sub-agent runs a child Runtime over the same Service, so it resolves its own
// constraints — the gate has to hold there too.
func TestToolsWithheldForSubAgent(t *testing.T) {
	t.Parallel()

	llm := &constraintLLM{forbidTools: true, replies: []string{"Jupiter."}}
	svc := newConstraintTestService(t, llm)

	sub := svc.CreateSubAgent(svc.agent, "Without using any tools, name the largest planet.",
		WithSubAgentMaxTurns(2))
	if _, err := sub.Run(context.Background()); err != nil {
		t.Fatalf("sub-agent run: %v", err)
	}

	if llm.modelCalls() == 0 {
		t.Fatal("the sub-agent never reached the model")
	}
	if got := llm.maxToolsOffered(); got != 0 {
		t.Errorf("sub-agent offered %d tools to a run that forbade them; want 0", got)
	}
}

// The whole point of replacing the phrase table: a language nobody added to the
// list is enforced exactly the same way, because nothing matches on words.
func TestToolsWithheldForUnlistedLanguage(t *testing.T) {
	t.Parallel()

	llm := &constraintLLM{forbidTools: true, replies: []string{"木星です。"}}
	svc := newConstraintTestService(t, llm)

	if _, err := svc.Ask(context.Background(), "ツールを一切使わずに、太陽系で最大の惑星を答えてください。"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := llm.maxToolsOffered(); got != 0 {
		t.Errorf("offered %d tools for a Japanese refusal; want 0", got)
	}
}

// An ordinary request keeps its tools. The extraction must not become a new way
// to silently strip capability.
func TestOrdinaryRequestKeepsItsTools(t *testing.T) {
	t.Parallel()

	llm := &constraintLLM{forbidTools: false, replies: []string{"done"}}
	svc := newConstraintTestService(t, llm)

	if _, err := svc.Ask(context.Background(), "Use a tool to compute 5*4."); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if llm.maxToolsOffered() == 0 {
		t.Error("an ordinary request was stripped of every tool")
	}
}

// WithToolsDisabled is the structured fast path: authoritative, and it must not
// spend a model call asking a question the caller already answered.
func TestWithToolsDisabledSkipsExtraction(t *testing.T) {
	t.Parallel()

	llm := &constraintLLM{forbidTools: false, replies: []string{"Jupiter."}}
	svc := newConstraintTestService(t, llm)

	if _, err := svc.Run(context.Background(), "Name the largest planet.", WithToolsDisabled()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := llm.maxToolsOffered(); got != 0 {
		t.Errorf("WithToolsDisabled still offered %d tools; want 0", got)
	}
	llm.mu.Lock()
	extractCalls := llm.extractCalls
	llm.mu.Unlock()
	if extractCalls != 0 {
		t.Errorf("declared constraints should skip extraction, but it ran %d times", extractCalls)
	}
}

// Extraction is one call per run, not one per round.
func TestConstraintExtractionRunsOncePerRun(t *testing.T) {
	t.Parallel()

	llm := &constraintLLM{forbidTools: false, replies: []string{"still working", "done"}}
	svc := newConstraintTestService(t, llm)

	if _, err := svc.Run(context.Background(), "Do something involved.", WithMaxTurns(3)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	llm.mu.Lock()
	extractCalls := llm.extractCalls
	llm.mu.Unlock()
	if extractCalls != 1 {
		t.Errorf("constraint extraction ran %d times; want exactly 1 per run", extractCalls)
	}
}

// Turning extraction off leaves only what the caller declared.
func TestConstraintExtractionCanBeDisabled(t *testing.T) {
	t.Parallel()

	llm := &constraintLLM{forbidTools: true, replies: []string{"done"}}
	svc := newConstraintTestService(t, llm)

	if _, err := svc.Run(context.Background(),
		"Without using any tools, name the largest planet.",
		WithConstraintExtraction(false)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	llm.mu.Lock()
	extractCalls := llm.extractCalls
	llm.mu.Unlock()
	if extractCalls != 0 {
		t.Errorf("extraction ran %d times despite being disabled", extractCalls)
	}
	if llm.maxToolsOffered() == 0 {
		t.Error("with extraction off and nothing declared, tools should still be offered")
	}
}

// A provider that cannot do structured output must not be able to break an
// otherwise ordinary run.
func TestConstraintExtractionFailureDegradesToNoConstraints(t *testing.T) {
	t.Parallel()

	svc := newConstraintTestService(t, &constraintLLM{replies: []string{"done"}})
	svc.llmService = &failingStructuredLLM{inner: &constraintLLM{replies: []string{"done"}}}

	got := svc.resolveRunConstraints(context.Background(), "send an email", DefaultRunConfig())
	if !got.Empty() {
		t.Fatalf("a failed extraction must degrade to no constraints, got %+v", got)
	}
}

type failingStructuredLLM struct{ inner *constraintLLM }

func (f *failingStructuredLLM) Generate(ctx context.Context, p string, o *domain.GenerationOptions) (string, error) {
	return f.inner.Generate(ctx, p, o)
}
func (f *failingStructuredLLM) Stream(ctx context.Context, p string, o *domain.GenerationOptions, cb func(string)) error {
	return nil
}
func (f *failingStructuredLLM) GenerateWithTools(ctx context.Context, m []domain.Message, tl []domain.ToolDefinition, o *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return f.inner.GenerateWithTools(ctx, m, tl, o)
}
func (f *failingStructuredLLM) StreamWithTools(ctx context.Context, m []domain.Message, tl []domain.ToolDefinition, o *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return f.inner.StreamWithTools(ctx, m, tl, o, cb)
}
func (f *failingStructuredLLM) GenerateStructured(ctx context.Context, p string, s interface{}, o *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return nil, context.DeadlineExceeded
}
func (f *failingStructuredLLM) RecognizeIntent(ctx context.Context, r string) (*domain.IntentResult, error) {
	return nil, nil
}
