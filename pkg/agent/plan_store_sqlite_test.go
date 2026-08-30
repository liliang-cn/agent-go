package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestPlanStore(t *testing.T) *SQLitePlanStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ps, err := NewSQLitePlanStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestSQLitePlanStoreRoundTrip(t *testing.T) {
	ps := newTestPlanStore(t)
	ctx := context.Background()

	if got, err := ps.LoadPlan(ctx, "never-seen"); err != nil || len(got) != 0 {
		t.Fatalf("unknown key should be empty and fine, got %v / %v", got, err)
	}

	want := []PlanItem{
		{Text: "build"},
		{Text: "test", Done: true, Note: "TestX passes"},
	}
	if err := ps.SavePlan(ctx, "task-1", want); err != nil {
		t.Fatal(err)
	}
	got, err := ps.LoadPlan(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

// The whole point of rows over a blob: a step that did not change keeps saying
// when it was created and when it was finished, however many times the rest of
// the plan is rewritten around it.
func TestSQLitePlanStorePreservesTimestampsAcrossRewrites(t *testing.T) {
	ps := newTestPlanStore(t)
	ctx := context.Background()

	plan := []PlanItem{{Text: "step one"}, {Text: "step two"}}
	if err := ps.SavePlan(ctx, "k", plan); err != nil {
		t.Fatal(err)
	}
	first, err := ps.Timeline(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if first[0].DoneAt.IsZero() != true {
		t.Fatal("an unfinished step should have no done_at")
	}

	time.Sleep(10 * time.Millisecond)
	plan[0].Done = true
	plan[0].Note = "gate green"
	if err := ps.SavePlan(ctx, "k", plan); err != nil {
		t.Fatal(err)
	}
	// Rewrite the whole list several more times without touching step one.
	for i := range 3 {
		time.Sleep(5 * time.Millisecond)
		plan[1].Note = string(rune('a' + i))
		if err := ps.SavePlan(ctx, "k", plan); err != nil {
			t.Fatal(err)
		}
	}

	after, err := ps.Timeline(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !after[0].CreatedAt.Equal(first[0].CreatedAt) {
		t.Fatalf("step one's created_at moved: %v -> %v", first[0].CreatedAt, after[0].CreatedAt)
	}
	if after[0].DoneAt.IsZero() {
		t.Fatal("step one finished but has no done_at")
	}
	if after[0].Duration() <= 0 {
		t.Fatalf("step one has no duration: created %v done %v", after[0].CreatedAt, after[0].DoneAt)
	}
	if !after[1].DoneAt.IsZero() {
		t.Fatal("step two is unfinished and must have no done_at")
	}
}

// Unchecking a step clears its completion time; a plan that reopens work must
// not keep claiming it was finished.
func TestSQLitePlanStoreClearsDoneAtOnUncheck(t *testing.T) {
	ps := newTestPlanStore(t)
	ctx := context.Background()
	plan := []PlanItem{{Text: "s", Done: true}}
	if err := ps.SavePlan(ctx, "k", plan); err != nil {
		t.Fatal(err)
	}
	plan[0].Done = false
	if err := ps.SavePlan(ctx, "k", plan); err != nil {
		t.Fatal(err)
	}
	tl, err := ps.Timeline(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !tl[0].DoneAt.IsZero() {
		t.Fatalf("unchecked step still reports done_at %v", tl[0].DoneAt)
	}
}

// A shortened plan must lose its tail rather than keep stale steps.
func TestSQLitePlanStoreDropsRemovedSteps(t *testing.T) {
	ps := newTestPlanStore(t)
	ctx := context.Background()
	if err := ps.SavePlan(ctx, "k", []PlanItem{{Text: "a"}, {Text: "b"}, {Text: "c"}}); err != nil {
		t.Fatal(err)
	}
	if err := ps.SavePlan(ctx, "k", []PlanItem{{Text: "a"}}); err != nil {
		t.Fatal(err)
	}
	got, err := ps.LoadPlan(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "a" {
		t.Fatalf("stale steps survived: %+v", got)
	}
}

// Build() must give a service a plan that outlives the process. It shipped
// none: a plan lived in memory and a run that died left no record of it.
func TestBuildWiresADurablePlanStoreByDefault(t *testing.T) {
	svc, err := New("plan-default").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&truncatingLLM{minTokens: 1}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if svc.planStore == nil {
		t.Fatal("a service built with a store still has nowhere to keep its plan")
	}
	if _, ok := svc.planStore.(*SQLitePlanStore); !ok {
		t.Fatalf("default plan store is %T, want *SQLitePlanStore", svc.planStore)
	}
}
