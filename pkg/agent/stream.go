package agent

import (
	"strings"
	"sync"
)

// This file provides small, composable primitives over the runtime's event
// channels (<-chan *Event) and generic channels. They mirror the per-step
// collection logic of pipeline.go's CollectPipelineResult but operate on a
// single stream, and add fan-in / fan-out helpers useful when wiring several
// RunStream channels together.

// Merge fans N channels of T into a single output channel. Each input's own
// ordering is preserved; no ordering is guaranteed across inputs. The output
// closes once every input has closed. Nil input channels are ignored.
func Merge[T any](chans ...<-chan T) <-chan T {
	out := make(chan T)
	var wg sync.WaitGroup
	for _, ch := range chans {
		if ch == nil {
			continue
		}
		wg.Add(1)
		go func(c <-chan T) {
			defer wg.Done()
			for v := range c {
				out <- v
			}
		}(ch)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// MergeEvents fans N event streams into one. It is the *Event specialization of
// Merge and exists so callers don't need to spell out the type parameter when
// combining RunStream channels.
func MergeEvents(chans ...<-chan *Event) <-chan *Event {
	return Merge(chans...)
}

// Concat drains an event stream and returns the final text: EventTypePartial
// deltas are concatenated, but any terminal Content (EventTypeComplete /
// EventTypeBlocked) overrides the accumulated partials — mirroring the
// per-step logic in CollectPipelineResult.
func Concat(ch <-chan *Event) string {
	var partials strings.Builder
	terminal := ""
	hasTerminal := false
	for evt := range ch {
		if evt == nil {
			continue
		}
		switch evt.Type {
		case EventTypePartial:
			partials.WriteString(evt.Content)
		case EventTypeComplete, EventTypeBlocked:
			if t := strings.TrimSpace(evt.Content); t != "" {
				terminal = t
				hasTerminal = true
			}
		}
	}
	if hasTerminal {
		return terminal
	}
	return strings.TrimSpace(partials.String())
}

// Box returns a channel that yields exactly v and then closes. Handy for
// feeding a single known value into a Merge / pipeline expecting a stream.
func Box[T any](v T) <-chan T {
	out := make(chan T, 1)
	out <- v
	close(out)
	return out
}

// Tee fans a single channel out to n independent consumers. Every value read
// from ch is delivered to all n outputs (in lockstep — a slow consumer slows
// the others). All outputs close when ch closes. n <= 0 returns nil.
func Tee[T any](ch <-chan T, n int) []<-chan T {
	if n <= 0 {
		return nil
	}
	outs := make([]chan T, n)
	ro := make([]<-chan T, n)
	for i := range outs {
		outs[i] = make(chan T)
		ro[i] = outs[i]
	}
	go func() {
		defer func() {
			for _, o := range outs {
				close(o)
			}
		}()
		for v := range ch {
			for _, o := range outs {
				o <- v
			}
		}
	}()
	return ro
}
