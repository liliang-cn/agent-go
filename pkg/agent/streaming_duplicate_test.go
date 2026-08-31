package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A model emits the same call several times inside one parallel batch, under
// different call ids, and the streaming path executed every one of them.
//
// handleDuplicateToolCalls has collapsed exactly this since it was written, but
// on the streaming path it runs AFTER execution -- its own doc says so -- so the
// policy existed and the work was already done by the time it applied. The
// id-keyed dispatch guard could not see the duplicates either: it answers "have
// I started this call", and a duplicate arrives under a different id.
//
// Measured on a graph-backed agent before this: one turn emitted ten tool calls
// inside ten milliseconds, four of them identical; across the run twenty-five
// calls were thirteen distinct questions.
func TestStreamingCollapsesIdenticalReadOnlyCallsInOneBatch(t *testing.T) {
	var runs atomic.Int64
	svc, r, collector := streamingHarness(t, &runs)

	cb := r.buildStreamingTurnCallbacks(context.Background(), "span", new(string), new(string), collector)
	// Four ids, one question -- the shape the model actually emits.
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		if err := cb.OnToolCall(readCall(id, "Makefile")); err != nil {
			t.Fatalf("OnToolCall(%s): %v", id, err)
		}
	}
	results := collector.collect()

	if n := runs.Load(); n != 1 {
		t.Errorf("the tool ran %d times for four identical calls, want 1", n)
	}
	// Every id still gets an answer. The provider requires one tool message per
	// tool call id, and a turn that skipped three would be malformed.
	if len(results) != 4 {
		t.Fatalf("got %d results for four calls, want 4", len(results))
	}
	ids := map[string]string{}
	for _, res := range results {
		ids[res.ToolCallID] = fmt.Sprint(res.Result)
	}
	// Through the same normalisation the dispatch applies, so the test asserts
	// on the ids the provider will actually be answered with rather than on the
	// ones it was handed.
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		if _, ok := ids[domain.NormalizeToolCallID(id)]; !ok {
			t.Errorf("call %s got no result", id)
		}
	}
	var real, hinted int
	for _, text := range ids {
		if strings.Contains(text, "Not run again") {
			hinted++
		} else if text == "content" {
			real++
		}
	}
	if real != 1 || hinted != 3 {
		t.Errorf("got %d real answers and %d hints, want 1 and 3: %v", real, hinted, ids)
	}
	_ = svc
}

// The policy is the same one the batch path applies, and its limit is the same
// too: a tool nobody declared read-only is re-executed. A repeated write may be
// a read-modify-write loop rather than a mistake, and assuming a tool nobody
// described is safe to skip is the wrong way round.
func TestStreamingDoesNotCollapseAToolThatIsNotDeclaredReadOnly(t *testing.T) {
	var runs atomic.Int64
	_, r, collector := streamingHarness(t, &runs)

	cb := r.buildStreamingTurnCallbacks(context.Background(), "span", new(string), new(string), collector)
	for _, id := range []string{"w1", "w2", "w3"} {
		call := readCall(id, "Makefile")
		call.Function.Name = "write_it"
		if err := cb.OnToolCall(call); err != nil {
			t.Fatalf("OnToolCall(%s): %v", id, err)
		}
	}
	collector.collect()
	if n := runs.Load(); n != 3 {
		t.Errorf("a tool that is not declared read-only ran %d times, want 3", n)
	}
}

// Different arguments are different questions, so they are not collapsed. The
// signature is name plus arguments, and a guard keyed on the name alone would
// answer a read of one path with another path's content.
func TestStreamingDoesNotCollapseDifferentArguments(t *testing.T) {
	var runs atomic.Int64
	_, r, collector := streamingHarness(t, &runs)

	cb := r.buildStreamingTurnCallbacks(context.Background(), "span", new(string), new(string), collector)
	for i, path := range []string{"Makefile", "README.md", "Makefile"} {
		if err := cb.OnToolCall(readCall(fmt.Sprintf("c%d", i), path)); err != nil {
			t.Fatalf("OnToolCall: %v", err)
		}
	}
	collector.collect()
	// Two distinct paths run; the third call repeats the first and collapses.
	if n := runs.Load(); n != 2 {
		t.Errorf("the tool ran %d times for two distinct paths, want 2", n)
	}
}

// The original guard still does its own job: the provider re-sends the full
// tool-call snapshot on every chunk, so one call arriving repeatedly under ONE
// id must start once and must not be reported as a duplicate of itself.
func TestStreamingStillIgnoresTheProvidersResentSnapshots(t *testing.T) {
	var runs atomic.Int64
	_, r, collector := streamingHarness(t, &runs)

	cb := r.buildStreamingTurnCallbacks(context.Background(), "span", new(string), new(string), collector)
	for i := 0; i < 5; i++ {
		if err := cb.OnToolCall(readCall("same", "Makefile")); err != nil {
			t.Fatalf("OnToolCall: %v", err)
		}
	}
	results := collector.collect()
	if n := runs.Load(); n != 1 {
		t.Errorf("the tool ran %d times for one re-sent call, want 1", n)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results for one call, want 1: a re-sent snapshot is not a duplicate call", len(results))
	}
	if got := fmt.Sprint(results[0].Result); got != "content" {
		t.Errorf("the one call was answered with %q, want its real result", got)
	}
}

func readCall(id, path string) domain.ToolCall {
	return domain.ToolCall{
		ID: id, Type: "function",
		Function: domain.FunctionCall{Name: "read_it", Arguments: map[string]interface{}{"path": path}},
	}
}

func streamingHarness(t *testing.T, runs *atomic.Int64) (*Service, *Runtime, *runtimeAsyncToolCollector) {
	t.Helper()
	svc, err := New("streaming-dupes").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { svc.Close() })

	params := map[string]interface{}{"type": "object", "properties": map[string]interface{}{
		"path": map[string]interface{}{"type": "string"}}}
	run := func(context.Context, map[string]interface{}) (interface{}, error) {
		runs.Add(1)
		return "content", nil
	}
	svc.AddToolWithMetadata("read_it", "Reads.", params, run,
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true})
	svc.AddToolWithMetadata("write_it", "Writes.", params, run, ToolMetadata{})

	// The minimum a Runtime needs to dispatch a tool: somebody to attribute the
	// event to, and somewhere to put it. The channel is drained so a test that
	// emits more events than it buffers cannot deadlock instead of failing.
	events := make(chan *Event, 256)
	go func() {
		for range events {
		}
	}()
	t.Cleanup(func() { close(events) })
	r := &Runtime{
		svc:            svc,
		eventChan:      events,
		currentAgent:   svc.resolveCurrentAgent(nil),
		cfg:            DefaultRunConfig(),
		pendingTools:   map[string]domain.ToolCall{},
		completedTools: map[string]bool{},
	}
	return svc, r, newRuntimeAsyncToolCollector()
}
