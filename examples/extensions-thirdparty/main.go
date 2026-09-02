// Package main is what a third-party extension looks like from the outside:
// its own module, its own package (./budgetgate), and one line in
// WithExtensions to install it. Nothing in the framework knows it exists.
//
// Usage:
//
//	cd examples/extensions-thirdparty
//	go test ./...      # the extension's own tests, through the real loop, no model
//	go run .           # a live run; provider from AGENTGO_HOME as in quickstart
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"example.com/budgetgate/budgetgate"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	gate := budgetgate.New(0.05) // five cents for the whole process

	svc, err := agent.New("budgeted").
		WithPrompt("Answer in one sentence.").
		WithExtensions(gate).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	for i := 1; i <= 3; i++ {
		result, err := svc.Run(ctx, fmt.Sprintf("Question %d: name a prime number larger than %d.", i, i*10))
		if err != nil {
			log.Fatal(err)
		}
		spent, unpriced := gate.Spent()
		switch {
		case result.Blocked:
			fmt.Printf("run %d refused: %s\n", i, result.Text())
		default:
			fmt.Printf("run %d: %s\n", i, result.Text())
		}
		fmt.Printf("   spent $%.5f, unpriced turns %d\n", spent, unpriced)
	}
}
