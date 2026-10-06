package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// A mock that reports nothing is "not measured" — never zero.
func TestRunWithoutUsageIsNotMeasured(t *testing.T) {
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
	raw, _ := json.Marshal(res)
	for _, want := range []string{`"tokens":null`, `"tokens_measured":false`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
}

// A mock that reports usage yields measured tokens, taken from the terminal
// event.
func TestRunWithMockUsageIsMeasured(t *testing.T) {
	res, err := Run(context.Background(), tokenScenario(&MockUsage{
		Model: "eval-mock-model", PromptTokens: 1000, CachedPromptTokens: 0, CompletionTokens: 500,
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
}

func TestSummarizeKeepsUnknownsNull(t *testing.T) {
	measured := []*RunResult{
		{Scenario: "a", Runs: 2, Pass: true, TokensMeasured: true, Tokens: &TokenStats{PromptTokens: 100, CompletionTokens: 20}},
		{Scenario: "b", Runs: 1, Pass: false, TokensMeasured: true, Tokens: &TokenStats{PromptTokens: 300, CompletionTokens: 60}},
	}
	s := Summarize(measured)
	if s.PassRate != 0.5 || s.TokensMeasured != 2 {
		t.Fatalf("summary = %+v", s)
	}
	if *s.MeanTotalTokens != 240 {
		t.Fatalf("mean tokens = %v, want 240", *s.MeanTotalTokens)
	}

	mixed := append(measured, &RunResult{Scenario: "c", Runs: 1, Pass: true})
	s = Summarize(mixed)
	if s.TokensMeasured != 2 {
		t.Fatalf("tokens measured = %d, want 2", s.TokensMeasured)
	}

	raw, _ := json.Marshal(Summarize([]*RunResult{{Scenario: "x", Pass: true}}))
	for _, want := range []string{`"mean_tokens":null`, `"mean_total_tokens":null`} {
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
		{Scenario: "a", Runs: 1, Pass: true, PassCount: 1},
		{Scenario: "b", Runs: 1, Pass: true, PassCount: 1},
	}
	degraded := []*RunResult{
		{Scenario: "a", Runs: 1, Pass: true, PassCount: 1},
		{Scenario: "b", Runs: 1, Pass: false, FailCount: 1},
	}
	A := load(t, writeResults(t, dir, "a.json", base))
	B := load(t, writeResults(t, dir, "b.json", degraded))

	if d := Diff(A, A); d.Failed() {
		t.Fatalf("identical files must pass: %v", d.Failures)
	}
	d := Diff(A, B)
	if !d.Failed() || !strings.Contains(d.Failures[0], "pass rate dropped") {
		t.Fatalf("pass-rate drop must fail: %v", d.Failures)
	}
	if !strings.Contains(FormatDiff(d), "PASS 1/1 -> FAIL 0/1") {
		t.Fatalf("per-scenario change missing:\n%s", FormatDiff(d))
	}
	if d := Diff(B, A); d.Failed() {
		t.Fatalf("an improvement must pass: %v", d.Failures)
	}
}

// A file written before tokens were recorded loads as not measured rather
// than as zero.
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
	if s.TokensMeasured != 0 || s.MeanTokens != nil {
		t.Fatalf("legacy file must read as not measured: %+v", s)
	}
}
