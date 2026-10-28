package agent

import (
	"context"
	"fmt"
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
	// The index tells the model to search; with no search in the schema
	// nothing it lists could ever be made callable, so it would only be a
	// list of things the model is told it has and cannot use.
	if !offersToolSearch(sent) {
		return ""
	}
	// The flat collect plus the registry's own deferred set. Neither alone is
	// the whole answer: MCP tools and skills never enter the registry, and a
	// registry tool held back by SetDeferredPatterns never enters the collect.
	all := s.collectTools(ctx, currentAgent, policy, false)
	if s.toolRegistry != nil {
		all = append(all, s.toolRegistry.ListDeferredTools()...)
	}
	return renderToolIndex(s, all, sent)
}

// renderToolIndex is toolIndex once the two catalogues are in hand: everything
// that exists, and everything this turn is sending.
//
// A few hidden tools are listed one per line with what each is for. Past
// indexWithDescriptions that becomes a page on every turn — 239 lines and 30KB
// on an install with CortexDB's MCP server, more than the schemas it was
// saving, and some of it twice over when one server was registered from two
// config files. So a large index names tools by family instead, without
// descriptions: the search the model has to run anyway returns those.
func renderToolIndex(s *Service, all, sent []domain.ToolDefinition) string {
	if !s.shouldIndexDeferredTools() {
		return ""
	}
	present := make(map[string]bool, len(sent))
	for _, t := range sent {
		present[t.Function.Name] = true
	}

	// Each hidden tool once. Sorted, so the same install produces the same
	// prefix every turn and a provider's prompt cache can keep it.
	seen := map[string]bool{}
	var hidden []domain.ToolDefinition
	for _, t := range all {
		name := strings.TrimSpace(t.Function.Name)
		if name == "" || present[name] || seen[name] {
			continue
		}
		// The search tools are how hidden tools are reached, not tools to
		// reach: listed here, a turn without them read "search_available_tools
		// exists but cannot be called yet — search first".
		if name == "search_available_tools" || domain.IsToolSearchTool(name) {
			continue
		}
		seen[name] = true
		hidden = append(hidden, t)
	}
	if len(hidden) == 0 {
		return ""
	}
	sort.Slice(hidden, func(i, j int) bool { return hidden[i].Function.Name < hidden[j].Function.Name })

	var b strings.Builder
	b.WriteString("\n\n## Tools you have but cannot call yet\n")
	b.WriteString("Each is not in your schema yet it exists: search first (tool_search_tool_bm25 by meaning, " +
		"tool_search_tool_regex by name) to make it callable. Never report one as missing — it exists.\n\n")

	if len(hidden) <= indexWithDescriptions {
		for _, t := range hidden {
			b.WriteString("- " + strings.TrimSpace(t.Function.Name) + " — " + firstLineOfDescription(t.Function.Description) + "\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}

	// By family: "mcp_cortexdb_*: aggregate_metadata, apply_inference, …".
	type family struct {
		prefix string
		names  []string
	}
	var fams []*family
	byPrefix := map[string]*family{}
	for _, t := range hidden {
		name := strings.TrimSpace(t.Function.Name)
		prefix := toolFamily(name)
		f := byPrefix[prefix]
		if f == nil {
			f = &family{prefix: prefix}
			byPrefix[prefix] = f
			fams = append(fams, f)
		}
		f.names = append(f.names, strings.TrimPrefix(name, prefix))
	}
	var full strings.Builder
	for _, f := range fams {
		if f.prefix == "" || len(f.names) == 1 {
			for _, n := range f.names {
				full.WriteString("- " + f.prefix + n + "\n")
			}
			continue
		}
		full.WriteString(fmt.Sprintf("- %s* (%d): %s\n", f.prefix, len(f.names), strings.Join(f.names, ", ")))
	}
	if full.Len() <= indexBudget {
		b.WriteString(full.String())
		return strings.TrimRight(b.String(), "\n")
	}
	// Still too much to carry every turn: families and counts only.
	for _, f := range fams {
		if f.prefix == "" {
			b.WriteString(fmt.Sprintf("- %d more tools with single-word names\n", len(f.names)))
			continue
		}
		b.WriteString(fmt.Sprintf("- %s* (%d tools)\n", f.prefix, len(f.names)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// indexWithDescriptions is how many hidden tools the index still describes
// one by one.
const indexWithDescriptions = 24

// indexBudget is the most the grouped index may take, in bytes.
const indexBudget = 1200

// toolFamily is the prefix a tool shares with its siblings: an MCP tool's
// "mcp_<server>_", otherwise its first word and the underscore after it, or ""
// for a name with no underscore.
func toolFamily(name string) string {
	if strings.HasPrefix(name, "mcp_") {
		if parts := strings.SplitN(name, "_", 3); len(parts) == 3 {
			return "mcp_" + parts[1] + "_"
		}
	}
	if i := strings.IndexByte(name, '_'); i > 0 {
		return name[:i+1]
	}
	return ""
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
