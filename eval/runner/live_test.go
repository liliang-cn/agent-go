package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// resultsDir resolves eval/results relative to this file so the suite writes
// to the same place regardless of the working directory.
func resultsDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(filepath.Dir(scenarioDir(t)), "results")
}

// TestLiveScenarios runs every `mode: live` scenario against the real provider
// configured in the caller's agentgo config, and saves a timestamped JSON to
// eval/results/ for cross-commit diffing.
//
// It is opt-in: without AGENTGO_EVAL_LIVE=1 it skips, so `go test ./...` stays
// deterministic and offline. This replaces the old `agentgo eval --profile=live
// --save` CLI entry point.
//
//	AGENTGO_EVAL_LIVE=1 go test ./eval/runner -run TestLiveScenarios -v
func TestLiveScenarios(t *testing.T) {
	if os.Getenv("AGENTGO_EVAL_LIVE") != "1" {
		t.Skip("live eval is opt-in; set AGENTGO_EVAL_LIVE=1 to run it against a real provider")
	}

	all, err := LoadScenariosFromDir(scenarioDir(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	live := make([]*Scenario, 0, len(all))
	for _, sc := range all {
		if sc.Mode == ModeLive {
			live = append(live, sc)
		}
	}
	if len(live) == 0 {
		t.Skip("no live scenarios defined")
	}

	builder, err := BuildPoolLiveBuilder()
	if err != nil {
		t.Fatalf("build live builder: %v", err)
	}

	results := make([]*RunResult, 0, len(live))
	failed := 0
	for _, sc := range live {
		res, err := Run(context.Background(), sc, RunOptions{Live: builder})
		if err != nil {
			t.Errorf("scenario %s: Run errored: %v", sc.Name, err)
			failed++
			continue
		}
		results = append(results, res)
		if !res.Pass {
			failed++
			t.Errorf("scenario %s failed (pass=%d/%d): %v",
				res.Scenario, res.PassCount, res.Runs, res.Reasons)
		}
	}

	t.Log("\n" + FormatSummary(results))
	path, saveErr := SaveResults(results, "live", resultsDir(t))
	if saveErr != nil {
		t.Errorf("save results: %v", saveErr)
	} else {
		t.Logf("results written to %s", path)
	}
	if failed > 0 {
		t.Logf("live eval summary: %d/%d scenarios passed", len(live)-failed, len(live))
	}
}
