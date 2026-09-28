package runner

import (
	"fmt"
	"sort"
	"strings"
)

// DiffOptions sets the gates Diff enforces beyond the pass rate.
type DiffOptions struct {
	// MaxCostPerPassRise fails the diff when cost per passed scenario rose by
	// more than this fraction (0.2 = 20%). Negative disables the gate. When
	// either side's cost per pass is unknown the gate cannot see, and says so
	// instead of passing or failing silently.
	MaxCostPerPassRise float64
}

// ScenarioDiff is one scenario present in both files.
type ScenarioDiff struct {
	Name          string
	Before, After *RunResult
}

// DiffReport compares two result files. Failures lists every gate that
// tripped; an empty list means the new file is no worse where it is gated.
type DiffReport struct {
	A, B     Summary
	Changed  []ScenarioDiff
	Same     int
	Added    []string
	Removed  []string
	Notes    []string
	Failures []string
}

// Failed reports whether any gate tripped.
func (d *DiffReport) Failed() bool { return len(d.Failures) > 0 }

// Diff compares an old result file a with a new one b.
func Diff(a, b *ResultsFile, opts DiffOptions) *DiffReport {
	d := &DiffReport{A: a.Summary, B: b.Summary}
	before := indexResults(a.Results)
	after := indexResults(b.Results)
	for _, name := range sortedKeys(before) {
		old := before[name]
		cur, ok := after[name]
		if !ok {
			d.Removed = append(d.Removed, name)
			continue
		}
		if scenarioChanged(old, cur) {
			d.Changed = append(d.Changed, ScenarioDiff{Name: name, Before: old, After: cur})
		} else {
			d.Same++
		}
	}
	for _, name := range sortedKeys(after) {
		if _, ok := before[name]; !ok {
			d.Added = append(d.Added, name)
		}
	}

	if d.B.PassRate < d.A.PassRate-1e-9 {
		d.Failures = append(d.Failures, fmt.Sprintf("pass rate dropped: %s -> %s",
			pct(d.A.PassRate), pct(d.B.PassRate)))
	}
	if opts.MaxCostPerPassRise >= 0 {
		switch {
		case d.A.CostPerPassUSD == nil || d.B.CostPerPassUSD == nil:
			d.Notes = append(d.Notes, "cost-per-pass gate not applied: cost per pass is unknown (unpriced or no passes) on "+unknownSide(d.A.CostPerPassUSD, d.B.CostPerPassUSD))
		case *d.A.CostPerPassUSD > 0:
			rise := (*d.B.CostPerPassUSD - *d.A.CostPerPassUSD) / *d.A.CostPerPassUSD
			if rise > opts.MaxCostPerPassRise+1e-12 {
				d.Failures = append(d.Failures, fmt.Sprintf("cost per pass rose %+.1f%% (limit %+.1f%%): %s -> %s",
					rise*100, opts.MaxCostPerPassRise*100, usd(d.A.CostPerPassUSD), usd(d.B.CostPerPassUSD)))
			}
		case *d.B.CostPerPassUSD > 0:
			d.Failures = append(d.Failures, fmt.Sprintf("cost per pass rose from $0 to %s", usd(d.B.CostPerPassUSD)))
		}
	}
	return d
}

// FormatDiff renders a report for a terminal.
func FormatDiff(d *DiffReport) string {
	var b strings.Builder
	b.WriteString("scenario changes:\n")
	if len(d.Changed) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, c := range d.Changed {
		fmt.Fprintf(&b, "  %s\n", c.Name)
		fmt.Fprintf(&b, "      result  %s -> %s\n", resultCell(c.Before), resultCell(c.After))
		if tokensCell(c.Before) != tokensCell(c.After) {
			fmt.Fprintf(&b, "      tokens  %s -> %s\n", tokensCell(c.Before), tokensCell(c.After))
		}
		if costCell(c.Before) != costCell(c.After) {
			fmt.Fprintf(&b, "      cost    %s -> %s\n", costCell(c.Before), costCell(c.After))
		}
	}
	if d.Same > 0 {
		fmt.Fprintf(&b, "  %d unchanged\n", d.Same)
	}
	for _, n := range d.Added {
		fmt.Fprintf(&b, "  + %s (only in B)\n", n)
	}
	for _, n := range d.Removed {
		fmt.Fprintf(&b, "  - %s (only in A)\n", n)
	}

	b.WriteString("\ntotals:            A                 B\n")
	row := func(label, x, y string) { fmt.Fprintf(&b, "  %-16s %-17s %s\n", label, x, y) }
	row("scenarios", fmt.Sprint(d.A.Total), fmt.Sprint(d.B.Total))
	row("passed", fmt.Sprintf("%d/%d", d.A.Pass, d.A.Total), fmt.Sprintf("%d/%d", d.B.Pass, d.B.Total))
	row("pass rate", pct(d.A.PassRate), pct(d.B.PassRate))
	row("mean tokens", meanTokensCell(d.A), meanTokensCell(d.B))
	row("mean cost", meanCostCell(d.A), meanCostCell(d.B))
	row("cost per pass", costPerPassCell(d.A), costPerPassCell(d.B))

	for _, n := range d.Notes {
		fmt.Fprintf(&b, "\nnote: %s\n", n)
	}
	if d.Failed() {
		b.WriteString("\nFAIL\n")
		for _, f := range d.Failures {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	} else {
		b.WriteString("\nOK\n")
	}
	return b.String()
}

func indexResults(rs []*RunResult) map[string]*RunResult {
	out := make(map[string]*RunResult, len(rs))
	for _, r := range rs {
		if r != nil {
			out[r.Scenario] = r
		}
	}
	return out
}

func sortedKeys(m map[string]*RunResult) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func scenarioChanged(a, b *RunResult) bool {
	return resultCell(a) != resultCell(b) || tokensCell(a) != tokensCell(b) || costCell(a) != costCell(b)
}

func resultCell(r *RunResult) string {
	mark := "PASS"
	if !r.Pass {
		mark = "FAIL"
	}
	return fmt.Sprintf("%s %d/%d", mark, r.PassCount, r.Runs)
}

func tokensCell(r *RunResult) string {
	if !r.TokensMeasured || r.Tokens == nil {
		return "not measured"
	}
	return fmt.Sprintf("%.0f (prompt %.0f, cached %.0f, completion %.0f)",
		r.Tokens.Total(), r.Tokens.PromptTokens, r.Tokens.CachedPromptTokens, r.Tokens.CompletionTokens)
}

func costCell(r *RunResult) string {
	c, ok := r.costKnown()
	if !ok {
		return "unpriced"
	}
	return usd(&c)
}

func meanTokensCell(s Summary) string {
	if s.MeanTotalTokens == nil {
		return "not measured"
	}
	cell := fmt.Sprintf("%.0f", *s.MeanTotalTokens)
	if s.TokensMeasured < s.Total {
		cell += fmt.Sprintf(" (%d/%d)", s.TokensMeasured, s.Total)
	}
	return cell
}

func meanCostCell(s Summary) string {
	if s.MeanCostUSD == nil {
		return "unpriced"
	}
	cell := usd(s.MeanCostUSD)
	if s.CostUnpriced > 0 {
		cell += fmt.Sprintf(" (%d unpriced)", s.CostUnpriced)
	}
	return cell
}

func costPerPassCell(s Summary) string {
	if s.CostPerPassUSD == nil && s.CostUnpriced == 0 && s.Pass == 0 {
		return "n/a (no passes)"
	}
	return usd(s.CostPerPassUSD)
}

func usd(v *float64) string {
	if v == nil {
		return "unpriced"
	}
	return fmt.Sprintf("$%.6f", *v)
}

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

func unknownSide(a, b *float64) string {
	switch {
	case a == nil && b == nil:
		return "A and B"
	case a == nil:
		return "A"
	default:
		return "B"
	}
}
