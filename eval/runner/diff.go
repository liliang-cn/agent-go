package runner

import (
	"fmt"
	"sort"
	"strings"
)

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
	Failures []string
}

// Failed reports whether any gate tripped.
func (d *DiffReport) Failed() bool { return len(d.Failures) > 0 }

// Diff compares an old result file a with a new one b. The gate is the pass
// rate: the diff fails when it dropped.
func Diff(a, b *ResultsFile) *DiffReport {
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
	return resultCell(a) != resultCell(b) || tokensCell(a) != tokensCell(b)
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

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }
