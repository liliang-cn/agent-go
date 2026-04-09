package agent

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

const discoveredToolsTag = "discovered-tools"

var discoveredToolsRe = regexp.MustCompile(`(?s)<discovered-tools>(.*?)</discovered-tools>`)

func extractDiscoveredToolNames(messages []domain.Message, summary string) []string {
	seen := make(map[string]struct{})

	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		seen[name] = struct{}{}
	}

	for _, name := range parseDiscoveredToolsTag(summary) {
		add(name)
	}

	for _, msg := range messages {
		for _, name := range parseDiscoveredToolsTag(msg.Content) {
			add(name)
		}
		if msg.Role != "tool" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		var result domain.ToolSearchResult
		if err := json.Unmarshal([]byte(msg.Content), &result); err != nil {
			continue
		}
		for _, ref := range result.ToolReferences {
			add(ref.ToolName)
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func appendDiscoveredToolsSnapshot(content string, names []string) string {
	clean := stripDiscoveredToolsTag(content)
	if len(names) == 0 {
		return clean
	}
	snapshot := "<" + discoveredToolsTag + ">" + strings.Join(names, ",") + "</" + discoveredToolsTag + ">"
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return snapshot
	}
	return clean + "\n\n" + snapshot
}

func stripDiscoveredToolsTag(content string) string {
	clean := discoveredToolsRe.ReplaceAllString(content, "")
	return strings.TrimSpace(clean)
}

func parseDiscoveredToolsTag(content string) []string {
	matches := discoveredToolsRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		for _, part := range strings.Split(match[1], ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}
