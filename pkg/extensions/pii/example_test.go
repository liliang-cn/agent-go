package pii_test

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensions/pii"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

func ExampleExtension_Redact() {
	e := pii.New()
	out, kinds := e.Redact("mail alice@example.com or call +1 415 555 0142")
	fmt.Println(out)
	fmt.Println(kinds)
	// Output:
	// mail a***@example.com or call ***0142
	// [email phone]
}

func ExampleNew() {
	home, err := os.MkdirTemp("", "agentgo-pii-example")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)

	lookup := extensiontest.ToolModule("lookup", "looks a customer up",
		func(context.Context, map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"email": "bob@corp.io"}, nil
		})

	llm := extensiontest.Script(
		extensiontest.CallTool("lookup", map[string]interface{}{}),
		extensiontest.Answer("Found the customer."),
	)
	svc, err := extensiontest.NewServiceWithBuilder(
		agent.New("support").WithLLM(llm).WithExtensions(pii.New(), lookup),
		home,
	)
	if err != nil {
		panic(err)
	}
	defer svc.Close()

	if _, err := svc.Ask(context.Background(), "look the customer up"); err != nil {
		panic(err)
	}
	for _, round := range llm.Rounds() {
		for _, msg := range extensiontest.ToolMessages(round) {
			if strings.Contains(msg.Content, "bob@corp.io") {
				fmt.Println("leaked")
				return
			}
		}
	}
	fmt.Println("the model never saw the address")
	// Output: the model never saw the address
}
