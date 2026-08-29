package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

type msgSpyLLM struct {
	mu    sync.Mutex
	turns int32
	seen  [][]domain.Message
}

func (l *msgSpyLLM) reply(msgs []domain.Message, tools []domain.ToolDefinition) *domain.GenerationResult {
	l.mu.Lock()
	cp := make([]domain.Message, len(msgs))
	copy(cp, msgs)
	l.seen = append(l.seen, cp)
	l.mu.Unlock()
	if len(tools) == 0 {
		return &domain.GenerationResult{Content: "Done.", FinishReason: "stop"}
	}
	n := atomic.AddInt32(&l.turns, 1)
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID: fmt.Sprintf("call-%d", n), Type: "function",
			Function: domain.FunctionCall{Name: "read_it", Arguments: map[string]interface{}{"path": "Makefile"}},
		}},
		FinishReason: "tool_calls",
	}
}

func (l *msgSpyLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *msgSpyLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *msgSpyLLM) GenerateWithTools(_ context.Context, m []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(m, t), nil
}
func (l *msgSpyLLM) StreamWithTools(_ context.Context, m []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.reply(m, t))
}
func (l *msgSpyLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *msgSpyLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// The bug this pins is the one that made a soak run ask for the same file a
// hundred and twenty-four times and never get it.
//
// The duplicate check removed the repeated call from the assistant message
// that goes into the transcript. The result written for it — real or a "not
// run again" note — is addressed to that call's id, so with the call gone it
// was an orphan and the pairing sanitiser dropped it. The model asked, and
// nothing at all came back: no result, no error, no explanation. Asking again
// is the only sane response to that, and it is what the model did until its
// round budget ran out.
func TestEveryToolCallGetsAnAnswer(t *testing.T) {
	llm := &msgSpyLLM{}
	svc, _ := New("probe2").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	defer svc.Close()
	var reads int32
	svc.AddToolWithMetadata("read_it", "Reads a file.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"}}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			atomic.AddInt32(&reads, 1)
			return "MAGIC_FILE_CONTENT", nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true})

	events, _ := svc.RunStreamWithOptions(context.Background(), "Read it repeatedly.", WithMaxTurns(3))
	for range events {
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.seen) < 3 {
		t.Fatalf("only %d turns ran; the loop ended too early to test this", len(llm.seen))
	}
	// Every turn the model asks, it must come away with an answer to that ask.
	// Before the fix the count stuck at one however many times it asked.
	answers := func(msgs []domain.Message) int {
		n := 0
		for _, m := range msgs {
			b, _ := json.Marshal(m)
			if strings.Contains(string(b), "MAGIC_FILE_CONTENT") {
				n++
			}
		}
		return n
	}
	last := llm.seen[len(llm.seen)-1]
	asks := int(atomic.LoadInt32(&llm.turns))
	if got := answers(last); got < asks-1 {
		t.Errorf("the model asked %d times and could see %d answers; a tool call that "+
			"returns nothing at all is one the model can only repeat", asks, got)
	}
	// And exactly one message per call: a real result and a "not run again"
	// note for the same call id is malformed for the provider.
	for i, msgs := range llm.seen {
		calls, results := map[string]bool{}, map[string]int{}
		for _, m := range msgs {
			for _, tc := range m.ToolCalls {
				calls[tc.ID] = true
			}
			if m.Role == "tool" && m.ToolCallID != "" {
				results[m.ToolCallID]++
			}
		}
		for id, n := range results {
			if n > 1 {
				t.Errorf("turn %d: tool call %s has %d results", i, id, n)
			}
		}
		for id := range results {
			if !calls[id] {
				t.Errorf("turn %d: a result for %s with no matching call — it will be stripped", i, id)
			}
		}
	}
}
