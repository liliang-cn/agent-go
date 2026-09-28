package runner

import (
	"encoding/json"
	"fmt"
	"os"
)

// TokenStats is a token count split the way providers bill it. In a
// RunResult it is the per-run average; in a Summary, the mean over scenarios.
type TokenStats struct {
	PromptTokens       float64 `json:"prompt_tokens"`
	CachedPromptTokens float64 `json:"cached_prompt_tokens"`
	CompletionTokens   float64 `json:"completion_tokens"`
}

// Total is prompt plus completion. Cached tokens are part of the prompt.
func (t TokenStats) Total() float64 { return t.PromptTokens + t.CompletionTokens }

// Summary is the whole-file readout of a results JSON.
//
// Every token and cost figure is a pointer on purpose: null means nothing
// could measure or price it, which must never read as zero. A partly
// measured file says how much of it was measured in TokensMeasured and
// CostUnpriced, and the totals that need every scenario — TotalCostUSD,
// CostPerPassUSD — stay null until every scenario is priced.
type Summary struct {
	Total    int     `json:"total"`
	Pass     int     `json:"pass"`
	Fail     int     `json:"fail"`
	PassRate float64 `json:"pass_rate"`

	// TokensMeasured counts scenarios whose every run reported usage.
	// MeanTokens and MeanTotalTokens average over those scenarios only.
	TokensMeasured  int         `json:"tokens_measured"`
	MeanTokens      *TokenStats `json:"mean_tokens"`
	MeanTotalTokens *float64    `json:"mean_total_tokens"`

	// CostUnpriced counts scenarios with at least one unpriced run.
	// MeanCostUSD averages the per-run cost over the priced scenarios.
	CostUnpriced   int      `json:"cost_unpriced"`
	MeanCostUSD    *float64 `json:"mean_cost_usd"`
	TotalCostUSD   *float64 `json:"total_cost_usd"`
	CostPerPassUSD *float64 `json:"cost_per_pass_usd"`
}

// ResultsFile is the saved JSON document: eval/results/<ts>.json.
type ResultsFile struct {
	Timestamp string       `json:"timestamp"`
	Profile   string       `json:"profile"`
	Summary   Summary      `json:"summary"`
	Results   []*RunResult `json:"results"`
}

// Summarize computes the Summary of a result set.
func Summarize(results []*RunResult) Summary {
	var s Summary
	s.Total = len(results)
	var tokSum TokenStats
	costSum, totalCost := 0.0, 0.0
	priced := 0
	for _, r := range results {
		if r == nil {
			continue
		}
		if r.Pass {
			s.Pass++
		} else {
			s.Fail++
		}
		if r.TokensMeasured && r.Tokens != nil {
			s.TokensMeasured++
			tokSum.PromptTokens += r.Tokens.PromptTokens
			tokSum.CachedPromptTokens += r.Tokens.CachedPromptTokens
			tokSum.CompletionTokens += r.Tokens.CompletionTokens
		}
		if c, ok := r.costKnown(); ok {
			priced++
			costSum += c
			totalCost += c * float64(max(r.Runs, 1))
		} else {
			s.CostUnpriced++
		}
	}
	if s.Total > 0 {
		s.PassRate = float64(s.Pass) / float64(s.Total)
	}
	if s.TokensMeasured > 0 {
		n := float64(s.TokensMeasured)
		mean := TokenStats{
			PromptTokens:       tokSum.PromptTokens / n,
			CachedPromptTokens: tokSum.CachedPromptTokens / n,
			CompletionTokens:   tokSum.CompletionTokens / n,
		}
		total := mean.Total()
		s.MeanTokens, s.MeanTotalTokens = &mean, &total
	}
	if priced > 0 {
		mean := costSum / float64(priced)
		s.MeanCostUSD = &mean
	}
	if s.Total > 0 && s.CostUnpriced == 0 {
		s.TotalCostUSD = &totalCost
		if s.Pass > 0 {
			per := totalCost / float64(s.Pass)
			s.CostPerPassUSD = &per
		}
	}
	return s
}

// costKnown returns the per-run average cost when it is actually known. A
// result written before cost was recorded has neither field, and that is
// unknown too.
func (r *RunResult) costKnown() (float64, bool) {
	if r == nil || r.CostUnpriced || r.AvgCostUSD == nil {
		return 0, false
	}
	return *r.AvgCostUSD, true
}

// LoadResultsFile reads a saved results JSON. The summary is recomputed from
// the results rather than trusted, so a file written before tokens and cost
// were recorded loads with them as not measured and unpriced.
func LoadResultsFile(path string) (*ResultsFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read results %s: %w", path, err)
	}
	var doc struct {
		Timestamp string       `json:"timestamp"`
		Profile   string       `json:"profile"`
		Results   []*RunResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse results %s: %w", path, err)
	}
	return &ResultsFile{
		Timestamp: doc.Timestamp,
		Profile:   doc.Profile,
		Summary:   Summarize(doc.Results),
		Results:   doc.Results,
	}, nil
}
