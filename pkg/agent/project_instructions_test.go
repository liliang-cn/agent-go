package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// realDir resolves symlinks so paths compare equal on macOS, where t.TempDir()
// lives under /var -> /private/var.
func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func paths(files []ProjectInstructionFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

// Nearest first, walking up; every file found is kept (concatenation), and
// within one directory the configured name order holds.
func TestProjectInstructionsDiscoveryOrderNearestFirst(t *testing.T) {
	root := realDir(t)
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules")
	writeFile(t, filepath.Join(root, "svc", "AGENTS.md"), "svc rules")
	writeFile(t, filepath.Join(root, "svc", "api", "CLAUDE.md"), "api claude")
	writeFile(t, filepath.Join(root, "svc", "api", "AGENTS.md"), "api agents")

	start := filepath.Join(root, "svc", "api")
	got := paths(discoverProjectInstructions(start, ProjectInstructions{Files: []string{"AGENTS.md", "CLAUDE.md"}}))
	want := []string{
		filepath.Join(root, "svc", "api", "AGENTS.md"),
		filepath.Join(root, "svc", "api", "CLAUDE.md"),
		filepath.Join(root, "svc", "AGENTS.md"),
		filepath.Join(root, "AGENTS.md"),
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("order\n got %v\nwant %v", got, want)
	}

	// Default names: AGENTS.md only.
	got = paths(discoverProjectInstructions(start, ProjectInstructions{}))
	if len(got) != 3 || strings.HasSuffix(got[1], "CLAUDE.md") {
		t.Fatalf("default names should find only AGENTS.md files, got %v", got)
	}

	// Rendered: nearer text comes first, and the precedence rule is stated.
	text := formatProjectInstructions(discoverProjectInstructions(start, ProjectInstructions{}), DefaultProjectInstructionsMaxBytes)
	if !(strings.Index(text, "api agents") < strings.Index(text, "svc rules") && strings.Index(text, "svc rules") < strings.Index(text, "root rules")) {
		t.Fatalf("rendered order is not nearest first:\n%s", text)
	}
	if !strings.Contains(text, "nearer file (listed earlier) takes precedence") {
		t.Fatalf("precedence rule missing:\n%s", text)
	}
	if !strings.Contains(text, "### "+filepath.Join(root, "svc", "AGENTS.md")+"\n") {
		t.Fatalf("each file must be labeled with its path:\n%s", text)
	}
}

// The repository root is the boundary: a file above it belongs to some other
// project. StopDir is a boundary too, and inclusive.
func TestProjectInstructionsBoundary(t *testing.T) {
	outer := realDir(t)
	writeFile(t, filepath.Join(outer, "AGENTS.md"), "outside the repo")
	repo := filepath.Join(outer, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "repo rules")
	writeFile(t, filepath.Join(repo, "pkg", "AGENTS.md"), "pkg rules")

	got := paths(discoverProjectInstructions(filepath.Join(repo, "pkg"), ProjectInstructions{}))
	if len(got) != 2 || got[1] != filepath.Join(repo, "AGENTS.md") {
		t.Fatalf("walk must stop at the repo root, got %v", got)
	}

	got = paths(discoverProjectInstructions(filepath.Join(repo, "pkg"), ProjectInstructions{StopDir: filepath.Join(repo, "pkg")}))
	if len(got) != 1 || got[0] != filepath.Join(repo, "pkg", "AGENTS.md") {
		t.Fatalf("StopDir is the last directory searched, got %v", got)
	}

	// No repo, StopDir set above: reaches it and stops there.
	plain := filepath.Join(outer, "plain", "deep")
	writeFile(t, filepath.Join(plain, "AGENTS.md"), "deep")
	got = paths(discoverProjectInstructions(plain, ProjectInstructions{StopDir: filepath.Join(outer, "plain")}))
	if len(got) != 1 {
		t.Fatalf("StopDir must keep %s out, got %v", filepath.Join(outer, "AGENTS.md"), got)
	}
	got = paths(discoverProjectInstructions(plain, ProjectInstructions{StopDir: outer}))
	if len(got) != 2 || got[1] != filepath.Join(outer, "AGENTS.md") {
		t.Fatalf("StopDir is inclusive, got %v", got)
	}
}

// CLAUDE.md -> AGENTS.md is a common symlink; the text is included once.
func TestProjectInstructionsSymlinkIncludedOnce(t *testing.T) {
	dir := realDir(t)
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "shared")
	if err := os.Symlink("AGENTS.md", filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	got := discoverProjectInstructions(dir, ProjectInstructions{Files: []string{"CLAUDE.md", "AGENTS.md"}})
	if len(got) != 1 {
		t.Fatalf("want one file, got %v", paths(got))
	}
}

// Names are names: a path in the list must not read outside the walk.
func TestProjectInstructionsRejectsPathNames(t *testing.T) {
	names := ProjectInstructions{Files: []string{"../secret", "a/b.md", "..", " ", "AGENTS.md", "AGENTS.md"}}.names()
	if strings.Join(names, ",") != "AGENTS.md" {
		t.Fatalf("got %v", names)
	}
}

// The cap is total, spent nearest first, and every cut is stated.
func TestProjectInstructionsSizeCap(t *testing.T) {
	files := []ProjectInstructionFile{
		{Path: "/p/near/AGENTS.md", Content: strings.Repeat("n", 60)},
		{Path: "/p/mid/AGENTS.md", Content: strings.Repeat("m", 60)},
		{Path: "/p/AGENTS.md", Content: strings.Repeat("r", 60)},
	}
	text := formatProjectInstructions(files, 100)
	if !strings.Contains(text, strings.Repeat("n", 60)) {
		t.Fatal("the nearest file must survive the cap whole")
	}
	if strings.Contains(text, strings.Repeat("m", 41)) || !strings.Contains(text, strings.Repeat("m", 40)) {
		t.Fatalf("the second file should be cut at the remaining 40 bytes:\n%s", text)
	}
	if !strings.Contains(text, "[truncated: showing 40 of 60 bytes of /p/mid/AGENTS.md; size limit of 100 bytes reached]") {
		t.Fatalf("truncation must be visible:\n%s", text)
	}
	if !strings.Contains(text, "not included: /p/AGENTS.md") || strings.Contains(text, "rrr") {
		t.Fatalf("the farthest file must be named as omitted, not included:\n%s", text)
	}

	// A cut never splits a UTF-8 rune.
	cut := formatProjectInstructions([]ProjectInstructionFile{{Path: "/x", Content: "日本語テキスト"}}, 4)
	if !strings.Contains(cut, "\n日\n[truncated: showing 3 of") {
		t.Fatalf("rune boundary not respected:\n%q", cut)
	}
}

// With a sandbox, discovery starts at the sandbox workspace — never the host
// process directory, which the agent's tools cannot reach.
func TestProjectInstructionsStartAtSandboxNotHostCwd(t *testing.T) {
	ws := realDir(t)
	if err := os.Mkdir(filepath.Join(ws, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, "AGENTS.md"), "sandbox project rules")
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(ws))
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	svc := &Service{execSandbox: sb, projectInstructions: &ProjectInstructions{Files: []string{"AGENTS.md", "CLAUDE.md"}}}
	got := svc.projectInstructionsForRun()
	if !strings.Contains(got, "sandbox project rules") {
		t.Fatalf("sandbox AGENTS.md not found:\n%s", got)
	}
	// The host cwd is this package directory inside the agent-go checkout,
	// whose repo root carries a CLAUDE.md. None of it may appear.
	if strings.Contains(got, getCwd()) || strings.Contains(got, "What this project is") {
		t.Fatalf("host cwd leaked into the prompt:\n%s", got)
	}

	// Without a sandbox the process directory is the honest start.
	host := &Service{projectInstructions: &ProjectInstructions{Files: []string{"CLAUDE.md"}}}
	if !strings.Contains(host.projectInstructionsForRun(), "CLAUDE.md") {
		t.Fatal("without a sandbox, discovery should start at the process directory and find the checkout's CLAUDE.md")
	}

	// Off by default.
	if (&Service{execSandbox: sb}).projectInstructionsForRun() != "" {
		t.Fatal("project instructions must be off unless enabled")
	}
}

// editingLLM calls one tool in its first turn and answers in its second, so a
// run has two model rounds; it records every system prompt it was sent.
type editingLLM struct {
	promptCapturingLLM
	mu    sync.Mutex
	calls int
}

func (l *editingLLM) next() *domain.GenerationResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls%2 == 1 {
		return &domain.GenerationResult{
			FinishReason: "tool_calls",
			ToolCalls: []domain.ToolCall{{
				ID: "call_edit", Type: "function",
				Function: domain.FunctionCall{Name: "edit_agents_md", Arguments: map[string]interface{}{}},
			}},
		}
	}
	return &domain.GenerationResult{Content: "Done.", FinishReason: "stop"}
}

func (l *editingLLM) GenerateWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.capture(messages)
	return l.next(), nil
}

func (l *editingLLM) StreamWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	l.capture(messages)
	return cb(l.next())
}

func staticPrefix(system string) string {
	if i := strings.Index(system, SystemPromptDynamicBoundary); i >= 0 {
		return system[:i]
	}
	return system
}

// The prompt-cache contract: two rounds of one run send an identical system
// message, even when the agent edits AGENTS.md between them (the run holds
// the snapshot it took at start); two runs over unchanged files send an
// identical static prefix; and an edit shows up in the next run.
func TestProjectInstructionsByteStableAcrossRoundsAndRuns(t *testing.T) {
	startHour := time.Now().Hour()
	ws := realDir(t)
	if err := os.Mkdir(filepath.Join(ws, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(ws, "AGENTS.md")
	writeFile(t, agentsPath, "Run go test ./... before finishing.")
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(ws))
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	llm := &editingLLM{}
	svc, err := New("project-instructions-stable").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithSandbox(sb).
		WithProjectInstructions(ProjectInstructions{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	edits := 0
	svc.RegisterTool(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "edit_agents_md",
			Description: "rewrites AGENTS.md",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}, func(context.Context, map[string]interface{}) (interface{}, error) {
		edits++
		// Rewritten with the same bytes on the first run, so the second run's
		// prefix can be compared with the first; changed on the third.
		content := "Run go test ./... before finishing."
		if edits >= 3 {
			content = "Run go vet too."
		}
		return "ok", os.WriteFile(agentsPath, []byte(content), 0o644)
	})

	runOnce(t, svc, "first run")
	first := llm.systemPrompts()
	if len(first) < 2 {
		t.Fatalf("want two model rounds, got %d", len(first))
	}
	for i := 1; i < len(first); i++ {
		if first[i] != first[0] {
			t.Fatalf("round %d system message differs from round 1 (%d vs %d bytes)", i+1, len(first[i]), len(first[0]))
		}
	}
	if !strings.Contains(first[0], "Run go test ./... before finishing.") || !strings.Contains(first[0], "### "+agentsPath) {
		t.Fatalf("AGENTS.md missing from the system prompt:\n%s", first[0])
	}
	if strings.Index(first[0], "## Project instructions") > strings.Index(first[0], SystemPromptDynamicBoundary) {
		t.Fatal("project instructions must sit in the static prefix, before the dynamic boundary")
	}

	runOnce(t, svc, "second run")
	second := llm.systemPrompts()[len(first):]
	if len(second) < 2 {
		t.Fatalf("want two rounds in run 2, got %d", len(second))
	}
	if time.Now().Hour() != startHour {
		t.Skip("the hour turned mid-test; the Date/Time line legitimately changed")
	}
	if staticPrefix(second[0]) != staticPrefix(first[0]) {
		t.Fatalf("static prefix differs across runs over unchanged files:\n--- run1\n%s\n--- run2\n%s", staticPrefix(first[0]), staticPrefix(second[0]))
	}
	t.Logf("static prefix: %d bytes, identical across 2 rounds x 2 runs", len(staticPrefix(first[0])))

	// Run 3's tool call changes the file mid-run: run 3 still sees the old
	// text in both rounds, run 4 sees the edit.
	runOnce(t, svc, "third run")
	all := llm.systemPrompts()
	third := all[len(first)+len(second):]
	if third[len(third)-1] != third[0] || strings.Contains(third[len(third)-1], "Run go vet too.") {
		t.Fatal("a mid-run edit changed this run's prompt")
	}
	runOnce(t, svc, "fourth run")
	all = llm.systemPrompts()
	if !strings.Contains(all[len(all)-1], "Run go vet too.") {
		t.Fatal("the next run must see the edited AGENTS.md")
	}
}
