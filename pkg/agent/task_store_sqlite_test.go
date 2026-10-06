package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestTaskStore(t *testing.T) *SQLiteTaskStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ts, err := NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestTaskStoreRoundTripAndUnknown(t *testing.T) {
	ts := newTestTaskStore(t)
	ctx := context.Background()

	if got, err := ts.LoadTask(ctx, "never-seen"); err != nil || got != nil {
		t.Fatalf("unknown task should be (nil, nil), got %v / %v", got, err)
	}

	if err := ts.SaveTask(ctx, TaskState{ID: "t1", Goal: "build the thing"}); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal != "build the thing" || got.Status != TaskStatusPending {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

// The original ask is what a drifted task is measured against; an update
// must not be able to rewrite it.
func TestTaskStoreGoalIsWriteOnce(t *testing.T) {
	ts := newTestTaskStore(t)
	ctx := context.Background()

	if err := ts.SaveTask(ctx, TaskState{ID: "t1", Goal: "the original ask"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.SaveTask(ctx, TaskState{
		ID: "t1", Goal: "a drifted restatement", Status: TaskStatusRunning, ResumeBrief: "halfway",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal != "the original ask" {
		t.Fatalf("goal was rewritten to %q", got.Goal)
	}
	if got.Status != TaskStatusRunning || got.ResumeBrief != "halfway" {
		t.Fatalf("mutable fields did not update: %+v", got)
	}
}

func TestTaskStoreResumeBriefUnknownTaskIsAnError(t *testing.T) {
	ts := newTestTaskStore(t)
	if err := ts.SaveResumeBrief(context.Background(), "nope", "brief"); err == nil {
		t.Fatal("brief for an unknown task should be an error")
	}
}

func TestTaskStoreRunsNewestFirstWithOpenRun(t *testing.T) {
	ts := newTestTaskStore(t)
	ctx := context.Background()

	if err := ts.SaveTask(ctx, TaskState{ID: "t1", Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	r1, err := ts.BeginRun(ctx, TaskRun{TaskID: "t1"})
	if err != nil || r1 == "" {
		t.Fatalf("begin run: %v / id=%q", err, r1)
	}
	if err := ts.EndRun(ctx, r1, TaskRunOutcomeFailed, "first attempt hit the wall"); err != nil {
		t.Fatal(err)
	}
	r2, err := ts.BeginRun(ctx, TaskRun{TaskID: "t1"})
	if err != nil {
		t.Fatal(err)
	}

	runs, err := ts.RecentRuns(ctx, "t1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("want 2 runs, got %d", len(runs))
	}
	if runs[0].ID != r2 || !runs[0].EndedAt.IsZero() {
		t.Fatalf("newest first should be the open run: %+v", runs[0])
	}
	if runs[1].Outcome != TaskRunOutcomeFailed || runs[1].Summary == "" {
		t.Fatalf("closed run lost its ending: %+v", runs[1])
	}

	if err := ts.EndRun(ctx, "no-such-run", TaskRunOutcomeSuccess, ""); err == nil {
		t.Fatal("ending an unknown run should be an error")
	}
}

// The idempotency key is the whole crash-retry story: the second append with
// the same key must come back as the first one, not as a second row.
func TestTaskStoreJournalIdempotency(t *testing.T) {
	ts := newTestTaskStore(t)
	ctx := context.Background()

	entry := TaskJournalEntry{
		TaskID: "t1", RunID: "r1", Kind: "tool_call",
		Payload: `{"tool":"send_email","to":"ops"}`, IdemKey: "t1/send-launch-email",
	}
	seq1, dup, err := ts.AppendJournal(ctx, entry)
	if err != nil || dup {
		t.Fatalf("first append: seq=%d dup=%v err=%v", seq1, dup, err)
	}
	seq2, dup, err := ts.AppendJournal(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	if !dup || seq2 != seq1 {
		t.Fatalf("retry should report the original: seq=%d dup=%v (want seq=%d dup=true)", seq2, dup, seq1)
	}

	// Ordinary entries carry no key and must never collide with each other.
	a, _, err := ts.AppendJournal(ctx, TaskJournalEntry{TaskID: "t1", RunID: "r1", Kind: "note", Payload: "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, dup, err := ts.AppendJournal(ctx, TaskJournalEntry{TaskID: "t1", RunID: "r1", Kind: "note", Payload: "x"})
	if err != nil || dup || b == a {
		t.Fatalf("keyless entries collided: a=%d b=%d dup=%v err=%v", a, b, dup, err)
	}
}

func TestTaskStoreJournalPagesByAfterSeq(t *testing.T) {
	ts := newTestTaskStore(t)
	ctx := context.Background()

	var last int64
	for i := 0; i < 5; i++ {
		seq, _, err := ts.AppendJournal(ctx, TaskJournalEntry{TaskID: "t1", RunID: "r1", Kind: "note", Payload: "p"})
		if err != nil {
			t.Fatal(err)
		}
		last = seq
	}
	first, err := ts.Journal(ctx, "r1", 0, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page: %d entries, err=%v", len(first), err)
	}
	rest, err := ts.Journal(ctx, "r1", first[1].Seq, 100)
	if err != nil || len(rest) != 3 {
		t.Fatalf("second page: %d entries, err=%v", len(rest), err)
	}
	if rest[len(rest)-1].Seq != last {
		t.Fatalf("paging lost the tail: got %d want %d", rest[len(rest)-1].Seq, last)
	}
}

func TestTaskResumeContextRendersOnlyWhenThereIsSomethingToHandOver(t *testing.T) {
	ts := newTestTaskStore(t)
	ctx := context.Background()

	if got := TaskResumeContext(ctx, ts, "never-seen"); got != "" {
		t.Fatalf("unknown task should render nothing, got %q", got)
	}

	// A task with no brief, no runs and no lessons reduces to its goal,
	// which the caller already has.
	if err := ts.SaveTask(ctx, TaskState{ID: "bare", Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if got := TaskResumeContext(ctx, ts, "bare"); got != "" {
		t.Fatalf("bare task should render nothing, got %q", got)
	}

	if err := ts.SaveTask(ctx, TaskState{ID: "t1", Goal: "ship the API", Status: TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	rid, err := ts.BeginRun(ctx, TaskRun{TaskID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.EndRun(ctx, rid, TaskRunOutcomeBlocked, "auth done; blocked on the rate limiter design"); err != nil {
		t.Fatal(err)
	}
	if err := ts.SaveResumeBrief(ctx, "t1", "auth + routes done; next: rate limiter, then load test"); err != nil {
		t.Fatal(err)
	}
	if err := ts.AddLearning(ctx, "t1", "the vendor SDK's retry conflicts with ours; disable one"); err != nil {
		t.Fatal(err)
	}

	got := TaskResumeContext(ctx, ts, "t1")
	for _, want := range []string{
		"ship the API",
		"rate limiter, then load test",
		"[blocked] auth done",
		"vendor SDK",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("resume context missing %q:\n%s", want, got)
		}
	}
}

// A database written before the task store stopped recording money still has
// a cost column on task_runs. The store must open it and use it as is: the
// column is NOT NULL with a default, so a run that never names it still ends.
func TestTaskStoreOpensATableWithTheOldCostColumn(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE task_runs (
		id         TEXT PRIMARY KEY,
		task_id    TEXT NOT NULL,
		started_at DATETIME NOT NULL,
		ended_at   DATETIME,
		outcome    TEXT NOT NULL DEFAULT '',
		summary    TEXT NOT NULL DEFAULT '',
		cost_usd   REAL NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	ts, err := NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("open over an old schema: %v", err)
	}
	ctx := context.Background()
	rid, err := ts.BeginRun(ctx, TaskRun{TaskID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.EndRun(ctx, rid, TaskRunOutcomeSuccess, "done"); err != nil {
		t.Fatal(err)
	}
	runs, err := ts.RecentRuns(ctx, "t1", 5)
	if err != nil || len(runs) != 1 || runs[0].Summary != "done" {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
}
