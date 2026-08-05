// Package main demonstrates the streaming merge primitives in pkg/agent:
// MergeEvents, Merge[T], Concat, Box, and Tee. It is fully offline — it builds
// synthetic event channels rather than driving a real model — so it runs in CI.
//
//	go run ./examples/stream
package main

import (
	"fmt"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// streamOf turns a final answer into a small event stream: a couple of partial
// deltas followed by a terminal complete event (what RunStream would emit).
func streamOf(parts []string, final string) <-chan *agent.Event {
	out := make(chan *agent.Event, len(parts)+1)
	for _, p := range parts {
		out <- &agent.Event{Type: agent.EventTypePartial, Content: p}
	}
	out <- &agent.Event{Type: agent.EventTypeComplete, Content: final}
	close(out)
	return out
}

func main() {
	// Concat: join partial deltas, overridden by a terminal Content.
	final := agent.Concat(streamOf([]string{"Hel", "lo, ", "wor"}, "Hello, world!"))
	fmt.Printf("Concat -> %q\n", final)

	// MergeEvents: fan two event streams into one drain.
	a := streamOf([]string{"a1 ", "a2"}, "stream A done")
	b := streamOf([]string{"b1 ", "b2"}, "stream B done")
	merged := agent.MergeEvents(a, b)
	partials, completes := 0, 0
	for evt := range merged {
		switch evt.Type {
		case agent.EventTypePartial:
			partials++
		case agent.EventTypeComplete:
			completes++
		}
	}
	fmt.Printf("MergeEvents -> partials=%d completes=%d\n", partials, completes)

	// Box + generic Merge over ints.
	sum := 0
	for v := range agent.Merge(agent.Box(10), agent.Box(20), agent.Box(12)) {
		sum += v
	}
	fmt.Printf("Merge(Box...) ints -> sum=%d\n", sum)

	// Tee: broadcast one source to two independent consumers.
	src := agent.Box("broadcast-me")
	outs := agent.Tee(src, 2)
	got := make([]string, len(outs))
	done := make(chan struct{}, len(outs))
	for i, o := range outs {
		go func(idx int, c <-chan string) {
			for v := range c {
				got[idx] = v
			}
			done <- struct{}{}
		}(i, o)
	}
	for range outs {
		<-done
	}
	fmt.Printf("Tee -> consumer0=%q consumer1=%q\n", got[0], got[1])
}
