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

// streamedCallLLM answers with a tool call whose arguments are whole, so the
// streaming executor runs it at once, then with text; its constraint check is
// slow and asks for nothing.
type streamedCallLLM struct {
	mu            sync.Mutex
	checkDone     time.Time
	secondTurn    time.Time
	answers       int
	constraintLag time.Duration
}

func (l *streamedCallLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *streamedCallLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *streamedCallLLM) GenerateStructured(_ context.Context, prompt string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if !strings.Contains(prompt, "report ONLY the constraints") {
		return structuredJSON(map[string]interface{}{}), nil
	}
	time.Sleep(l.constraintLag)
	l.mu.Lock()
	l.checkDone = time.Now()
	l.mu.Unlock()
	return &domain.StructuredResult{Raw: `{"forbid_tools":false,"deliverables":[],"requested_actions":[]}`, Valid: true}, nil
}
func (l *streamedCallLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}
func (l *streamedCallLLM) answer() *domain.GenerationResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.answers++
	if l.answers == 1 {
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{ID: "c1", Type: "function",
			Function: domain.FunctionCall{Name: "lookup", Arguments: map[string]interface{}{"q": "x"}}}}}
	}
	if l.secondTurn.IsZero() {
		l.secondTurn = time.Now()
	}
	return &domain.GenerationResult{Content: "Found it: 42."}
}
func (l *streamedCallLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.answer(), nil
}
func (l *streamedCallLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.answer())
}

// A tool call that streamed in has already run, so the constraint verdict can
// no longer stop it. The next round used to wait for the check anyway: a
// median 1.4s and up to 6.4s on a 100-tool assistant, every round one.
func TestARoundOfCallsThatAlreadyRanDoesNotWaitForTheConstraintCheck(t *testing.T) {
	llm := &streamedCallLLM{constraintLag: 1500 * time.Millisecond}
	svc, err := New("streamed-call").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	var ran int32
	svc.AddTool("lookup", "Looks something up.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{"q": map[string]interface{}{"type": "string"}}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			atomic.AddInt32(&ran, 1)
			return map[string]interface{}{"answer": 42}, nil
		})

	if _, err := svc.Ask(context.Background(), "look it up"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// The check outlives the run (the lints give up on it after a second);
	// let it finish so the two moments can be compared.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		llm.mu.Lock()
		done := !llm.checkDone.IsZero()
		llm.mu.Unlock()
		if done {
			break
		}
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if atomic.LoadInt32(&ran) != 1 {
		t.Fatalf("lookup ran %d times, want 1", ran)
	}
	if llm.secondTurn.IsZero() || llm.checkDone.IsZero() {
		t.Fatalf("second turn at %v, check at %v", llm.secondTurn, llm.checkDone)
	}
	if !llm.secondTurn.Before(llm.checkDone) {
		t.Fatalf("the second turn waited for the constraint check (%v after it)", llm.secondTurn.Sub(llm.checkDone))
	}
}

// batchedFinishLLM emits a real tool call and task_complete in the same turn.
type batchedFinishLLM struct{ streamedCallLLM }

func (l *batchedFinishLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(&domain.GenerationResult{ToolCalls: []domain.ToolCall{
		{ID: "w1", Type: "function", Function: domain.FunctionCall{Name: "slow_write", Arguments: map[string]interface{}{"v": "x"}}},
		{ID: "d1", Type: "function", Function: domain.FunctionCall{Name: "task_complete", Arguments: map[string]interface{}{"result": "Saved."}}},
	}})
}

// A write batched with task_complete is still running when the terminal
// signal arrives. The run used to end under it, and the write's result was
// sent on the closed event channel: the process panicked.
func TestTaskCompleteWaitsForTheCallsItWasBatchedWith(t *testing.T) {
	llm := &batchedFinishLLM{}
	svc, err := New("batched-finish").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	var finished int32
	svc.AddTool("slow_write", "Writes slowly.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{"v": map[string]interface{}{"type": "string"}}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			time.Sleep(300 * time.Millisecond)
			atomic.AddInt32(&finished, 1)
			return map[string]interface{}{"ok": true}, nil
		})

	res, err := svc.Run(context.Background(), "save it")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if atomic.LoadInt32(&finished) != 1 {
		t.Fatal("the run ended before the write batched with task_complete finished")
	}
	if !strings.Contains(res.Text(), "Saved.") {
		t.Fatalf("answer = %q", res.Text())
	}
	time.Sleep(100 * time.Millisecond)
}
