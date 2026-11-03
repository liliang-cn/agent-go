package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// listMode is the smallest useful mode: the program pushes numbers into a
// list, and the validator wants the list to sum to 10.
func listMode() *Mode {
	return &Mode{
		Name:    "list",
		Docs:    "list.add(n) appends a number.",
		Prelude: `var __items = []; var list = { add: function (n) { if (typeof n !== "number") throw new Error("list.add: n must be a number"); __items.push(n); } };`,
		Export:  "__items",
		Validators: []Validator{func(_ context.Context, raw json.RawMessage) Check {
			var xs []float64
			_ = json.Unmarshal(raw, &xs)
			sum := 0.0
			for _, x := range xs {
				sum += x
			}
			c := Check{Stats: []Stat{{Key: "items", Value: fmt.Sprint(len(xs))}, {Key: "sum", Value: fmt.Sprint(sum)}}}
			if sum != 10 {
				c.Issues = append(c.Issues, Issue{Severity: SeverityError, Message: fmt.Sprintf("the items must sum to 10, they sum to %v", sum)})
			}
			return c
		}},
		Limits: Limits{Timeout: 300 * time.Millisecond},
	}
}

func TestRunProducesArtifact(t *testing.T) {
	res := Run(context.Background(), listMode(), "for (let i = 1; i <= 4; i++) list.add(i);")
	if !res.OK() {
		t.Fatalf("want OK, got:\n%s", res.Report())
	}
	if string(res.Artifact) != "[1,2,3,4]" {
		t.Fatalf("artifact = %s", res.Artifact)
	}
	if !strings.Contains(res.Report(), "- sum: 10") {
		t.Fatalf("report lacks stats:\n%s", res.Report())
	}
}

func TestInfiniteLoopIsKilled(t *testing.T) {
	started := time.Now()
	res := Run(context.Background(), listMode(), "list.add(1);\nlet i = 0;\nwhile (true) { i++; }\n")
	if res.Error == nil || res.Error.Kind != ErrTimeout {
		t.Fatalf("want timeout, got %+v", res.Error)
	}
	if res.Error.Line != 3 {
		t.Fatalf("timeout should point at the loop on line 3, got %d", res.Error.Line)
	}
	if d := time.Since(started); d > 2*time.Second {
		t.Fatalf("took %v to stop", d)
	}
}

func TestCancelledContextStopsTheProgram(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	m := listMode()
	m.Limits.Timeout = 10 * time.Second
	res := Run(ctx, m, "for(;;){}")
	if res.Error == nil || res.Error.Kind != ErrTimeout || !strings.Contains(res.Error.Message, "cancelled") {
		t.Fatalf("got %+v", res.Error)
	}
}

func TestSandboxHasNoHostAccess(t *testing.T) {
	for _, probe := range []string{
		"require('fs')",
		"fetch('http://example.com')",
		"new XMLHttpRequest()",
		"process.exit(1)",
		"setTimeout(function(){}, 1)",
		"Deno.readFile('x')",
		"import('fs')",
	} {
		res := Run(context.Background(), listMode(), probe)
		if res.Error == nil {
			t.Errorf("%s: expected the sandbox to refuse", probe)
		}
	}
	// typeof checks agree: nothing host-shaped is bound.
	res := Run(context.Background(), listMode(), `
for (const name of ['require','process','fetch','XMLHttpRequest','setTimeout','global','module','Deno','Bun']) {
  assert(eval('typeof ' + name) === 'undefined', name + ' is bound');
}
list.add(10);`)
	if !res.OK() {
		t.Fatal(res.Report())
	}
}

func TestSyntaxErrorHasLine(t *testing.T) {
	res := Run(context.Background(), listMode(), "list.add(1);\nlist.add(2;\n")
	if res.Error == nil || res.Error.Kind != ErrSyntax || res.Error.Line != 2 {
		t.Fatalf("got %+v", res.Error)
	}
	if !strings.Contains(res.Report(), "> list.add(2;") {
		t.Fatalf("report should quote the line:\n%s", res.Report())
	}
}

func TestRuntimeErrorAndAssert(t *testing.T) {
	res := Run(context.Background(), listMode(), "list.add(5);\nassert(false, 'need more');\n")
	if res.Error == nil || res.Error.Line != 2 || !strings.Contains(res.Error.Message, "assertion failed: need more") {
		t.Fatalf("got %+v", res.Error)
	}
	res = Run(context.Background(), listMode(), "\n\nlist.add('x');")
	if res.Error == nil || res.Error.Line != 3 || !strings.Contains(res.Error.Message, "n must be a number") {
		t.Fatalf("builder error should point at the program line, got %+v", res.Error)
	}
}

func TestRecursionIsCapped(t *testing.T) {
	res := Run(context.Background(), listMode(), "function f(n){ return f(n+1); }\nf(0);")
	if res.Error == nil || res.Error.Kind != ErrLimit {
		t.Fatalf("got %+v", res.Error)
	}
}

func TestConsoleIsCapturedAndBounded(t *testing.T) {
	res := Run(context.Background(), listMode(), "for (let i = 0; i < 100; i++) console.log('row', i, {i});\nlist.add(10);")
	if !res.OK() {
		t.Fatal(res.Report())
	}
	if res.Logs[0] != `row 0 {"i":0}` {
		t.Fatalf("first log = %q", res.Logs[0])
	}
	if len(res.Logs) != 41 || !strings.Contains(res.Logs[40], "dropped") {
		t.Fatalf("logs not bounded: %d", len(res.Logs))
	}
}

func TestRandomIsRepeatable(t *testing.T) {
	prog := "list.add(Math.random()); list.add(10 - __items[0]);"
	a := Run(context.Background(), listMode(), prog)
	b := Run(context.Background(), listMode(), prog)
	if string(a.Artifact) != string(b.Artifact) {
		t.Fatalf("Math.random differs between runs: %s vs %s", a.Artifact, b.Artifact)
	}
}

func TestValidatorErrorBecomesFeedback(t *testing.T) {
	s := NewSession(listMode(), 3)
	res, err := s.Submit(context.Background(), "list.add(3);\nlist.add(4);")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("sum 7 must fail")
	}
	rep := s.Report(res)
	for _, want := range []string{"attempt 1/3: FAILED (1 error(s)", "the items must sum to 10, they sum to 7", "- sum: 7", "fix the program", "2 attempt(s) left"} {
		if !strings.Contains(rep, want) {
			t.Fatalf("report missing %q:\n%s", want, rep)
		}
	}
}

func TestSessionKeepsBestAndStopsAtBudget(t *testing.T) {
	s := NewSession(listMode(), 3)
	ctx := context.Background()
	r1, _ := s.Submit(ctx, "list.add(9);")     // fails validation, has artifact
	r2, _ := s.Submit(ctx, "list.add(;")       // syntax error
	r3, _ := s.Submit(ctx, "list.add(1, 2);x") // runtime error
	if s.Best() != r1 {
		t.Fatalf("best should stay attempt 1 (artifact, one error); got attempt %d", s.Best().Attempt)
	}
	_ = r2
	_ = r3
	if _, err := s.Submit(ctx, "list.add(10);"); err != ErrAttemptsExhausted {
		t.Fatalf("want ErrAttemptsExhausted, got %v", err)
	}
	if !strings.Contains(s.Report(r3), "no attempts left") {
		t.Fatalf("last report should say no attempts left:\n%s", s.Report(r3))
	}
}

func TestIterateFixesOnSecondTry(t *testing.T) {
	programs := []string{"list.add(4); list.add(4);", "list.add(4); list.add(6);"}
	var feedbacks []string
	author := func(_ context.Context, prev *Result, feedback string) (string, error) {
		feedbacks = append(feedbacks, feedback)
		return programs[len(feedbacks)-1], nil
	}
	s := NewSession(listMode(), 5)
	best, err := Iterate(context.Background(), s, author)
	if err != nil {
		t.Fatal(err)
	}
	if !best.OK() || best.Attempt != 2 || string(best.Artifact) != "[4,6]" {
		t.Fatalf("best = %+v", best)
	}
	if feedbacks[0] != "" || !strings.Contains(feedbacks[1], "they sum to 8") {
		t.Fatalf("second call should carry the first report, got %q", feedbacks[1])
	}
	if len(s.Attempts()) != 2 {
		t.Fatalf("should stop after the passing attempt, ran %d", len(s.Attempts()))
	}
}

func TestDescribeCarriesDocsAndRules(t *testing.T) {
	d := listMode().Describe()
	if !strings.Contains(d, "list.add(n)") || !strings.Contains(d, "Do not paste the program") {
		t.Fatalf("describe:\n%s", d)
	}
	if listMode().ToolName() != "run_list" {
		t.Fatal(listMode().ToolName())
	}
}
