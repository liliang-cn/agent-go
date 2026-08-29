package agent

import (
	"context"
	"strings"
	"testing"
)

// A long task hands over at segment boundaries, and the hand-off could only
// carry finished work. A step that took a whole segment and did not finish
// reached the next segment as a bare unchecked line — what was tried, what
// nearly worked, what was ruled out, all gone. Two segments of a soak run
// went the same way on the same milestone before this existed.
func TestUnfinishedStepCarriesItsProgress(t *testing.T) {
	store := &memoryPlanStore{}
	svc, err := New("plan-inprogress").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		WithPlanStore(store).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	pad := svc.scratchpadStore()
	pad.set(scratchpadDefaultKey, []string{"write the store", "make checkout atomic"})
	if _, err := pad.check(scratchpadDefaultKey, 0, "store.go, TestStoreProducts passes"); err != nil {
		t.Fatalf("check: %v", err)
	}
	if _, err := pad.note(scratchpadDefaultKey, 1,
		"BEGIN IMMEDIATE works, plain BEGIN deadlocks under -race; still fixing the retry"); err != nil {
		t.Fatalf("note: %v", err)
	}

	summary := svc.PlanSummary(scratchpadDefaultKey)
	if !strings.Contains(summary, "in progress") {
		t.Errorf("the summary should mark the unfinished step as in progress:\n%s", summary)
	}
	if !strings.Contains(summary, "BEGIN IMMEDIATE works") {
		t.Errorf("the summary lost what the last attempt learned:\n%s", summary)
	}
	if !strings.Contains(summary, "[x] 0.") || !strings.Contains(summary, "[ ] 1.") {
		t.Errorf("both steps should appear with their state:\n%s", summary)
	}
	// A note is not a tick.
	if strings.Contains(summary, "1 of 2 steps done") == false {
		t.Errorf("an in-progress note must not count as done:\n%s", summary)
	}
}

// Progress on an unfinished step is worth handing over even when nothing at
// all has been ticked yet — that is precisely the segment that ran out of
// rounds partway through its first milestone.
func TestProgressWithoutAnyCompletedStepIsStillHandedOver(t *testing.T) {
	store := &memoryPlanStore{}
	svc, err := New("plan-inprogress-none-done").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		WithPlanStore(store).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	pad := svc.scratchpadStore()
	pad.set(scratchpadDefaultKey, []string{"make checkout atomic"})
	if _, err := pad.note(scratchpadDefaultKey, 0, "ruled out a table lock; going with a conditional UPDATE"); err != nil {
		t.Fatalf("note: %v", err)
	}

	summary := svc.PlanSummary(scratchpadDefaultKey)
	if !strings.Contains(summary, "ruled out a table lock") {
		t.Errorf("a segment that finished nothing still learned something:\n%s", summary)
	}
}

// And a plan nobody has touched still hands over nothing.
func TestUntouchedPlanStillHandsOverNothing(t *testing.T) {
	svc, err := New("plan-untouched").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		WithPlanStore(&memoryPlanStore{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.scratchpadStore().set(scratchpadDefaultKey, []string{"a", "b"})
	if got := svc.PlanSummary(scratchpadDefaultKey); got != "" {
		t.Errorf("expected no hand-off, got:\n%s", got)
	}
}

// The tool has to be registered, or the model has no way to leave the note.
func TestScratchpadNoteToolIsRegistered(t *testing.T) {
	svc, err := New("plan-note-tool").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		WithAutonomy(AutonomyProfile{Scratchpad: true}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	if !svc.toolRegistry.Has("scratchpad_note") {
		t.Fatal("scratchpad_note is not registered; an unfinished step has no way to record where it got to")
	}
	res, err := svc.toolRegistry.Call(context.Background(), "scratchpad_note",
		map[string]interface{}{"index": 0, "note": "x"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	// An out-of-range index is a tool error, not a panic.
	if res == nil {
		t.Fatal("expected a result")
	}
}
