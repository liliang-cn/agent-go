package agent_test

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

func ExampleService_RunSegments() {
	svc, cleanup := exampleService(extensiontest.Script(
		extensiontest.Answer("Report written."),
	))
	defer cleanup()

	result, err := svc.RunSegments(context.Background(), "write the quarterly report", agent.LongRunConfig{
		MaxSegments:      4,
		RoundsPerSegment: 5,
		MaxTotalCostUSD:  1.0,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Stop, result.Done(), len(result.Segments))
	fmt.Println(result.Text)
	// Output:
	// finished true 1
	// Report written.
}
