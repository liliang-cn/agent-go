package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// A tool says it failed in its result far more often than by returning an
// error. Whatever the tool, the few ways of saying so are read the same.
func TestToolOutcomeReadsTheResultsOwnVerdict(t *testing.T) {
	t.Parallel()

	type mcpResult struct {
		IsError bool                     `json:"is_error"`
		Content []map[string]interface{} `json:"content"`
	}
	cases := []struct {
		name   string
		res    interface{}
		failed bool
		want   []string
	}{
		{"shell command that exited non-zero", map[string]interface{}{
			"ok": false, "error": "command exited 1: boom",
			"data": map[string]interface{}{"exit_code": 1, "stdout": "checking\nPROBLEM: B7 is #REF!\nRESULT: 1 problem(s)\n", "stderr": ""},
		}, true, []string{"exit=1", "command exited 1: boom"}},
		{"exit status with stderr", map[string]interface{}{
			"exit_code": 2, "stderr": "Traceback\nKeyError: 'total'\n", "stdout": "",
		}, true, []string{"exit=2", "KeyError: 'total'"}},
		{"failure whose only words are its output", map[string]interface{}{
			"exit_code": 1, "stdout": "ran 3 checks\nPROBLEM: page 2 is blank\nRESULT: 1 problem(s)\n",
		}, true, []string{"exit=1", "PROBLEM: page 2 is blank | RESULT: 1 problem(s)"}},
		{"ok false", map[string]interface{}{"ok": false, "error": "no such note"}, true, []string{"no such note"}},
		{"typed result with is_error", mcpResult{IsError: true, Content: []map[string]interface{}{{"type": "text", "text": "rate limited"}}}, true, []string{"rate limited"}},
		{"JSON in a string", `{"success": false, "message": "quota exceeded"}`, true, []string{"quota exceeded"}},
		{"http status", map[string]interface{}{"status_code": 503, "content": "upstream down"}, true, []string{"exit=503", "upstream down"}},
		{"timed out", map[string]interface{}{"timed_out": true, "stdout": "still working"}, true, []string{"timed out"}},

		{"success", map[string]interface{}{"ok": true, "data": map[string]interface{}{"exit_code": 0, "stdout": "fine", "stderr": "a warning"}}, false, nil},
		{"http ok", map[string]interface{}{"status_code": 200, "content": "page"}, false, nil},
		{"plain text, even text about errors", "Error handling is described in chapter 4.", false, nil},
		{"nothing", nil, false, nil},
		{"a number", 42, false, nil},
	}
	for _, c := range cases {
		failed, note := toolOutcome(c.res)
		if failed != c.failed {
			t.Errorf("%s: failed=%v, want %v (note %q)", c.name, failed, c.failed, note)
		}
		for _, w := range c.want {
			if !strings.Contains(note, w) {
				t.Errorf("%s: note %q is missing %q", c.name, note, w)
			}
		}
		if !c.failed && note != "" {
			t.Errorf("%s: a call that did not fail has a note: %q", c.name, note)
		}
	}

	// A secret printed by the failing command does not reach the note.
	_, note := toolOutcome(map[string]interface{}{"exit_code": 1, "stderr": "401 for Authorization: Bear" + "er s" + "k-live-abcdEFGH1234ijklMNOP"})
	if strings.Contains(note, "abcdEFGH1234") {
		t.Errorf("a credential reached the trace note: %s", note)
	}
}

// The whole point: a run that went wrong can be diagnosed from its trace
// lines. A command fails, the run gives up, and the lines say both — which
// tool, with what exit status and last words, and what the run's blocker was.
func TestAFailedRunIsDiagnosableFromItsTraceLines(t *testing.T) {
	var lines bytes.Buffer
	trace.SetEnabled(true)
	trace.SetOutput(&lines)
	t.Cleanup(func() { trace.SetEnabled(false); trace.SetLines(false) })

	llm := &turnScriptLLM{turns: []*domain.GenerationResult{
		turnCalling("c1", "bash", map[string]interface{}{"command": "python3 build.py"}),
		turnBlocked("b", "The workbook could not be built: the script fails on row 7."),
	}}
	svc, err := New("traced-failure").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.AddTool("bash", "Run a shell command", map[string]interface{}{"type": "object"},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"ok": false, "error": "command exited 1: ValueError: bad row 7",
				"data": map[string]interface{}{"exit_code": 1, "stderr": "Traceback (most recent call last):\nValueError: bad row 7\n"}}, nil
		})

	res, err := svc.Run(context.Background(), "Build the workbook.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Blocked {
		t.Fatalf("want blocked, got %q", res.Text())
	}
	out := lines.String()
	for _, want := range []string{
		`tool bash dur=`,
		`fail="exit=1 Traceback (most recent call last): | ValueError: bad row 7"`,
		`run end total=`,
		`outcome=blocked`,
		`tool_failures=1`,
		`reason="The workbook could not be built: the script fails on row 7."`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the trace does not show %q:\n%s", want, out)
		}
	}
}

// The runtime's own interventions are on the trace with their reason.
func TestPushbacksAreOnTheTrace(t *testing.T) {
	var lines bytes.Buffer
	trace.SetEnabled(true)
	trace.SetOutput(&lines)
	t.Cleanup(func() { trace.SetEnabled(false); trace.SetLines(false) })

	llm := &turnScriptLLM{turns: []*domain.GenerationResult{
		turnBlocked("b", "I have no tool for that."),
	}}
	svc, err := New("traced-pushback").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.AddTool("bash", "Run a shell command", map[string]interface{}{"type": "object"},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) { return "ok", nil })

	if _, err := svc.Run(context.Background(), "Make a workbook."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := lines.String()
	for _, want := range []string{"decision untried_block", `"pushback":1`, "outcome=blocked", `reason="I have no tool for that."`} {
		if !strings.Contains(out, want) {
			t.Errorf("the trace does not show %q:\n%s", want, out)
		}
	}
}
