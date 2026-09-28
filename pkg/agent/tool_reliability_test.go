package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type reliabilityWatcher struct {
	BaseObserver
	mu       sync.Mutex
	outcomes []ToolOutcome
	last     ToolReliability
}

func (w *reliabilityWatcher) OnToolOutcome(_ context.Context, o ToolOutcome, r ToolReliability) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.outcomes = append(w.outcomes, o)
	w.last = r
}

func buildReliabilityService(t *testing.T, home string) *Service {
	t.Helper()
	svc, err := New("reliability").
		WithConfig(testAgentConfig(home)).
		WithLLM(&optsRecordingLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	flaky := 0
	svc.AddToolWithMetadata("flaky", "Fails every other call.", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"n": map[string]interface{}{"type": "integer"}},
		"required":   []interface{}{"n"},
	}, func(context.Context, map[string]interface{}) (interface{}, error) {
		flaky++
		time.Sleep(2 * time.Millisecond)
		if flaky%2 == 0 {
			return nil, errors.New("upstream 502")
		}
		return "ok", nil
	}, ToolMetadata{})
	return svc
}

// A tool's record outlives the Service that measured it: two Services on one
// store, the second built after the first closed, and a third open beside it.
func TestToolReliabilityPersistsAcrossServices(t *testing.T) {
	home := t.TempDir()

	first := buildReliabilityService(t, home)
	watcher := &reliabilityWatcher{}
	first.RegisterObserver(watcher)
	for i := 0; i < 4; i++ { // success, error, success, error
		_, _ = callDirect(t, first, "flaky", map[string]interface{}{"n": i})
	}
	_, _ = callDirect(t, first, "flaky", map[string]interface{}{}) // invalid args, not run
	r, ok := first.ToolReliabilityOf("flaky")
	if !ok {
		t.Fatal("no record for flaky")
	}
	if r.Successes != 2 || r.Errors != 2 || r.InvalidArgs != 1 {
		t.Fatalf("first service record = %+v", r)
	}
	if r.ErrorRate() != 0.5 || r.MeanLatency() < 2*time.Millisecond || r.LastError != "upstream 502" {
		t.Fatalf("rate/latency/last error wrong: rate=%v mean=%v last=%q", r.ErrorRate(), r.MeanLatency(), r.LastError)
	}
	watcher.mu.Lock()
	if len(watcher.outcomes) != 5 || watcher.outcomes[4].Kind != ToolOutcomeInvalidArgs || watcher.last.InvalidArgs != 1 {
		t.Fatalf("observer saw %d outcomes, last record %+v", len(watcher.outcomes), watcher.last)
	}
	watcher.mu.Unlock()
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := buildReliabilityService(t, home)
	defer second.Close()
	r, ok = second.ToolReliabilityOf("flaky")
	if !ok || r.Successes != 2 || r.Errors != 2 || r.InvalidArgs != 1 {
		t.Fatalf("a fresh Service on the same store sees %+v (ok=%v)", r, ok)
	}

	// A second Service open on the same store at the same time: both count,
	// and neither overwrites the other.
	third := buildReliabilityService(t, home)
	defer third.Close()
	w2 := &reliabilityWatcher{}
	second.RegisterObserver(w2)
	_, _ = callDirect(t, second, "flaky", map[string]interface{}{"n": 1})
	_, _ = callDirect(t, third, "flaky", map[string]interface{}{"n": 1})
	r, _ = third.ToolReliabilityOf("flaky")
	if got := r.Executions(); got != 6 {
		t.Fatalf("executions after both services = %d, want 6 (%+v)", got, r)
	}
	// The observer's record starts from what was stored, not from zero.
	w2.mu.Lock()
	if w2.last.Executions() != 5 {
		t.Fatalf("observer record should include stored history: %+v", w2.last)
	}
	w2.mu.Unlock()
}

// Without a store the counts still work, in memory.
func TestToolReliabilityInMemory(t *testing.T) {
	svc := buildReliabilityService(t, t.TempDir())
	defer svc.Close()
	svc.SetToolReliabilityStore(nil)
	_, _ = callDirect(t, svc, "flaky", map[string]interface{}{"n": 1})
	r, ok := svc.ToolReliabilityOf("flaky")
	if !ok || r.Successes != 1 {
		t.Fatalf("in-memory record = %+v ok=%v", r, ok)
	}
}
