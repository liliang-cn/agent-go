package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/codemode/scene"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// The broken first program calls a builder method that does not exist on
// line 3; the fix is the obvious one.
const brokenRack = `scene.material('steel', {color: '#3d6fb6'});
for (let b = 0; b < 6; b++) {
  scene.bx({w: 0.95, h: 0.03, d: 0.6}, {x: b, y: 0.1});
}`

const fixedRack = `scene.material('steel', {color: '#3d6fb6'});
for (let b = 0; b < 6; b++) {
  scene.group('bay-' + b, {position: {x: b}}, () => {
    for (let l = 0; l < 4; l++) {
      const y = 0.1 + l * 0.45;
      scene.box({w: 0.95, h: 0.03, d: 0.6}, {y}, {name: 'shelf-' + l, material: 'steel'});
      scene.box({w: 0.4, h: 0.25, d: 0.35}, {y: y + 0.015 + 0.125}, {name: 'box-' + l, color: '#c8a26b'});
    }
  });
}`

// codeModeLLM plays a model that gets the program wrong, reads the report,
// fixes it, then first pastes the code into its answer (and is told off by
// the lint) before answering properly.
type codeModeLLM struct {
	mu        sync.Mutex
	reports   []string // tool results it was shown
	lintNotes []string // retry feedback it was shown
	answers   int
}

func (l *codeModeLLM) answer(messages []domain.Message, tools []domain.ToolDefinition) (*domain.GenerationResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var toolResults []string
	for _, m := range messages {
		if m.Role == "tool" {
			toolResults = append(toolResults, m.Content)
		}
	}
	l.reports = toolResults
	l.lintNotes = nil
	for _, m := range messages {
		if m.Role != "tool" && strings.Contains(m.Content, "contains the program") {
			l.lintNotes = append(l.lintNotes, m.Content)
		}
	}
	hasTool := false
	for _, t := range tools {
		if t.Function.Name == "build_scene" {
			hasTool = true
		}
	}
	call := func(code string) *domain.GenerationResult {
		return &domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID: "call-" + string(rune('a'+len(toolResults))), Type: "function",
				Function: domain.FunctionCall{Name: "build_scene", Arguments: map[string]interface{}{"code": code}},
			}},
			FinishReason: "tool_calls",
		}
	}
	switch {
	case hasTool && len(toolResults) == 0:
		return call(brokenRack), nil
	case hasTool && len(toolResults) == 1:
		return call(fixedRack), nil
	}
	l.answers++
	if l.answers == 1 {
		return &domain.GenerationResult{Content: "Done. Here is the program:\n```js\n" + fixedRack + "\n```", FinishReason: "stop"}, nil
	}
	return &domain.GenerationResult{Content: "Built a rack of 6 bays with 4 shelves each and a carton on every shelf.", FinishReason: "stop"}, nil
}

func (l *codeModeLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *codeModeLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *codeModeLLM) GenerateWithTools(_ context.Context, m []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.answer(m, t)
}
func (l *codeModeLLM) StreamWithTools(_ context.Context, m []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	res, err := l.answer(m, t)
	if err != nil {
		return err
	}
	return cb(res)
}
func (l *codeModeLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *codeModeLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestRunCodeFixesProgramOnSecondTry(t *testing.T) {
	llm := &codeModeLLM{}
	svc, err := New("scene-builder").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithConstraintExtraction(false).
		WithCodeMode(scene.Scene3D(), CodeModeAttempts(4)).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	out, err := svc.RunCode(context.Background(), "Build a rack of 6 bays × 4 shelves with a carton on each shelf.")
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Attempts != 2 || out.Best.Attempt != 2 {
		t.Fatalf("want a passing second attempt, got ok=%v attempts=%d\n%s", out.OK, out.Attempts, out.Report)
	}

	// The first report told the model what was wrong and where.
	if len(llm.reports) < 2 {
		t.Fatalf("model saw %d reports", len(llm.reports))
	}
	first := llm.reports[0]
	for _, want := range []string{"attempt 1/4: FAILED", "line 3", "scene.bx does not exist; the scene API has box, cylinder", "> scene.bx("} {
		if !strings.Contains(first, want) {
			t.Fatalf("first report missing %q:\n%s", want, first)
		}
	}
	if !strings.Contains(llm.reports[1], "attempt 2/4: PASSED") || !strings.Contains(llm.reports[1], "meshes: 48 (box 48)") {
		t.Fatalf("second report:\n%s", llm.reports[1])
	}

	// Pasting the program into the answer was rejected; the kept answer is prose.
	if len(llm.lintNotes) == 0 {
		t.Fatal("the code-in-answer lint never fired")
	}
	if strings.Contains(out.Answer, "```") || strings.Contains(out.Answer, "scene.box") {
		t.Fatalf("answer leaks code: %q", out.Answer)
	}

	// The artifact is the scene, as data.
	sc, err := scene.Parse(out.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Nodes) != 6 || len(sc.Meshes()) != 48 {
		t.Fatalf("want 6 bays / 48 meshes, got %d / %d", len(sc.Nodes), len(sc.Meshes()))
	}
	if !strings.Contains(out.Code, "scene.group('bay-'") {
		t.Fatal("kept code should be the fixed program")
	}
	// RunCode cleans up after itself.
	if svc.codeModes.runFor(out.Result.SessionID, false) != nil {
		t.Fatal("RunCode left its attempts behind")
	}
}

func TestRunCodeWithoutModeFails(t *testing.T) {
	svc, err := New("plain").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&codeModeLLM{}).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.RunCode(context.Background(), "x"); err != ErrNoCodeMode {
		t.Fatalf("want ErrNoCodeMode, got %v", err)
	}
}

func TestCodeModeAttemptBudget(t *testing.T) {
	svc, err := New("budget").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&codeModeLLM{}).
		WithCodeMode(scene.Scene3D(), CodeModeAttempts(1)).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	run := svc.codeModes.pin("s1")
	defer svc.codeModes.release("s1")
	ctx := withCurrentSession(context.Background(), &Session{ID: "s1"})
	cfg := svc.codeModes.modes[0]
	r1, _ := svc.codeModes.submit(ctx, cfg, map[string]interface{}{"code": brokenRack})
	r2, _ := svc.codeModes.submit(ctx, cfg, map[string]interface{}{"code": fixedRack})
	if !strings.Contains(r1.(string), "no attempts left") {
		t.Fatalf("first report should say the budget is spent:\n%s", r1)
	}
	if !strings.Contains(r2.(string), "No attempts left (1 used); this program was not run") {
		t.Fatalf("second submit should be refused:\n%s", r2)
	}
	if got := len(run.sessions["build_scene"].Attempts()); got != 1 {
		t.Fatalf("ran %d programs", got)
	}
}

func TestLeaksProgram(t *testing.T) {
	if !leaksProgram("```js\na\nb\nc\n```", nil) {
		t.Fatal("a fenced block of code should count")
	}
	if leaksProgram("Built 6 bays (`scene.group`) with 24 cartons.", nil) {
		t.Fatal("an inline mention is fine")
	}
}
