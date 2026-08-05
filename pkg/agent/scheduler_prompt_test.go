package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/scheduler"
)

// scheduledTestLLM answers whatever it is asked, recording the prompts so a test
// can prove the scheduled goal is what actually reached the model.
type scheduledTestLLM struct {
	mu      sync.Mutex
	prompts []string
}

func (l *scheduledTestLLM) record(text string) {
	l.mu.Lock()
	l.prompts = append(l.prompts, text)
	l.mu.Unlock()
}

func (l *scheduledTestLLM) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.prompts...)
}

func (l *scheduledTestLLM) Generate(_ context.Context, prompt string, _ *domain.GenerationOptions) (string, error) {
	l.record(prompt)
	return "done", nil
}

func (l *scheduledTestLLM) Stream(_ context.Context, prompt string, _ *domain.GenerationOptions, cb func(string)) error {
	l.record(prompt)
	if cb != nil {
		cb("done")
	}
	return nil
}

func (l *scheduledTestLLM) GenerateWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	for _, m := range messages {
		if m.Role == "user" {
			l.record(m.Content)
		}
	}
	return &domain.GenerationResult{Content: "股票收益已统计并发送。"}, nil
}

func (l *scheduledTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	res, _ := l.GenerateWithTools(ctx, messages, tools, opts)
	if cb != nil {
		return cb(res)
	}
	return nil
}

func (l *scheduledTestLLM) GenerateStructured(_ context.Context, prompt string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	l.record(prompt)
	return &domain.StructuredResult{Raw: "{}", Valid: true}, nil
}

func (l *scheduledTestLLM) RecognizeIntent(_ context.Context, request string) (*domain.IntentResult, error) {
	l.record(request)
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

func newScheduledTestService(t *testing.T) (*Service, *scheduledTestLLM) {
	t.Helper()
	llm := &scheduledTestLLM{}
	svc, err := New("scheduled-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, llm
}

// TestScheduledPromptRunsTheAgent is the one that matters: a schedule has to
// actually execute. Storing a row and never firing is what the feature looked
// like before this existed.
func TestScheduledPromptRunsTheAgent(t *testing.T) {
	svc, llm := newScheduledTestService(t)

	var (
		mu   sync.Mutex
		runs []PromptRun
	)
	sch, err := svc.NewPromptScheduler(WithPromptObserver(func(r PromptRun) {
		mu.Lock()
		runs = append(runs, r)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatalf("NewPromptScheduler: %v", err)
	}
	if err := sch.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = sch.Stop() })

	const goal = "统计我的股票收益并发消息给我"
	task, err := sch.Schedule(goal, "0 8 * * *", "每天早上八点", "stocks")
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if task.ID == "" || task.Prompt != goal || task.Schedule != "0 8 * * *" {
		t.Fatalf("stored task is wrong: %+v", task)
	}
	if !task.Enabled {
		t.Error("a new schedule should be enabled")
	}
	if task.NextRun == nil {
		t.Error("a cron schedule must resolve a next run time, or nothing will ever fire")
	}

	// RunNow is the only way to verify a schedule without waiting for the hour.
	if _, err := sch.RunNow(task.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}

	mu.Lock()
	got := append([]PromptRun{}, runs...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("observer saw %d runs, want 1", len(got))
	}
	if got[0].Err != nil {
		t.Errorf("run failed: %v", got[0].Err)
	}
	if got[0].SessionID != "stocks" {
		t.Errorf("run session = %q, want the one the schedule named", got[0].SessionID)
	}
	if got[0].Answer == "" {
		t.Error("a successful run should carry the agent's answer, so a host can notify with it")
	}

	// The goal must be what reached the model, not the description.
	var sawGoal bool
	for _, p := range llm.seen() {
		if strings.Contains(p, goal) {
			sawGoal = true
		}
	}
	if !sawGoal {
		t.Errorf("the scheduled goal never reached the model; prompts were %q", llm.seen())
	}

	// The run belongs to the session the schedule named, so a user can find it.
	turns := svc.mustSessionMessages(t, "stocks")
	if turns == 0 {
		t.Error("the run should be recorded in its session, or it is invisible after the fact")
	}
}

// mustSessionMessages counts stored messages, so the test does not depend on the
// visible-turn filtering an application layer might do.
func (s *Service) mustSessionMessages(t *testing.T, sessionID string) int {
	t.Helper()
	sess, err := s.GetSession(sessionID)
	if err != nil || sess == nil {
		return 0
	}
	return len(sess.GetMessages())
}

func TestScheduledPromptLifecycle(t *testing.T) {
	svc, _ := newScheduledTestService(t)
	sch, err := svc.NewPromptScheduler()
	if err != nil {
		t.Fatalf("NewPromptScheduler: %v", err)
	}
	if err := sch.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = sch.Stop() })

	a, err := sch.Schedule("morning report", "@daily", "", "s1")
	if err != nil {
		t.Fatalf("schedule a: %v", err)
	}
	if _, err := sch.Schedule("hourly check", "0 * * * *", "", "s2"); err != nil {
		t.Fatalf("schedule b: %v", err)
	}

	list, err := sch.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d schedules, want 2: %+v", len(list), list)
	}

	// Pausing keeps the schedule but stops it firing.
	if err := sch.SetEnabled(a.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	list, _ = sch.List()
	for _, s := range list {
		if s.ID == a.ID && s.Enabled {
			t.Error("a disabled schedule should report Enabled=false")
		}
	}

	if err := sch.Delete(a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ = sch.List()
	if len(list) != 1 {
		t.Errorf("after delete there should be 1 schedule, got %d", len(list))
	}
}

func TestScheduledPromptRejectsNonsense(t *testing.T) {
	svc, _ := newScheduledTestService(t)
	sch, err := svc.NewPromptScheduler()
	if err != nil {
		t.Fatalf("NewPromptScheduler: %v", err)
	}
	if err := sch.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = sch.Stop() })

	// A prompt-less task would fail silently every morning; refuse it up front.
	if _, err := sch.Schedule("", "@daily", "", ""); err == nil {
		t.Error("an empty prompt must be refused")
	}
	if _, err := sch.Schedule("something", "", "", ""); err == nil {
		t.Error("an empty schedule must be refused")
	}
	if _, err := sch.Schedule("something", "not a cron expression", "", ""); err == nil {
		t.Error("an unparseable schedule must be refused, not stored to never fire")
	}

	// The executor contract, independent of the convenience layer.
	exec := NewPromptExecutor(svc)
	if exec.Type() != TaskTypeAgentPrompt {
		t.Errorf("executor type = %q", exec.Type())
	}
	if err := exec.Validate(map[string]string{}); err == nil {
		t.Error("Validate must reject a missing prompt")
	}
	if err := exec.Validate(map[string]string{ParamPrompt: "x"}); err != nil {
		t.Errorf("Validate rejected a valid task: %v", err)
	}
	if _, err := exec.Execute(context.Background(), map[string]string{}); err == nil {
		t.Error("Execute must reject a missing prompt")
	}
}

// TestScheduledPromptTimeoutIsConfigurable pins the option, since the default is
// deliberately long and a host may want it shorter.
func TestScheduledPromptTimeoutIsConfigurable(t *testing.T) {
	svc, _ := newScheduledTestService(t)
	exec := NewPromptExecutor(svc, WithPromptTimeout(2*time.Second), WithPromptSessionID("elsewhere"))
	if exec.timeout != 2*time.Second {
		t.Errorf("timeout = %v, want 2s", exec.timeout)
	}
	if exec.defaultSession != "elsewhere" {
		t.Errorf("default session = %q", exec.defaultSession)
	}
	// A zero or negative timeout is ignored rather than making every run fail.
	exec = NewPromptExecutor(svc, WithPromptTimeout(0))
	if exec.timeout != 15*time.Minute {
		t.Errorf("a zero timeout should leave the default, got %v", exec.timeout)
	}

	var _ scheduler.Executor = exec // the adapter must satisfy the interface
}

// TestScheduledPromptDoesNotAnswerFromMemory is the regression for a schedule
// that fired every morning, called nothing, and replied "I couldn't find that in
// memory."
//
// With memory enabled, the recall short-circuit answers a turn straight out of
// long-term memory whenever anything relevant comes back — which is right for a
// question and wrong for every scheduled prompt, because a schedule is an
// instruction to go and do something. The tests above missed it by building a
// service with no memory service at all, so the shortcut could never fire.
//
// explicitRecallTestLLM discriminates the two paths for us: Generate (the
// shortcut) answers "I couldn't find that in memory.", GenerateWithTools (the
// real path) answers "tool-path".
func TestScheduledPromptDoesNotAnswerFromMemory(t *testing.T) {
	ctx := context.Background()
	llm := &explicitRecallTestLLM{}

	svc, err := New("scheduled-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	// Something recallable, so the shortcut has every reason to fire.
	if err := svc.MemoryService().Add(ctx, &domain.Memory{
		ID:         "memory-stocks-1",
		SessionID:  "agent:scheduled-agent",
		ScopeType:  domain.MemoryScopeAgent,
		ScopeID:    "scheduled-agent",
		Type:       domain.MemoryTypeContext,
		Content:    "用户持有的股票是茅台和宁德时代。",
		Importance: 0.9,
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("add memory: %v", err)
	}

	exec := NewPromptExecutor(svc)
	res, err := exec.Execute(ctx, map[string]string{
		ParamPrompt: "统计我持有的股票今天的收益，然后发消息告诉我",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("run failed: %s", res.Error)
	}
	if strings.Contains(res.Output, "couldn't find that in memory") {
		t.Fatalf("the run answered out of memory instead of acting: %q", res.Output)
	}
	if llm.generateWithToolsCalls == 0 {
		t.Error("a scheduled prompt must reach the tool path — it exists to do something")
	}

	// A host can still thread its own run options through the executor.
	custom := NewPromptExecutor(svc, WithPromptRunOptions(WithMaxTurns(3)))
	cfg := DefaultRunConfig()
	for _, opt := range custom.runOpts {
		opt(cfg)
	}
	if cfg.MaxTurns != 3 {
		t.Errorf("WithPromptRunOptions must reach the run config; MaxTurns = %d", cfg.MaxTurns)
	}
}
