package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/resource"
	"github.com/liliang-cn/agent-go/v3/pkg/search"
)

// ToolHandler executes a tool call synchronously.
type ToolHandler func(ctx context.Context, args map[string]interface{}) (interface{}, error)

type ToolMetadata struct {
	ReadOnly          bool
	ConcurrencySafe   bool
	Destructive       bool
	InterruptBehavior string
	ExposureMode      ToolExposureMode
}

type ToolExposureMode string

const (
	ToolExposureDual       ToolExposureMode = "dual"
	ToolExposureDirectOnly ToolExposureMode = "direct_only"
	ToolExposureCodeOnly   ToolExposureMode = "code_only"
)

type ToolExecutionPolicy struct {
	Default ToolExposureMode
	Rules   map[string]ToolExposureMode
}

// Tool categories used by ToolRegistry.
const (
	CategoryCustom = "custom" // user-registered via AddTool()
	CategoryRAG    = "rag"    // rag_query, rag_ingest
	CategoryMemory = "memory" // memory_save/recall/update/delete
	CategorySkill  = "skill"  // skill tools
	CategoryMCP    = "mcp"    // MCP tools (dynamic; servers may change at runtime)
)

type registeredTool struct {
	def      domain.ToolDefinition
	handler  ToolHandler
	category string
	metadata ToolMetadata
}

// ToolRegistry is the single source of truth for tool definitions and handlers.
//
// All modules (custom, RAG, Memory) register here, so tool dispatch has one
// source of truth instead of a per-module list.
type ToolRegistry struct {
	mu               sync.RWMutex
	tools            map[string]*registeredTool
	sessionActivated map[string]map[string]bool // sessionID -> toolName -> bool
	policy           ToolExecutionPolicy
	// deferPatterns names the tools this install keeps behind the index.
	deferPatterns []string
}

// NewToolRegistry creates an empty ToolRegistry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:            make(map[string]*registeredTool),
		sessionActivated: make(map[string]map[string]bool),
	}
}

func (r *ToolRegistry) SetExecutionPolicy(policy ToolExecutionPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policy = policy
	for name, tool := range r.tools {
		tool.metadata.ExposureMode = r.resolveExposureModeLocked(name, tool.category, tool.metadata.ExposureMode)
	}
}

// ActivateForSession marks a deferred tool as active for the given session.
func (r *ToolRegistry) ActivateForSession(sessionID, toolName string) {
	if sessionID == "" || toolName == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessionActivated[sessionID]; !ok {
		r.sessionActivated[sessionID] = make(map[string]bool)
	}
	r.sessionActivated[sessionID][toolName] = true
}

// IsActivatedForSession reports whether a deferred tool has been activated for the given session.
func (r *ToolRegistry) IsActivatedForSession(sessionID, toolName string) bool {
	if sessionID == "" || toolName == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	activeMap := r.sessionActivated[sessionID]
	return activeMap != nil && activeMap[toolName]
}

// Register adds (or replaces) a tool. Tools registered here are:
//   - Visible to the LLM

// SearchAllTools searches ALL registered tools (not just deferred ones)
func (r *ToolRegistry) SearchAllTools(query string) []domain.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	query = strings.ToLower(query)
	keywords := strings.Fields(query)
	var matches []domain.ToolDefinition

	for _, t := range r.tools {
		name := strings.ToLower(t.def.Function.Name)
		desc := strings.ToLower(t.def.Function.Description)

		matched := false
		for _, kw := range keywords {
			if strings.Contains(name, kw) || strings.Contains(desc, kw) {
				matched = true
				break
			}
		}

		if matched {
			matches = append(matches, t.def)
		}
	}
	return matches
}

// Register adds (or replaces) a tool. The tool will be:
//   - returned by ListForLLM(false) for native function calling

func (r *ToolRegistry) Register(def domain.ToolDefinition, handler ToolHandler, category string) {
	r.RegisterWithMetadata(def, handler, category, ToolMetadata{})
}

// RegisterWithMetadata adds a tool plus execution metadata used by runtime orchestration.
func (r *ToolRegistry) RegisterWithMetadata(def domain.ToolDefinition, handler ToolHandler, category string, metadata ToolMetadata) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if metadata.ExposureMode == "" {
		metadata.ExposureMode = r.resolveExposureModeLocked(def.Function.Name, category, "")
	}
	if matchesAnyToolPattern(r.deferPatterns, def.Function.Name) {
		def.DeferLoading = true
	}
	r.tools[def.Function.Name] = &registeredTool{
		def:      def,
		handler:  handler,
		category: category,
		metadata: metadata,
	}
}

// SetDeferredPatterns tells the registry which of its tools this install keeps
// behind the tool index, as exact names or "prefix*".
//
// Applied at the registry rather than per turn, and that is the whole point:
// DeferLoading is what the tool search filters on, so a tool deferred any other
// way is not merely absent from the schema — it cannot be found at all. That
// was the first version of this feature, and it produced a model that searched,
// found nothing, and fell back to the wrong tool.
//
// Retroactive, because tools register in an order the caller does not control.
func (r *ToolRegistry) SetDeferredPatterns(patterns []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deferPatterns = patterns
	for _, t := range r.tools {
		if matchesAnyToolPattern(patterns, t.def.Function.Name) {
			t.def.DeferLoading = true
		}
	}
}

func matchesAnyToolPattern(patterns []string, name string) bool {
	for _, p := range patterns {
		if toolPolicyPatternMatches(p, name) {
			return true
		}
	}
	return false
}

func (r *ToolRegistry) resolveExposureModeLocked(name, category string, explicit ToolExposureMode) ToolExposureMode {
	if explicit != "" {
		return explicit
	}
	for pattern, mode := range r.policy.Rules {
		if toolPolicyPatternMatches(pattern, name) {
			return mode
		}
	}
	if r.policy.Default != "" {
		return r.policy.Default
	}
	return defaultToolExposureMode(category)
}

func toolPolicyPatternMatches(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	name = strings.TrimSpace(name)
	if pattern == "" || name == "" {
		return false
	}
	if pattern == name {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

func defaultToolExposureMode(category string) ToolExposureMode {
	switch category {
	case CategoryMemory:
		return ToolExposureDirectOnly
	case CategoryRAG, CategoryMCP, CategorySkill:
		return ToolExposureCodeOnly
	default:
		return ToolExposureDual
	}
}

// Unregister removes a tool from the registry.
func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Has reports whether a tool is registered.
// Names returns every registered tool name.
func (r *ToolRegistry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

func (r *ToolRegistry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// CategoryOf returns the category of a registered tool, or "" if not found.
func (r *ToolRegistry) CategoryOf(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.tools[name]; ok {
		return t.category
	}
	return ""
}

// MetadataOf returns execution metadata for a registered tool.
func (r *ToolRegistry) MetadataOf(name string) ToolMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.tools[name]; ok {
		return t.metadata
	}
	return ToolMetadata{}
}

func (r *ToolRegistry) Resources() []resource.Resource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]resource.Resource, 0, len(r.tools))
	for _, tool := range r.tools {
		out = append(out, resource.Resource{
			ID:          "tool:" + tool.def.Function.Name,
			Kind:        resource.KindTool,
			Name:        tool.def.Function.Name,
			Description: tool.def.Function.Description,
			Execution:   resource.ExecutionMode(tool.metadata.ExposureMode),
			Metadata: map[string]any{
				"category":           tool.category,
				"read_only":          tool.metadata.ReadOnly,
				"concurrency_safe":   tool.metadata.ConcurrencySafe,
				"destructive":        tool.metadata.Destructive,
				"interrupt_behavior": tool.metadata.InterruptBehavior,
			},
		})
	}
	return out
}

// DefinitionOf returns the registered tool definition, if present.
func (r *ToolRegistry) DefinitionOf(name string) (domain.ToolDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.tools[name]; ok {
		return t.def, true
	}
	return domain.ToolDefinition{}, false
}

// ListForLLM returns the tool definitions that should be passed to the LLM.
//
//	function calls; code_only tools move behind execute_javascript/callTool.
func (r *ToolRegistry) ListForLLM(sessionID string) []domain.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	activeMap := r.sessionActivated[sessionID]

	out := make([]domain.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		// Include if not deferred, or if explicitly activated for this session
		if !t.def.DeferLoading || (activeMap != nil && activeMap[t.def.Function.Name]) {
			out = append(out, t.def)
		}
	}
	return out
}

// ListDeferredTools returns all deferred tool definitions currently registered.
func (r *ToolRegistry) ListDeferredTools() []domain.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		if t.def.DeferLoading {
			out = append(out, t.def)
		}
	}
	return out
}

// Call dispatches a tool call to the registered handler.
func (r *ToolRegistry) Call(ctx context.Context, name string, args map[string]interface{}) (interface{}, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tool %q not found in registry", name)
	}
	if tool.handler == nil {
		return nil, fmt.Errorf("tool %q has no handler", name)
	}
	return tool.handler(ctx, args)
}

// GetToolSearchTools returns the tool search tool definitions
func GetToolSearchTools() []domain.ToolDefinition {
	return []domain.ToolDefinition{
		{
			Type: "tool_search_tool_regex_20251119",
			Function: domain.ToolFunction{
				Name:        "tool_search_tool_regex",
				Description: "Search for tools by regex pattern. Use when you need to find specific tools among many available tools. Query should be a regex pattern like 'weather' or 'get_.*_data'",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "Regex pattern to search tool names and descriptions",
						},
					},
					"required": []string{"query"},
				},
			},
		},
		{
			Type: "tool_search_tool_bm25_20251119",
			Function: domain.ToolFunction{
				Name:        "tool_search_tool_bm25",
				Description: "Search for tools using natural language. Use when you need to find relevant tools based on semantic meaning. Describe what you want to do in plain language.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "Natural language query to search tools",
						},
					},
					"required": []string{"query"},
				},
			},
		},
	}
}

// ExecuteToolSearch executes a tool search and returns the results
func (r *ToolRegistry) ExecuteToolSearch(query, searchType string) ([]domain.ToolDefinition, error) {
	var results []domain.ToolDefinition

	if searchType == "bm25" {
		results = r.SearchDeferredToolsBM25(query)
	} else {
		// Regex search - treat the query as a regex pattern
		results = r.SearchDeferredToolsRegex(query)
	}

	// Limit results
	maxResults := 5
	if len(results) > maxResults {
		results = results[:maxResults]
	}

	return results, nil
}

// SearchDeferredToolsBM25 searches deferred tools using BM25 over tool names and descriptions.
func (r *ToolRegistry) SearchDeferredToolsBM25(query string) []domain.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if strings.TrimSpace(query) == "" {
		return nil
	}

	documents := make([]search.Document, 0, len(r.tools))
	defsByName := make(map[string]domain.ToolDefinition, len(r.tools))
	for _, tool := range r.tools {
		if !tool.def.DeferLoading {
			continue
		}
		name := tool.def.Function.Name
		defsByName[name] = tool.def
		documents = append(documents, search.Document{
			ID: name,
			Text: strings.Join([]string{
				name,
				strings.ReplaceAll(name, "_", " "),
				tool.def.Function.Description,
			}, " "),
		})
	}

	scored := search.Rank(query, documents, 5, nil)
	results := make([]domain.ToolDefinition, 0, len(scored))
	for _, item := range scored {
		if def, ok := defsByName[item.ID]; ok {
			results = append(results, def)
		}
	}
	return results
}

// SearchDeferredToolsRegex searches deferred tools using regex pattern matching
func (r *ToolRegistry) SearchDeferredToolsRegex(pattern string) []domain.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var matches []domain.ToolDefinition

	// Try to compile as regex first
	_, err := regexp.Compile("(?i)" + pattern)
	isRegex := err == nil

	for _, t := range r.tools {
		if !t.def.DeferLoading {
			continue
		}

		name := t.def.Function.Name
		desc := t.def.Function.Description

		var matched bool
		if isRegex {
			// Use regex matching
			if regexp.MustCompile("(?i)"+pattern).MatchString(name) ||
				regexp.MustCompile("(?i)"+pattern).MatchString(desc) {
				matched = true
			}
		} else {
			// Fall back to keyword matching
			patternLower := strings.ToLower(pattern)
			nameLower := strings.ToLower(name)
			descLower := strings.ToLower(desc)
			if strings.Contains(nameLower, patternLower) ||
				strings.Contains(descLower, patternLower) {
				matched = true
			}
		}

		if matched {
			matches = append(matches, t.def)
		}
	}

	return matches
}
