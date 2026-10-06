package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// Build() must give a service somewhere to write checkpoints. It shipped
// none: SetCheckpointSink was called only by Manager, so an ordinary service
// hit `sink == nil` and wrote nothing — verified against a successful
// one-hour run whose task_checkpoints table held zero rows.
func TestBuildWiresACheckpointSinkByDefault(t *testing.T) {
	svc, err := New("ckpt-default").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&truncatingLLM{minTokens: 1}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if svc.CheckpointSink() == nil {
		t.Fatal("a service built with a store still has nowhere to write checkpoints")
	}
}

// A run must actually leave snapshots behind, not merely own a sink.
func TestARunLeavesCheckpointsOnDisk(t *testing.T) {
	svc, err := New("ckpt-writes").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&truncatingLLM{minTokens: 1}).
		WithAutonomy(AutonomyProfile{CheckpointEveryRounds: 1}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	taskID := "task-under-test"
	if _, err := svc.Run(context.Background(), "Say something.",
		WithConstraintExtraction(false), WithTaskID(taskID)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cps, err := svc.store.ListTaskCheckpoints(taskID, 0)
	if err != nil {
		t.Fatalf("list checkpoints: %v", err)
	}
	if len(cps) == 0 {
		t.Fatal("the run finished and left nothing to resume from")
	}
}

// Two long tasks sharing one database must not share one plan. PlanKey
// defaulted to a single constant, which was harmless while plans lived in
// memory and became cross-task contamination the moment they were persisted.
func TestUnnamedPlanKeyIsScopedToTheTask(t *testing.T) {
	llm := &scriptedLLM{finishAt: 0}
	svc := buildSegmentedService(t, "plan-scope", llm, nil)
	defer svc.Close()

	a, err := svc.RunSegments(context.Background(), "Work.",
		LongRunConfig{MaxSegments: 1, RoundsPerSegment: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.RunSegments(context.Background(), "Work.",
		LongRunConfig{MaxSegments: 1, RoundsPerSegment: 1})
	if err != nil {
		t.Fatal(err)
	}
	if a.TaskID == b.TaskID {
		t.Fatal("two runs got the same task id; the test cannot tell the keys apart")
	}
}

// A caller who names the key keeps it — including when they name the default.
func TestNamedPlanKeyIsNotRewritten(t *testing.T) {
	llm := &scriptedLLM{finishAt: 0}
	store := &memoryPlanStore{plans: map[string][]PlanItem{
		"my-key": {{Text: "only step", Done: true}},
	}}
	svc := buildSegmentedService(t, "plan-named", llm, store)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work.",
		LongRunConfig{MaxSegments: 1, RoundsPerSegment: 1, PlanKey: "my-key"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done() {
		t.Fatalf("a fully-checked plan under the named key should finish; stop=%s", res.Stop)
	}
}

type recordingLongRunObserver struct {
	BaseObserver
	mu       sync.Mutex
	errors   []ErrorInfo
	segments []SegmentInfo
}

func (o *recordingLongRunObserver) OnError(_ context.Context, i ErrorInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.errors = append(o.errors, i)
}
func (o *recordingLongRunObserver) OnSegment(_ context.Context, i SegmentInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.segments = append(o.segments, i)
}

// Segment boundaries had to be reverse-engineered from round numbers
// restarting at 1, because long_run.go emitted nothing at all.
func TestSegmentBoundariesAreObservable(t *testing.T) {
	llm := &scriptedLLM{finishAt: 0}
	obs := &recordingLongRunObserver{}
	var log strings.Builder
	svc := buildSegmentedService(t, "segment-observed", llm, nil)
	defer svc.Close()
	svc.RegisterObserver(obs)
	svc.RegisterObserver(NewActivityLog(&log))

	if _, err := svc.RunSegments(context.Background(), "Work.",
		LongRunConfig{MaxSegments: 2, RoundsPerSegment: 1}); err != nil {
		t.Fatal(err)
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	starts, ends := 0, 0
	for _, s := range obs.segments {
		if s.Ending {
			ends++
		} else {
			starts++
		}
	}
	if starts == 0 || ends == 0 {
		t.Fatalf("segments not reported: %d starts, %d ends", starts, ends)
	}
	if starts != ends {
		t.Errorf("%d segment starts but %d ends", starts, ends)
	}
	if !strings.Contains(log.String(), "segment") {
		t.Errorf("ActivityLog shows no segment boundary:\n%s", log.String())
	}
}
