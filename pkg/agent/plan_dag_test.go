package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// oldFormatPlanJSON is a plan as stored before steps had IDs.
const oldFormatPlanJSON = `[{"text":"find the port","done":true,"note":"port 43510"},{"text":"write the client","done":false,"note":"half of it"},{"text":"ship it","done":false}]`

// oldFormatSummary is PlanSummary's output for oldFormatPlanJSON, captured
// from the code before plans were graphs. It must not move by a byte.
const oldFormatSummary = "Plan in progress (1 of 3 steps done):\n[x] 0. find the port\n      → port 43510\n[ ] 1. write the client\n      … in progress: half of it\n[ ] 2. ship it\nCarry on from the first unchecked step. Do not repeat finished ones, and pick up an in-progress one where it was left rather than starting it again."

func TestOldFormatPlanLoadsAndSummarisesUnchanged(t *testing.T) {
	var items []PlanItem
	if err := json.Unmarshal([]byte(oldFormatPlanJSON), &items); err != nil {
		t.Fatal(err)
	}
	svc := &Service{}
	svc.SetPlanStore(&memoryPlanStore{plans: map[string][]PlanItem{"k": items, "default:t1": items}})
	if got := svc.PlanSummary("k"); got != oldFormatSummary {
		t.Fatalf("summary changed:\n got %q\nwant %q", got, oldFormatSummary)
	}
	if got := svc.planSummaryForRun("", "t1"); got != oldFormatSummary {
		t.Fatalf("run summary changed:\n got %q", got)
	}
	// Re-serialised, an old plan carries no new keys.
	raw, _ := json.Marshal(items)
	if string(raw) != oldFormatPlanJSON {
		t.Fatalf("old plan re-serialised differently:\n%s", raw)
	}
	// A flat plan's tool payload gains no ready/id/after fields either.
	for _, item := range scratchpadItemsPayload(scratchpadItemsFrom(items)) {
		for _, k := range []string{"ready", "id", "after"} {
			if _, ok := item[k]; ok {
				t.Fatalf("flat plan payload grew %q: %v", k, item)
			}
		}
	}
}

// A plan_items table written before the graph columns existed is migrated
// in place and its rows read back as the flat plan they were.
func TestSQLitePlanStoreMigratesOldTable(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE plan_items (
		plan_key TEXT NOT NULL, idx INTEGER NOT NULL, text TEXT NOT NULL,
		done INTEGER NOT NULL DEFAULT 0, note TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		done_at DATETIME, PRIMARY KEY (plan_key, idx))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plan_items (plan_key, idx, text, done, note) VALUES
		('k', 0, 'find the port', 1, 'port 43510'), ('k', 1, 'write the client', 0, 'half of it'), ('k', 2, 'ship it', 0, '')`); err != nil {
		t.Fatal(err)
	}
	ps, err := NewSQLitePlanStore(db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Twice: the migration must be idempotent.
	if _, err := NewSQLitePlanStore(db); err != nil {
		t.Fatalf("second open: %v", err)
	}
	svc := &Service{}
	svc.SetPlanStore(ps)
	if got := svc.PlanSummary("k"); got != oldFormatSummary {
		t.Fatalf("migrated plan summarises differently:\n got %q", got)
	}

	ctx := context.Background()
	graph := []PlanItem{
		{ID: "a", Text: "a"}, {ID: "b", Text: "b"},
		{ID: "s", Text: "sum", After: []string{"a", "b"}},
	}
	if err := ps.SavePlan(ctx, "g", graph); err != nil {
		t.Fatal(err)
	}
	got, err := ps.LoadPlan(ctx, "g")
	if err != nil || len(got) != 3 {
		t.Fatalf("load: %v %+v", err, got)
	}
	for i := range graph {
		if !planItemEqual(got[i], graph[i]) {
			t.Fatalf("step %d lost its edges: %+v", i, got[i])
		}
	}
}

func graphPad(t *testing.T) *scratchpadManager {
	t.Helper()
	m := newScratchpadManager(nil)
	if _, err := m.setSteps("k", []scratchpadItem{
		{ID: "a", Text: "collect A"}, {ID: "b", Text: "collect B"}, {ID: "c", Text: "collect C"},
		{ID: "sum", Text: "summarise", After: []string{"a", "b", "c"}},
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPlanGraphRefusesBadShapes(t *testing.T) {
	m := newScratchpadManager(nil)
	cases := []struct {
		name  string
		steps []scratchpadItem
		field string
	}{
		{"cycle", []scratchpadItem{{ID: "x", Text: "x", After: []string{"z"}}, {ID: "y", Text: "y", After: []string{"x"}}, {ID: "z", Text: "z", After: []string{"y"}}}, "cycle"},
		{"self", []scratchpadItem{{ID: "x", Text: "x", After: []string{"x"}}}, "cycle"},
		{"unknown", []scratchpadItem{{ID: "x", Text: "x", After: []string{"nope"}}}, "unknown_id"},
		{"duplicate", []scratchpadItem{{ID: "x", Text: "x"}, {ID: "x", Text: "again"}}, "duplicate_id"},
	}
	for _, c := range cases {
		_, err := m.setSteps("k", c.steps)
		pg, ok := err.(*planGraphError)
		if !ok {
			t.Fatalf("%s: want a plan-graph refusal, got %v", c.name, err)
		}
		payload := pg.payload()
		if payload["ok"] != false || payload[c.field] == nil {
			t.Fatalf("%s: payload lacks %q: %v", c.name, c.field, payload)
		}
	}
	if got := m.get("k"); len(got) != 0 {
		t.Fatalf("a refused plan was stored: %+v", got)
	}

	// Adding a step that closes a cycle is refused too.
	g := graphPad(t)
	if _, err := g.addStep("k", scratchpadItem{ID: "a", Text: "dup"}); err == nil {
		t.Fatal("duplicate id via add was accepted")
	}
	if list, err := g.addStep("k", scratchpadItem{ID: "publish", Text: "publish", After: []string{"sum"}}); err != nil || len(list) != 5 {
		t.Fatalf("valid add refused: %v", err)
	}
}

func TestPlanCheckRefusesOpenPredecessors(t *testing.T) {
	m := graphPad(t)
	_, err := m.check("k", 3, "too early")
	pg, ok := err.(*planGraphError)
	if !ok {
		t.Fatalf("checking the summary first was not refused: %v", err)
	}
	blocked, _ := pg.payload()["blocked_by"].([]planStepRef)
	if len(blocked) != 3 {
		t.Fatalf("blocked_by should name a, b, c: %+v", pg.payload())
	}
	if m.get("k")[3].Done {
		t.Fatal("refused check still marked the step done")
	}
	for i := 0; i < 3; i++ {
		if _, err := m.check("k", i, ""); err != nil {
			t.Fatalf("ready step %d refused: %v", i, err)
		}
	}
	if _, err := m.check("k", 3, "summarised"); err != nil {
		t.Fatalf("summary refused once its inputs were done: %v", err)
	}
}

func TestPlanSummaryMarksReadySteps(t *testing.T) {
	svc := &Service{}
	pad := svc.scratchpadStore()
	if _, err := pad.setSteps("k", []scratchpadItem{
		{ID: "a", Text: "collect A"}, {ID: "b", Text: "collect B"}, {ID: "c", Text: "collect C"},
		{ID: "sum", Text: "summarise", After: []string{"a", "b", "c"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pad.check("k", 0, "A is in a.json"); err != nil {
		t.Fatal(err)
	}
	got := svc.PlanSummary("k")
	for _, want := range []string{
		"[x] 0. collect A (id: a)",
		"[ ] 1. collect B (id: b) — ready",
		"[ ] 2. collect C (id: c) — ready",
		"[ ] 3. summarise (id: sum; after: a, b, c) — waiting on step 1, 2",
		"Carry on with a step marked ready",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestPlanReadyStepsLint(t *testing.T) {
	lint := PlanReadyStepsDone()
	if ok, _ := lint.Check("done", LintContext{}); !ok {
		t.Fatal("no plan must pass")
	}
	view := newPlanView("k", graphPad(t).get("k"))
	ok, reason := lint.Check("done", LintContext{Plan: view})
	if ok {
		t.Fatal("open ready steps passed")
	}
	if strings.Contains(reason, lint.Name()) {
		t.Fatalf("feedback names the lint: %s", reason)
	}
	if !strings.Contains(reason, "0 (collect A)") || strings.Contains(reason, "3 (summarise)") {
		t.Fatalf("reason should list ready steps only: %s", reason)
	}
}

// Across segments, a segment the lint blocked on an open plan hands the task
// to the next segment instead of ending it as blocked — the same answer the
// plan gate gives a segment that stopped early.
func TestSegmentBlockedOnOpenPlanContinues(t *testing.T) {
	actor := &planActor{actions: []string{"set", "check:0", "finish", "check:1", "finish", "check:2", "finish", "check:3", "finish"}}
	svc, err := New("plan-segments").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(actor).
		WithPlanStore(&memoryPlanStore{plans: map[string][]PlanItem{}}).
		WithAutonomy(AutonomyProfile{Scratchpad: true}).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	res, err := svc.RunSegments(context.Background(), "Collect three sources and summarise them.", LongRunConfig{
		MaxSegments: 3, RoundsPerSegment: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done() {
		t.Fatalf("task did not finish across segments: stop=%s segments=%d", res.Stop, len(res.Segments))
	}
	if len(res.Segments) != 2 {
		t.Fatalf("want the blocked segment plus one more, got %d", len(res.Segments))
	}
}

// A later turn that never touches the plan is not held to it: the lint sees
// a plan only when the run used the scratchpad tools.
func TestPlanLintIgnoresRunsThatDidNotTouchThePlan(t *testing.T) {
	actor := &planActor{actions: []string{"finish"}}
	svc, err := New("plan-untouched").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(actor).
		WithAutonomy(AutonomyProfile{Scratchpad: true}).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	taskID := "task-untouched"
	if _, err := svc.scratchpadStore().setSteps(taskScopedPlanKey(taskID), []scratchpadItem{{ID: "a", Text: "open step"}}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Run(context.Background(), "What is 2+2?", WithTaskID(taskID), WithConstraintExtraction(false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked || actor.finishes != 1 {
		t.Fatalf("an unrelated turn was held to an old plan: blocked=%v finishes=%d", res.Blocked, actor.finishes)
	}
}
