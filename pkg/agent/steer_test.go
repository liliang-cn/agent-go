package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

func hasMessage(msgs []domain.Message, role, content string) bool {
	for _, m := range msgs {
		if m.Role == role && strings.Contains(m.Content, content) {
			return true
		}
	}
	return false
}

func eventsOfType(evs []*agent.Event, t agent.EventType) []*agent.Event {
	var out []*agent.Event
	for _, e := range evs {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// A message steered while a tool is running lands after that tool's result
// and before the next model call, is announced as an event, and is in the
// session history afterwards.
func TestSteerLandsAtTheNextRoundBoundary(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	slow := extensiontest.ToolModule("slow", "takes a while", func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return "slow tool finished", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	llm := extensiontest.Script(
		extensiontest.CallTool("slow", map[string]interface{}{}),
		extensiontest.Answer("done"),
	)
	svc := extensiontest.NewService(t, llm, slow)
	const session = "steer-session"

	go func() {
		<-started
		if !svc.Steer(session, domain.Message{Content: "[message from worker-1] the ledger is on port 5433"}) {
			t.Error("Steer returned false with a run in flight")
		}
		close(release)
	}()
	out := extensiontest.Run(t, svc, "check the ledger", agent.WithSessionID(session))
	if out.Final != "done" {
		t.Fatalf("run ended %q (blocked %q, errors %v)", out.Final, out.Blocked, out.Errors)
	}

	rounds := llm.Rounds()
	if len(rounds) != 2 {
		t.Fatalf("model calls = %d, want 2", len(rounds))
	}
	second := rounds[1]
	if !hasMessage(second, "user", "[message from worker-1]") {
		t.Fatalf("second model call did not carry the steered message:\n%+v", second)
	}
	// After the tool result, not before it.
	toolAt, steerAt := -1, -1
	for i, m := range second {
		if m.Role == "tool" {
			toolAt = i
		}
		if m.Role == "user" && strings.Contains(m.Content, "[message from worker-1]") {
			steerAt = i
		}
	}
	if toolAt < 0 || steerAt < toolAt {
		t.Fatalf("steer at %d, tool result at %d: the steer must follow the round's tool results", steerAt, toolAt)
	}
	if got := eventsOfType(out.Events, agent.EventTypeSteer); len(got) != 1 || !strings.Contains(got[0].Content, "worker-1") {
		t.Fatalf("steer events = %+v", got)
	}
	if got := eventsOfType(out.Events, agent.EventTypeSteerDropped); len(got) != 0 {
		t.Fatalf("an applied steer was reported dropped: %+v", got)
	}
	sess, err := svc.GetSession(session)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMessage(sess.GetMessages(), "user", "[message from worker-1]") {
		t.Fatal("the steered message is not in the session history")
	}
}

// steeringLLM steers the run from inside its own first model call, which is
// how a message that arrives during the final answer looks to the loop.
type steeringLLM struct {
	*extensiontest.ScriptedLLM
	once sync.Once
	hook func()
}

func (l *steeringLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.once.Do(l.hook)
	return l.ScriptedLLM.GenerateWithTools(ctx, messages, tools, opts)
}

func (l *steeringLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	l.once.Do(l.hook)
	return l.ScriptedLLM.StreamWithTools(ctx, messages, tools, opts, cb)
}

// A steer that lands during the final model call is not lost: the run takes
// one more round with the answer as the assistant's turn and the message
// after it.
func TestSteerDuringTheFinalAnswerGetsOneMoreRound(t *testing.T) {
	const session = "steer-final"
	var svc *agent.Service
	llm := &steeringLLM{ScriptedLLM: extensiontest.Script(
		extensiontest.Answer("first answer"),
		extensiontest.Answer("second answer"),
	)}
	llm.hook = func() {
		if !svc.Steer(session, domain.Message{Content: "[message from queen] also check the backups"}) {
			t.Error("Steer returned false during the model call")
		}
	}
	svc = extensiontest.NewService(t, llm)
	out := extensiontest.Run(t, svc, "check the ledger", agent.WithSessionID(session))
	if out.Final != "second answer" {
		t.Fatalf("final = %q, want the answer to the extra round (blocked %q, errors %v)", out.Final, out.Blocked, out.Errors)
	}
	rounds := llm.Rounds()
	if len(rounds) != 2 {
		t.Fatalf("model calls = %d, want 2", len(rounds))
	}
	second := rounds[1]
	if !hasMessage(second, "assistant", "first answer") || !hasMessage(second, "user", "[message from queen]") {
		t.Fatalf("the extra round did not carry the first answer and the steer:\n%+v", second)
	}
	sess, _ := svc.GetSession(session)
	msgs := sess.GetMessages()
	if !hasMessage(msgs, "assistant", "first answer") || !hasMessage(msgs, "user", "[message from queen]") || !hasMessage(msgs, "assistant", "second answer") {
		t.Fatalf("history is missing part of the exchange:\n%+v", msgs)
	}
}

// A run that is cancelled with a steer still queued says so, so the host can
// start a turn with it instead.
func TestSteerDroppedWhenTheRunEndsWithoutApplyingIt(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	llm := extensiontest.Script(extensiontest.CallTool("slow", map[string]interface{}{}), extensiontest.Answer("never"))
	svc := extensiontest.NewService(t, llm)
	// Declared cancellable: a tool that leaves InterruptBehavior unset is
	// treated as blocking, and a cancel waits for it.
	svc.AddToolWithMetadata("slow", "takes a while", map[string]interface{}{"type": "object"},
		func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return nil, ctx.Err()
		}, agent.ToolMetadata{InterruptBehavior: agent.InterruptBehaviorCancel})
	const session = "steer-cancel"
	go func() {
		<-started
		svc.Steer(session, domain.Message{Content: "[message from queen] stop what you are doing"})
		time.Sleep(50 * time.Millisecond)
		svc.CancelSession(session)
	}()
	out := extensiontest.Run(t, svc, "check the ledger", agent.WithSessionID(session))
	if out.Final != "" {
		t.Fatalf("a cancelled run completed with %q", out.Final)
	}
	if got := eventsOfType(out.Events, agent.EventTypeSteerDropped); len(got) != 1 || !strings.Contains(got[0].Content, "stop what you are doing") {
		t.Fatalf("dropped-steer events = %+v", got)
	}
}

func TestSteerWithNoRunReturnsFalse(t *testing.T) {
	svc := extensiontest.NewService(t, extensiontest.Script(extensiontest.Answer("x")))
	if svc.Steer("nobody", domain.Message{Content: "hi"}) {
		t.Fatal("Steer returned true with no run on the session")
	}
	if svc.SteerRun("no-such-run", domain.Message{Content: "hi"}) {
		t.Fatal("SteerRun returned true for an unknown run")
	}
	if svc.Steer("nobody", domain.Message{}) {
		t.Fatal("an empty message was queued")
	}
}
