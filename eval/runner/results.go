package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SaveResults writes the marshaled results JSON to <dir>/<timestamp>.json
// and returns the file path. The directory is created if missing.
func SaveResults(results []*RunResult, profile, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("ensure results dir: %w", err)
	}
	bytes, err := MarshalResults(results, profile)
	if err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	if profile != "" {
		stamp = stamp + "-" + sanitizeProfile(profile)
	}
	out := filepath.Join(dir, stamp+".json")
	if err := os.WriteFile(out, bytes, 0o644); err != nil {
		return "", fmt.Errorf("write results: %w", err)
	}
	return out, nil
}

// FormatSummary returns a multi-line table suitable for printing to the
// terminal. Columns: scenario, mode, pass/runs, lint trips, avg LLM
// calls, avg duration ms.
func FormatSummary(results []*RunResult) string {
	if len(results) == 0 {
		return "(no results)\n"
	}
	rows := make([][]string, 0, len(results)+1)
	rows = append(rows, []string{"SCENARIO", "MODE", "RESULT", "AVG_LLM", "AVG_MS", "LINT_TRIPS"})
	totalPass, totalFail := 0, 0
	for _, r := range results {
		mark := "PASS"
		if !r.Pass {
			mark = "FAIL"
			totalFail++
		} else {
			totalPass++
		}
		lintCol := "-"
		if len(r.LintViolations) > 0 {
			parts := make([]string, 0, len(r.LintViolations))
			for name, count := range r.LintViolations {
				parts = append(parts, fmt.Sprintf("%s:%d", name, count))
			}
			lintCol = strings.Join(parts, ",")
		}
		rows = append(rows, []string{
			r.Scenario,
			string(r.Mode),
			fmt.Sprintf("%s %d/%d", mark, r.PassCount, r.Runs),
			fmt.Sprintf("%.1f", r.AvgLLMCalls),
			fmt.Sprintf("%.0f", r.AvgDurationMs),
			lintCol,
		})
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if w := len(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	var b strings.Builder
	for _, row := range rows {
		for i, cell := range row {
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len(cell)+2))
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nsummary: %d passed / %d failed (%d total)\n", totalPass, totalFail, len(results))
	for _, r := range results {
		if !r.Pass && len(r.Reasons) > 0 {
			fmt.Fprintf(&b, "  - %s:\n", r.Scenario)
			for _, reason := range r.Reasons {
				fmt.Fprintf(&b, "      %s\n", reason)
			}
		}
	}
	return b.String()
}

func sanitizeProfile(p string) string {
	out := make([]rune, 0, len(p))
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
