package plugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
	"github.com/liliang-cn/agent-go/v3/pkg/plugin"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func manifest(name string) string {
	return `{"name":"` + name + `","version":"1.0.0","description":"d"}`
}

// fullPlugin writes a plugin carrying every part.
func fullPlugin(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	write(t, filepath.Join(dir, "plugin.json"), manifest(name))
	write(t, filepath.Join(dir, "skills", "greet", "SKILL.md"),
		"---\nname: greet\ndescription: say hello\n---\nSay hello.\n")
	write(t, filepath.Join(dir, "mcp.json"), `{"mcpServers":{}}`)
	write(t, filepath.Join(dir, "agents", "reviewer.md"),
		"---\nname: reviewer\ndescription: reviews code\ntools: [fs_read]\nmax_turns: 4\n---\nYou review code.\n")
	write(t, filepath.Join(dir, "prompt.md"), "PLUGIN-PROMPT-MARKER\n")
	write(t, filepath.Join(dir, "extension.json"), `{"command":["./bin/x"]}`)
	return dir
}

func TestDiscoverSortsAndRejectsDuplicates(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, n := range []string{"zeta", "alpha"} {
		write(t, filepath.Join(a, n, "plugin.json"), manifest(n))
	}
	write(t, filepath.Join(a, "not-a-plugin", "readme.txt"), "x")
	got, err := plugin.Discover(a, filepath.Join(a, "zeta"+"-missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("want [alpha zeta], got %+v", got)
	}
	write(t, filepath.Join(b, "plugin.json"), manifest("alpha"))
	if _, err := plugin.Discover(a, b); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate name must fail, got %v", err)
	}
}

func TestBadManifestFails(t *testing.T) {
	for name, body := range map[string]string{
		"bad name": `{"name":"Has Space"}`,
		"colon":    `{"name":"a:b"}`,
		"not json": `{`,
	} {
		d := t.TempDir()
		write(t, filepath.Join(d, "plugin.json"), body)
		if _, err := plugin.Load(d); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestBadAgentFails(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "p", "plugin.json"), manifest("p"))
	write(t, filepath.Join(root, "p", "agents", "a.md"), "no frontmatter")
	err := plugin.Install(agent.New("x"), plugin.Dirs(root), plugin.NoDefaultDir())
	if err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Fatalf("want frontmatter error, got %v", err)
	}
}

func build(t *testing.T, opts ...plugin.Option) (*agent.Service, *extensiontest.ScriptedLLM) {
	t.Helper()
	llm := extensiontest.Script(extensiontest.Answer("done"))
	b := agent.New("host").WithLLM(llm)
	opts = append(opts, plugin.NoDefaultDir())
	if err := plugin.Install(b, opts...); err != nil {
		t.Fatal(err)
	}
	svc, err := extensiontest.NewServiceWithBuilder(b, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc, llm
}

func TestInstallWiresPartsAndExecIsOffByDefault(t *testing.T) {
	root := t.TempDir()
	fullPlugin(t, root, "kit")
	svc, llm := build(t, plugin.Dirs(root))

	infos := svc.Plugins()
	if len(infos) != 1 {
		t.Fatalf("want 1 plugin, got %+v", infos)
	}
	got := strings.Join(infos[0].Components, ",")
	if got != "skills,mcp,agents,prompt" {
		t.Fatalf("components = %q", got)
	}
	if len(infos[0].Skipped) != 1 || !strings.Contains(infos[0].Skipped[0], "exec not allowed") {
		t.Fatalf("skipped = %+v", infos[0].Skipped)
	}
	for _, e := range svc.Extensions() {
		if e.Name() == "kit:ext" {
			t.Fatal("exec extension installed without AllowExec")
		}
	}

	extensiontest.Run(t, svc, "hi")
	var seen bool
	for _, round := range llm.Rounds() {
		for _, m := range round {
			if m.Role == "system" && strings.Contains(m.Content, "PLUGIN-PROMPT-MARKER") {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("prompt.md never reached the model")
	}
}

func TestAllowExecInstallsExtensionWithPluginRelativePath(t *testing.T) {
	root := t.TempDir()
	fullPlugin(t, root, "kit")
	b := agent.New("host")
	if err := plugin.Install(b, plugin.Dirs(root), plugin.NoDefaultDir(), plugin.AllowExec("kit")); err != nil {
		t.Fatal(err)
	}
	// The extension is not started here (Build would spawn ./bin/x); only
	// that it was accepted and recorded.
	infos := agentInfos(t, b)
	if !strings.Contains(strings.Join(infos[0].Components, ","), "extension") || len(infos[0].Skipped) != 0 {
		t.Fatalf("got %+v", infos[0])
	}
}

func agentInfos(t *testing.T, b *agent.Builder) []agent.PluginInfo {
	t.Helper()
	return b.InstalledPlugins()
}

func TestDisableAndStateFile(t *testing.T) {
	root := t.TempDir()
	fullPlugin(t, root, "kit")
	fullPlugin(t, root, "other")
	write(t, filepath.Join(root, "state.json"), `{"disabled":["other"]}`)
	svc, _ := build(t, plugin.Dirs(root), plugin.Disable("nope"))
	infos := svc.Plugins()
	if len(infos) != 1 || infos[0].Name != "kit" {
		t.Fatalf("state.json disabled list ignored: %+v", infos)
	}
	svc2, _ := build(t, plugin.Dirs(root), plugin.Disable("kit"))
	if len(svc2.Plugins()) != 0 {
		t.Fatalf("Disable ignored: %+v", svc2.Plugins())
	}
}
