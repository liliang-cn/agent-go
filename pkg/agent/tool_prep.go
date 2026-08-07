// Tool preparation for the loop — concept 3 of the seven.
//
// Deciding what a turn may call: collecting tools from the registry, MCP and
// skills, applying the run's allow/deny lists and its resolved constraints,
// the skill-first policy, and the generation options that go out alongside
// them. The loop asks "what can this turn use?"; this file answers.
package agent

import (
	"context"
	"slices"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/skills"
)

// Tool preparation for the loop — concept 3 of the seven.
//
// Deciding which tools a turn may use: collecting them from the registry, MCP
// and skills, applying the run's allow/deny lists and constraints, the
// skill-first policy, and the per-turn generation options that go out with
// them. The loop asks for a tool list; this file answers.

func (s *Service) buildToolPreparationPolicy(ctx context.Context) toolPreparationPolicy {
	policy := toolPreparationPolicy{
		SearchMode:          s.shouldExposeSearchTools(),
		ExposeSearchTools:   s.shouldExposeSearchTools(),
		HideNativeWebSearch: s.shouldHideMCPWebSearchTools(),
	}
	if session := getCurrentSession(ctx); session != nil {
		policy.SessionID = strings.TrimSpace(session.GetID())
		policy.TaskID = currentTaskID(session)
	}
	if policy.SessionID == "" {
		policy.SessionID = s.CurrentSessionID()
	}
	policy.RelevantSkillNames = s.relevantSkillsForSession(policy.SessionID)
	if len(policy.RelevantSkillNames) > 0 && !s.isRelevantSkillSatisfied(policy.SessionID, policy.TaskID) {
		policy.ForceSkillFirst = true
	}
	return policy
}

func shouldKeepToolForSkillFirst(toolName string, relevantSkillNames []string) bool {
	toolName = strings.TrimSpace(toolName)
	switch {
	case toolName == "":
		return false
	case isTaskTerminalToolName(toolName):
		return true
	case toolName == "search_available_tools" || domain.IsToolSearchTool(toolName):
		return true
	case strings.HasPrefix(toolName, "skill_"):
		skillID := strings.TrimPrefix(toolName, "skill_")
		return len(relevantSkillNames) == 0 || slices.Contains(relevantSkillNames, skillID)
	default:
		return false
	}
}

// prepareTurnInputsWithConfig is the single place where a turn's tool list and
// message list are assembled. cfg may be nil; when it carries an allow/deny
// list the tool surface is narrowed accordingly, which is how a sub-agent run
// gets a restricted toolset without a separate execution path.
func (s *Service) prepareTurnInputsWithConfig(ctx context.Context, currentAgent *Agent, messages []domain.Message, goal string, cfg *RunConfig) ([]domain.ToolDefinition, []domain.Message) {
	s.syncDiscoveredToolsFromHistory(messages, "")
	policy := s.buildToolPreparationPolicy(ctx)
	// A run that forbids tools must not assemble a tool catalogue at all — the
	// list feeds the prompt as well as the request.
	if cfg != nil && cfg.resolvedConstraints != nil && cfg.resolvedConstraints.ForbidTools {
		policy.ExposeSearchTools = false
	}
	tools := s.collectAllAvailableToolsWithPolicy(ctx, currentAgent, policy)
	if cfg != nil && (len(cfg.ToolAllowlist) > 0 || len(cfg.ToolDenylist) > 0) {
		tools = filterTools(tools, cfg.ToolAllowlist, cfg.ToolDenylist)
	}
	// A user who refused tool use is obeyed by withholding the tools, not by
	// asking the model to resist what is attached to its request. Nothing
	// survives — not the terminal signals, not search_available_tools; the loop
	// still terminates on a plain text turn.
	//
	// The decision comes from the run's resolved constraints (see
	// constraints.go), never from matching phrases in the goal.
	if cfg != nil && cfg.resolvedConstraints != nil && cfg.resolvedConstraints.ForbidTools {
		tools = nil
	}

	systemMsg := s.buildSystemPromptForRun(ctx, currentAgent, cfg)
	if cfg != nil && strings.TrimSpace(cfg.SystemPromptOverride) != "" {
		systemMsg = cfg.SystemPromptOverride
	}
	genMessages := append([]domain.Message{{Role: "system", Content: systemMsg}}, messages...)
	return tools, genMessages
}

// collectAllAvailableTools collects tools from MCP, Skills, RAG, and Agent Handoffs.
// the LLM must call them through execute_javascript + callTool(), mirroring Anthropic's
// allowed_callers: ["code_execution"] behaviour where direct model invocation is removed.
func (s *Service) collectAllAvailableTools(ctx context.Context, currentAgent *Agent) []domain.ToolDefinition {
	return s.collectAllAvailableToolsWithPolicy(ctx, currentAgent, s.buildToolPreparationPolicy(ctx))
}

func (s *Service) collectAllAvailableToolsWithPolicy(ctx context.Context, currentAgent *Agent, policy toolPreparationPolicy) []domain.ToolDefinition {
	toolsMap := make(map[string]domain.ToolDefinition)
	sessionID := policy.SessionID
	searchMode := policy.SearchMode
	relevantSkillNames := policy.RelevantSkillNames
	hasRelevantSkillFilter := len(relevantSkillNames) > 0

	// Helper to add tools with deduplication
	addTools := func(defs []domain.ToolDefinition) {
		for _, d := range defs {
			if policy.ForceSkillFirst && !shouldKeepToolForSkillFirst(d.Function.Name, relevantSkillNames) {
				continue
			}
			toolsMap[d.Function.Name] = d
		}
	}

	// 1. Add static tools and active deferred tools from Registry
	// This includes built-in tools like delegate_to_subagent and task_complete
	addTools(s.toolRegistry.ListForLLM(sessionID))

	// In saving mode, expose search tools instead of sending large MCP/skill catalogs directly.
	if policy.ExposeSearchTools {
		for _, ts := range GetToolSearchTools() {
			toolsMap[ts.Function.Name] = ts
		}
	}

	if currentAgent != nil {
		// Per-agent custom tools (e.g. tools added directly to an Agent in multi-agent
		{
			for _, def := range currentAgent.Tools() {
				// Skip if already in registry (AddTool registers in both places).
				if !s.toolRegistry.Has(def.Function.Name) {
					toolsMap[def.Function.Name] = def
				}
			}
		}
	}

	// MCP tools — dynamic (servers may change at runtime).
	if s.mcpService != nil {
		allMCP := s.mcpService.ListTools()
		activeMap := s.toolRegistry.sessionActivated[sessionID]
		deferAllMCP := searchMode
		hideNativeWebSearchTools := policy.HideNativeWebSearch

		if currentAgent == nil || isAllAllowed(currentAgent.mcpTools) {
			for _, tool := range allMCP {
				if hideNativeWebSearchTools && isMCPWebSearchToolName(tool.Function.Name) {
					continue
				}
				if !deferAllMCP || (activeMap != nil && activeMap[tool.Function.Name]) {
					// Set DeferLoading based on whether we're deferring
					t := tool
					if deferAllMCP {
						t.DeferLoading = true
					}
					addTools([]domain.ToolDefinition{t})
				}
			}
		} else {
			for _, tool := range allMCP {
				if hideNativeWebSearchTools && isMCPWebSearchToolName(tool.Function.Name) {
					continue
				}
				if containsStr(currentAgent.mcpTools, tool.Function.Name) {
					if !deferAllMCP || (activeMap != nil && activeMap[tool.Function.Name]) {
						// Set DeferLoading based on whether we're deferring
						t := tool
						if deferAllMCP {
							t.DeferLoading = true
						}
						addTools([]domain.ToolDefinition{t})
					}
				}
			}
		}
	}

	// Skills tools — dynamic.
	if s.skillsService != nil {
		skillsList, _ := s.skillsService.ListSkills(ctx, skills.SkillFilter{})
		activeMap := s.toolRegistry.sessionActivated[sessionID]
		deferAllSkills := searchMode

		allowedAll := currentAgent == nil || isAllAllowed(currentAgent.skills)
		for _, sk := range skillsList {
			// Skip if disabled or explicitly hidden from model invocation
			if !sk.Enabled || sk.DisableModelInvocation {
				continue
			}
			if hasRelevantSkillFilter && !slices.Contains(relevantSkillNames, sk.ID) {
				continue
			}

			if allowedAll || containsStr(currentAgent.skills, sk.ID) {
				toolName := "skill_" + sk.ID
				if !deferAllSkills || (activeMap != nil && (activeMap[toolName] || activeMap[sk.ID])) {
					// Build variable schema from skill definition
					properties := make(map[string]interface{})
					required := make([]string, 0)
					for _, v := range sk.Variables {
						prop := map[string]interface{}{
							"type":        getSkillVarTypeString(v.Type),
							"description": v.Description,
						}
						if v.Default != nil {
							prop["default"] = v.Default
						}
						properties[v.Name] = prop
						if v.Required {
							required = append(required, v.Name)
						}
					}

					desc := sk.Description
					if desc == "" {
						desc = sk.Name
					}
					// Clarify that calling this skill returns its workflow instructions.
					desc = "Skill workflow: " + desc + ". Call this tool to receive step-by-step instructions for this task; you MUST then follow those instructions to complete the work."

					// Use "skill_" prefix to match RegisterAsMCPTools and isSkill check
					// Set DeferLoading based on whether we're deferring skills
					deferLoading := deferAllSkills
					toolsMap[toolName] = domain.ToolDefinition{
						Type:         "function",
						DeferLoading: deferLoading,
						Function: domain.ToolFunction{
							Name:        toolName,
							Description: desc,
							Parameters: map[string]interface{}{
								"type":       "object",
								"properties": properties,
								"required":   required,
							},
						},
					}
				}
			}
		}
	}

	// 4. Convert map back to slice
	tools := make([]domain.ToolDefinition, 0, len(toolsMap))
	for _, tool := range toolsMap {
		tools = append(tools, tool)
	}

	return tools
}

func (s *Service) shouldExposeSearchTools() bool {
	if s == nil || s.cfg == nil {
		return false
	}
	return s.cfg.Tooling.SavingMode && s.cfg.Tooling.EnableSearchTools
}

func (s *Service) webSearchMode() domain.WebSearchMode {
	if s == nil || s.cfg == nil {
		return domain.WebSearchModeMCP
	}
	return domain.NormalizeWebSearchMode(domain.WebSearchMode(s.cfg.Tooling.WebSearch.Mode))
}

func (s *Service) webSearchContextSize() string {
	if s == nil || s.cfg == nil {
		return "medium"
	}
	return domain.NormalizeWebSearchContextSize(s.cfg.Tooling.WebSearch.SearchContextSize)
}

func (s *Service) shouldHideMCPWebSearchTools() bool {
	mode := s.webSearchMode()
	return mode == domain.WebSearchModeNative || mode == domain.WebSearchModeOff
}

func isMCPWebSearchToolName(name string) bool {
	return strings.HasPrefix(name, "mcp_websearch_")
}

func (s *Service) toolGenerationOptions(temperature float64, maxTokens int, toolChoice string) *domain.GenerationOptions {
	opts := &domain.GenerationOptions{
		Temperature:          temperature,
		MaxTokens:            maxTokens,
		WebSearchMode:        s.webSearchMode(),
		WebSearchContextSize: s.webSearchContextSize(),
	}
	if toolChoice != "" {
		opts.ToolChoice = toolChoice
	}
	// Forward the per-run Thinking knob (set via WithThinking) onto
	// every tool round so DeepSeek/reasoner providers see a consistent
	// setting throughout the task.
	if t := s.currentThinkingOptions(); t != nil {
		opts.Thinking = t
	}
	// Forward the per-run ResponseFormat (set via WithStructuredOutput).
	// Tool calls bypass response_format on the provider side; this is
	// only enforced when the model emits a text response.
	if rf := s.currentResponseFormat(); rf != nil {
		opts.ResponseFormat = rf
	}
	return opts
}

// currentResponseFormat returns the run-scoped ResponseFormat if one was
// set by the runtime (from RunConfig.StructuredOutput). Nil = provider
// default.
func (s *Service) currentResponseFormat() *domain.ResponseFormat {
	if s == nil {
		return nil
	}
	s.responseFormatMu.RLock()
	defer s.responseFormatMu.RUnlock()
	return s.responseFormat
}

// setCurrentResponseFormat is called by the runtime at the start of a
// run to push RunConfig.StructuredOutput onto the service, and again
// (with nil) at the end to clear it.
func (s *Service) setCurrentResponseFormat(rf *domain.ResponseFormat) {
	if s == nil {
		return
	}
	s.responseFormatMu.Lock()
	s.responseFormat = rf
	s.responseFormatMu.Unlock()
}

// currentThinkingOptions returns the run-scoped ThinkingOptions if
// one was set by the runtime (from RunConfig.Thinking). Nil = provider
// default.
func (s *Service) currentThinkingOptions() *domain.ThinkingOptions {
	if s == nil {
		return nil
	}
	s.thinkingMu.RLock()
	defer s.thinkingMu.RUnlock()
	return s.thinkingOpts
}

// setCurrentThinkingOptions is called by the runtime at the start of a
// run to push the RunConfig.Thinking knob onto the service, and again
// (with nil) at the end to clear it.
func (s *Service) setCurrentThinkingOptions(t *domain.ThinkingOptions) {
	if s == nil {
		return
	}
	s.thinkingMu.Lock()
	s.thinkingOpts = t
	s.thinkingMu.Unlock()
}

func (s *Service) buildToolCatalogSummary(ctx context.Context) string {
	if !s.shouldExposeSearchTools() {
		return ""
	}

	var lines []string

	if s.mcpService != nil {
		serverNames := make([]string, 0)
		seenServers := make(map[string]struct{})
		for _, tool := range s.mcpService.ListTools() {
			parts := strings.SplitN(tool.Function.Name, "_", 3)
			if len(parts) < 3 || parts[0] != "mcp" {
				continue
			}
			server := parts[0] + "_" + parts[1]
			if _, ok := seenServers[server]; ok {
				continue
			}
			seenServers[server] = struct{}{}
			serverNames = append(serverNames, server)
		}
		slices.Sort(serverNames)
		if len(serverNames) > 0 {
			lines = append(lines, "- MCP servers available: "+strings.Join(serverNames, ", "))
		}
	}

	if s.skillsService != nil {
		skillsList, _ := s.skillsService.ListSkills(ctx, skills.SkillFilter{})
		skillNames := make([]string, 0, len(skillsList))
		for _, sk := range skillsList {
			if !sk.Enabled || sk.DisableModelInvocation {
				continue
			}
			skillNames = append(skillNames, sk.ID)
		}
		slices.Sort(skillNames)
		if len(skillNames) > 0 {
			lines = append(lines, "- Skills available: "+strings.Join(skillNames, ", "))
		}
	}

	toolHints := make([]string, 0)
	for _, name := range s.toolRegistry.Names() {
		if name == "search_available_tools" || domain.IsToolSearchTool(name) {
			continue
		}
		if strings.HasPrefix(name, "mcp_") || strings.HasPrefix(name, "skill_") {
			continue
		}
		toolHints = append(toolHints, name)
	}
	slices.Sort(toolHints)
	if len(toolHints) > 0 {
		if len(toolHints) > 12 {
			toolHints = toolHints[:12]
		}
		lines = append(lines, "- Built-in tool names you can search for: "+strings.Join(toolHints, ", "))
	}

	if len(lines) == 0 {
		return ""
	}

	return "Search-mode tool catalog:\n" +
		"- Tool schemas are minimized to save tokens.\n" +
		"- Use search tools when you need an exact callable name.\n" +
		strings.Join(lines, "\n")
}

// isMCPTool checks if a tool name is from MCP
func (s *Service) isMCPTool(name string) bool {
	if s.mcpService == nil {
		return false
	}
	for _, tool := range s.mcpService.ListTools() {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

// isSkill checks if a tool name is a skill
func (s *Service) isSkill(ctx context.Context, name string) bool {
	if s.skillsService == nil {
		return false
	}
	// Remove "skill_" prefix if present
	skillID := strings.TrimPrefix(name, "skill_")
	skills, _ := s.skillsService.ListSkills(ctx, skills.SkillFilter{})
	for _, sk := range skills {
		if sk.ID == skillID {
			return true
		}
	}
	return false
}

// collectAvailableTools collects tools from all available sources
func collectAvailableTools(mcpService MCPToolExecutor, ragProcessor domain.Processor, skillsService *skills.Service) []domain.ToolDefinition {
	tools := []domain.ToolDefinition{}

	// Add RAG tools
	if ragProcessor != nil {
		tools = append(tools, domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        "rag_query",
				Description: "Query the RAG system to retrieve relevant document chunks",
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
			},
		})

		tools = append(tools, domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        "rag_ingest",
				Description: "Ingest a document into the RAG system",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content": map[string]interface{}{
							"type":        "string",
							"description": "The document content",
						},
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "Path to the document file",
						},
					},
				},
			},
		})
	}

	// Add Skills tools
	if skillsService != nil {
		skillTools, err := skillsService.RegisterAsMCPTools()
		if err == nil {
			tools = append(tools, skillTools...)
		}
	}

	// Add MCP tools
	if mcpService != nil {
		mcpTools := mcpService.ListTools()
		tools = append(tools, mcpTools...)
	}

	return tools
}
