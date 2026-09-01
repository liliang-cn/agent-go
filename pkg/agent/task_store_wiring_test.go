package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// A segmented run should leave a complete account of itself: the task row
// with a final status and brief, one run per segment with its outcome, and a
// journal entry per segment.
func TestRunSegmentsWritesTaskMemory(t *testing.T) {
	llm := &scriptedLLM{finishAt: 2}
	svc := buildSegmentedService(t, "segments-task-memory", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work through it.", LongRunConfig{
		MaxSegments:      5,
		RoundsPerSegment: 3,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if !res.Done() {
		t.Fatalf("stop = %q, want finished", res.Stop)
	}

	ts := svc.TaskStore()
	if ts == nil {
		t.Fatal("Build() should have wired a task store over the service's own database")
	}
	ctx := context.Background()

	task, err := ts.LoadTask(ctx, res.TaskID)
	if err != nil || task == nil {
		t.Fatalf("task row missing: %v / %v", task, err)
	}
	if task.Status != TaskStatusCompleted {
		t.Errorf("status = %q, want %q", task.Status, TaskStatusCompleted)
	}
	if task.Goal != "Work through it." {
		t.Errorf("goal = %q", task.Goal)
	}
	if !strings.Contains(task.ResumeBrief, "Finished after 3 segment") {
		t.Errorf("brief does not say how it ended: %q", task.ResumeBrief)
	}

	runs, err := ts.RecentRuns(ctx, res.TaskID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("want one run per segment (3), got %d", len(runs))
	}
	for _, r := range runs {
		if r.EndedAt.IsZero() || r.Outcome != TaskRunOutcomeSuccess {
			t.Errorf("run %s left open or mislabelled: outcome=%q ended=%v", r.ID, r.Outcome, r.EndedAt)
		}
		entries, err := ts.Journal(ctx, r.ID, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Kind != "segment" {
			t.Errorf("run %s should carry one segment journal entry, got %+v", r.ID, entries)
		}
	}
}

// The write path is only half the feature: the segment after a hand-off must
// actually be told what earlier segments concluded.
func TestRunSegmentsInjectsTaskMemoryIntoLaterSegments(t *testing.T) {
	llm := &scriptedLLM{finishAt: 1} // segment 0 loops out; segment 1 concludes
	svc := buildSegmentedService(t, "segments-task-inject", llm, nil)
	defer svc.Close()

	if _, err := svc.RunSegments(context.Background(), "Work through it.", LongRunConfig{
		MaxSegments:      3,
		RoundsPerSegment: 2,
	}); err != nil {
		t.Fatalf("RunSegments: %v", err)
	}

	// prompts is chronological, so the first captured system prompt belongs
	// to segment 0. (It cannot be zipped against snapshot's segments slice:
	// the forced-synthesis pass carries no system message, so the two slices
	// are different lengths.)
	_, prompts := llm.snapshot()
	if len(prompts) < 2 {
		t.Fatalf("expected prompts from at least two segments, got %d", len(prompts))
	}
	if strings.Contains(prompts[0], "## Task memory") {
		t.Fatal("segment 0 has no history and should get no task memory section")
	}
	sawTaskMemory := false
	for _, p := range prompts[1:] {
		if strings.Contains(p, "## Task memory") && strings.Contains(p, "Previous runs") {
			sawTaskMemory = true
		}
	}
	if !sawTaskMemory {
		t.Fatal("a later segment never saw the task memory section")
	}
}

// Picking a task back up must not eat the brief the previous process left —
// the brief is exactly the thing the resumed run is about to be given.
func TestTaskMemoryBeginPreservesAnEarlierBrief(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ts, err := NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := ts.SaveTask(ctx, TaskState{
		ID: "t1", Goal: "g", Status: TaskStatusPending,
		ResumeBrief: "auth done; next: rate limiter",
	}); err != nil {
		t.Fatal(err)
	}
	// A run the dead process never closed.
	abandoned, err := ts.BeginRun(ctx, TaskRun{TaskID: "t1"})
	if err != nil {
		t.Fatal(err)
	}

	svc := &Service{}
	svc.SetTaskStore(ts)
	svc.taskMemoryBeginTask("t1", "g")

	got, err := ts.LoadTask(ctx, "t1")
	if err != nil || got == nil {
		t.Fatalf("load: %v / %v", got, err)
	}
	if got.Status != TaskStatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.ResumeBrief != "auth done; next: rate limiter" {
		t.Errorf("begin clobbered the brief: %q", got.ResumeBrief)
	}

	runs, err := ts.RecentRuns(ctx, "t1", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == abandoned {
			if r.EndedAt.IsZero() || r.Outcome != TaskRunOutcomeInterrupted {
				t.Errorf("abandoned run not closed as interrupted: %+v", r)
			}
		}
	}
}
