package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// Four things a real long run on a real gateway found, each pinned here.
//
// The run: "create a directory wordfreq with a Go CLI and tests". The agent
// had it done — module, tests, binary, `go test` green — at round 7. The
// delivery lint then rejected the answer three times because "wordfreq" was a
// directory, the model went looking for the lint by name, and the task ended
// blocked after 31 rounds and 1.4M tokens priced at $0.

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

// The doctor says when a model cannot be priced, because a cost ceiling on an
// unpriced model is a ceiling on zero.
func TestDoctorWarnsWhenAModelIsUnpriced(t *testing.T) {
	home := healthyHome(t) // provider "local" serves "test-model", which no table prices
	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	got := findCheck(t, report, "llm.provider.local.pricing")
	if got.Status != DoctorWarn {
		t.Fatalf("pricing check = %v %q, want a warning", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "test-model") || !strings.Contains(got.Detail, "MaxTotalCostUSD") {
		t.Fatalf("the warning should name the model and the ceiling it disables: %q", got.Detail)
	}
	if !report.Healthy() {
		t.Fatalf("an unpriced model is a warning, not a failure:\n%s", report.Summary())
	}

	// Registered rates turn it green.
	pool.RegisterModelPricing("test-model", pool.ModelPricing{InputPer1K: 0.001, OutputPer1K: 0.002})
	defer pool.UnregisterModelPricing("test-model")
	report, err = Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if got := findCheck(t, report, "llm.provider.local.pricing"); got.Status != DoctorOK {
		t.Fatalf("pricing check after registering rates = %v %q", got.Status, got.Detail)
	}
}

type pricedScriptedLLM struct{ scriptedLLM }

func (p *pricedScriptedLLM) UsageModel() string { return "priced-scripted-model" }

// A run reports that its cost figure is unknown rather than letting 0 pass
// for free — and stops saying so once the model is priced.
func TestExecutionResultSaysWhenCostIsUnpriced(t *testing.T) {
	cfg := testAgentConfig(t.TempDir())

	unpriced, err := New("unpriced").WithConfig(cfg).WithLLM(&scriptedLLM{finishAt: 0}).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer unpriced.Close()
	res, err := unpriced.Run(context.Background(), "Work.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.CostUnpriced {
		t.Fatalf("a nameless model priced its run: cost=%v unpriced=%v", res.EstimatedCostUSD, res.CostUnpriced)
	}

	pool.RegisterModelPricing("priced-scripted-model", pool.ModelPricing{InputPer1K: 0.001, OutputPer1K: 0.002})
	defer pool.UnregisterModelPricing("priced-scripted-model")
	var gen interface {
		UsageModel() string
	} = &pricedScriptedLLM{scriptedLLM{finishAt: 0}}
	priced, err := New("priced").WithConfig(cfg).WithLLM(gen.(*pricedScriptedLLM)).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer priced.Close()
	res, err = priced.Run(context.Background(), "Work.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CostUnpriced {
		t.Fatal("a priced model reported its cost as unknown")
	}
}
