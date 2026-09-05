package agent

import (
	"context"
	"sort"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// The index of what discovery is holding back.
//
// Tool discovery has always had a hole in it. Above the threshold the runtime
// stops putting MCP tools and skills in the schema and offers a search tool
// instead — but nothing ever told the model what it was searching. A hundred
// tools would sit behind tool_search_tool_bm25 with no announcement, so the
// only way to find one was to guess a query for a capability the model had no
// reason to believe existed. Measured on a real install: 54 tools deferred,
// zero mentions of any of them anywhere in the turn.
//
// The index closes it. Every tool held back appears as one line — its name and
// the first sentence of its description — which is enough to know a capability
// is there and roughly what it does, and cheap enough to carry every turn. On
// that same install the full schemas were 124KB; the index of all of them is
// 11KB.
//
// It is deliberately not the schema. A name and a sentence let the model decide
// to look; the parameters it needs to make the call still come from the search,
// which is the step that also activates the tool for the session.

// indexToolLine is the most one tool may spend in the index.
//
// Long enough for a real sentence, short enough that a catalogue of two hundred
// stays a few kilobytes. Descriptions in the wild run to whole paragraphs with
// examples and warnings; those belong in the schema the search returns, not in
// a list whose only job is to say the tool exists.
const indexToolLine = 160

// shouldIndexDeferredTools reports whether this turn carries the index.
func (s *Service) shouldIndexDeferredTools() bool {
	if s == nil || s.cfg == nil {
		return true
	}
	if s.cfg.Tooling.IndexDeferredTools != nil {
		return *s.cfg.Tooling.IndexDeferredTools
	}
	return true
}

// deferredToolPatterns are the registry tools this install keeps behind the
// index. Empty means discovery behaves as it always did and defers only the
// dynamic sources.
func (s *Service) deferredToolPatterns() []string {
	if s == nil || s.cfg == nil {
		return nil
	}
	return s.cfg.Tooling.DeferTools
}

// isDeferredByConfig reports whether a registry tool is one this install holds
// back. Session activation is checked by the caller, which has the session.
func (s *Service) isDeferredByConfig(name string) bool {
	for _, pattern := range s.deferredToolPatterns() {
		if toolPolicyPatternMatches(pattern, name) {
			return true
		}
	}
	return false
}

// deferConfiguredRegistryTools removes the tools this install keeps behind the
// index, except any the session has already looked up.
//
// Only ever called when the turn is already in discovery mode. Below the
// threshold there is nothing to pay for hiding things, and a catalogue small
// enough to send flat should be sent flat.
func (s *Service) deferConfiguredRegistryTools(tools []domain.ToolDefinition, sessionID string) []domain.ToolDefinition {
	if len(s.deferredToolPatterns()) == 0 {
		return tools
	}
	var active map[string]bool
	if s.toolRegistry != nil {
		active = s.toolRegistry.sessionActivated[sessionID]
	}
	out := make([]domain.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		name := t.Function.Name
		if s.isDeferredByConfig(name) && !active[name] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// toolIndex renders the tools that exist but are not in this turn's schema.
//
// Computed by difference rather than by asking the registry what is deferred:
// MCP tools and skills never enter the registry at all, they are assembled per
// turn, so a registry-only answer would leave out the very tools discovery
// hides most of. Collecting the flat catalogue and subtracting what was sent
// is the only way to name everything that is genuinely missing.
func (s *Service) toolIndex(ctx context.Context, currentAgent *Agent, policy toolPreparationPolicy, sent []domain.ToolDefinition) string {
	if !s.shouldIndexDeferredTools() {
		return ""
	}
	return renderToolIndex(s, s.collectTools(ctx, currentAgent, policy, false), sent)
}

// renderToolIndex is toolIndex once the two catalogues are in hand: everything
// that exists, and everything this turn is sending.
func renderToolIndex(s *Service, all, sent []domain.ToolDefinition) string {
	if !s.shouldIndexDeferredTools() {
		return ""
	}
	present := make(map[string]bool, len(sent))
	for _, t := range sent {
		present[t.Function.Name] = true
	}

	lines := make([]string, 0, len(all))
	for _, t := range all {
		name := strings.TrimSpace(t.Function.Name)
		if name == "" || present[name] {
			continue
		}
		lines = append(lines, "- "+name+" — "+firstLineOfDescription(t.Function.Description))
	}
	if len(lines) == 0 {
		return ""
	}
	// Sorted, so the same install produces the same prefix every turn and a
	// provider's prompt cache can keep it.
	sort.Strings(lines)

	var b strings.Builder
	b.WriteString("\n\n## Tools you have but cannot call yet\n")
	b.WriteString("These exist and are not in your schema. To use one, search for it first " +
		"(tool_search_tool_bm25 with a plain-language query, or tool_search_tool_regex with a name pattern); " +
		"the search returns its parameters and makes it callable for the rest of this conversation. " +
		"Do not tell the user a capability is missing because it is on this list — it is here precisely because it exists.\n\n")
	b.WriteString(strings.Join(lines, "\n"))
	return b.String()
}

// firstLineOfDescription reduces a description to the sentence that says what
// the tool is for.
//
// The first sentence, or the first line, whichever comes first — descriptions
// are written for the schema, where a paragraph of guidance is right, and the
// index needs the opening claim rather than the guidance.
func firstLineOfDescription(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "(no description)"
	}
	if i := strings.IndexByte(desc, '\n'); i >= 0 {
		desc = strings.TrimSpace(desc[:i])
	}
	// A sentence end, but not the dot inside "e.g." or a tool name.
	if i := strings.Index(desc, ". "); i > 0 {
		desc = desc[:i+1]
	}
	if len([]rune(desc)) > indexToolLine {
		desc = string([]rune(desc)[:indexToolLine-1]) + "…"
	}
	return desc
}
