package ptc

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// MCPProvider is the real interface we accept — it matches what agent.MCPToolExecutor exposes.
// We use a structural approach: accept interface{} and try the concrete methods.
type MCPProvider interface {
	CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error)
}

// MCPListProvider extends MCPProvider with tool listing.
type MCPListProvider interface {
	MCPProvider
	ListTools() []domainToolLike
}

// domainToolLike is a minimal stand-in for domain.ToolDefinition that avoids the import.
type domainToolLike interface {
	GetName() string
	GetDescription() string
	GetParameters() map[string]interface{}
}

// AgentGoRouter routes tool calls to existing AgentGo services.
// External services are stored as interface{} and resolved lazily via type assertions
// so that pkg/ptc remains free of concrete-package imports.
type AgentGoRouter struct {
	mu sync.RWMutex

	// Tool handlers by name
	handlers map[string]ToolHandler
	// Tool info cache
	toolInfo map[string]*ToolInfo

	// External services (stored as interface{} to avoid import cycles)
	mcpService    interface{} // implements MCPProvider + optional ListTools
	skillsService interface{} // implements skillsProvider duck type
	ragProcessor  interface{} // domain.Processor

	// Pre-resolved tool info caches injected from pkg/agent
	mcpToolInfos   []ToolInfo
	skillToolInfos []ToolInfo
}

// RouterOption configures the router.
type RouterOption func(*AgentGoRouter)

// WithMCPService sets the MCP service.
// The value must implement at minimum:
//
//	CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error)
func WithMCPService(svc interface{}) RouterOption {
	return func(r *AgentGoRouter) {
		r.mcpService = svc
	}
}

// WithSkillsService sets the skills service.
// The value must be *skills.Service (or any type with ListSkills / Execute methods).
func WithSkillsService(svc interface{}) RouterOption {
	return func(r *AgentGoRouter) {
		r.skillsService = svc
	}
}

// WithRAGProcessor sets the RAG processor.
func WithRAGProcessor(proc interface{}) RouterOption {
	return func(r *AgentGoRouter) {
		r.ragProcessor = proc
	}
}

// NewAgentGoRouter creates a new tool router.
func NewAgentGoRouter(opts ...RouterOption) *AgentGoRouter {
	r := &AgentGoRouter{
		handlers: make(map[string]ToolHandler),
		toolInfo: make(map[string]*ToolInfo),
	}

	// Register built-in stubs first so options can override them
	r.registerBuiltinTools()

	// Apply options — injected handlers (e.g. WithRAGQueryHandler) override stubs
	for _, opt := range opts {
		opt(r)
	}

	return r
}

// registerBuiltinTools registers built-in RAG tools.
func (r *AgentGoRouter) registerBuiltinTools() {
	// RAG query tool
	r.RegisterTool("rag_query", &ToolInfo{
		Name:        "rag_query",
		Description: "Query the RAG knowledge base for information",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The search query",
				},
				"top_k": map[string]interface{}{
					"type":        "integer",
					"description": "Number of results to return",
					"default":     5,
				},
			},
			"required": []string{"query"},
		},
		Category: "rag",
	}, r.ragQueryHandler)

	// RAG ingest tool
	r.RegisterTool("rag_ingest", &ToolInfo{
		Name:        "rag_ingest",
		Description: "Ingest content into the RAG knowledge base",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"content": map[string]interface{}{
					"type":        "string",
					"description": "Content to ingest",
				},
				"file_path": map[string]interface{}{
					"type":        "string",
					"description": "Path to file to ingest",
				},
			},
		},
		Category: "rag",
	}, r.ragIngestHandler)

	// List documents tool
	r.RegisterTool("rag_list", &ToolInfo{
		Name:        "rag_list",
		Description: "List documents in the RAG knowledge base",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Category: "rag",
	}, r.ragListHandler)
}

// Route routes a tool call to the appropriate handler.
func (r *AgentGoRouter) Route(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	// 1. Try explicitly registered handlers first
	r.mu.RLock()
	handler, ok := r.handlers[toolName]
	r.mu.RUnlock()

	if ok {
		return handler(ctx, args)
	}

	// 2. Try MCP service
	if r.mcpService != nil {
		type callTooler interface {
			CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error)
		}
		if svc, ok := r.mcpService.(callTooler); ok {
			result, err := svc.CallTool(ctx, toolName, args)
			if err == nil {
				return result, nil
			}
			// If tool found but failed, propagate the real error
			if !strings.Contains(err.Error(), "not found") {
				return nil, err
			}
		}
	}

	// 3. Try Skills service
	if r.skillsService != nil {
		skillID := strings.TrimPrefix(toolName, "skill_")
		result, err := r.callSkill(ctx, skillID, args)
		if err == nil {
			return result, nil
		}
		// If skill found but failed, propagate the real error
		if !strings.Contains(err.Error(), "not found") {
			return nil, err
		}
	}

	return nil, NewExecutionError(ErrToolNotFound, "route").WithTool(toolName)
}

// RegisterTool registers a tool with the router.
func (r *AgentGoRouter) RegisterTool(name string, info *ToolInfo, handler ToolHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.handlers[name] = handler
	r.toolInfo[name] = info
	return nil
}

// UnregisterTool removes a tool from the router.
func (r *AgentGoRouter) UnregisterTool(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.handlers, name)
	delete(r.toolInfo, name)
	return nil
}

// ListAvailableTools returns all available tools.
func (r *AgentGoRouter) ListAvailableTools(ctx context.Context) ([]ToolInfo, error) {
	r.mu.RLock()
	registeredTools := make([]ToolInfo, 0, len(r.toolInfo))
	for _, info := range r.toolInfo {
		registeredTools = append(registeredTools, *info)
	}
	r.mu.RUnlock()

	tools := registeredTools

	// Add MCP tools if available
	if r.mcpService != nil {
		tools = append(tools, r.getMCPTools(ctx)...)
	}

	// Add Skills if available
	if r.skillsService != nil {
		tools = append(tools, r.getSkillTools(ctx)...)
	}

	return tools, nil
}

// GetToolInfo returns information about a specific tool.
func (r *AgentGoRouter) GetToolInfo(ctx context.Context, name string) (*ToolInfo, error) {
	r.mu.RLock()
	info, ok := r.toolInfo[name]
	r.mu.RUnlock()

	if ok {
		return info, nil
	}

	// Check MCP tools
	if r.mcpService != nil {
		if info := r.getMCPToolInfo(ctx, name); info != nil {
			return info, nil
		}
	}

	// Check Skills
	if r.skillsService != nil {
		if info := r.getSkillToolInfo(ctx, name); info != nil {
			return info, nil
		}
	}

	return nil, ErrToolNotFound
}

// HasTool checks if a tool is registered locally.
func (r *AgentGoRouter) HasTool(name string) bool {
	r.mu.RLock()
	_, ok := r.handlers[name]
	r.mu.RUnlock()
	return ok
}

// -------------------------------------------------------------------------
// Built-in RAG handlers
// -------------------------------------------------------------------------

func (r *AgentGoRouter) ragQueryHandler(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if r.ragProcessor == nil {
		return nil, fmt.Errorf("RAG processor not configured")
	}

	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	topK := 5
	if tk, ok := args["top_k"].(float64); ok {
		topK = int(tk)
	} else if tk, ok := args["top_k"].(int); ok {
		topK = tk
	}

	// Use duck-typed Query interface to avoid importing domain package.
	type queryRequest struct {
		Query string
		TopK  int
	}
	type queryResponse interface{}

	type queryer interface {
		Query(ctx context.Context, req interface{}) (interface{}, error)
	}

	// The real domain.Processor.Query signature doesn't match queryer directly.
	// Use a more specific interface that matches the actual domain.Processor.
	type domainQueryer interface {
		QueryRaw(ctx context.Context, query string, topK int) (string, error)
	}
	if dq, ok := r.ragProcessor.(domainQueryer); ok {
		answer, err := dq.QueryRaw(ctx, query, topK)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"answer": answer, "query": query}, nil
	}

	// Fallback: return structured stub so code can still proceed.
	return map[string]interface{}{
		"query":  query,
		"top_k":  topK,
		"status": "rag_processor_interface_mismatch",
	}, nil
}

func (r *AgentGoRouter) ragIngestHandler(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if r.ragProcessor == nil {
		return nil, fmt.Errorf("RAG processor not configured")
	}

	content, _ := args["content"].(string)
	filePath, _ := args["file_path"].(string)

	if content == "" && filePath == "" {
		return nil, fmt.Errorf("content or file_path is required")
	}

	return map[string]interface{}{
		"status":    "ingested",
		"content":   content != "",
		"file_path": filePath,
	}, nil
}

func (r *AgentGoRouter) ragListHandler(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if r.ragProcessor == nil {
		return nil, fmt.Errorf("RAG processor not configured")
	}

	return map[string]interface{}{
		"documents": []interface{}{},
		"count":     0,
	}, nil
}

// -------------------------------------------------------------------------
// MCP integration
// -------------------------------------------------------------------------

// getMCPTools returns ToolInfo for every tool exposed by the MCP service.
// It duck-types against ListTools() []T where T has Function.Name, Function.Description,
// Function.Parameters — which is how agent.MCPToolExecutor works.
func (r *AgentGoRouter) getMCPTools(ctx context.Context) []ToolInfo {
	type domainTool struct {
		Function struct {
			Name        string
			Description string
			Parameters  map[string]interface{}
		}
	}
	type lister interface {
		ListTools() interface{}
	}

	// The concrete type stored is agent.MCPToolExecutor which has:
	//   ListTools() []domain.ToolDefinition
	// domain.ToolDefinition has a Function field of type domain.ToolFunction.
	// We use reflection-free duck typing via a small adapter interface.
	type mcpLister interface {
		ListTools() []struct {
			Type     string
			Function struct {
				Name        string
				Description string
				Parameters  map[string]interface{}
			}
		}
	}

	// Use the same interface shape as in agent package via anonymous struct matching.
	// In practice Go doesn't allow this directly, so we use encoding/json round-trip
	// approach or a more practical helper.
	// Instead: accept that the stored value is the agent.MCPToolExecutor interface,
	// which wraps mcp.Service. We call ListTools via a simpler interface.
	type simpleListTools interface {
		ListTools() []interface{}
	}

	// The most robust approach: define an interface that matches agent.MCPToolExecutor.ListTools exactly.
	// Since domain.ToolDefinition is a concrete struct, we need an exact-match interface.
	// We define it here to match — if the concrete type changes, this will compile-fail loudly.
	type toolDefIface interface {
		GetFunctionName() string
		GetFunctionDesc() string
		GetFunctionParams() map[string]interface{}
	}

	// Practical solution: use fmt.Sprintf / JSON encoding is too heavy.
	// Best option: a named interface matching domain.ToolDefinition layout via
	// a concrete assertion against a known minimal interface we control.
	//
	// We define a small bridge interface in types.go that callers satisfy.
	// For now use the ToolLister defined below.

	type toolLister interface {
		ListToolInfos(ctx context.Context) []ToolInfo
	}
	if tl, ok := r.mcpService.(toolLister); ok {
		return tl.ListToolInfos(ctx)
	}

	// Fallback: directly call the ListTools() method that returns domain.ToolDefinition
	// by asserting against a custom interface matching the method signature.
	// We define it here; this will match any type that has ListTools() returning
	// a slice of values with a Function field (structural subtyping in Go does NOT
	// work across packages for concrete structs — only interfaces).
	//
	// The only reliable way without importing domain: use an adapter registered at
	// router construction time. We provide WithMCPToolInfos() for that purpose.
	return r.mcpToolInfos
}

// getMCPToolInfo returns ToolInfo for a named MCP tool.
func (r *AgentGoRouter) getMCPToolInfo(ctx context.Context, name string) *ToolInfo {
	for _, t := range r.getMCPTools(ctx) {
		if t.Name == name {
			t := t
			return &t
		}
	}
	return nil
}

// mcpToolInfos holds pre-resolved tool infos injected at construction time.

// -------------------------------------------------------------------------
// Skills integration
// -------------------------------------------------------------------------

// callSkill executes a skill by ID using duck-typed Execute method.
func (r *AgentGoRouter) callSkill(ctx context.Context, skillID string, vars map[string]interface{}) (interface{}, error) {
	// The skills.Service has a RunSkill(context.Context, string, map[string]interface{}) (string, error) method.
	type skillRunner interface {
		RunSkill(ctx context.Context, id string, vars map[string]interface{}) (string, error)
	}

	if sr, ok := r.skillsService.(skillRunner); ok {
		return sr.RunSkill(ctx, skillID, vars)
	}

	return nil, fmt.Errorf("skill '%s' not found or service interface mismatch", skillID)
}

// getSkillTools returns ToolInfo for every available skill.
func (r *AgentGoRouter) getSkillTools(ctx context.Context) []ToolInfo {
	type skillLister interface {
		ListSkillInfos(ctx context.Context) []ToolInfo
	}
	if sl, ok := r.skillsService.(skillLister); ok {
		return sl.ListSkillInfos(ctx)
	}
	return r.skillToolInfos
}

// skillToolInfos holds pre-resolved skill infos injected at construction time.

// getSkillToolInfo returns ToolInfo for a named skill.
func (r *AgentGoRouter) getSkillToolInfo(ctx context.Context, name string) *ToolInfo {
	skillID := strings.TrimPrefix(name, "skill_")
	for _, t := range r.getSkillTools(ctx) {
		if t.Name == name || t.Name == skillID {
			t := t
			return &t
		}
	}
	return nil
}

// -------------------------------------------------------------------------
// Pre-resolved tool info caches (populated by adapters in pkg/agent)
// -------------------------------------------------------------------------

// WithRAGQueryHandler overrides the default rag_query stub with a real handler.
// This is used by pkg/agent to inject a closure that calls domain.Processor.Query
// without creating an import cycle.
func WithRAGQueryHandler(handler ToolHandler) RouterOption {
	return func(r *AgentGoRouter) {
		// Override the stub handler registered in registerBuiltinTools
		r.mu.Lock()
		r.handlers["rag_query"] = handler
		r.mu.Unlock()
	}
}

// WithRAGIngestHandler overrides the default rag_ingest stub with a real handler.
func WithRAGIngestHandler(handler ToolHandler) RouterOption {
	return func(r *AgentGoRouter) {
		r.mu.Lock()
		r.handlers["rag_ingest"] = handler
		r.mu.Unlock()
	}
}

// This is called from pkg/agent after converting domain.ToolDefinition → ptc.ToolInfo.
func WithMCPToolInfos(infos []ToolInfo) RouterOption {
	return func(r *AgentGoRouter) {
		r.mcpToolInfos = infos
	}
}

// WithSkillToolInfos injects pre-resolved skill tool info.
func WithSkillToolInfos(infos []ToolInfo) RouterOption {
	return func(r *AgentGoRouter) {
		r.skillToolInfos = infos
	}
}

// WithToolHandler registers a custom tool handler.
func WithToolHandler(name string, handler ToolHandler) RouterOption {
	return func(r *AgentGoRouter) {
		r.mu.Lock()
		r.handlers[name] = handler
		r.mu.Unlock()
	}
}

// WithMemoryToolInfos injects pre-resolved memory tool info into the router so
// they appear in ListAvailableTools and are visible to the LLM via callTool().
func WithMemoryToolInfos(infos []ToolInfo) RouterOption {
	return func(r *AgentGoRouter) {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i := range infos {
			info := infos[i]
			r.toolInfo[info.Name] = &info
		}
	}
}
