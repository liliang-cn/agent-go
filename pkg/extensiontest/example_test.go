package extensiontest_test

import (
	"context"
	"fmt"
	"os"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

func ExampleScript() {
	home, err := os.MkdirTemp("", "agentgo-extensiontest-example")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)

	llm := extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "hi"}),
		extensiontest.Answer("done"),
	)
	svc, err := extensiontest.NewServiceWithBuilder(
		agent.New("under-test").WithLLM(llm).WithExtensions(extensiontest.EchoTool()),
		home,
	)
	if err != nil {
		panic(err)
	}
	defer svc.Close()

	answer, err := svc.Ask(context.Background(), "say hi")
	if err != nil {
		panic(err)
	}
	fmt.Println(answer, llm.Calls())

	for _, msg := range extensiontest.ToolMessages(llm.Rounds()[1]) {
		fmt.Println(msg.Content)
	}
	// Output:
	// done 2
	// {"echoed":"hi"}
}
