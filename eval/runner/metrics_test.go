package runner

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

func tokenScenario(usage *MockUsage) *Scenario {
	return &Scenario{
		Name:       "tokens",
		Mode:       ModeMock,
		Input:      "what is 2 + 2?",
		LLMReplies: []string{"2 + 2 = 4."},
		Runs:       2,
		MockUsage:  usage,
		Expect:     ExpectSpec{Status: "completed"},
	}
}

// A mock that reports nothing is "not measured" and unpriced — never zero.
func TestRunWithoutUsageIsNotMeasuredAndUnpriced(t *testing.T) {
	res, err := Run(context.Background(), tokenScenario(nil), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("scenario failed: %v", res.Reasons)
	}
	if res.TokensMeasured || res.Tokens != nil {
		t.Fatalf("tokens should be not measured, got measured=%v %+v", res.TokensMeasured, res.Tokens)
	}
	if !res.CostUnpriced || res.AvgCostUSD != nil {
		t.Fatalf("cost should be unpriced, got unpriced=%v avg=%v", res.CostUnpriced, res.AvgCostUSD)
	}
	raw, _ := json.Marshal(res)
	for _, want := range []string{`"tokens":null`, `"tokens_measured":false`, `"cost_unpriced":true`, `"avg_cost_usd":null`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
}

// A mock that reports usage for a priced model yields measured tokens and a
// real cost, taken from the terminal event.
func TestRunWithMockUsageIsMeasuredAndPriced(t *testing.T) {
	const model = "eval-mock-priced"
	pool.RegisterModelPricing(model, pool.ModelPricing{InputPer1K: 1, OutputPer1K: 2})
	defer pool.UnregisterModelPricing(model)

	res, err := Run(context.Background(), tokenScenario(&MockUsage{
		Model: model, PromptTokens: 1000, CachedPromptTokens: 0, CompletionTokens: 500,
	}), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("scenario failed: %v", res.Reasons)
	}
	if !res.TokensMeasured || res.Tokens == nil {
		t.Fatalf("tokens should be measured")
	}
	if res.Tokens.PromptTokens != 1000 || res.Tokens.CompletionTokens != 500 {
		t.Fatalf("per-run tokens = %+v, want prompt 1000 completion 500", res.Tokens)
	}
	if res.CostUnpriced || res.AvgCostUSD == nil {
		t.Fatalf("cost should be priced, unpriced=%v", res.CostUnpriced)
	}
	// 1000/1K*1 + 500/1K*2 = 2.0 per run.
	if math.Abs(*res.AvgCostUSD-2.0) > 1e-9 {
		t.Fatalf("avg cost = %v, want 2.0", *res.AvgCostUSD)
	}
}

// A named but unpriced model reports tokens while its cost stays unpriced.
func TestRunWithUsageButUnknownModelIsUnpriced(t *testing.T) {
	res, err := Run(context.Background(), tokenScenario(&MockUsage{
		Model: "eval-mock-nobody-priced-this", PromptTokens: 10, CompletionTokens: 5,
	}), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TokensMeasured {
		t.Fatal("tokens should be measured")
	}
	if !res.CostUnpriced || res.AvgCostUSD != nil {
		t.Fatalf("unknown model must be unpriced, got avg=%v", res.AvgCostUSD)
	}
}

func f(v float64) *float64 { return &v }

func TestSummarizeKeepsUnknownsNull(t *testing.T) {
	priced := []*RunResult{
		{Scenario: "a", Runs: 2, Pass: true, TokensMeasured: true, Tokens: &TokenStats{PromptTokens: 100, CompletionTokens: 20}, AvgCostUSD: f(0.01)},
		{Scenario: "b", Runs: 1, Pass: false, TokensMeasured: true, Tokens: &TokenStats{PromptTokens: 300, CompletionTokens: 60}, AvgCostUSD: f(0.03)},
	}
	s := Summarize(priced)
	if s.PassRate != 0.5 || s.TokensMeasured != 2 || s.CostUnpriced != 0 {
		t.Fatalf("summary = %+v", s)
	}
	if *s.MeanTotalTokens != 240 {
		t.Fatalf("mean tokens = %v, want 240", *s.MeanTotalTokens)
	}
	// total = 0.01*2 + 0.03*1 = 0.05; one pass.
	if math.Abs(*s.TotalCostUSD-0.05) > 1e-12 || math.Abs(*s.CostPerPassUSD-0.05) > 1e-12 {
		t.Fatalf("total %v per pass %v", *s.TotalCostUSD, *s.CostPerPassUSD)
	}

	mixed := append(priced, &RunResult{Scenario: "c", Runs: 1, Pass: true, CostUnpriced: true})
	s = Summarize(mixed)
	if s.CostUnpriced != 1 || s.TotalCostUSD != nil || s.CostPerPassUSD != nil {
		t.Fatalf("one unpriced scenario must null the totals: %+v", s)
	}
	if s.MeanCostUSD == nil || math.Abs(*s.MeanCostUSD-0.02) > 1e-12 {
		t.Fatalf("mean cost over priced scenarios should be 0.02, got %v", s.MeanCostUSD)
	}
	if s.TokensMeasured != 2 {
		t.Fatalf("tokens measured = %d, want 2", s.TokensMeasured)
	}

	raw, _ := json.Marshal(Summarize([]*RunResult{{Scenario: "x", Pass: true, CostUnpriced: true}}))
	for _, want := range []string{`"mean_tokens":null`, `"mean_cost_usd":null`, `"cost_per_pass_usd":null`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
}

func writeResults(t *testing.T, dir, name string, results []*RunResult) string {
	t.Helper()
	raw, err := MarshalResults(results, "test")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func load(t *testing.T, p string) *ResultsFile {
	t.Helper()
	rf, err := LoadResultsFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return rf
}

func TestDiffGates(t *testing.T) {
	dir := t.TempDir()
	base := []*RunResult{
		{Scenario: "a", Runs: 1, Pass: true, PassCount: 1, AvgCostUSD: f(0.01)},
		{Scenario: "b", Runs: 1, Pass: true, PassCount: 1, AvgCostUSD: f(0.01)},
	}
	degraded := []*RunResult{
		{Scenario: "a", Runs: 1, Pass: true, PassCount: 1, AvgCostUSD: f(0.01)},
		{Scenario: "b", Runs: 1, Pass: false, FailCount: 1, AvgCostUSD: f(0.01)},
	}
	pricier := []*RunResult{
		{Scenario: "a", Runs: 1, Pass: true, PassCount: 1, AvgCostUSD: f(0.015)},
		{Scenario: "b", Runs: 1, Pass: true, PassCount: 1, AvgCostUSD: f(0.015)},
	}
	unpriced := []*RunResult{
		{Scenario: "a", Runs: 1, Pass: true, PassCount: 1, CostUnpriced: true},
		{Scenario: "b", Runs: 1, Pass: true, PassCount: 1, CostUnpriced: true},
	}
	A := load(t, writeResults(t, dir, "a.json", base))
	B := load(t, writeResults(t, dir, "b.json", degraded))
	C := load(t, writeResults(t, dir, "c.json", pricier))
	U := load(t, writeResults(t, dir, "u.json", unpriced))

	if d := Diff(A, A, DiffOptions{MaxCostPerPassRise: -1}); d.Failed() {
		t.Fatalf("identical files must pass: %v", d.Failures)
	}
	d := Diff(A, B, DiffOptions{MaxCostPerPassRise: -1})
	if !d.Failed() || !strings.Contains(d.Failures[0], "pass rate dropped") {
		t.Fatalf("pass-rate drop must fail: %v", d.Failures)
	}
	if !strings.Contains(FormatDiff(d), "PASS 1/1 -> FAIL 0/1") {
		t.Fatalf("per-scenario change missing:\n%s", FormatDiff(d))
	}
	if d := Diff(B, A, DiffOptions{MaxCostPerPassRise: -1}); d.Failed() {
		t.Fatalf("an improvement must pass: %v", d.Failures)
	}
	if d := Diff(A, C, DiffOptions{MaxCostPerPassRise: -1}); d.Failed() {
		t.Fatalf("cost gate disabled by default: %v", d.Failures)
	}
	if d := Diff(A, C, DiffOptions{MaxCostPerPassRise: 0.2}); !d.Failed() {
		t.Fatal("a 50% rise in cost per pass must fail a 20% gate")
	}
	if d := Diff(A, C, DiffOptions{MaxCostPerPassRise: 0.6}); d.Failed() {
		t.Fatalf("a 50%% rise is within a 60%% gate: %v", d.Failures)
	}
	d = Diff(A, U, DiffOptions{MaxCostPerPassRise: 0.2})
	if d.Failed() || len(d.Notes) == 0 {
		t.Fatalf("an unpriced side must be noted, not guessed: failures=%v notes=%v", d.Failures, d.Notes)
	}
	if out := FormatDiff(d); !strings.Contains(out, "unpriced") || strings.Contains(out, "$0.000000") {
		t.Fatalf("unpriced must read as unpriced, never $0:\n%s", out)
	}
}

// A file written before tokens and cost were recorded loads as not measured
// and unpriced rather than as zero.
func TestLoadLegacyResultsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "legacy.json")
	legacy := `{"profile":"live","results":[{"scenario":"s","mode":"live","runs":2,"pass":true,"pass_count":2,"fail_count":0}],"summary":{"fail":0,"pass":1,"total":1},"timestamp":"2026-05-14T01:45:51Z"}`
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	rf := load(t, p)
	s := rf.Summary
	if s.Total != 1 || s.Pass != 1 || s.PassRate != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.TokensMeasured != 0 || s.MeanTokens != nil || s.CostUnpriced != 1 || s.MeanCostUSD != nil || s.CostPerPassUSD != nil {
		t.Fatalf("legacy file must read as not measured / unpriced: %+v", s)
	}
}
