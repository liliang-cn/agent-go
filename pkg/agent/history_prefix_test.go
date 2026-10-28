package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// prefixLLM answers every turn in one round and keeps what each request sent.
type prefixLLM struct {
	mu       sync.Mutex
	sent     [][]domain.Message
	withTool bool // call look once per turn before answering
	calls    int
}

func (p *prefixLLM) record(m []domain.Message) *domain.GenerationResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, append([]domain.Message(nil), m...))
	if p.withTool && m[len(m)-1].Role == "user" && m[len(m)-1].Content != toolResultsAnalysisPrompt {
		p.calls++
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID: fmt.Sprintf("call-%d", p.calls), Type: "function",
			Function: domain.FunctionCall{Name: "look", Arguments: map[string]interface{}{"q": "x"}},
		}}}
	}
	return &domain.GenerationResult{Content: "done"}
}

func (p *prefixLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (p *prefixLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (p *prefixLLM) GenerateWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return p.record(m), nil
}
func (p *prefixLLM) StreamWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(p.record(m))
}
func (p *prefixLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return structuredJSON(map[string]interface{}{}), nil
}
func (p *prefixLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func wireForm(m domain.Message) string {
	b, _ := json.Marshal(struct {
		Role, Content string
		Calls         []domain.ToolCall
		ID            string
	}{m.Role, m.Content, m.ToolCalls, m.ToolCallID})
	return string(b)
}

// Measured against claude-opus-5-5: the first request of every turn read
// only the system prompt and tools from the cache, never the conversation
// before it. Each turn sends its own context (the clock, recalled memory,
// relevant skills) just before the question, and that context was not kept
// in the history, so the next turn's prefix lost a message near the top and
// everything after it missed. What was sent stays sent.
func TestTheNextTurnRepeatsThePreviousTurnsRequestExactly(t *testing.T) {
	t.Parallel()
	llm := &prefixLLM{}
	svc, err := New("prefix").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	for _, q := range []string{"first question", "second question"} {
		if _, err := svc.Run(context.Background(), q, WithSessionID("s")); err != nil {
			t.Fatalf("Run %q: %v", q, err)
		}
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.sent) < 2 {
		t.Fatalf("%d requests", len(llm.sent))
	}
	prev, next := llm.sent[len(llm.sent)-2], llm.sent[len(llm.sent)-1]
	if len(next) <= len(prev) {
		t.Fatalf("the second turn sent %d messages, the first %d", len(next), len(prev))
	}
	for i := range prev {
		if wireForm(prev[i]) != wireForm(next[i]) {
			t.Fatalf("message %d differs between turns:\n first: %s\nsecond: %s", i, wireForm(prev[i]), wireForm(next[i]))
		}
	}
}

// The same, with what made it fail live: a tool round in every turn, whose
// prompt after the results reads the same each time, and a question asked
// twice. Each turn's first request must begin with the whole of the turn
// before it.
func TestRepeatedWordsAndToolRoundsKeepThePrefix(t *testing.T) {
	t.Parallel()
	llm := &prefixLLM{withTool: true}
	svc, err := New("prefix-tools").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	svc.AddToolWithMetadata("look", "Look something up.", map[string]interface{}{"type": "object", "properties": map[string]interface{}{"q": map[string]interface{}{"type": "string"}}},
		func(context.Context, map[string]interface{}) (interface{}, error) { return "found", nil }, ToolMetadata{ReadOnly: true})
	var turnFirst []int
	for _, q := range []string{"same question", "same question", "another", "fourth", "fifth", "sixth"} {
		llm.mu.Lock()
		turnFirst = append(turnFirst, len(llm.sent))
		llm.mu.Unlock()
		if _, err := svc.Run(context.Background(), q, WithSessionID("s")); err != nil {
			t.Fatalf("Run %q: %v", q, err)
		}
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	for turn := 1; turn < len(turnFirst); turn++ {
		prev, next := llm.sent[turnFirst[turn]-1], llm.sent[turnFirst[turn]]
		if len(next) <= len(prev) {
			t.Fatalf("turn %d sent %d messages after %d", turn+1, len(next), len(prev))
		}
		for i := range prev {
			if wireForm(prev[i]) != wireForm(next[i]) {
				t.Fatalf("turn %d, message %d differs:\n before: %s\n  after: %s", turn+1, i, wireForm(prev[i]), wireForm(next[i]))
			}
		}
	}
}

// A conversation longer than the history window used to lose its oldest
// message every turn, so its prefix changed every turn and nothing of it was
// ever read from the cache. The window now starts at fixed steps: between
// two steps every turn's request extends the one before; at a step it
// starts over once. It never shows less than it used to.
func TestTheHistoryWindowMovesInSteps(t *testing.T) {
	var history []domain.Message
	var prevStart = -1
	moves := 0
	for turn := 0; turn < 60; turn++ {
		history = append(history,
			domain.Message{Role: "user", Content: fmt.Sprintf("q%d", turn)},
			domain.Message{Role: "assistant", ToolCalls: []domain.ToolCall{{ID: fmt.Sprintf("c%d", turn)}}},
			domain.Message{Role: "tool", ToolCallID: fmt.Sprintf("c%d", turn), Content: "r"},
			domain.Message{Role: "assistant", Content: "a"},
		)
		window := stableHistoryWindow(history)
		if n := len(window); n < recentConversationWindow+olderConversationLimit && n != len(history) {
			t.Fatalf("turn %d shows %d of %d messages, fewer than before", turn, n, len(history))
		}
		if window[0].Role == "tool" {
			t.Fatalf("turn %d starts on a tool result with no call", turn)
		}
		start := len(history) - len(window)
		if start != prevStart {
			moves++
			prevStart = start
		}
	}
	if moves > 60*4/historyWindowStep+2 {
		t.Fatalf("the window moved %d times in 60 turns", moves)
	}
}
