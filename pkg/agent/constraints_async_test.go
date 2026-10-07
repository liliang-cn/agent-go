package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// slowConstraintLLM takes its time over the constraint check and says tools
// are forbidden; the answer itself comes at once, first as a tool call.
type slowConstraintLLM struct {
	mu            sync.Mutex
	checkDone     time.Time
	firstAnswer   time.Time
	answers       int
	sawRefusal    bool
	constraintLag time.Duration
}

func (l *slowConstraintLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *slowConstraintLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *slowConstraintLLM) GenerateStructured(_ context.Context, prompt string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if !strings.Contains(prompt, "report ONLY the constraints") {
		return structuredJSON(map[string]interface{}{}), nil
	}
	time.Sleep(l.constraintLag)
	l.mu.Lock()
	l.checkDone = time.Now()
	l.mu.Unlock()
	return &domain.StructuredResult{Raw: `{"forbid_tools":true,"deliverables":[]}`, Valid: true}, nil
}
func (l *slowConstraintLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}
func (l *slowConstraintLLM) answer(messages []domain.Message) *domain.GenerationResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.answers++
	if l.answers == 1 {
		l.firstAnswer = time.Now()
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{ID: "c1", Type: "function",
			Function: domain.FunctionCall{Name: "noop", Arguments: map[string]interface{}{}}}}}
	}
	for _, m := range messages {
		if strings.Contains(m.Content, "explicitly forbids tool use") {
			l.sawRefusal = true
		}
	}
	return &domain.GenerationResult{Content: "Jupiter."}
}
func (l *slowConstraintLLM) GenerateWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.answer(messages), nil
}
func (l *slowConstraintLLM) StreamWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.answer(messages))
}

// The constraint check no longer stands in front of the answer: the first
// request goes out while the check is still running. What the check decides
// still holds — a tool call made before it answered is refused once it says
// tools are forbidden, and the tool never runs.
func TestConstraintCheckRunsBesideTheFirstTurn(t *testing.T) {
	llm := &slowConstraintLLM{constraintLag: 400 * time.Millisecond}
	svc, err := New("async-constraints").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	var ran int32
	svc.AddTool("noop", "Does nothing.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			atomic.AddInt32(&ran, 1)
			return map[string]interface{}{"ok": true}, nil
		})

	if _, err := svc.Ask(context.Background(), "Without using any tools, name the largest planet."); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if llm.firstAnswer.IsZero() || llm.checkDone.IsZero() {
		t.Fatalf("answer at %v, check at %v", llm.firstAnswer, llm.checkDone)
	}
	if !llm.firstAnswer.Before(llm.checkDone) {
		t.Fatalf("the first request waited for the constraint check (answer %v after check)", llm.firstAnswer.Sub(llm.checkDone))
	}
	if atomic.LoadInt32(&ran) != 0 {
		t.Fatal("a tool ran in a run whose constraints forbid tools")
	}
	if !llm.sawRefusal {
		t.Fatal("the tool call was not refused with the forbidden-tools feedback")
	}
}
