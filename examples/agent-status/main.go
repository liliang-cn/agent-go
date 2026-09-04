// What is this agent doing, asked from outside the run.
//
// A Service's event stream has exactly one consumer: whoever called
// RunStream. A status endpoint, an operator's console, a second window and a
// metrics scrape are none of them. StatusSnapshot is the read side of the
// loop — it takes a lock and copies, calls no model, and touches no store.
//
// This example runs a task and, while it runs, polls the service the way a
// separate goroutine (or an HTTP handler, shown at the bottom) would.
//
//	go run ./examples/agent-status
//	go run ./examples/agent-status -serve
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// statusAddr is a deliberately unusual port: 8080 and friends are already
// somebody else's on any machine worth running this on.
const statusAddr = "127.0.0.1:47311"

func main() {
	serve := flag.Bool("serve", false, "expose the snapshot as JSON on "+statusAddr)
	goal := flag.String("goal", "List three prime numbers, then explain in one sentence why 1 is not one.", "what to ask the agent")
	flag.Parse()

	svc, err := agent.New("status-demo").
		WithSystemPrompt("You answer briefly.").
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	// --- what a service says about itself before it does anything ---------
	before := svc.StatusSnapshot()
	fmt.Printf("state      %s\n", before.State)
	fmt.Printf("agent      %s (%s)\n", before.Agent.Name, before.Agent.Model)
	fmt.Printf("tools      %d\n", len(before.Agent.Tools))
	fmt.Printf("lints      %v\n", before.Lints)
	if before.Workspace != "" {
		// Where the run's files actually land — rarely this process's own
		// working directory, and the first thing an operator looks for.
		fmt.Printf("workspace  %s\n", before.Workspace)
	}
	fmt.Println()

	if *serve {
		// Three lines is the whole integration: every field is JSON-tagged.
		//
		// Goal carries the user's own prompt, so a host serving more than one
		// person must decide who may read this before exposing it.
		http.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(svc.StatusSnapshot(agent.WithProcessStats()))
		})
		go func() { _ = http.ListenAndServe(statusAddr, nil) }()
		fmt.Printf("serving http://%s/status\n\n", statusAddr)
	}

	// --- watch it work, from a goroutine that is not the caller -----------
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				for _, r := range svc.RunStatuses() {
					if !r.Reported {
						// Registered, but the loop has not published a
						// reading yet. "starting" — never "round 0 of 0".
						fmt.Printf("  %s  starting\n", short(r.RunID))
						continue
					}
					fmt.Printf("  %s  %-22s %-11s tools %-3d %-10s %s\n",
						short(r.RunID), r.Stage, rounds(r), r.ToolCalls,
						money(r.Usage), r.Duration.Round(time.Millisecond))
				}
			}
		}
	}()

	res, err := svc.Run(context.Background(), *goal)
	close(stop)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\n%s\n\n", res.Text())

	after := svc.StatusSnapshot()
	// The run is gone, not stale: it left the registry when its event stream
	// closed. Its durable record is the checkpoint and the task store.
	fmt.Printf("state      %s (%d runs in flight)\n", after.State, len(after.Runs))
	fmt.Printf("background %d running, %d finished\n",
		after.Background.Running, after.Background.Completed+after.Background.Failed)

	if *serve {
		fmt.Printf("\nstill serving http://%s/status — ctrl-c to stop\n", statusAddr)
		select {}
	}
}

// rounds is blank before the first round starts. The two stages that come
// first — resolving the run's constraints, then retrieving memory and
// documents — happen before there is a round budget to count against, and on
// a live run they are not brief: measured at four seconds, nearly all of it
// the constraint-extraction model call. Turn it off with
// agent.WithConstraintExtraction(false) if the caller already knows.
func rounds(r agent.RunStatus) string {
	if r.Round == 0 {
		return ""
	}
	return fmt.Sprintf("round %d/%d", r.Round, r.MaxRounds)
}

// money renders spend the one honest way: an unpriced model is not free.
func money(u agent.RunUsage) string {
	if u.CostUnpriced {
		return "$unpriced"
	}
	return fmt.Sprintf("$%.4f", u.CostUSD)
}

func short(runID string) string {
	if len(runID) > 8 {
		return runID[:8]
	}
	return runID
}
