// Package plugin loads directory-shaped plugins onto an agent.Builder.
//
// A plugin is a directory with a plugin.json and any of: skills/, mcp.json,
// agents/*.md, prompt.md, extension.json. Loading one adds nothing to the
// framework: each part is translated into an option the Builder already has
// (skills paths, MCP server files, sub-agents, extensions), so a plugin cannot
// do anything a host could not do in Go, and the loop stays one loop.
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// ManifestFile is the file that makes a directory a plugin.
const ManifestFile = "plugin.json"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Manifest is plugin.json.
type Manifest struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

// Plugin is a discovered, validated plugin directory.
type Plugin struct {
	Manifest
	Dir string
}

// Validate checks the manifest.
func (m Manifest) Validate() error {
	if !namePattern.MatchString(m.Name) {
		return fmt.Errorf("plugin name %q must match %s", m.Name, namePattern)
	}
	return nil
}

// Load reads one plugin directory.
func Load(dir string) (*Plugin, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(abs, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("plugin %s: %w", abs, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("plugin %s: parse %s: %w", abs, ManifestFile, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("plugin %s: %w", abs, err)
	}
	return &Plugin{Manifest: m, Dir: abs}, nil
}

// Discover finds plugins under each dir. A dir that itself holds a plugin.json
// is one plugin; otherwise each immediate subdirectory holding one is. A dir
// that does not exist is skipped — an unused plugins directory is not an
// error. A broken plugin is an error: silently dropping it would read as
// "this plugin has no skills". Results are sorted by name, so everything a
// plugin adds reaches the prompt in the same order on every build (the prompt
// cache matches on a prefix), and a name seen twice is an error.
func Discover(dirs ...string) ([]*Plugin, error) {
	var out []*Plugin
	seen := map[string]string{}
	add := func(p *Plugin) error {
		if prev, dup := seen[p.Name]; dup {
			return fmt.Errorf("plugin %q found twice: %s and %s", p.Name, prev, p.Dir)
		}
		seen[p.Name] = p.Dir
		out = append(out, p)
		return nil
	}
	for _, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err == nil {
			p, err := Load(dir)
			if err != nil {
				return nil, err
			}
			if err := add(p); err != nil {
				return nil, err
			}
			continue
		}
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			sub := filepath.Join(dir, e.Name())
			if !e.IsDir() {
				if fi, err := os.Stat(sub); err != nil || !fi.IsDir() {
					continue
				}
			}
			if _, err := os.Stat(filepath.Join(sub, ManifestFile)); err != nil {
				continue
			}
			p, err := Load(sub)
			if err != nil {
				return nil, err
			}
			if err := add(p); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// State is state.json, kept beside the plugins: which are switched off and
// which may start a subprocess.
type State struct {
	Disabled  []string `json:"disabled,omitempty"`
	AllowExec []string `json:"allow_exec,omitempty"`
}

func loadState(dir string) (State, error) {
	var st State
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s: %w", filepath.Join(dir, "state.json"), err)
	}
	return st, nil
}
