package trace

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// A step that returned normally and still failed says so on its line, with
// the reason; and the run's last line says how the run ended.
func TestAFailedStepAndABlockedRunSayWhyOnTheirLines(t *testing.T) {
	var lineBuf bytes.Buffer
	SetEnabled(true)
	SetOutput(&lineBuf)
	t.Cleanup(func() { SetEnabled(false); SetLines(false) })

	ctx := WithRun(context.Background(), "run-outcome-1")
	StepOutcome(ctx, "tool", "bash")(nil, true, "exit=1 RESULT: 2 problem(s)")
	StepOutcome(ctx, "tool", "fetch")(errors.New("dial tcp: refused"), false, "")
	StepOutcome(ctx, "tool", "bash")(nil, false, "")
	StepOutcome(ctx, "tool", "silent")(nil, true, "")
	EndWith(ctx, "blocked", "no way to render the file")

	out := lineBuf.String()
	for _, want := range []string{
		`tool bash dur=0.00s fail="exit=1 RESULT: 2 problem(s)"`,
		`tool fetch dur=0.00s err="dial tcp: refused"`,
		`tool silent dur=0.00s fail="reported failure, with no reason given"`,
		`run end total=`,
		`outcome=blocked`,
		`tool_failures=3`,
		`reason="no way to render the file"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("trace lines are missing %q:\n%s", want, out)
		}
	}
	// The step that worked is as short as it ever was.
	if !strings.Contains(out, "tool bash dur=0.00s\n") {
		t.Errorf("a successful step grew a failure note:\n%s", out)
	}

	_, sum, _ := Timeline(ctx)
	if sum.ToolFailures != 3 || sum.ToolCalls != 4 {
		t.Errorf("tally: %d failures of %d calls, want 3 of 4", sum.ToolFailures, sum.ToolCalls)
	}

	// A completed run carries no reason.
	lineBuf.Reset()
	ctx = WithRun(context.Background(), "run-outcome-2")
	EndWith(ctx, "completed", "")
	if got := lineBuf.String(); !strings.Contains(got, "outcome=completed") || strings.Contains(got, "reason=") {
		t.Errorf("completed run line: %s", got)
	}
}

// What is written to a log is masked first: the end of a failing command's
// output is the likeliest place in a run for a credential to be printed.
func TestRedactMasksWhatLooksLikeACredential(t *testing.T) {
	// Put together from pieces, so that this file does not itself look, to a
	// scanner reading the repository, like one that holds credentials.
	dash := strings.Repeat("-", 5)
	secrets := []string{
		"s" + "k-proj-abcdEFGH1234ijklMNOP5678",
		"gh" + "p_abcdefghijklmnopqrstuvwxyz0123456789",
		"AK" + "IAIOSFODNN7EXAMPLE",
		"hunter2secret",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9abc123def456",
		"s3cr3tpassw0rd",
	}
	text := "curl failed: Authorization: Bear" + "er " + secrets[0] +
		" token=" + secrets[1] + " aws " + secrets[2] +
		` {"api_key": "` + secrets[3] + `"} jwt ` + secrets[4] +
		" postgres://app:" + secrets[5] + "@db.internal:5432/main " +
		dash + "BEGIN PRIVATE KEY" + dash + "\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n" + dash + "END PRIVATE KEY" + dash
	got := Redact(text)
	for _, s := range secrets {
		if strings.Contains(got, s) {
			t.Errorf("%q survived redaction: %s", s, got)
		}
	}
	if strings.Contains(got, "MIIEvQ") {
		t.Errorf("a key block survived: %s", got)
	}
	// What is left still says what happened and where.
	for _, keep := range []string{"curl failed", "db.internal:5432/main", "postgres://app:"} {
		if !strings.Contains(got, keep) {
			t.Errorf("redaction ate %q: %s", keep, got)
		}
	}

	// Ordinary failure text passes through untouched.
	for _, plain := range []string{
		"exit=1 Traceback (most recent call last): KeyError: 'total'",
		"PROBLEM: Sheet1!C14 =SUM(C2:C13) evaluates to #DIV/0! | RESULT: 1 problem(s)",
		"open /Users/someone/Documents/reports/2026/quarterly-summary-final-v2.xlsx: no such file",
		"页面 3 缺少标题 | RESULT: 2 problem(s)",
		"commit 3f2a91c0 not found",
	} {
		if got := Redact(plain); got != plain {
			t.Errorf("plain text was changed:\n  in:  %s\n  out: %s", plain, got)
		}
	}
}

func TestTailKeepsTheEndWhereTheFailureIs(t *testing.T) {
	out := "step 1 ok\nstep 2 ok\n\nTraceback (most recent call last):\n  File \"x.py\", line 3\nValueError: bad row 7\n\n"
	if got := Tail(out, 2); got != `File "x.py", line 3 | ValueError: bad row 7` {
		t.Errorf("tail: %q", got)
	}
	long := strings.Repeat("word ", 200) + "the real error"
	got := Tail(long, 1)
	if !strings.HasSuffix(got, "the real error") || len([]rune(got)) > maxNote+1 {
		t.Errorf("a long tail must keep its end and stay short: %d runes, %q", len([]rune(got)), got[len(got)-30:])
	}
	if Tail("", 3) != "" {
		t.Error("empty output has no tail")
	}
	if n := Note(strings.Repeat("x y ", 200)); len([]rune(n)) > maxNote+1 {
		t.Errorf("note not cut: %d", len([]rune(n)))
	}
}
