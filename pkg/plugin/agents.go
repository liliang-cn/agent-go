package plugin

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

type agentFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tools       []string `yaml:"tools"`
	Model       string   `yaml:"model"`
	Provider    string   `yaml:"provider"`
	MaxTurns    int      `yaml:"max_turns"`
	Parallel    bool     `yaml:"parallel"`
}

// parseAgent reads one agents/*.md: YAML frontmatter, then the instructions.
func parseAgent(path, prefix string) (agent.SubagentSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agent.SubagentSpec{}, err
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return agent.SubagentSpec{}, fmt.Errorf("%s: missing --- frontmatter", path)
	}
	head, body, ok := bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return agent.SubagentSpec{}, fmt.Errorf("%s: unterminated frontmatter", path)
	}
	body = bytes.TrimPrefix(body, []byte("\n"))
	var fm agentFrontmatter
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return agent.SubagentSpec{}, fmt.Errorf("%s: %w", path, err)
	}
	if fm.Name == "" {
		fm.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if fm.Description == "" {
		return agent.SubagentSpec{}, fmt.Errorf("%s: description is required", path)
	}
	instructions := strings.TrimSpace(string(body))
	if instructions == "" {
		return agent.SubagentSpec{}, fmt.Errorf("%s: no instructions after the frontmatter", path)
	}
	return agent.SubagentSpec{
		Name:         prefix + ":" + fm.Name,
		Description:  fm.Description,
		Instructions: instructions,
		Tools:        fm.Tools,
		Model:        fm.Model,
		Provider:     fm.Provider,
		MaxTurns:     fm.MaxTurns,
		Parallel:     fm.Parallel,
	}, nil
}

func loadAgents(dir, prefix string) ([]agent.SubagentSpec, error) {
	files, err := filepath.Glob(filepath.Join(dir, "agents", "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []agent.SubagentSpec
	for _, f := range files {
		spec, err := parseAgent(f, prefix)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}
