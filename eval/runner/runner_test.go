package runner

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scenarioDir resolves eval/scenarios relative to this test file so the
// suite is runnable from any working directory (`go test ./...`,
// `go test ./eval/runner`, IDE clicks, etc.).
func scenarioDir(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(here), "..", "scenarios")
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve scenarios dir: %v", err)
	}
	return abs
}

func TestEvalScenariosLoadCleanly(t *testing.T) {
	scs, err := LoadScenariosFromDir(scenarioDir(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(scs) == 0 {
		t.Fatal("no scenarios found")
	}
	seen := make(map[string]struct{})
	for _, sc := range scs {
		if _, dup := seen[sc.Name]; dup {
			t.Fatalf("duplicate scenario name %q", sc.Name)
		}
		seen[sc.Name] = struct{}{}
	}
}

func TestEvalMockScenariosAllPass(t *testing.T) {
	// Filter to mock scenarios so this test can run without a real LLM
	// provider; live-only scenarios still live in eval/scenarios/ for
	// the CLI to pick up via `agentgo eval --profile=live`.
	all, err := LoadScenariosFromDir(scenarioDir(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	mock := make([]*Scenario, 0, len(all))
	for _, sc := range all {
		if sc.Mode == ModeMock || sc.Mode == "" {
			mock = append(mock, sc)
		}
	}
	if len(mock) == 0 {
		t.Fatal("no mock scenarios found")
	}

	failed := 0
	for _, sc := range mock {
		res, err := Run(context.Background(), sc, RunOptions{})
		if err != nil {
			t.Errorf("scenario %s: Run errored: %v", sc.Name, err)
			failed++
			continue
		}
		if !res.Pass {
			failed++
			t.Errorf("scenario %s failed (pass=%d/%d): %v",
				res.Scenario, res.PassCount, res.Runs, res.Reasons)
		}
	}
	if failed > 0 {
		t.Logf("mock eval summary: %d/%d scenarios passed", len(mock)-failed, len(mock))
	}
}

func TestMarshalResultsContainsSummaryAndProfile(t *testing.T) {
	results := []*RunResult{
		{Scenario: "a", Mode: ModeMock, Runs: 1, Pass: true, PassCount: 1, Status: "completed"},
		{Scenario: "b", Mode: ModeMock, Runs: 1, Pass: false, FailCount: 1, Status: "blocked", Reasons: []string{"x"}},
	}
	bytes, err := MarshalResults(results, "mock")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(bytes)
	for _, want := range []string{`"summary"`, `"total": 2`, `"pass": 1`, `"fail": 1`, `"profile": "mock"`, `"timestamp"`, `"scenario": "a"`, `"scenario": "b"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q in:\n%s", want, out)
		}
	}
}

func TestRunReturnsErrorForLiveScenarioWithoutBuilder(t *testing.T) {
	sc := &Scenario{
		Name:  "fake_live",
		Mode:  ModeLive,
		Input: "anything",
		Runs:  1,
		Expect: ExpectSpec{
			Status: "completed",
		},
	}
	if _, err := Run(context.Background(), sc, RunOptions{}); err == nil {
		t.Fatal("expected error when live scenario runs without a Live builder")
	}
}
