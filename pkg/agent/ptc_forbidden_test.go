package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// codeBlockLLM writes a PTC <code> block in its reply instead of emitting a
// tool_call. That is the second tool channel: the runtime parses the block out
// of the text and executes it, so emptying the tool-definition list does not
// close it. The first reply reaches for the channel; the second answers plainly.
type codeBlockLLM struct {
	mu           sync.Mutex
	calls        int
	toolsOffered []int
	systemPrompt string
	sawRefusal   bool
}

func (c *codeBlockLLM) note(messages []domain.Message, tools []domain.ToolDefinition) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolsOffered = append(c.toolsOffered, len(tools))
	for _, m := range messages {
		if m.Role == "system" && c.systemPrompt == "" {
			c.systemPrompt = m.Content
		}
		if strings.Contains(m.Content, "forbids tool use") {
			c.sawRefusal = true
		}
	}
}

func (c *codeBlockLLM) reply() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return "<code>return callTool('mcp_websearch_basic', {query: 'largest planet'});</code>"
	}
	return "Jupiter is the largest planet in the solar system."
}

func (c *codeBlockLLM) Generate(ctx context.Context, p string, o *domain.GenerationOptions) (string, error) {
	return c.reply(), nil
}

func (c *codeBlockLLM) Stream(ctx context.Context, p string, o *domain.GenerationOptions, cb func(string)) error {
	return nil
}

func (c *codeBlockLLM) GenerateWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions) (*domain.GenerationResult, error) {
	c.note(m, t)
	return &domain.GenerationResult{Content: c.reply()}, nil
}

func (c *codeBlockLLM) StreamWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	c.note(m, t)
	return cb(&domain.GenerationResult{Content: c.reply()})
}

func (c *codeBlockLLM) GenerateStructured(ctx context.Context, p string, s interface{}, o *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if strings.Contains(p, "report ONLY the constraints") {
		return &domain.StructuredResult{Raw: `{"forbid_tools":true,"deliverables":[]}`, Valid: true}, nil
	}
	return structuredJSON(map[string]interface{}{}), nil
}

func (c *codeBlockLLM) RecognizeIntent(ctx context.Context, r string) (*domain.IntentResult, error) {
	return nil, nil
}

// The hole this closes: ForbidTools emptied the function-calling tool list and
// refused result.ToolCalls, but PTC never produces a tool_call — the model
// writes <code> in its reply and the runtime executes it from the text. Both
// gates missed it, so a "no tools" run still ran execute_javascript.
func TestForbiddenRunRefusesPTCCodeBlock(t *testing.T) {
	t.Parallel()

	llm := &codeBlockLLM{}
	svc, err := New("ptc-forbidden").
		WithPTC(true).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	reply, err := svc.Ask(context.Background(), "Without using any tools, name the largest planet.")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	llm.mu.Lock()
	offered := append([]int(nil), llm.toolsOffered...)
	sysPrompt := llm.systemPrompt
	sawRefusal := llm.sawRefusal
	llm.mu.Unlock()

	for i, n := range offered {
		if n != 0 {
			t.Errorf("turn %d was offered %d tools in a run that forbids them", i+1, n)
		}
	}
	if !sawRefusal {
		t.Error("the <code> block was not refused — the model never got the structured feedback")
	}
	if !strings.Contains(reply, "Jupiter") {
		t.Errorf("run should end in a plain-text answer, got %q", reply)
	}
	// The code block must never have executed: a tool result would have been
	// threaded back as a tool-role message and shown up in the final answer.
	if strings.Contains(reply, "callTool") || strings.Contains(reply, "mcp_websearch") {
		t.Errorf("PTC code appears to have executed: %q", reply)
	}

	// A capability that will not be offered must not be taught either.
	if strings.Contains(sysPrompt, "<code>") {
		t.Error("system prompt still contains the PTC tutorial in a no-tools run")
	}
	if strings.Contains(sysPrompt, "search_available_tools") {
		t.Error("system prompt still instructs the model to use tool search in a no-tools run")
	}
}

// The same run with tools allowed must still teach and use PTC — the gate has
// to be conditional, not a permanent removal.
func TestOrdinaryRunStillGetsPTC(t *testing.T) {
	t.Parallel()

	llm := &constraintLLM{forbidTools: false, replies: []string{"done"}}
	svc, err := New("ptc-ordinary").
		WithPTC(true).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if _, err := svc.Ask(context.Background(), "What is 2+2?"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if llm.maxToolsOffered() == 0 {
		t.Error("an ordinary run lost its tools")
	}
}
