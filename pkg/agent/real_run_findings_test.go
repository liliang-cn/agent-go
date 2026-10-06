package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Things a real long run on a real gateway found, each pinned here.
//
// The run: "create a directory wordfreq with a Go CLI and tests". The agent
// had it done — module, tests, binary, `go test` green — at round 7. The
// delivery lint then rejected the answer three times because "wordfreq" was a
// directory, the model went looking for the lint by name, and the task ended
// blocked after 31 rounds and 1.4M tokens.

// A directory with content is a delivered artifact.
func TestDeliveryContractAcceptsADirectoryArtifact(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "wordfreq"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "wordfreq", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	lint := TaskDeliveryContract()
	ok, reason := lint.Check("Done.", LintContext{
		Workspace:    ws,
		Deliverables: []DeliverableRequirement{{Kind: "file", Path: "wordfreq"}},
	})
	if !ok {
		t.Fatalf("a directory holding the work was rejected: %q", reason)
	}

	// An empty directory is a mkdir, not a deliverable.
	if ok, _ := lint.Check("Done.", LintContext{
		Workspace:    ws,
		Deliverables: []DeliverableRequirement{{Kind: "file", Path: "empty"}},
	}); ok {
		t.Fatal("an empty directory passed as an artifact")
	}

	// file_task_must_write shares the check and must agree.
	if ok, reason := FileTaskMustWrite().Check("Done.", LintContext{
		Workspace:    ws,
		Deliverables: []DeliverableRequirement{{Kind: "file", Path: "wordfreq"}},
	}); !ok {
		t.Fatalf("file_task_must_write rejected the directory: %q", reason)
	}
}

// A task with no plan of its own is not handed another task's.
func TestPlanSummaryForRunNeverReadsTheSharedDefaultList(t *testing.T) {
	store := &memoryPlanStore{plans: map[string][]PlanItem{
		scratchpadDefaultKey: {
			{Text: "someone else's step", Done: true, Note: "did it"},
			{Text: "someone else's next step", Done: false},
		},
		taskScopedPlanKey("task-b"): {
			{Text: "task b step", Done: true, Note: "wrote b.go"},
			{Text: "task b next", Done: false},
		},
	}}
	svc := buildSegmentedService(t, "plan-scope", &scriptedLLM{finishAt: 0}, store)
	defer svc.Close()

	// A fresh task: nothing under its own key, and the shared list must not
	// leak in as "work already done on this task".
	if got := svc.planSummaryForRun("", "task-a"); got != "" {
		t.Fatalf("fresh task was handed the shared default plan:\n%s", got)
	}
	// A task that has a plan reads its own.
	got := svc.planSummaryForRun("", "task-b")
	if !strings.Contains(got, "task b step") || strings.Contains(got, "someone else") {
		t.Fatalf("task-b did not get its own plan:\n%s", got)
	}
	// An explicit key still wins.
	if got := svc.planSummaryForRun(scratchpadDefaultKey, "task-a"); !strings.Contains(got, "someone else's step") {
		t.Fatalf("a named plan key was ignored:\n%s", got)
	}
	// No task at all is the only case the shared list serves.
	if got := svc.planSummaryForRun("", ""); !strings.Contains(got, "someone else's step") {
		t.Fatalf("a run with no task lost the default list:\n%s", got)
	}
}
