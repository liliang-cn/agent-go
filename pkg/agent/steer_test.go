package agent_test

import (
	"context"
	"strconv"
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

// What the history keeps of a run that was steered: every tool call, its
// result right after it and only once, and the steer after the round it
// arrived in. A history that broke this is what a strict backend refused on
// the next turn ("'tool_call_id' of X not found in 'tool_calls' of previous
// message"): calls with no content shared one dedup key so only the first
// was stored, and results were appended again after the steer.
func TestSteeredRunStoresEachCallBeforeItsResult(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	slow := extensiontest.ToolModule("slow", "takes a while", func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return "slow tool finished", nil
	})
	fast := extensiontest.ToolModule("fast", "quick", func(context.Context, map[string]interface{}) (interface{}, error) {
		return "fast tool finished", nil
	})
	llm := extensiontest.Script(
		extensiontest.CallTool("slow", map[string]interface{}{}),
		extensiontest.CallTool("fast", map[string]interface{}{}),
		extensiontest.Answer("first done"),
		// A call id is unique per call on every real backend; the scripted
		// one is per tool name, so the second turn's call gets its own.
		func() *domain.GenerationResult {
			r := extensiontest.CallTool("fast", map[string]interface{}{"again": true})
			r.ToolCalls[0].ID = "call-fast-2"
			return r
		}(),
		extensiontest.Answer("second done"),
	)
	svc := extensiontest.NewService(t, llm, slow, fast)
	const session = "steer-history"
	go func() {
		<-started
		svc.Steer(session, domain.Message{Content: "steered in mid-run"})
		close(release)
	}()
	if out := extensiontest.Run(t, svc, "first", agent.WithSessionID(session)); out.Final != "first done" {
		t.Fatalf("first run ended %q (blocked %q, errors %v)", out.Final, out.Blocked, out.Errors)
	}
	if out := extensiontest.Run(t, svc, "second", agent.WithSessionID(session)); out.Final != "second done" {
		t.Fatalf("second run ended %q (blocked %q, errors %v)", out.Final, out.Blocked, out.Errors)
	}

	sess, err := svc.GetSession(session)
	if err != nil {
		t.Fatal(err)
	}
	msgs := sess.GetMessages()
	calls, results := 0, map[string]int{}
	steerAt, firstResultAt := -1, -1
	for i, m := range msgs {
		calls += len(m.ToolCalls)
		if m.Role == "user" && strings.Contains(m.Content, "steered in mid-run") {
			steerAt = i
		}
		if m.Role != "tool" {
			continue
		}
		results[m.ToolCallID]++
		if firstResultAt < 0 {
			firstResultAt = i
		}
		j := i - 1
		for j >= 0 && msgs[j].Role == "tool" {
			j--
		}
		found := false
		for _, tc := range msgs[max(j, 0)].ToolCalls {
			found = found || tc.ID == m.ToolCallID
		}
		if j < 0 || !found {
			t.Fatalf("result %d (%s) does not follow its call:\n%s", i, m.ToolCallID, dumpRoles(msgs))
		}
	}
	if calls != 3 || len(results) != 3 {
		t.Fatalf("stored %d calls and %d results, want 3 and 3:\n%s", calls, len(results), dumpRoles(msgs))
	}
	for id, n := range results {
		if n != 1 {
			t.Fatalf("result %s stored %d times:\n%s", id, n, dumpRoles(msgs))
		}
	}
	if steerAt < firstResultAt {
		t.Fatalf("steer at %d is before the round it arrived in (first result %d):\n%s", steerAt, firstResultAt, dumpRoles(msgs))
	}
}

func dumpRoles(msgs []domain.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		ids := make([]string, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			ids = append(ids, tc.ID)
		}
		c := m.Content
		if len(c) > 30 {
			c = c[:30]
		}
		b.WriteString(strings.Join([]string{"", itoa(i), m.Role, m.ToolCallID, strings.Join(ids, ","), c}, " | ") + "\n")
	}
	return b.String()
}

func itoa(i int) string { return strconv.Itoa(i) }
