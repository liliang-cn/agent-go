package agent

import (
	"context"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// Installable is a capability the host could add but has not: an MCP server
// in a registry, a skill on disk. A tool search returns these beside the tools
// that are installed, so a model that searched for something it lacks is told
// where it is and how to get it, instead of being told there is nothing.
type Installable struct {
	// Kind says what it is: "mcp" or "skill".
	Kind string `json:"kind"`
	// Name and Description are what the model reads to decide whether this
	// is the capability it was looking for.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// InstallTool is the registered tool that installs it and InstallArgs the
	// arguments to call that tool with, complete.
	InstallTool string         `json:"install_tool"`
	InstallArgs map[string]any `json:"install_args,omitempty"`
	// Needs lists what the install will not work without and the model has
	// to get from the user first: the names of required environment
	// variables, for one.
	Needs []string `json:"needs,omitempty"`
}

// InstallableSearch finds what could be installed for a tool-search query.
// The host owns where it looks. It is called with a deadline and its failures
// are its own: nothing found and an unreachable registry both return nothing.
type InstallableSearch func(ctx context.Context, query string) []Installable

// installableSearchTimeout bounds the host's search. The model is waiting on
// the tool result, and the installed matches are already in hand.
const installableSearchTimeout = 8 * time.Second

// toolSearchInstallableNote is the one line that goes with the installables.
const toolSearchInstallableNote = "The entries under \"installable\" are NOT installed yet. To get one, call its install_tool with its install_args exactly as given (ask the user first for anything listed under needs), then use the tools it adds."

// WithInstallableSearch makes search_available_tools also return what could
// be installed for the query, found by fn. Without it a search returns only
// what is installed.
func (b *Builder) WithInstallableSearch(fn InstallableSearch) *Builder {
	b.installableSearch = fn
	return b
}

// SetInstallableSearch sets, replaces or (with nil) removes the search for
// installable capabilities that search_available_tools runs beside its own.
func (s *Service) SetInstallableSearch(fn InstallableSearch) {
	s.installableMu.Lock()
	defer s.installableMu.Unlock()
	s.installableSearch = fn
}

// searchInstallables asks the host what could be installed for query and
// returns the entries the current agent could act on: named, and with an
// install tool that is registered and allowed. Those install tools are
// activated for the session, so the model can call them on its next turn.
func (s *Service) searchInstallables(ctx context.Context, currentAgent *Agent, query string) []Installable {
	s.installableMu.RLock()
	fn := s.installableSearch
	s.installableMu.RUnlock()
	if fn == nil || strings.TrimSpace(query) == "" {
		return nil
	}

	searchCtx, cancel := context.WithTimeout(ctx, installableSearchTimeout)
	defer cancel()
	found := make(chan []Installable, 1)
	go func() {
		defer func() {
			if recover() != nil {
				found <- nil
			}
		}()
		found <- fn(searchCtx, query)
	}()
	var raw []Installable
	select {
	case raw = <-found:
	case <-searchCtx.Done():
		// A search that does not honour its context is left behind.
		return nil
	}

	// Both entry points: search_available_tools activates for the service's
	// current session, the tool-search tools for the session in the context.
	sessionIDs := []string{s.CurrentSessionID()}
	if session := getCurrentSession(ctx); session != nil && session.GetID() != sessionIDs[0] {
		sessionIDs = append(sessionIDs, session.GetID())
	}
	out := make([]Installable, 0, len(raw))
	tools := map[string]bool{}
	for _, in := range raw {
		if strings.TrimSpace(in.Name) == "" || in.InstallTool == "" {
			continue
		}
		if !s.toolRegistry.Has(in.InstallTool) || !s.isToolAllowedForAgent(currentAgent, in.InstallTool) {
			continue
		}
		if !tools[in.InstallTool] {
			tools[in.InstallTool] = true
			for _, id := range sessionIDs {
				s.toolRegistry.ActivateForSession(id, in.InstallTool)
			}
		}
		out = append(out, in)
	}
	if len(out) == 0 {
		return nil
	}

	names := make([]string, 0, len(out))
	for _, in := range out {
		names = append(names, in.Kind+":"+in.Name)
	}
	installTools := make([]string, 0, len(tools))
	for _, in := range out {
		if tools[in.InstallTool] {
			installTools = append(installTools, in.InstallTool)
			delete(tools, in.InstallTool)
		}
	}
	trace.Decide(ctx, "offer_installables",
		"the tool search also returned capabilities that are not installed, with the tool that installs each",
		map[string]any{"query": query, "count": len(out), "installables": names, "install_tools": installTools})
	return out
}

// toolSearchResultWithInstallables is what a search returns when there is
// something to install: the installed matches as they would have been
// returned alone, the installables, and the line that tells them apart.
func toolSearchResultWithInstallables(tools []map[string]interface{}, installables []Installable) map[string]interface{} {
	if tools == nil {
		tools = []map[string]interface{}{}
	}
	return map[string]interface{}{
		"tools":       tools,
		"installable": installables,
		"note":        toolSearchInstallableNote,
	}
}

// toolSearchWithInstallables is what tool_search_tool_bm25 returns when there
// is something to install. It still reads as a domain.ToolSearchResult, and
// the install tools are among its references: they were made callable too.
type toolSearchWithInstallables struct {
	ToolReferences []domain.ToolReference `json:"tool_references"`
	Installable    []Installable          `json:"installable"`
	Note           string                 `json:"note"`
}

func toolReferencesWithInstallables(refs []domain.ToolReference, installables []Installable) toolSearchWithInstallables {
	out := toolSearchWithInstallables{
		ToolReferences: append([]domain.ToolReference{}, refs...),
		Installable:    installables,
		Note:           toolSearchInstallableNote,
	}
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.ToolName] = true
	}
	for _, in := range installables {
		if !seen[in.InstallTool] {
			seen[in.InstallTool] = true
			out.ToolReferences = append(out.ToolReferences, domain.ToolReference{ToolName: in.InstallTool})
		}
	}
	return out
}
