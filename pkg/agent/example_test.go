package agent_test

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

// exampleService builds a service over a scripted model in a throwaway home,
// so the examples below run under `go test` without a provider.
func exampleService(llm domain.Generator, opts ...func(*agent.Builder) *agent.Builder) (*agent.Service, func()) {
	home, err := os.MkdirTemp("", "agentgo-example")
	if err != nil {
		panic(err)
	}
	b := agent.New("demo").WithLLM(llm)
	for _, opt := range opts {
		b = opt(b)
	}
	svc, err := extensiontest.NewServiceWithBuilder(b, home)
	if err != nil {
		panic(err)
	}
	return svc, func() {
		_ = svc.Close()
		_ = os.RemoveAll(home)
	}
}

func ExampleService_Ask() {
	svc, cleanup := exampleService(extensiontest.Script(
		extensiontest.Answer("Paris."),
	))
	defer cleanup()

	answer, err := svc.Ask(context.Background(), "What is the capital of France?")
	if err != nil {
		panic(err)
	}
	fmt.Println(answer)
	// Output: Paris.
}

func ExampleService_RunStream() {
	svc, cleanup := exampleService(extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "hi"}),
		extensiontest.Answer("Echoed."),
	), func(b *agent.Builder) *agent.Builder {
		return b.WithExtensions(extensiontest.EchoTool())
	})
	defer cleanup()

	events, err := svc.RunStream(context.Background(), "echo hi")
	if err != nil {
		panic(err)
	}
	for ev := range events {
		switch ev.Type {
		case agent.EventTypeToolCall:
			fmt.Println("tool:", ev.ToolName)
		case agent.EventTypeComplete:
			fmt.Println("done:", ev.Content)
		}
	}
	// Output:
	// tool: echo
	// done: Echoed.
}

func ExampleService_Run() {
	svc, cleanup := exampleService(extensiontest.Script(
		extensiontest.Answer("Four."),
	))
	defer cleanup()

	result, err := svc.Run(context.Background(), "What is 2+2?", agent.WithMaxTurns(3))
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Text(), result.StopReason)
	// Output: Four. end_turn
}

// shoutLint rejects any answer that is not shouted.
type shoutLint struct{}

func (shoutLint) Name() string { return "must_shout" }

func (shoutLint) Check(text string, _ agent.LintContext) (bool, string) {
	if strings.ToUpper(text) == text {
		return true, ""
	}
	return false, "answer in capitals"
}

func ExampleService_RegisterOutputLint() {
	svc, cleanup := exampleService(extensiontest.Script(
		extensiontest.Answer("hello"),
		extensiontest.Answer("HELLO"),
	))
	defer cleanup()

	svc.RegisterOutputLint(shoutLint{})

	answer, err := svc.Ask(context.Background(), "say hello")
	if err != nil {
		panic(err)
	}
	fmt.Println(answer)
	// Output: HELLO
}

// briefing appends one system message to every run.
type briefing struct{}

func (briefing) Name() string { return "briefing" }

func (briefing) ContributeContext(_ context.Context, _ agent.ContextInput) ([]domain.Message, error) {
	return []domain.Message{{Role: "system", Content: "The customer is on the enterprise plan."}}, nil
}

func ExampleBuilder_WithExtensions() {
	llm := extensiontest.Script(extensiontest.Answer("Noted."))
	svc, cleanup := exampleService(llm, func(b *agent.Builder) *agent.Builder {
		return b.WithExtensions(briefing{})
	})
	defer cleanup()

	if _, err := svc.Ask(context.Background(), "which plan is this customer on?"); err != nil {
		panic(err)
	}
	for _, msg := range llm.Rounds()[0] {
		if strings.Contains(msg.Content, "enterprise plan") {
			fmt.Println("the model saw the briefing")
		}
	}
	// Output: the model saw the briefing
}

func ExampleNewActivityLog() {
	var log strings.Builder
	svc, cleanup := exampleService(extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "hi"}),
		extensiontest.Answer("Echoed."),
	), func(b *agent.Builder) *agent.Builder {
		return b.WithObserver(agent.NewActivityLog(&log)).
			WithExtensions(extensiontest.EchoTool())
	})
	defer cleanup()

	if _, err := svc.Ask(context.Background(), "echo hi"); err != nil {
		panic(err)
	}
	for _, line := range strings.Split(log.String(), "\n") {
		if strings.Contains(line, "tool>") {
			fmt.Println(strings.TrimSpace(line[strings.Index(line, "tool>"):]))
		}
	}
	// Output: tool>   echo text=hi
}
