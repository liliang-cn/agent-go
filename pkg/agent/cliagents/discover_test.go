package cliagents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agentcli/cliagent"
)

// writeFakeCLI drops an executable script under dir and returns its path. The
// whole suite runs against these: a real `claude` costs money, needs a login,
// and is not installed on a build machine, so nothing here may touch one.
func writeFakeCLI(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return path
}

// isolatePATH empties PATH for the test, so Discover sees only what the test
// put in front of it. Without this the suite's results depend on which agent
// CLIs the developer happens to have installed.
func isolatePATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", "")
}

func TestDiscoverUsesBinaryOverrideAndReadsVersion(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	binary := writeFakeCLI(t, dir, "claude", "#!/bin/sh\necho '2.1.3 (Claude Code)'\n")

	agents := Discover(map[string]string{"claude": binary})
	if len(agents) != 1 {
		t.Fatalf("expected only the overridden agent, got %+v", agents)
	}
	got := agents[0]
	if got.Name != "claude" {
		t.Errorf("name = %q, want claude", got.Name)
	}
	if got.Binary != binary {
		t.Errorf("binary = %q, want %q", got.Binary, binary)
	}
	if got.Version != "2.1.3 (Claude Code)" {
		t.Errorf("version = %q, want the first line of --version output", got.Version)
	}
	if !got.Streaming || !got.Resume {
		t.Errorf("claude should report streaming and resume, got %+v", got)
	}
}

func TestDiscoverSkipsAgentsThatAreNotInstalled(t *testing.T) {
	isolatePATH(t)
	if agents := Discover(nil); len(agents) != 0 {
		t.Fatalf("expected nothing on an empty PATH, got %+v", agents)
	}
	// A stale override pointing at nothing must drop the agent rather than
	// list one whose Binary cannot be executed.
	missing := filepath.Join(t.TempDir(), "not-there")
	if agents := Discover(map[string]string{"claude": missing}); len(agents) != 0 {
		t.Fatalf("expected a missing override to drop the agent, got %+v", agents)
	}
}

func TestDiscoverVersionStaysEmptyWhenTheCLIRefuses(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	binary := writeFakeCLI(t, dir, "gemini", "#!/bin/sh\necho 'not logged in' >&2\nexit 1\n")

	agents := Discover(map[string]string{"gemini": binary})
	if len(agents) != 1 {
		t.Fatalf("expected the agent to be listed anyway, got %+v", agents)
	}
	if agents[0].Version != "" {
		t.Errorf("version = %q, want empty when --version failed", agents[0].Version)
	}
	if agents[0].Resume {
		t.Error("gemini has no session id in its stream, so it must not claim resume")
	}
}

func TestDiscoverPlacesAnAliasOntoItsRealDialect(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	binary := writeFakeCLI(t, dir, "claude-work", "#!/bin/sh\necho 9.9.9\n")

	agents := Discover(map[string]string{"claude-work": binary})
	if len(agents) != 1 {
		t.Fatalf("expected the alias to be listed, got %+v", agents)
	}
	if !agents[0].Streaming {
		t.Error("an alias of claude should inherit claude's traits")
	}

	// A name that matches no known CLI is dropped: Registry could not build a
	// runner for it, and listing an agent nothing can run is a promise broken
	// one call later.
	other := writeFakeCLI(t, dir, "wibble", "#!/bin/sh\nexit 0\n")
	if agents := Discover(map[string]string{"wibble": other}); len(agents) != 0 {
		t.Fatalf("expected an unplaceable name to be dropped, got %+v", agents)
	}
}

func TestRegistryBuildsARunnerForEveryDiscoveredAgent(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	overrides := map[string]string{
		"claude":       writeFakeCLI(t, dir, "claude", "#!/bin/sh\nexit 0\n"),
		"codex":        writeFakeCLI(t, dir, "codex", "#!/bin/sh\nexit 0\n"),
		"gemini":       writeFakeCLI(t, dir, "gemini", "#!/bin/sh\nexit 0\n"),
		"cursor-agent": writeFakeCLI(t, dir, "cursor-agent", "#!/bin/sh\nexit 0\n"),
	}
	agents := Discover(overrides)
	reg := Registry(agents)

	for name, binary := range overrides {
		provider, err := reg.Get(name)
		if err != nil {
			t.Fatalf("registry has no provider for %s: %v", name, err)
		}
		spec, err := provider.NewSession().BuildCommand(context.Background(), cliagent.Request{
			Prompt:        "say OK",
			WorkspacePath: dir,
		})
		if err != nil {
			t.Fatalf("%s BuildCommand: %v", name, err)
		}
		if spec.Argv[0] != binary {
			t.Errorf("%s argv[0] = %q, want the overridden binary %q", name, spec.Argv[0], binary)
		}
		// gemini passes the prompt through --prompt rather than as the last
		// operand, so this asserts only that it made it into the command.
		if !containsArg(spec.Argv, "say OK") {
			t.Errorf("%s argv does not carry the prompt: %v", name, spec.Argv)
		}
	}
}

// The headless flags are the difference between a run and a refusal: in a
// scratch directory that is not a trusted git repo, codex and gemini both stop
// and ask. Request.Sandbox's zero value is what emits them, so this asserts we
// are relying on it rather than quietly not passing it.
func TestZeroSandboxEmitsTheBypassFlags(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	overrides := map[string]string{
		"codex":        writeFakeCLI(t, dir, "codex", "#!/bin/sh\nexit 0\n"),
		"gemini":       writeFakeCLI(t, dir, "gemini", "#!/bin/sh\nexit 0\n"),
		"cursor-agent": writeFakeCLI(t, dir, "cursor-agent", "#!/bin/sh\nexit 0\n"),
	}
	reg := Registry(Discover(overrides))

	want := map[string]string{
		"codex":        "--skip-git-repo-check",
		"gemini":       "--skip-trust",
		"cursor-agent": "--trust",
	}
	for name, flag := range want {
		provider, err := reg.Get(name)
		if err != nil {
			t.Fatalf("registry has no provider for %s: %v", name, err)
		}
		spec, err := provider.NewSession().BuildCommand(context.Background(), cliagent.Request{
			Prompt:        "say OK",
			WorkspacePath: dir,
		})
		if err != nil {
			t.Fatalf("%s BuildCommand: %v", name, err)
		}
		if !containsArg(spec.Argv, flag) {
			t.Errorf("%s argv is missing %s: %v", name, flag, spec.Argv)
		}
	}
}

func TestCursorSessionParsesTheClaudeDialect(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	binary := writeFakeCLI(t, dir, "cursor-agent", "#!/bin/sh\nexit 0\n")
	reg := Registry(Discover(map[string]string{"cursor-agent": binary}))

	provider, err := reg.Get("cursor-agent")
	if err != nil {
		t.Fatalf("no cursor-agent provider: %v", err)
	}
	session := provider.NewSession()

	// The point of borrowing claude's session: cursor-agent's stream-json is
	// the same dialect, so the answer is the assistant-role message and the
	// is_error verdict survives an exit code of zero.
	frames := `{"type":"system","subtype":"init","session_id":"chat-7"}
{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]},"session_id":"chat-7"}
{"type":"result","subtype":"success","is_error":true,"result":"quota exceeded","session_id":"chat-7"}
`
	events, err := session.ParseChunk([]byte(frames))
	if err != nil {
		t.Fatalf("ParseChunk: %v", err)
	}
	var sawAssistant bool
	for _, e := range events {
		if e.Type == cliagent.EventAgentMessage && e.Payload["role"] == "assistant" {
			sawAssistant = true
		}
	}
	if !sawAssistant {
		t.Errorf("expected an assistant message among %+v", events)
	}
	res, _, err := session.Finalize(context.Background(), nil, 0)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if !res.Failed {
		t.Error("is_error true must be a failure even with exit code 0")
	}
	if session.SessionID() != "chat-7" {
		t.Errorf("session id = %q, want chat-7", session.SessionID())
	}
}

func containsArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}
