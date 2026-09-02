package usage_test

import (
	"context"
	"fmt"
	"os"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensions/usage"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

func ExampleNew() {
	home, err := os.MkdirTemp("", "agentgo-usage-example")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)

	meter := usage.New()
	svc, err := extensiontest.NewServiceWithBuilder(
		agent.New("worker").
			WithLLM(extensiontest.Script(
				extensiontest.CallTool("echo", map[string]interface{}{"text": "hi"}),
				extensiontest.Answer("Echoed."),
			)).
			WithExtensions(meter, extensiontest.EchoTool()),
		home,
	)
	if err != nil {
		panic(err)
	}
	defer svc.Close()

	if _, err := svc.Ask(context.Background(), "echo hi"); err != nil {
		panic(err)
	}

	snap := meter.Snapshot()
	fmt.Println("model calls:", snap.Total.Calls)
	fmt.Println("retries:", snap.Retries)
	// Output:
	// model calls: 2
	// retries: 0
}

func ExampleTotals_CacheHitRate() {
	t := usage.Totals{PromptTokens: 1000, CachedTokens: 750}
	fmt.Printf("%.2f\n", t.CacheHitRate())
	// Output: 0.75
}
