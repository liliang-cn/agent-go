// Tool arguments checked before they run, and a tool's track record kept
// across runs.
//
// Every model-issued tool call is validated against the tool's declared
// parameter schema before it executes. A call missing a required argument, or
// sending the wrong type, is not run: the model gets the failing field paths
// back as the tool's error and repairs the call.
// Builder.WithToolArgValidation(false) turns this off.
//
// Every call's outcome — success, error, or refused for invalid arguments —
// and its latency is counted per tool and written to the Service's own
// database, so the numbers printed at the end include earlier runs of this
// program. A ToolReliabilityObserver is told about each outcome as it happens.
//
//	go run ./examples/tool-reliability
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

type printer struct{ agent.BaseObserver }

func (printer) OnToolOutcome(_ context.Context, o agent.ToolOutcome, r agent.ToolReliability) {
	fmt.Printf("  %-10s %-13s %8s   (runs=%d errors=%d invalid=%d)\n",
		o.Tool, o.Kind, o.Latency.Round(time.Microsecond), r.Executions(), r.Errors, r.InvalidArgs)
}

func main() {
	svc, err := agent.New("tool-reliability-demo").
		WithSystemPrompt("You answer briefly. Use the weather tool for weather questions.").
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()
	svc.RegisterObserver(printer{})

	svc.AddTool("weather", "Current weather for a city.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"city": map[string]interface{}{"type": "string", "description": "city name"},
				"unit": map[string]interface{}{"type": "string", "enum": []interface{}{"C", "F"}},
			},
			"required": []interface{}{"city"},
		},
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			if rand.Intn(4) == 0 {
				return nil, errors.New("weather service timed out")
			}
			return fmt.Sprintf("%v: 21°C, clear", args["city"]), nil
		})

	res, err := svc.Run(context.Background(), "What is the weather in Vienna and in Tokyo?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\nanswer:", res.Text())

	fmt.Println("\ntrack record (this run and every earlier one on this database):")
	for _, r := range svc.ToolReliability() {
		fmt.Printf("  %-24s runs=%-4d error_rate=%.0f%%  invalid_args=%-3d mean=%s max=%s\n",
			r.Tool, r.Executions(), 100*r.ErrorRate(), r.InvalidArgs,
			r.MeanLatency().Round(time.Microsecond), r.MaxLatency.Round(time.Microsecond))
	}
}
