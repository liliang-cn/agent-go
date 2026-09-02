// Work that outlives the turn that started it.
//
// Some things a person would never stand and wait for: a crawl, a build, a
// report over a week of logs. Making the model wait stops the conversation
// and burns the round budget on a tool that is merely slow.
//
//	go run ./examples/background
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func main() {
	svc, err := agent.New("assistant").
		WithSystemPrompt("You answer briefly.").
		// Off by default: a background task is a whole run with its own
		// budget, so its author decides whether the agent may start one.
		// The agent gets background_start, background_check, background_cancel.
		WithBackgroundTasks(4).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	ctx := context.Background()

	// --- The host's side ------------------------------------------------
	//
	// The task does not inherit this context. That is the whole difference
	// from a sub-agent, which runs under its parent and dies with it: this
	// one has to survive the turn that started it.
	task, err := svc.StartBackgroundTask(ctx,
		"List the first ten prime numbers and explain the sieve of Eratosthenes.",
		agent.WithBackgroundLabel("primes"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("started %s (%s)\n", task.Label, task.ID)

	// The conversation carries on while it runs.
	if res, err := svc.Run(ctx, "Say hello in one word."); err == nil {
		fmt.Printf("meanwhile, the chat answered: %s\n", res.Text())
	}

	// Collect it later. In a real host this is a following turn, or the
	// agent itself calling background_check.
	for {
		got, ok := svc.BackgroundTask(task.ID)
		if !ok {
			log.Fatal("the task vanished")
		}
		if got.Status.Done() {
			fmt.Printf("\n%s finished %s after %s\n", got.Label, got.Status, got.Duration().Round(time.Second))
			fmt.Println(got.Result)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Everything in flight, for a host that wants to show it.
	for _, t := range svc.BackgroundTasks() {
		fmt.Printf("- %-8s %-10s %s\n", t.Label, t.Status, t.Goal)
	}

	// And Close stops anything still running, rather than leaving it to
	// write through a store that has been released.
}
