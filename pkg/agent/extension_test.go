package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// everySeam is one extension that touches every seam, so a single run shows
// each one wired and in order.
type everySeam struct {
	BaseObserver
	mu          sync.Mutex
	modelTurns  int
	lintCalls   int
	before      []string
	after       []string
	rewriteArgs bool
	block       string
	replaceWith interface{}
	handlerSaw  map[string]interface{}
}

func (e *everySeam) Name() string { return "every-seam" }

func (e *everySeam) OnModelStart(context.Context, ModelInfo) {
	e.mu.Lock()
	e.modelTurns++
	e.mu.Unlock()
}

func (e *everySeam) Check(text string, _ LintContext) (bool, string) {
	e.mu.Lock()
	e.lintCalls++
	e.mu.Unlock()
	return true, ""
}

func (e *everySeam) ID() string { return "every-seam" }

func (e *everySeam) RegisterTools(reg *ToolRegistry) error {
	reg.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "echo",
			Description: "echoes its input",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}},
			},
		},
	}, func(_ context.Context, args map[string]interface{}) (interface{}, error) {
		e.mu.Lock()
		e.handlerSaw = args
		e.mu.Unlock()
		return map[string]interface{}{"echoed": args["text"]}, nil
	}, "test")
	return nil
}

func (e *everySeam) ContributeContext(_ context.Context, in ContextInput) ([]domain.Message, error) {
	return []domain.Message{{Role: "system", Content: "EXTENSION CONTEXT for " + in.Goal}}, nil
}

func (e *everySeam) BeforeTool(_ context.Context, call ToolCallInfo) (ToolVerdict, error) {
	e.mu.Lock()
	e.before = append(e.before, call.Name)
	e.mu.Unlock()
	if e.block != "" {
		return ToolVerdict{Block: e.block}, nil
	}
	if e.rewriteArgs {
		return ToolVerdict{Args: map[string]interface{}{"text": "rewritten"}}, nil
	}
	return ToolVerdict{}, nil
}

func (e *everySeam) AfterTool(_ context.Context, res ToolResultInfo) (interface{}, bool, error) {
	e.mu.Lock()
	e.after = append(e.after, res.Name)
	e.mu.Unlock()
	if e.replaceWith != nil {
		return e.replaceWith, true, nil
	}
	return nil, false, nil
}

func echoThenDone() *captureStreamLLM {
	return &captureStreamLLM{replies: []*domain.GenerationResult{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Type: "function", Function: domain.FunctionCall{
			Name: "echo", Arguments: map[string]interface{}{"text": "original"},
		}}}},
		{Content: "done"},
	}}
}

func buildWith(t *testing.T, llm *captureStreamLLM, exts ...Extension) *Service {
	t.Helper()
	svc, err := New("ext-agent").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithExtensions(exts...).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// One extension, every seam: the tool it registered is callable, its
// context reaches the first round, its filters rewrite the call and replace
// the result, its lint and observer fire.
func TestExtensionIsWiredIntoEverySeam(t *testing.T) {
	ext := &everySeam{rewriteArgs: true, replaceWith: map[string]interface{}{"echoed": "REPLACED"}}
	llm := echoThenDone()
	svc := buildWith(t, llm, ext)

	if got := svc.Extensions(); len(got) != 1 || got[0].Name() != "every-seam" {
		t.Fatalf("Extensions() = %v", got)
	}

	events, err := svc.RunStream(context.Background(), "test goal")
	if err != nil {
		t.Fatal(err)
	}
	final, blocked, _ := collectStreamContent(t, events)
	if blocked != "" || final != "done" {
		t.Fatalf("final=%q blocked=%q", final, blocked)
	}

	ext.mu.Lock()
	defer ext.mu.Unlock()
	if ext.handlerSaw["text"] != "rewritten" {
		t.Fatalf("tool handler saw %v, want the filter's rewrite", ext.handlerSaw)
	}
	if len(ext.before) != 1 || len(ext.after) != 1 {
		t.Fatalf("before=%v after=%v", ext.before, ext.after)
	}
	if ext.modelTurns == 0 {
		t.Fatal("observer never saw a model turn")
	}
	if ext.lintCalls == 0 {
		t.Fatal("lint never ran on the final answer")
	}
	first := llm.firstRound()
	foundCtx := false
	for _, m := range first {
		if strings.Contains(m.Content, "EXTENSION CONTEXT for test goal") {
			foundCtx = true
		}
	}
	if !foundCtx {
		t.Fatal("contributed context did not reach the model's first round")
	}
	// The replaced result is what the second round carries as the tool
	// message; the original is nowhere.
	var rounds [][]domain.Message
	llm.mu.Lock()
	rounds = llm.captured
	llm.mu.Unlock()
	if len(rounds) < 2 {
		t.Fatalf("expected two rounds, got %d", len(rounds))
	}
	joined := ""
	for _, m := range rounds[1] {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "REPLACED") || strings.Contains(joined, "original") {
		t.Fatalf("second round should carry the replaced result only:\n%s", joined)
	}
}

// A refused call never reaches the handler, and the model is told why.
func TestExtensionCanRefuseAToolCall(t *testing.T) {
	ext := &everySeam{block: "this tool sends data out"}
	llm := echoThenDone()
	svc := buildWith(t, llm, ext)

	events, err := svc.RunStream(context.Background(), "test goal")
	if err != nil {
		t.Fatal(err)
	}
	collectStreamContent(t, events)

	ext.mu.Lock()
	defer ext.mu.Unlock()
	if ext.handlerSaw != nil {
		t.Fatalf("handler ran with %v despite the refusal", ext.handlerSaw)
	}
	llm.mu.Lock()
	rounds := llm.captured
	llm.mu.Unlock()
	if len(rounds) < 2 {
		t.Fatalf("expected the model to get a second round with the refusal, got %d", len(rounds))
	}
	joined := ""
	for _, m := range rounds[1] {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "refused by extension \"every-seam\"") || !strings.Contains(joined, "sends data out") {
		t.Fatalf("model was not told why the call was refused:\n%s", joined)
	}
}

type named string

func (n named) Name() string { return string(n) }

// Registration is strict: a duplicate or an empty name is a build error,
// not a silent second copy.
func TestExtensionRegistrationIsStrict(t *testing.T) {
	llm := echoThenDone()
	_, err := New("dup").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).
		WithExtensions(named("x"), named("x")).Build()
	if err == nil || !strings.Contains(err.Error(), "registered twice") {
		t.Fatalf("duplicate name: err=%v", err)
	}
	_, err = New("empty").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).
		WithExtensions(named("")).Build()
	if err == nil || !strings.Contains(err.Error(), "empty name") {
		t.Fatalf("empty name: err=%v", err)
	}
}

// Extensions run in the order they were listed, at the same seam.
type orderSeam struct {
	name string
	log  *[]string
	mu   *sync.Mutex
}

func (o orderSeam) Name() string { return o.name }
func (o orderSeam) BeforeTool(context.Context, ToolCallInfo) (ToolVerdict, error) {
	o.mu.Lock()
	*o.log = append(*o.log, o.name)
	o.mu.Unlock()
	return ToolVerdict{}, nil
}

func TestExtensionsRunInRegistrationOrder(t *testing.T) {
	var log []string
	var mu sync.Mutex
	tool := &everySeam{}
	llm := echoThenDone()
	svc := buildWith(t, llm,
		orderSeam{"first", &log, &mu}, orderSeam{"second", &log, &mu}, orderSeam{"third", &log, &mu}, tool)

	events, err := svc.RunStream(context.Background(), "order")
	if err != nil {
		t.Fatal(err)
	}
	collectStreamContent(t, events)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(log, ",") != "first,second,third" {
		t.Fatalf("order = %v", log)
	}
}

// A result filter that errors fails closed: the model gets the error, not
// the result it could not inspect.
type failingAfter struct{ everySeam }

func (f *failingAfter) Name() string { return "failing-after" }
func (f *failingAfter) AfterTool(context.Context, ToolResultInfo) (interface{}, bool, error) {
	return nil, false, errors.New("could not inspect")
}

func TestToolResultFilterFailsClosed(t *testing.T) {
	ext := &failingAfter{}
	llm := echoThenDone()
	svc := buildWith(t, llm, ext)
	events, err := svc.RunStream(context.Background(), "fail closed")
	if err != nil {
		t.Fatal(err)
	}
	collectStreamContent(t, events)
	llm.mu.Lock()
	rounds := llm.captured
	llm.mu.Unlock()
	if len(rounds) < 2 {
		t.Fatalf("expected a second round, got %d", len(rounds))
	}
	joined := ""
	for _, m := range rounds[1] {
		joined += m.Content + "\n"
	}
	if strings.Contains(joined, "echoed") || !strings.Contains(joined, "could not inspect") {
		t.Fatalf("model should see the failure, not the result:\n%s", joined)
	}
}
