package agent

import (
	"context"
	"testing"
	"time"
)

// RunSegments returns when the task is over, which on the tasks it exists for
// is hours away, and every event its segments produced was collected into a
// result and discarded. A host with a window had nothing to draw until the end.
func TestRunSegmentsStreamForwardsSegmentEvents(t *testing.T) {
	llm := &scriptedLLM{finishAt: 0}
	svc := buildSegmentedService(t, "segments-stream", llm, nil)
	defer svc.Close()

	stream := svc.RunSegmentsStream(context.Background(), "Work.", LongRunConfig{
		MaxSegments:      3,
		RoundsPerSegment: 1,
	})

	var (
		seen      int
		completes int
	)
	for evt := range stream.Events {
		seen++
		if evt.Type == EventTypeComplete {
			completes++
		}
	}
	if seen == 0 {
		t.Fatal("no events reached the caller; the segments' stream is still being swallowed")
	}
	if completes == 0 {
		t.Error("no terminal event forwarded")
	}

	select {
	case out := <-stream.Result:
		if out.Err != nil {
			t.Fatalf("RunSegmentsStream: %v", out.Err)
		}
		if out.Result == nil {
			t.Fatal("no LongRunResult delivered")
		}
		if len(out.Result.Segments) == 0 {
			t.Error("result carries no segments")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("result never arrived after the event stream closed")
	}
}

// Cancelling stops it, and both channels close rather than leaking.
func TestRunSegmentsStreamStopsOnCancel(t *testing.T) {
	llm := &scriptedLLM{finishAt: 99}
	svc := buildSegmentedService(t, "segments-stream-cancel", llm, nil)
	defer svc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	stream := svc.RunSegmentsStream(ctx, "Work forever.", LongRunConfig{
		MaxSegments:      50,
		RoundsPerSegment: 2,
	})

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	for range stream.Events {
	}

	select {
	case out := <-stream.Result:
		if out.Result == nil && out.Err == nil {
			t.Fatal("cancelled run delivered neither result nor error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled run never delivered a result")
	}
}

// The plain RunSegments must keep working with no sink at all.
func TestRunSegmentsStillWorksWithoutASink(t *testing.T) {
	llm := &scriptedLLM{finishAt: 0}
	svc := buildSegmentedService(t, "segments-no-sink", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work.", LongRunConfig{
		MaxSegments: 1, RoundsPerSegment: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 1 {
		t.Fatalf("ran %d segments, want 1", len(res.Segments))
	}
}
