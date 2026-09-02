package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// lifecycleExt records the order of Start/Stop and what each run told it.
type lifecycleExt struct {
	name     string
	log      *[]string
	mu       *sync.Mutex
	startErr error
	vetoRun  error
	outcomes []RunOutcome
}

func (l *lifecycleExt) Name() string { return l.name }
func (l *lifecycleExt) note(s string) {
	l.mu.Lock()
	*l.log = append(*l.log, s)
	l.mu.Unlock()
}
func (l *lifecycleExt) Start(context.Context) error {
	l.note("start:" + l.name)
	return l.startErr
}
func (l *lifecycleExt) Stop(context.Context) error {
	l.note("stop:" + l.name)
	return nil
}
func (l *lifecycleExt) OnRunStart(_ context.Context, run RunInfo) error {
	l.note("run-start:" + run.Goal)
	return l.vetoRun
}
func (l *lifecycleExt) OnRunEnd(_ context.Context, run RunInfo, out RunOutcome) {
	l.note("run-end:" + run.Goal + ":" + string(out.StopReason))
	l.mu.Lock()
	l.outcomes = append(l.outcomes, out)
	l.mu.Unlock()
}

func TestLifecycleStartsInOrderAndStopsInReverse(t *testing.T) {
	var log []string
	var mu sync.Mutex
	a := &lifecycleExt{name: "a", log: &log, mu: &mu}
	b := &lifecycleExt{name: "b", log: &log, mu: &mu}
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "done"}}}

	svc, err := New("lc").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).
		WithExtensions(a, b).Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "start:a,start:b" {
		t.Fatalf("after Build: %s", got)
	}

	events, err := svc.RunStream(context.Background(), "g1")
	if err != nil {
		t.Fatal(err)
	}
	final, blocked, _ := collectStreamContent(t, events)
	if blocked != "" || final != "done" {
		t.Fatalf("final=%q blocked=%q", final, blocked)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	mu.Lock()
	got := strings.Join(log, ",")
	mu.Unlock()
	want := "start:a,start:b,run-start:g1,run-start:g1,run-end:g1:end_turn,run-end:g1:end_turn,stop:b,stop:a"
	if got != want {
		t.Fatalf("lifecycle order:\n got %s\nwant %s", got, want)
	}
	if len(a.outcomes) != 1 || a.outcomes[0].Text != "done" || a.outcomes[0].Duration <= 0 {
		t.Fatalf("outcome = %+v", a.outcomes)
	}
}

// A Start that fails fails the Build and unwinds what already started.
func TestLifecycleStartFailureUnwinds(t *testing.T) {
	var log []string
	var mu sync.Mutex
	a := &lifecycleExt{name: "a", log: &log, mu: &mu}
	b := &lifecycleExt{name: "b", log: &log, mu: &mu, startErr: errors.New("no socket")}
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "done"}}}

	_, err := New("lc-fail").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).
		WithExtensions(a, b).Build()
	if err == nil || !strings.Contains(err.Error(), `extension "b": start: no socket`) {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(log, ","); got != "start:a,start:b,stop:a" {
		t.Fatalf("unwind order: %s", got)
	}
}

// OnRunStart can refuse a run; it ends blocked, with the reason, and
// OnRunEnd still sees it.
func TestRunLifecycleCanVetoARun(t *testing.T) {
	var log []string
	var mu sync.Mutex
	gate := &lifecycleExt{name: "gate", log: &log, mu: &mu, vetoRun: errors.New("daily budget spent")}
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "done"}}}
	svc, err := New("veto").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).
		WithExtensions(gate).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "spend")
	if err != nil {
		t.Fatal(err)
	}
	_, blocked, _ := collectStreamContent(t, events)
	if !strings.Contains(blocked, "daily budget spent") {
		t.Fatalf("blocked = %q", blocked)
	}
	if n := len(llm.firstRound()); n != 0 {
		t.Fatalf("model was called %d messages despite the veto", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gate.outcomes) != 1 || !gate.outcomes[0].Blocked {
		t.Fatalf("outcomes = %+v", gate.outcomes)
	}
}
