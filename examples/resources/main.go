// Watching the process, not just the agent.
//
// Every other observer in this framework reports what the agent did. This one
// reports what the program is using while it does it — which on a task that
// runs for hours is the half that decides whether it finishes.
//
//	go run ./examples/resources
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// watcher receives one reading per round, plus a final one.
type watcher struct {
	agent.BaseObserver
	first, last agent.ProcessStats
}

func (w *watcher) OnResourceSample(_ context.Context, s agent.ResourceSample) {
	if w.first.At.IsZero() {
		w.first = s.Stats
	}
	w.last = s.Stats
	fmt.Printf("round %-3d heap %6.1f MiB  objects %-9d goroutines %-4d rss %6.1f MiB\n",
		s.Round, mib(s.Stats.HeapAllocBytes), s.Stats.HeapObjects,
		s.Stats.Goroutines, mib(s.Stats.RSSBytes))
}

func mib(b uint64) float64 { return float64(b) / (1024 * 1024) }

func main() {
	w := &watcher{}
	svc, err := agent.New("worker").
		WithObserver(w).
		// The same readings reach the trace file, one JSONL line per round,
		// which is where a long run's memory curve lives.
		WithObserver(agent.NewTraceWriter(os.Stdout)).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	if _, err := svc.Run(context.Background(), "List three prime numbers."); err != nil {
		log.Fatal(err)
	}

	// The comparison that matters: what the run ended holding versus what it
	// started with. A run that ends 400 MiB heavier is the whole diagnosis,
	// and nothing inside the loop can see it.
	fmt.Printf("\nheap %+.1f MiB, goroutines %+d over the run\n",
		mib(w.last.HeapAllocBytes)-mib(w.first.HeapAllocBytes),
		w.last.Goroutines-w.first.Goroutines)

	// Or ask directly, any time, without an observer.
	s := agent.SampleProcess()
	fmt.Printf("now: %.1f MiB heap, %d goroutines, %.1fs CPU, up %s\n",
		mib(s.HeapAllocBytes), s.Goroutines, s.CPUSeconds(), s.Uptime.Round(1e9))
}
