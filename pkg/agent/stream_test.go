package agent

import (
	"sort"
	"testing"
)

func TestMergeEventsInterleavesAndClosesOnce(t *testing.T) {
	a := make(chan *Event, 2)
	b := make(chan *Event, 2)
	a <- &Event{Type: EventTypePartial, Content: "a1"}
	a <- &Event{Type: EventTypePartial, Content: "a2"}
	close(a)
	b <- &Event{Type: EventTypePartial, Content: "b1"}
	b <- &Event{Type: EventTypePartial, Content: "b2"}
	close(b)

	merged := MergeEvents(a, b)
	var got []string
	for evt := range merged {
		got = append(got, evt.Content)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 merged events, got %d (%v)", len(got), got)
	}
	sort.Strings(got)
	want := []string{"a1", "a2", "b1", "b2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged contents = %v, want %v", got, want)
		}
	}

	// A second receive on a closed, drained channel returns the zero value
	// immediately — proving the output was closed exactly once.
	if _, ok := <-merged; ok {
		t.Fatal("expected merged channel to be closed")
	}
}

func TestConcatJoinsPartialsAndHonorsTerminal(t *testing.T) {
	// Partials only.
	ch := make(chan *Event, 3)
	ch <- &Event{Type: EventTypePartial, Content: "Hello "}
	ch <- &Event{Type: EventTypePartial, Content: "world"}
	close(ch)
	if got := Concat(ch); got != "Hello world" {
		t.Fatalf("partial-only Concat = %q, want %q", got, "Hello world")
	}

	// Terminal Content overrides partials.
	ch2 := make(chan *Event, 3)
	ch2 <- &Event{Type: EventTypePartial, Content: "streaming draft"}
	ch2 <- &Event{Type: EventTypeComplete, Content: "final answer"}
	close(ch2)
	if got := Concat(ch2); got != "final answer" {
		t.Fatalf("terminal Concat = %q, want %q", got, "final answer")
	}
}

func TestBoxYieldsOneThenCloses(t *testing.T) {
	ch := Box(42)
	v, ok := <-ch
	if !ok || v != 42 {
		t.Fatalf("Box first receive = (%d,%v), want (42,true)", v, ok)
	}
	if _, ok := <-ch; ok {
		t.Fatal("expected Box channel to be closed after one value")
	}
}

func TestMergeGenericInts(t *testing.T) {
	merged := Merge(Box(1), Box(2), Box(3))
	sum := 0
	count := 0
	for v := range merged {
		sum += v
		count++
	}
	if count != 3 || sum != 6 {
		t.Fatalf("Merge ints: count=%d sum=%d, want count=3 sum=6", count, sum)
	}
}

func TestTeeBroadcasts(t *testing.T) {
	src := make(chan int, 3)
	src <- 1
	src <- 2
	src <- 3
	close(src)

	outs := Tee(src, 2)
	if len(outs) != 2 {
		t.Fatalf("expected 2 tee outputs, got %d", len(outs))
	}

	done := make(chan []int, 2)
	for _, o := range outs {
		go func(c <-chan int) {
			var vals []int
			for v := range c {
				vals = append(vals, v)
			}
			done <- vals
		}(o)
	}
	for i := 0; i < 2; i++ {
		vals := <-done
		if len(vals) != 3 || vals[0] != 1 || vals[2] != 3 {
			t.Fatalf("tee output = %v, want [1 2 3]", vals)
		}
	}
}
