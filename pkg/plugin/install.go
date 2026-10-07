package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/extensions/exec"
)

type config struct {
	dirs      []string
	noDefault bool
	disabled  map[string]bool
	allowExec map[string]bool
}

// Option configures Install.
type Option func(*config)

// Dirs adds plugin locations: each is a plugins root or a single plugin.
func Dirs(dirs ...string) Option {
	return func(c *config) { c.dirs = append(c.dirs, dirs...) }
}

// NoDefaultDir stops Install from also searching $AGENTGO_HOME/plugins.
func NoDefaultDir() Option { return func(c *config) { c.noDefault = true } }

// Disable skips the named plugins.
func Disable(names ...string) Option {
	return func(c *config) {
		for _, n := range names {
			c.disabled[n] = true
		}
	}
}

// AllowExec lets the named plugins start the subprocess their
// extension.json describes. Without it the extension is skipped: it is an
// arbitrary program that can rewrite or refuse tool calls, and a directory
// appearing under plugins/ is not consent to run one.
func AllowExec(names ...string) Option {
	return func(c *config) {
		for _, n := range names {
			c.allowExec[n] = true
		}
	}
}

// DefaultDir is $AGENTGO_HOME/plugins, or ~/.agentgo/plugins.
func DefaultDir() string {
	if h := os.Getenv("AGENTGO_HOME"); h != "" {
		return filepath.Join(h, "plugins")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agentgo", "plugins")
}

// Install discovers plugins and wires each enabled one onto b. Call it before
// Build. A malformed plugin fails the install; a plugin part that cannot be
// wired (an extension that is not allowed) is recorded in PluginInfo.Skipped
// and the rest of the plugin still loads.
func Install(b *agent.Builder, opts ...Option) error {
	cfg := &config{disabled: map[string]bool{}, allowExec: map[string]bool{}}
	for _, o := range opts {
		o(cfg)
	}
	dirs := slices.Clone(cfg.dirs)
	if !cfg.noDefault {
		if d := DefaultDir(); d != "" {
			dirs = append(dirs, d)
		}
	}
	for _, d := range dirs {
		st, err := loadState(d)
		if err != nil {
			return err
		}
		for _, n := range st.Disabled {
			cfg.disabled[n] = true
		}
		for _, n := range st.AllowExec {
			cfg.allowExec[n] = true
		}
	}
	found, err := Discover(dirs...)
	if err != nil {
		return err
	}
	for _, p := range found {
		if cfg.disabled[p.Name] {
			continue
		}
		info, err := apply(b, p, cfg.allowExec[p.Name])
		if err != nil {
			return fmt.Errorf("plugin %q: %w", p.Name, err)
		}
		b.RecordPlugin(info)
	}
	return nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func apply(b *agent.Builder, p *Plugin, allowExec bool) (agent.PluginInfo, error) {
	info := agent.PluginInfo{Name: p.Name, Version: p.Version, Description: p.Description, Dir: p.Dir}

	if d := filepath.Join(p.Dir, "skills"); isDir(d) {
		b.AddSkillsPaths(d)
		info.Components = append(info.Components, "skills")
	}
	if f := filepath.Join(p.Dir, "mcp.json"); isFile(f) {
		b.AddMCPConfigPaths(f)
		info.Components = append(info.Components, "mcp")
	}
	specs, err := loadAgents(p.Dir, p.Name)
	if err != nil {
		return info, err
	}
	if len(specs) > 0 {
		b.WithSubagents(specs...)
		info.Components = append(info.Components, "agents")
	}
	if f := filepath.Join(p.Dir, "prompt.md"); isFile(f) {
		data, err := os.ReadFile(f)
		if err != nil {
			return info, err
		}
		if text := strings.TrimSpace(string(data)); text != "" {
			b.WithExtensions(&promptExt{name: p.Name + ":prompt", text: text})
			info.Components = append(info.Components, "prompt")
		}
	}
	if f := filepath.Join(p.Dir, "extension.json"); isFile(f) {
		if !allowExec {
			info.Skipped = append(info.Skipped, "extension: exec not allowed (plugin.AllowExec)")
		} else {
			ext, err := loadExec(p, f)
			if err != nil {
				return info, err
			}
			b.WithExtensions(ext)
			info.Components = append(info.Components, "extension")
		}
	}
	return info, nil
}

type execFile struct {
	Command []string          `json:"command"`
	Timeout string            `json:"timeout,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

func loadExec(p *Plugin, path string) (*exec.Extension, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ef execFile
	if err := json.Unmarshal(data, &ef); err != nil {
		return nil, fmt.Errorf("extension.json: %w", err)
	}
	if len(ef.Command) == 0 {
		return nil, fmt.Errorf("extension.json: command is required")
	}
	cmd := slices.Clone(ef.Command)
	// A relative program path is relative to the plugin, not to wherever the
	// host happens to be running.
	if strings.ContainsRune(cmd[0], '/') && !filepath.IsAbs(cmd[0]) {
		cmd[0] = filepath.Join(p.Dir, cmd[0])
	}
	opts := []exec.Option{exec.WithDir(p.Dir)}
	if ef.Timeout != "" {
		d, err := time.ParseDuration(ef.Timeout)
		if err != nil {
			return nil, fmt.Errorf("extension.json: timeout: %w", err)
		}
		opts = append(opts, exec.WithTimeout(d))
	}
	if len(ef.Env) > 0 {
		keys := make([]string, 0, len(ef.Env))
		for k := range ef.Env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		env := make([]string, 0, len(keys))
		for _, k := range keys {
			env = append(env, k+"="+ef.Env[k])
		}
		opts = append(opts, exec.WithEnv(env...))
	}
	return exec.New(p.Name+":ext", cmd, opts...), nil
}

// promptExt appends a plugin's prompt.md as one system message. The text is
// read once at install, so it is byte-identical on every round.
type promptExt struct {
	name string
	text string
}

func (e *promptExt) Name() string { return e.name }

func (e *promptExt) ContributeContext(context.Context, agent.ContextInput) ([]domain.Message, error) {
	return []domain.Message{{Role: "system", Content: e.text}}, nil
}
