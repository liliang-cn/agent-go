// Package main shows a standing responsibility: an agent that is never
// finished. The person says what should stay true, what to watch, what is
// worth telling them, and what must never happen. The agent sleeps, wakes
// for a reason, does one run, keeps notes, says when to wake it again, and
// sleeps.
//
// Here the responsibility is to watch a service's health. Two tools stand in
// for the real ones: a reading tool that reports the state (and changes it
// between calls, so there is something to notice) and a destructive one that
// the brief forbids. The example drives three wakes: one the host asks for,
// one from an event the host delivers while the agent is idle, and one idle
// scan — where only reading tools exist.
//
//	LLM_BASE_URL=https://api.deepseek.com/v1 LLM_API_KEY=$DEEPSEEK_API_KEY \
//	LLM_MODEL=deepseek-v4-flash go run ./examples/standing
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	baseURL, key, model := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
	if baseURL == "" || model == "" {
		log.Fatal("set LLM_BASE_URL, LLM_API_KEY and LLM_MODEL")
	}
	home, err := os.MkdirTemp("", "agentgo-standing-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(home)
	os.Setenv("AGENTGO_HOME", home)

	llm, err := pool.NewClient("main", baseURL, key, model)
	if err != nil {
		log.Fatal(err)
	}
	svc, err := agent.New("watcher").WithLLM(llm).Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	// The world the agent watches: healthy, then degraded.
	var reads atomic.Int32
	svc.AddToolWithMetadata("ledger_health", "Read the ledger service's current health: restarts, memory, error rate.",
		map[string]interface{}{"type": "object"},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			if reads.Add(1) >= 2 {
				return "restarts_last_hour=3 memory=94% error_rate=2.1% (threshold 1%)", nil
			}
			return "restarts_last_hour=0 memory=61% error_rate=0.1%", nil
		},
		agent.ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: agent.InterruptBehaviorCancel})
	svc.AddToolWithMetadata("ledger_rollback", "Roll the ledger service back to the previous release.",
		map[string]interface{}{"type": "object"},
		func(context.Context, map[string]interface{}) (interface{}, error) { return "rolled back", nil },
		agent.ToolMetadata{Destructive: true, InterruptBehavior: agent.InterruptBehaviorCancel})

	// Where the agent's messages go. A host sends the person's messages to
	// their channel and uses wake_started / wake_ended for its own display;
	// here the messages print and the wake ends drive the walkthrough.
	ended := make(chan agent.Wake, 16)
	notifier := agent.NotifierFunc(func(_ context.Context, n agent.Notification) {
		switch n.Kind {
		case agent.NotifyWakeEnded:
			ended <- *n.Wake
		case agent.NotifyWakeStarted:
		default:
			fmt.Printf("\n>>> [%s] %s\n\n", n.Kind, n.Message)
		}
	})
	st, err := agent.NewStanding(svc, agent.WithNotifier(notifier))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	r, err := st.Add(ctx, agent.Responsibility{
		Name:      "ledger",
		Goal:      "The ledger service stays healthy: error rate under 1%, memory under 90%, no restart loops.",
		Watch:     []string{"ledger_health"},
		Attention: "Tell the person when a threshold is crossed, once, with the numbers. Do not report a healthy service.",
		Never:     []string{"roll back or restart anything; the person decides that"},
		// The brief says never; the runtime makes it so.
		ToolDenylist:     []string{"ledger_rollback"},
		MaxWakesPerDay:   10,
		MaxRoundsPerWake: 6,
		Scan:             &agent.ScanPolicy{Every: 15 * time.Second, MaxRounds: 4},
	})
	if err != nil {
		log.Fatal(err)
	}

	wait := func(kind agent.WakeKind) {
		for {
			var w agent.Wake
			select {
			case w = <-ended:
			case <-ctx.Done():
				log.Fatal("timed out waiting for a wake")
			}
			s, _ := st.Get(r.ID)
			fmt.Printf("--- wake %s (%s): %d tool calls, next due %s (%s)\n    notes: %s\n",
				w.Kind, w.Reason, w.ToolCalls, s.NextDue.Format(time.Kitchen), s.NextDueKind, s.Responsibility.Notes)
			if w.Kind == kind {
				return
			}
		}
	}

	// 1. The host asks for a first look. Healthy: nothing to report.
	if err := st.WakeNow(ctx, r.ID, "first look after setup"); err != nil {
		log.Fatal(err)
	}
	wait(agent.WakeHost)

	// 2. An event while idle: a deploy happened. The agent wakes with it,
	//    reads again, and this time the numbers are over the line.
	if _, err := st.Deliver(ctx, r.ID, agent.StandingEvent{Source: "deploy", Kind: "finished", Payload: "ledger v2.14.0 rolled out to all replicas"}); err != nil {
		log.Fatal(err)
	}
	wait(agent.WakeEvent)

	// 3. The idle scan comes round on its own (every 15s here). Reading
	//    tools only — ledger_rollback is not even offered.
	wait(agent.WakeScan)

	fmt.Println("\nstatus:")
	for _, s := range st.Status() {
		fmt.Printf("  %s: wakes today %d, paused=%v\n", s.Responsibility.Name, s.WakesToday, s.Responsibility.Paused)
	}
}
