// Package main demonstrates cancelling work in AgentGo: a single agent run,
// and a single execution of a scheduled prompt.
//
// The two are different mechanisms for the same idea. A run is cancelled
// through the service's in-flight registry (Cancel / CancelRun /
// CancelSession); a scheduled execution is cancelled through the scheduler's
// own per-execution registry (PromptScheduler.CancelRun), because there the
// caller never held the context in the first place.
//
// Neither of them is an error. A cancelled run reports Cancelled with a nil
// Err(), and a cancelled execution lands in the history as "cancelled" — so a
// UI can say "you stopped this" instead of painting the user's own stop button
// red.
//
// Usage:
//
//	go run ./examples/cancel
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func main() {
	svc, err := agent.New("cancel-demo").Build()
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	defer svc.Close()

	cancelARun(svc)
	cancelAScheduledRun(svc)
}

// cancelARun starts a streaming run under a name of our choosing and stops
// that one run by name. WithRunID matters for a UI: it can register the stop
// button before the first event arrives, instead of waiting to read the id
// back out of ActiveRuns.
func cancelARun(svc *agent.Service) {
	fmt.Println("=== cancelling one run ===")

	const runID = "demo-run"
	events, err := svc.RunStreamWithOptions(context.Background(),
		"Write an exhaustive analysis of every prime number under one million.",
		agent.WithRunID(runID),
		agent.WithSessionID("cancel-demo-session"),
	)
	if err != nil {
		log.Fatalf("start run: %v", err)
	}

	// Let it get going, then pull the plug.
	go func() {
		time.Sleep(3 * time.Second)
		for _, r := range svc.ActiveRuns() {
			fmt.Printf("in flight: run=%s session=%s started=%s\n",
				r.RunID, r.SessionID, r.StartedAt.Format(time.TimeOnly))
		}
		// CancelRun stops exactly this run. svc.Cancel() would stop every run
		// on the service; svc.CancelSession(id) every run in one conversation.
		if !svc.CancelRun(runID) {
			fmt.Println("nothing to cancel — the run had already finished")
		}
	}()

	for evt := range events {
		switch evt.Type {
		case agent.EventTypeCancelled:
			// The fourth terminal event, alongside complete / blocked / error.
			fmt.Printf("stopped: %s (stop_reason=%s)\n", evt.Content, evt.StopReason)
		case agent.EventTypeComplete:
			fmt.Printf("finished before we could stop it: %s\n", evt.Content)
		case agent.EventTypeError:
			fmt.Printf("failed: %s\n", evt.Content)
		}
	}
	fmt.Println()
}

// cancelAScheduledRun creates a schedule, fires it off the clock, and stops
// that execution — leaving the schedule itself intact and runnable again.
func cancelAScheduledRun(svc *agent.Service) {
	fmt.Println("=== cancelling one scheduled execution ===")

	sched, err := svc.NewPromptScheduler(
		agent.WithPromptSessionID("cancel-demo-scheduled"),
		agent.WithPromptObserver(func(run agent.PromptRun) {
			switch {
			case run.Cancelled:
				fmt.Printf("scheduled run stopped after %s\n", run.Duration.Round(time.Millisecond))
			case run.Err != nil:
				fmt.Printf("scheduled run failed: %v\n", run.Err)
			default:
				fmt.Printf("scheduled run finished: %s\n", run.Answer)
			}
		}),
	)
	if err != nil {
		log.Fatalf("build scheduler: %v", err)
	}
	if err := sched.Start(); err != nil {
		log.Fatalf("start scheduler: %v", err)
	}
	defer func() { _ = sched.Stop() }()

	prompt, err := sched.Schedule(
		"Summarise every article published on the internet today.",
		"0 8 * * *", "morning digest", "cancel-demo-scheduled")
	if err != nil {
		log.Fatalf("schedule: %v", err)
	}
	defer func() { _ = sched.Delete(prompt.ID) }()

	// RunNowAsync rather than RunNow: RunNow blocks for as long as the run
	// takes (fifteen minutes by default), and a caller stuck inside it cannot
	// offer the cancel button that would end the wait.
	runID, err := sched.RunNowAsync(prompt.ID)
	if err != nil {
		log.Fatalf("run now: %v", err)
	}
	fmt.Printf("started execution %s\n", runID)

	time.Sleep(3 * time.Second)
	stopped, err := sched.CancelRun(prompt.ID)
	if err != nil {
		log.Fatalf("cancel: %v", err)
	}
	fmt.Printf("cancelled %d execution(s)\n", stopped)

	// Give the execution record a moment to settle, then read the history:
	// the run is "cancelled", not "failed".
	time.Sleep(time.Second)
	history, err := sched.History(prompt.ID, 1)
	if err != nil {
		log.Fatalf("history: %v", err)
	}
	for _, h := range history {
		fmt.Printf("history: status=%s error=%q\n", h.Status, h.Error)
	}

	// And the schedule is still there, still enabled, ready to run again.
	list, err := sched.List()
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	for _, s := range list {
		if s.ID == prompt.ID {
			fmt.Printf("schedule survives: enabled=%v running=%v next=%v\n",
				s.Enabled, s.Running, s.NextRun)
		}
	}
}
