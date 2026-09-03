package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Everything here runs against a shell script pretending to be `claude`.
// A real one costs money, needs a login, and is not installed on a build
// machine — and the behaviours worth pinning down (an is_error result that
// exits zero, a run that has to be killed) are ones a real CLI would not
// reproduce on demand anyway.

// claudeStreamJSON is one turn of Claude Code's stream-json: the init frame,
// an assistant message, and the result frame that carries the verdict and the
// authoritative usage.
func claudeStreamJSON(isError bool, answer string) string {
	errFlag := "false"
	if isError {
		errFlag = "true"
	}
	return `{"type":"system","subtype":"init","session_id":"sess-abc","tools":[]}
{"type":"assistant","message":{"model":"claude-fake-1","content":[{"type":"text","text":"` + answer + `"}],"usage":{"input_tokens":3,"output_tokens":2}},"session_id":"sess-abc"}
{"type":"result","subtype":"success","is_error":` + errFlag + `,"result":"` + answer + `","session_id":"sess-abc","total_cost_usd":0.0125,"usage":{"input_tokens":120,"output_tokens":34,"cache_read_input_tokens":7,"cache_creation_input_tokens":3}}
`
}

// fakeCLIService registers the tools against a script that emits body and then
// exits with code. PATH is cut down to the system directories so discovery
// cannot reach a real agent CLI — those install under ~/.local/bin or a
// package manager's prefix — while the fake script can still find `cat`.
func fakeCLIService(t *testing.T, body string, exitCode int) (*Service, string) {
	t.Helper()
	t.Setenv("PATH", "/usr/bin:/bin")

	dir := t.TempDir()
	// t.TempDir is under /var on macOS, which is a symlink to /private/var —
	// the exact mismatch resolveCLIAgentRoots exists to flatten.
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	script := filepath.Join(dir, "claude")
	source := "#!/bin/sh\ncat <<'AGENTGO_EOF'\n" + body + "AGENTGO_EOF\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(script, []byte(source), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	svc := &Service{toolRegistry: NewToolRegistry()}
	if err := RegisterCLIAgentTools(svc, CLIAgentConfig{
		Agents:       []string{"claude"},
		Binaries:     map[string]string{"claude": script},
		AllowedRoots: []string{root},
	}); err != nil {
		t.Fatalf("RegisterCLIAgentTools: %v", err)
	}
	return svc, root
}

// cliRunObserver records the sub-agent bracket so the accounting side can be
// asserted without a live observer implementation.
type cliRunObserver struct {
	BaseObserver
	started []SubAgentInfo
	ended   []SubAgentInfo
	results []any
	errs    []error
}

func (o *cliRunObserver) OnSubAgentStart(_ context.Context, info SubAgentInfo) {
	o.started = append(o.started, info)
}

func (o *cliRunObserver) OnSubAgentEnd(_ context.Context, info SubAgentInfo, result any, err error) {
	o.ended = append(o.ended, info)
	o.results = append(o.results, result)
	o.errs = append(o.errs, err)
}

func TestRegisterCLIAgentToolsRegistersBothTools(t *testing.T) {
	svc, _ := fakeCLIService(t, claudeStreamJSON(false, "ok"), 0)
	for _, name := range []string{"cli_agent_list", "cli_agent_run"} {
		if !svc.toolRegistry.Has(name) {
			t.Errorf("expected %q to be registered", name)
		}
	}
}

func TestRegisterCLIAgentToolsNeedsSomewhereToRun(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	svc := &Service{toolRegistry: NewToolRegistry()}
	err := RegisterCLIAgentTools(svc, CLIAgentConfig{})
	if err == nil {
		t.Fatal("expected an error when there is no workspace and no allowed roots")
	}
	if svc.toolRegistry.Has("cli_agent_run") {
		t.Error("a failed registration must not leave the run tool behind")
	}
}

func TestCLIAgentListReportsTheOverriddenBinary(t *testing.T) {
	svc, _ := fakeCLIService(t, claudeStreamJSON(false, "ok"), 0)
	data := mustOK(t, "cli_agent_list", callTool(t, svc, "cli_agent_list", nil))
	agents, _ := data["agents"].([]map[string]interface{})
	if len(agents) != 1 {
		t.Fatalf("expected exactly the fake claude, got %+v", data)
	}
	if agents[0]["name"] != "claude" {
		t.Errorf("name = %v, want claude", agents[0]["name"])
	}
	if note, _ := data["note"].(string); !strings.Contains(note, "logged in") {
		t.Errorf("the list must say installed is not logged in, got %q", note)
	}
}

func TestCLIAgentRunReportsSummaryUsageAndSession(t *testing.T) {
	svc, root := fakeCLIService(t, claudeStreamJSON(false, "the fake answered"), 0)
	obs := &cliRunObserver{}
	svc.RegisterObserver(obs)

	var streamed []*Event
	ctx := withEventSink(context.Background(), func(e *Event) { streamed = append(streamed, e) })
	res, err := svc.toolRegistry.Call(ctx, "cli_agent_run", map[string]interface{}{
		"agent":  "claude",
		"prompt": "say something",
		"cwd":    root,
	})
	if err != nil {
		t.Fatalf("cli_agent_run returned an error: %v", err)
	}
	out, _ := res.(map[string]interface{})
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("expected ok:true, got %+v", out)
	}
	if failed, _ := out["failed"].(bool); failed {
		t.Errorf("expected failed:false, got %+v", out)
	}
	if out["summary"] != "the fake answered" {
		t.Errorf("summary = %v, want the result frame's text", out["summary"])
	}
	if out["session_id"] != "sess-abc" {
		t.Errorf("session_id = %v, want sess-abc", out["session_id"])
	}
	usage, _ := out["usage"].(map[string]interface{})
	if usage["input"] != int64(120) || usage["output"] != int64(34) || usage["cache"] != int64(10) {
		t.Errorf("usage = %+v, want the result frame's totals, not the assistant frame's", usage)
	}
	if cost, _ := usage["cost_usd"].(float64); cost != 0.0125 {
		t.Errorf("cost_usd = %v, want 0.0125", cost)
	}

	// The delegated agent's text has to reach the parent's event stream, or a
	// UI watching the parent sees nothing at all while it works.
	var sawText bool
	for _, e := range streamed {
		if e.Type == EventTypePartial && strings.Contains(e.Content, "the fake answered") {
			sawText = true
		}
	}
	if !sawText {
		t.Errorf("expected the assistant text to be forwarded, got %+v", streamed)
	}

	if len(obs.started) != 1 || len(obs.ended) != 1 {
		t.Fatalf("expected one sub-agent bracket, got %d start / %d end", len(obs.started), len(obs.ended))
	}
	if obs.started[0].Kind != "cli" || obs.started[0].Provider != "claude" {
		t.Errorf("SubAgentInfo = %+v, want Kind cli and Provider claude", obs.started[0])
	}
	if obs.errs[0] != nil {
		t.Errorf("expected no error on a clean run, got %v", obs.errs[0])
	}
	accounting, ok := obs.results[0].(CLIAgentRunResult)
	if !ok {
		t.Fatalf("OnSubAgentEnd result = %T, want CLIAgentRunResult", obs.results[0])
	}
	if accounting.Input != 120 || accounting.Output != 34 || accounting.CostUSD != 0.0125 {
		t.Errorf("observer usage = %+v, want the delegated run's own tokens", accounting)
	}
	if accounting.Model != "claude-fake-1" {
		t.Errorf("observer model = %q, want claude-fake-1", accounting.Model)
	}
}

// The one that matters most: a revoked login writes an assistant message, sets
// is_error, and exits zero. Reading the exit code alone launders an
// authentication failure into an answer.
func TestCLIAgentRunTreatsIsErrorAsFailureDespiteExitZero(t *testing.T) {
	svc, root := fakeCLIService(t, claudeStreamJSON(true, "Failed to authenticate"), 0)
	obs := &cliRunObserver{}
	svc.RegisterObserver(obs)

	out := callTool(t, svc, "cli_agent_run", map[string]interface{}{
		"agent":  "claude",
		"prompt": "say something",
		"cwd":    root,
	})
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("expected ok:false for an is_error result, got %+v", out)
	}
	if failed, _ := out["failed"].(bool); !failed {
		t.Errorf("expected failed:true, got %+v", out)
	}
	if code, _ := out["exit_code"].(int); code != 0 {
		t.Errorf("exit_code = %v, want the honest 0 the CLI actually returned", out["exit_code"])
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "Failed to authenticate") {
		t.Errorf("error = %q, want the CLI's own words", msg)
	}
	if len(obs.errs) != 1 || obs.errs[0] == nil {
		t.Errorf("the observer must see the failure too, got %+v", obs.errs)
	}
}

func TestCLIAgentRunRefusesACwdOutsideTheAllowedRoots(t *testing.T) {
	svc, _ := fakeCLIService(t, claudeStreamJSON(false, "ok"), 0)
	outside := t.TempDir()

	out := callTool(t, svc, "cli_agent_run", map[string]interface{}{
		"agent":  "claude",
		"prompt": "say something",
		"cwd":    outside,
	})
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("expected the run to be refused, got %+v", out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "outside the allowed roots") {
		t.Errorf("error = %q, want it to name the reason", msg)
	}
}

func TestCLIAgentRunRefusesAnUnknownAgent(t *testing.T) {
	svc, _ := fakeCLIService(t, claudeStreamJSON(false, "ok"), 0)
	out := callTool(t, svc, "cli_agent_run", map[string]interface{}{
		"agent":  "codex",
		"prompt": "say something",
	})
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("expected an unknown agent to be refused, got %+v", out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "cli_agent_list") {
		t.Errorf("error = %q, want it to point at the list tool", msg)
	}
}

// A timeout has to be reported as a timeout — from the exit code alone a
// killed run is indistinguishable from a crash — and the process has to be
// gone afterwards. pty.Run signals the whole process group for exactly this:
// a leaked `claude` keeps talking to a model nobody is listening to, and keeps
// billing for it.
func TestCLIAgentRunTimesOutAndKillsTheProcess(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	dir := t.TempDir()
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	pidFile := filepath.Join(root, "pid")
	script := filepath.Join(dir, "claude")
	// The --version arm keeps discovery's own probe from starting a 30-second
	// sleep it would then have to outwait.
	source := "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0; exit 0;; esac\necho $$ > " + pidFile + "\nsleep 30\n"
	if err := os.WriteFile(script, []byte(source), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	svc := &Service{toolRegistry: NewToolRegistry()}
	if err := RegisterCLIAgentTools(svc, CLIAgentConfig{
		Agents:       []string{"claude"},
		Binaries:     map[string]string{"claude": script},
		AllowedRoots: []string{root},
	}); err != nil {
		t.Fatalf("RegisterCLIAgentTools: %v", err)
	}

	started := time.Now()
	out := callTool(t, svc, "cli_agent_run", map[string]interface{}{
		"agent":           "claude",
		"prompt":          "take your time",
		"timeout_seconds": 1,
	})
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("the timeout did not bound the run: took %s", elapsed)
	}
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("expected a timed-out run to fail, got %+v", out)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "did not finish within") {
		t.Errorf("error = %q, want it to say the run timed out", msg)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the fake never recorded its pid: %v", err)
	}
	pid := 0
	for _, c := range strings.TrimSpace(string(raw)) {
		if c < '0' || c > '9' {
			t.Fatalf("unexpected pid file contents %q", raw)
		}
		pid = pid*10 + int(c-'0')
	}
	if processAlive(t, pid) {
		t.Errorf("process %d survived the timeout", pid)
	}
}

// processAlive polls briefly: the child is signalled and reaped before Run
// returns, but a shell that forked leaves the reap racing with this check.
func processAlive(t *testing.T, pid int) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
}
