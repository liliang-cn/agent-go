package codemode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// DefaultMaxAttempts bounds a Session when no limit is given.
const DefaultMaxAttempts = 6

// ErrAttemptsExhausted is returned by Session.Submit once the attempt budget
// is spent.
var ErrAttemptsExhausted = errors.New("codemode: attempt budget exhausted")

// Session is many attempts at one task. It numbers the attempts, stops at the
// budget and keeps the best result: the latest passing one, or, while none
// passes, the one with the fewest errors.
type Session struct {
	mode        *Mode
	maxAttempts int

	mu       sync.Mutex
	attempts []*Result
	best     *Result
}

// NewSession starts a session. maxAttempts <= 0 takes DefaultMaxAttempts.
func NewSession(m *Mode, maxAttempts int) *Session {
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	return &Session{mode: m, maxAttempts: maxAttempts}
}

// Mode is the session's mode.
func (s *Session) Mode() *Mode { return s.mode }

// MaxAttempts is the session's attempt budget.
func (s *Session) MaxAttempts() int { return s.maxAttempts }

// Submit runs one attempt. Past the budget it runs nothing and returns
// ErrAttemptsExhausted.
func (s *Session) Submit(ctx context.Context, code string) (*Result, error) {
	s.mu.Lock()
	n := len(s.attempts)
	s.mu.Unlock()
	if n >= s.maxAttempts {
		return nil, ErrAttemptsExhausted
	}
	res := Run(ctx, s.mode, code)

	s.mu.Lock()
	defer s.mu.Unlock()
	res.Attempt = len(s.attempts) + 1
	s.attempts = append(s.attempts, res)
	if better(res, s.best) {
		s.best = res
	}
	return res, nil
}

// better reports whether a should replace b as the kept result.
func better(a, b *Result) bool {
	if b == nil {
		return true
	}
	if a.OK() != b.OK() {
		return a.OK()
	}
	if a.OK() {
		return true // both pass: the later one is the revision the model chose
	}
	if (len(a.Artifact) > 0) != (len(b.Artifact) > 0) {
		return len(a.Artifact) > 0
	}
	return a.ErrorCount() <= b.ErrorCount()
}

// Best is the kept result, or nil before the first attempt.
func (s *Session) Best() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.best
}

// Attempts returns every attempt so far, oldest first.
func (s *Session) Attempts() []*Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Result(nil), s.attempts...)
}

// Done reports whether the session has a passing result or no attempts left.
func (s *Session) Done() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return (s.best != nil && s.best.OK()) || len(s.attempts) >= s.maxAttempts
}

// Report is the compact feedback for one attempt, written for the model.
func (s *Session) Report(r *Result) string {
	s.mu.Lock()
	best := s.best
	used := len(s.attempts)
	s.mu.Unlock()
	return formatReport(r, best, used, s.maxAttempts)
}

// Report is the feedback for a single Run with no session around it.
func (r *Result) Report() string { return formatReport(r, nil, 0, 0) }

const maxIssuesPerKind = 12

func formatReport(r *Result, best *Result, used, max int) string {
	if r == nil {
		return "no result"
	}
	var b strings.Builder
	head := "attempt"
	if r.Attempt > 0 && max > 0 {
		head = fmt.Sprintf("attempt %d/%d", r.Attempt, max)
	}
	errs, warns := r.ErrorCount(), r.WarningCount()
	switch {
	case r.OK():
		fmt.Fprintf(&b, "%s: PASSED", head)
		if warns > 0 {
			fmt.Fprintf(&b, " with %d warning(s)", warns)
		}
	default:
		fmt.Fprintf(&b, "%s: FAILED (%d error(s), %d warning(s))", head, errs, warns)
	}
	fmt.Fprintf(&b, ", ran in %dms\n", r.Duration.Milliseconds())

	if r.Error != nil {
		b.WriteString("program error: ")
		if r.Error.Line > 0 {
			fmt.Fprintf(&b, "line %d", r.Error.Line)
			if r.Error.Column > 0 {
				fmt.Fprintf(&b, ":%d", r.Error.Column)
			}
			b.WriteString(": ")
		}
		fmt.Fprintf(&b, "%s (%s)\n", r.Error.Message, r.Error.Kind)
		if line := sourceLine(r.Code, r.Error.Line); line != "" {
			fmt.Fprintf(&b, "  > %s\n", line)
		}
	}
	writeIssues(&b, r, SeverityError, "errors")
	writeIssues(&b, r, SeverityWarning, "warnings")
	if len(r.Stats) > 0 {
		b.WriteString("measurements:\n")
		for _, st := range r.Stats {
			fmt.Fprintf(&b, "- %s: %s\n", st.Key, st.Value)
		}
	}
	if len(r.Logs) > 0 {
		b.WriteString("console:\n")
		for _, l := range r.Logs {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	if r.Render != nil && strings.TrimSpace(r.Render.Notes) != "" {
		fmt.Fprintf(&b, "render: %s\n", strings.TrimSpace(r.Render.Notes))
	}

	switch {
	case r.OK():
		b.WriteString("next: if the measurements match the request, stop and reply in one or two sentences (do not paste the program). Otherwise revise and run again.")
	case max > 0 && used >= max:
		b.WriteString("next: no attempts left. Reply in one or two sentences saying what was built and what is still wrong; the best attempt is kept.")
	default:
		b.WriteString("next: fix the program and run the whole program again.")
	}
	if best != nil && best != r && max > 0 {
		state := "failed"
		if best.OK() {
			state = "passed"
		}
		fmt.Fprintf(&b, " (kept so far: attempt %d, %s)", best.Attempt, state)
	}
	if max > 0 && used < max && !r.OK() {
		fmt.Fprintf(&b, " %d attempt(s) left.", max-used)
	}
	return b.String()
}

func writeIssues(b *strings.Builder, r *Result, sev Severity, title string) {
	var list []Issue
	for _, is := range r.Issues {
		if is.Severity == sev {
			list = append(list, is)
		}
	}
	if len(list) == 0 {
		return
	}
	b.WriteString(title + ":\n")
	for i, is := range list {
		if i == maxIssuesPerKind {
			fmt.Fprintf(b, "- … and %d more\n", len(list)-maxIssuesPerKind)
			break
		}
		b.WriteString("- ")
		if is.Line > 0 {
			fmt.Fprintf(b, "line %d: ", is.Line)
		}
		if is.Node != "" {
			fmt.Fprintf(b, "%q: ", is.Node)
		}
		b.WriteString(is.Message)
		b.WriteByte('\n')
	}
}

func sourceLine(code string, line int) string {
	if line <= 0 {
		return ""
	}
	lines := strings.Split(code, "\n")
	if line > len(lines) {
		return ""
	}
	s := strings.TrimSpace(lines[line-1])
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// Author writes the next program. previous is nil on the first call; after
// that it is the last attempt and feedback is its report.
type Author func(ctx context.Context, previous *Result, feedback string) (code string, err error)

// Iterate drives a session with an author until an attempt passes, the
// budget runs out, or the author fails. It returns the kept result. Use it
// to run the loop with your own model client, a script, or a test double;
// pkg/agent's WithCodeMode runs the same loop through an agent instead.
func Iterate(ctx context.Context, s *Session, author Author) (*Result, error) {
	var prev *Result
	feedback := ""
	for !s.Done() {
		if err := ctx.Err(); err != nil {
			return s.Best(), err
		}
		code, err := author(ctx, prev, feedback)
		if err != nil {
			return s.Best(), err
		}
		res, err := s.Submit(ctx, code)
		if err != nil {
			break
		}
		prev, feedback = res, s.Report(res)
	}
	return s.Best(), nil
}
