package agent

import (
	"context"
	"fmt"
	"strings"
)

// registeredAgentTool is a Go closure tool attached to a TeamManager-managed
// agent by name. Closures can't be persisted to SQLite, so they live in memory
// on the TeamManager and are re-applied every time the agent's Service is
// (re)built in buildServiceForModel.
type registeredAgentTool struct {
	name        string
	description string
	parameters  map[string]interface{}
	handler     func(context.Context, map[string]interface{}) (interface{}, error)
	metadata    ToolMetadata
}

// RegisterAgentTool attaches a Go closure tool to a team member / specialist by
// agent name. Unlike MCP/Skills, this lets a member call arbitrary Go code
// (e.g. a KB lookup or a similar-ticket retriever). The tool is applied to the
// agent's Service on build and re-applied on every rebuild; any cached service
// for that agent is dropped so the tool takes effect immediately.
//
//	manager.RegisterAgentTool("Triage", "lookup_kb", "Search the KB",
//	    map[string]any{"type":"object","properties":map[string]any{
//	        "query": map[string]any{"type":"string"}}},
//	    func(ctx context.Context, a map[string]any) (any, error) { return kb.Search(a["query"]) },
//	    agent.ToolMetadata{ReadOnly: true, ConcurrencySafe: true})
func (m *TeamManager) RegisterAgentTool(
	agentName, name, description string,
	parameters map[string]interface{},
	handler func(context.Context, map[string]interface{}) (interface{}, error),
	metadata ToolMetadata,
) error {
	agentName = strings.TrimSpace(agentName)
	name = strings.TrimSpace(name)
	if agentName == "" || name == "" {
		return fmt.Errorf("agentName and tool name are required")
	}
	if handler == nil {
		return fmt.Errorf("handler is required")
	}

	tool := registeredAgentTool{
		name:        name,
		description: description,
		parameters:  parameters,
		handler:     handler,
		metadata:    metadata,
	}

	m.mu.Lock()
	if m.agentTools == nil {
		m.agentTools = make(map[string][]registeredAgentTool)
	}
	// Replace an existing tool of the same name; otherwise append.
	existing := m.agentTools[agentName]
	replaced := false
	for i, t := range existing {
		if t.name == name {
			existing[i] = tool
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, tool)
	}
	m.agentTools[agentName] = existing
	// Drop any cached service so the next build picks up the new tool.
	if svc, ok := m.services[agentName]; ok {
		delete(m.services, agentName)
		_ = svc.Close()
	}
	m.mu.Unlock()
	return nil
}

// applyRegisteredAgentTools registers any closure tools attached to agentName
// onto the freshly-built service. Called from buildServiceForModel (which holds
// no lock on m.services at that point); it takes its own short read lock to copy
// the slice.
func (m *TeamManager) applyRegisteredAgentTools(svc *Service, agentName string) {
	if svc == nil {
		return
	}
	m.mu.RLock()
	tools := append([]registeredAgentTool(nil), m.agentTools[agentName]...)
	m.mu.RUnlock()

	for _, t := range tools {
		if svc.toolRegistry != nil && svc.toolRegistry.Has(t.name) {
			continue
		}
		svc.AddToolWithMetadata(t.name, t.description, t.parameters, t.handler, t.metadata)
	}
}
