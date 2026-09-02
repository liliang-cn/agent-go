package extensiontest_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

// redactor is the kind of extension a third party would write: it replaces
// a word in tool results and refuses a final answer that still carries it.
type redactor struct {
	replaced int32
	started  int32
	stopped  int32
}

func (r *redactor) Name() string { return "redactor" }

func (r *redactor) AfterTool(_ context.Context, res agent.ToolResultInfo) (interface{}, bool, error) {
	m, ok := res.Result.(map[string]interface{})
	if !ok {
		return nil, false, nil
	}
	if s, ok := m["echoed"].(string); ok && strings.Contains(s, "secret") {
		atomic.AddInt32(&r.replaced, 1)
		return map[string]interface{}{"echoed": strings.ReplaceAll(s, "secret", "[redacted]")}, true, nil
	}
	return nil, false, nil
}

func (r *redactor) Check(text string, _ agent.LintContext) (bool, string) {
	if strings.Contains(text, "secret") {
		return false, "the answer repeats the secret; leave it out"
	}
	return true, ""
}

func (r *redactor) Start(context.Context) error { atomic.AddInt32(&r.started, 1); return nil }
func (r *redactor) Stop(context.Context) error  { atomic.AddInt32(&r.stopped, 1); return nil }

func TestAThirdPartyExtensionThroughTheRealLoop(t *testing.T) {
	ext := &redactor{}
	llm := extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "the secret plan"}),
		extensiontest.Answer("I found the secret plan"), // rejected by the lint
		extensiontest.Answer("I found the plan"),        // accepted
	)
	svc := extensiontest.NewService(t, llm, extensiontest.EchoTool(), ext)

	out := extensiontest.Run(t, svc, "what did echo say?")
	if out.Blocked != "" || out.Final != "I found the plan" {
		t.Fatalf("final=%q blocked=%q errors=%v", out.Final, out.Blocked, out.Errors)
	}
	if atomic.LoadInt32(&ext.replaced) != 1 {
		t.Fatalf("result filter ran %d times", ext.replaced)
	}

	rounds := llm.Rounds()
	if len(rounds) < 2 {
		t.Fatalf("expected at least two rounds, got %d", len(rounds))
	}
	tools := extensiontest.ToolMessages(rounds[1])
	if len(tools) != 1 || !strings.Contains(tools[0].Content, "[redacted]") || strings.Contains(tools[0].Content, "secret") {
		t.Fatalf("model saw %+v", tools)
	}
	if llm.Calls() != 3 {
		t.Fatalf("expected the lint to force a third turn, got %d calls", llm.Calls())
	}
	if atomic.LoadInt32(&ext.started) != 1 {
		t.Fatal("Lifecycle.Start did not run at Build")
	}
	_ = svc.Close()
	if atomic.LoadInt32(&ext.stopped) != 1 {
		t.Fatal("Lifecycle.Stop did not run at Close")
	}
}

type veto struct{}

func (veto) Name() string { return "veto" }
func (veto) OnRunStart(context.Context, agent.RunInfo) error {
	return errors.New("not today")
}
func (veto) OnRunEnd(context.Context, agent.RunInfo, agent.RunOutcome) {}

func TestRunReportsABlockedRun(t *testing.T) {
	llm := extensiontest.Script(extensiontest.Answer("never"))
	svc := extensiontest.NewService(t, llm, veto{})
	out := extensiontest.Run(t, svc, "anything")
	if out.Final != "" || !strings.Contains(out.Blocked, "not today") {
		t.Fatalf("final=%q blocked=%q", out.Final, out.Blocked)
	}
	if llm.Calls() != 0 {
		t.Fatalf("model was called %d times despite the veto", llm.Calls())
	}
}
