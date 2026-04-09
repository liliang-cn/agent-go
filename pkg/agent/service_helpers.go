package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

const sessionContextSentSkillReminders = "skills.sent_relevant_names"

type skillReminder struct {
	Names []string
	Text  string
}

type toolPreparationPolicy struct {
	SessionID           string
	TaskID              string
	PTCEnabled          bool
	SearchMode          bool
	ExposeSearchTools   bool
	HideNativeWebSearch bool
	RelevantSkillNames  []string
	ForceSkillFirst     bool
}

func (s *Service) resolveCurrentAgent(session *Session) *Agent {
	currentAgent := s.agent
	if session != nil && session.AgentID != "" && s.registry != nil {
		if agent, ok := s.registry.GetAgent(session.AgentID); ok {
			currentAgent = agent
		}
	}
	return currentAgent
}

func currentTaskID(session *Session) string {
	if session == nil {
		return ""
	}
	if value, ok := session.GetContext(sessionContextTaskID); ok {
		if taskID, ok := value.(string); ok {
			return strings.TrimSpace(taskID)
		}
	}
	return ""
}

func ensureTaskID(session *Session, cfg *RunConfig) string {
	if cfg != nil && strings.TrimSpace(cfg.TaskID) != "" {
		taskID := strings.TrimSpace(cfg.TaskID)
		if session != nil {
			session.SetContext(sessionContextTaskID, taskID)
		}
		return taskID
	}
	if existing := currentTaskID(session); existing != "" {
		if cfg != nil {
			cfg.TaskID = existing
		}
		return existing
	}
	taskID := uuid.NewString()
	if cfg != nil {
		cfg.TaskID = taskID
	}
	if session != nil {
		session.SetContext(sessionContextTaskID, taskID)
	}
	return taskID
}

func withTaskID(msg domain.Message, taskID string) domain.Message {
	msg.TaskID = strings.TrimSpace(taskID)
	return msg
}

func (s *Service) rememberRelevantSkillsForSession(sessionID string, names []string) {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	trimmed := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(trimmed, name) {
			continue
		}
		trimmed = append(trimmed, name)
	}

	s.relevantSkillsMu.Lock()
	defer s.relevantSkillsMu.Unlock()
	if s.sessionRelevantSkills == nil {
		s.sessionRelevantSkills = make(map[string][]string)
	}
	s.sessionRelevantSkills[sessionID] = trimmed
}

func (s *Service) relevantSkillsForSession(sessionID string) []string {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	s.relevantSkillsMu.RLock()
	defer s.relevantSkillsMu.RUnlock()
	names := s.sessionRelevantSkills[sessionID]
	if len(names) == 0 {
		return nil
	}
	return append([]string(nil), names...)
}

func skillPolicyKey(sessionID, taskID string) string {
	sessionID = strings.TrimSpace(sessionID)
	taskID = strings.TrimSpace(taskID)
	if sessionID == "" {
		return ""
	}
	if taskID == "" {
		return sessionID
	}
	return sessionID + "::" + taskID
}

func (s *Service) markRelevantSkillSatisfied(sessionID, taskID string) {
	key := skillPolicyKey(sessionID, taskID)
	if key == "" {
		return
	}
	s.skillPolicyMu.Lock()
	defer s.skillPolicyMu.Unlock()
	if s.taskSkillSatisfied == nil {
		s.taskSkillSatisfied = make(map[string]bool)
	}
	s.taskSkillSatisfied[key] = true
}

func (s *Service) isRelevantSkillSatisfied(sessionID, taskID string) bool {
	key := skillPolicyKey(sessionID, taskID)
	if key == "" {
		return false
	}
	s.skillPolicyMu.RLock()
	defer s.skillPolicyMu.RUnlock()
	return s.taskSkillSatisfied[key]
}

func (s *Service) buildToolPreparationPolicy(ctx context.Context) toolPreparationPolicy {
	policy := toolPreparationPolicy{
		PTCEnabled:          s.isPTCEnabled(),
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
	case toolName == "task_complete":
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

func (s *Service) prepareTurnInputs(ctx context.Context, currentAgent *Agent, messages []domain.Message, goal string) ([]domain.ToolDefinition, []domain.Message) {
	s.syncDiscoveredToolsFromHistory(messages, "")
	tools := s.collectAllAvailableToolsWithPolicy(ctx, currentAgent, s.buildToolPreparationPolicy(ctx))
	if looksLikeInformationSeekingQuery(goal) {
		tools = filterToolDefinitions(tools, func(tool domain.ToolDefinition) bool {
			return tool.Function.Name != "memory_save"
		})
	}

	systemMsg := s.buildSystemPrompt(ctx, currentAgent)
	genMessages := append([]domain.Message{{Role: "system", Content: systemMsg}}, messages...)
	return tools, genMessages
}

func (s *Service) syncDiscoveredToolsFromHistory(messages []domain.Message, summary string) {
	if s == nil || s.toolRegistry == nil {
		return
	}
	sessionID := s.CurrentSessionID()
	if sessionID == "" {
		return
	}
	for _, name := range extractDiscoveredToolNames(messages, summary) {
		s.toolRegistry.ActivateForSession(sessionID, name)
	}
}

// addRAGSources adds sources with deduplication by ID
func (s *Service) addRAGSources(sources []domain.Chunk) {
	if len(sources) == 0 {
		return
	}
	s.ragSourcesMu.Lock()
	defer s.ragSourcesMu.Unlock()

	// Build map of existing IDs
	existing := make(map[string]bool)
	for _, src := range s.ragSources {
		existing[src.ID] = true
	}

	// Add only new sources
	for _, src := range sources {
		if !existing[src.ID] {
			s.ragSources = append(s.ragSources, src)
			existing[src.ID] = true
		}
	}
}

// collectAllAvailableTools collects tools from MCP, Skills, RAG, and Agent Handoffs.
// When PTC is enabled, RAG/MCP/Skills are NOT exposed as direct function-call tools —
// the LLM must call them through execute_javascript + callTool(), mirroring Anthropic's
// allowed_callers: ["code_execution"] behaviour where direct model invocation is removed.
func (s *Service) collectAllAvailableTools(ctx context.Context, currentAgent *Agent) []domain.ToolDefinition {
	return s.collectAllAvailableToolsWithPolicy(ctx, currentAgent, s.buildToolPreparationPolicy(ctx))
}

func (s *Service) collectAllAvailableToolsWithPolicy(ctx context.Context, currentAgent *Agent, policy toolPreparationPolicy) []domain.ToolDefinition {
	toolsMap := make(map[string]domain.ToolDefinition)
	ptcEnabled := policy.PTCEnabled
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
	addTools(s.toolRegistry.ListForLLM(ptcEnabled, sessionID))

	// In saving mode, expose search tools instead of sending large MCP/skill catalogs directly.
	if policy.ExposeSearchTools && !ptcEnabled {
		for _, ts := range GetToolSearchTools() {
			toolsMap[ts.Function.Name] = ts
		}
	}

	// Agent Handoffs — always visible so the LLM can route between agents.
	if currentAgent != nil {
		for _, handoff := range currentAgent.Handoffs() {
			if policy.ForceSkillFirst {
				continue
			}
			tool := handoff.ToToolDefinition().ToDomainTool()
			toolsMap[tool.Function.Name] = tool
		}
		// Per-agent custom tools (e.g. tools added directly to an Agent in multi-agent
		// scenarios) — hidden when PTC is enabled.
		if !ptcEnabled {
			for _, def := range currentAgent.Tools() {
				// Skip if already in registry (AddTool registers in both places).
				if !s.toolRegistry.Has(def.Function.Name) {
					toolsMap[def.Function.Name] = def
				}
			}
		}
	}

	// MCP tools — dynamic (servers may change at runtime); hidden in PTC mode.
	if s.mcpService != nil && !ptcEnabled {
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

	// Skills tools — dynamic; hidden in PTC mode.
	if s.skillsService != nil && !ptcEnabled {
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

	// PTC: expose execute_javascript as a direct LLM tool. Embed the dynamic
	// callTool() list so the model knows exactly what it can call.
	if s.ptcIntegration != nil {
		availableCallTools := s.ptcAvailableCallToolsWithPolicy(ctx, policy)
		addTools(s.ptcIntegration.GetPTCTools(availableCallTools))
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
	return opts
}

func (s *Service) ptcAvailableCallTools(ctx context.Context) []ptc.ToolInfo {
	return s.ptcAvailableCallToolsWithPolicy(ctx, s.buildToolPreparationPolicy(ctx))
}

func (s *Service) ptcAvailableCallToolsWithPolicy(ctx context.Context, policy toolPreparationPolicy) []ptc.ToolInfo {
	if s.ptcIntegration == nil {
		return nil
	}
	tools := s.ptcIntegration.GetAvailableCallTools(ctx)
	sessionID := policy.SessionID
	exposeSearch := s.shouldExposePTCToolSearch(ctx)
	relevantSkillNames := policy.RelevantSkillNames
	hasRelevantSkillFilter := len(relevantSkillNames) > 0

	filtered := make([]ptc.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "search_available_tools" || domain.IsToolSearchTool(tool.Name) {
			if exposeSearch {
				filtered = append(filtered, tool)
			}
			continue
		}
		if policy.ForceSkillFirst && !shouldKeepToolForSkillFirst(tool.Name, relevantSkillNames) {
			continue
		}
		if hasRelevantSkillFilter && strings.HasPrefix(tool.Name, "skill_") {
			skillID := strings.TrimPrefix(tool.Name, "skill_")
			if !slices.Contains(relevantSkillNames, skillID) {
				continue
			}
		}

		if !s.isDeferredPTCCallTool(tool.Name) || s.toolRegistry.IsActivatedForSession(sessionID, tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func (s *Service) shouldExposePTCToolSearch(ctx context.Context) bool {
	if s == nil || s.toolRegistry == nil {
		return false
	}
	sessionID := s.CurrentSessionID()
	for _, tool := range s.toolRegistry.ListDeferredTools() {
		if !s.toolRegistry.IsActivatedForSession(sessionID, tool.Function.Name) {
			return true
		}
	}
	if s.mcpService != nil {
		for _, tool := range s.mcpService.ListTools() {
			if s.shouldHideMCPWebSearchTools() && isMCPWebSearchToolName(tool.Function.Name) {
				continue
			}
			if !s.toolRegistry.IsActivatedForSession(sessionID, tool.Function.Name) {
				return true
			}
		}
	}
	if s.skillsService != nil {
		skillsList, _ := s.skillsService.ListSkills(ctx, skills.SkillFilter{})
		for _, sk := range skillsList {
			if !sk.Enabled || sk.DisableModelInvocation {
				continue
			}
			toolName := "skill_" + sk.ID
			if !s.toolRegistry.IsActivatedForSession(sessionID, toolName) {
				return true
			}
		}
	}
	return s.shouldExposeSearchTools()
}

func (s *Service) isDeferredPTCCallTool(toolName string) bool {
	if toolName == "" {
		return false
	}
	if def, ok := s.toolRegistry.DefinitionOf(toolName); ok {
		return def.DeferLoading
	}
	if strings.HasPrefix(toolName, "mcp_") {
		return true
	}
	if strings.HasPrefix(toolName, "skill_") {
		return true
	}
	return false
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
	for _, tool := range s.toolRegistry.ListForCallTool() {
		if tool.Name == "search_available_tools" || domain.IsToolSearchTool(tool.Name) {
			continue
		}
		if strings.HasPrefix(tool.Name, "mcp_") || strings.HasPrefix(tool.Name, "skill_") {
			continue
		}
		toolHints = append(toolHints, tool.Name)
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

func (s *Service) buildRelevantSkillReminder(ctx context.Context, goal string, session *Session) *skillReminder {
	if s == nil || s.skillsService == nil || strings.TrimSpace(goal) == "" {
		return nil
	}

	skillsList, err := s.skillsService.ResolveForModel(ctx, goal, extractTouchedPathsForSkills(goal, session))
	if err != nil || len(skillsList) == 0 {
		return nil
	}

	if len(skillsList) > 5 {
		skillsList = skillsList[:5]
	}

	sent := sentRelevantSkillNames(session)
	newNames := make([]string, 0, len(skillsList))
	for _, sk := range skillsList {
		if !slices.Contains(sent, sk.ID) {
			newNames = append(newNames, sk.ID)
		}
	}
	if len(newNames) == 0 {
		return nil
	}
	sessionID := ""
	if session != nil {
		sessionID = session.GetID()
	}
	currentNames := make([]string, 0, len(skillsList))
	for _, sk := range skillsList {
		currentNames = append(currentNames, sk.ID)
	}
	if sessionID != "" {
		s.rememberRelevantSkillsForSession(sessionID, currentNames)
	}
	for _, name := range newNames {
		if sessionID != "" && s.toolRegistry != nil {
			s.toolRegistry.ActivateForSession(sessionID, "skill_"+name)
		}
	}

	var sb strings.Builder
	sb.WriteString("<skill-discovery>\n")
	sb.WriteString("Skills relevant to your task:\n")
	for _, sk := range skillsList {
		if !slices.Contains(newNames, sk.ID) {
			continue
		}
		line := "- skill_" + sk.ID
		if strings.TrimSpace(sk.Description) != "" {
			line += ": " + strings.TrimSpace(sk.Description)
		}
		if strings.TrimSpace(sk.WhenToUse) != "" {
			line += " | use when: " + strings.TrimSpace(sk.WhenToUse)
		}
		if len(line) > 320 {
			line = line[:319] + "…"
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("</skill-discovery>")
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return nil
	}
	return &skillReminder{
		Names: newNames,
		Text:  text,
	}
}

func extractTouchedPathsForSkills(goal string, session *Session) []string {
	seen := make(map[string]struct{})
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		candidate = strings.Trim(candidate, ".,!?;:()[]{}\"'")
		if candidate == "" {
			return
		}
		if strings.Contains(candidate, "/") || strings.Contains(candidate, ".") {
			seen[filepath.Clean(candidate)] = struct{}{}
		}
	}

	for _, token := range strings.Fields(goal) {
		add(token)
	}

	if session != nil {
		for _, msg := range session.GetLastNMessages(6) {
			for _, token := range strings.Fields(msg.Content) {
				add(token)
			}
		}
	}

	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	slices.Sort(out)
	return out
}

func sentRelevantSkillNames(session *Session) []string {
	if session == nil {
		return nil
	}
	raw, ok := session.GetContext(sessionContextSentSkillReminders)
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func markRelevantSkillsSent(session *Session, names []string) {
	if session == nil || len(names) == 0 {
		return
	}
	existing := sentRelevantSkillNames(session)
	merged := append([]string(nil), existing...)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(merged, name) {
			continue
		}
		merged = append(merged, name)
	}
	slices.Sort(merged)
	session.SetContext(sessionContextSentSkillReminders, merged)
}

func (s *Service) buildWebSearchPromptNote(currentAgent *Agent) string {
	if isDispatchOnlyAgent(currentAgent) || (currentAgent == nil && isDispatchOnlyAgent(s.agent)) {
		return ""
	}

	switch s.webSearchMode() {
	case domain.WebSearchModeNative:
		return "Web search capability:\n- Up-to-date web lookups are available through the model's native web search capability.\n- Do not search the tool catalog for mcp_websearch tools when you need current web information."
	case domain.WebSearchModeAuto:
		return "Web search capability:\n- Prefer the model's native web search capability for up-to-date web lookups.\n- If native search is unavailable or insufficient, use the available mcp_websearch_* tools as a fallback."
	case domain.WebSearchModeOff:
		return "Web search capability:\n- Web search is disabled for this run.\n- Do not look for mcp_websearch tools."
	default:
		return ""
	}
}

func (s *Service) resetRunMemorySaved() {
	s.memorySaveMu.Lock()
	s.memorySavedInRun = false
	s.memorySaveMu.Unlock()
}

func (s *Service) markRunMemorySaved() {
	s.memorySaveMu.Lock()
	s.memorySavedInRun = true
	s.memorySaveMu.Unlock()
}

func (s *Service) hasRunMemorySaved() bool {
	s.memorySaveMu.RLock()
	defer s.memorySaveMu.RUnlock()
	return s.memorySavedInRun
}

// containsStr checks if a string slice contains a string
func containsStr(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
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

// executeToolViaSubAgent runs a tool or skill call using a separate SubAgent goroutine
func (s *Service) executeToolViaSubAgent(ctx context.Context, currentAgent *Agent, session *Session, tc domain.ToolCall) (interface{}, error, bool) {
	return s.executeToolViaSubAgentWithEvents(ctx, currentAgent, session, tc, nil, s.debug)
}

func (s *Service) executeToolViaSubAgentWithEvents(ctx context.Context, currentAgent *Agent, session *Session, tc domain.ToolCall, sink func(*Event), debug bool) (interface{}, error, bool) {
	// Create subagent config
	subCfg := SubAgentConfig{
		Agent:         currentAgent,
		ParentSession: session,
		Goal:          fmt.Sprintf("Execute tool: %s", tc.Function.Name),
		Service:       s,
		ToolCall:      &tc,
		Debug:         debug,
	}

	sa := NewSubAgent(subCfg)

	var (
		result interface{}
		err    error
	)
	if sink == nil {
		result, err = sa.Run(ctx)
	} else {
		for evt := range sa.RunAsync(ctx) {
			sink(evt)
		}
		result, err = sa.GetResult()
	}

	// Check if this was a handoff
	isHandoff := strings.HasPrefix(tc.Function.Name, "transfer_to_") && err == nil

	return result, err, isHandoff
}

// EmitDebugPrint prints formatted debug information to console if debug mode is enabled.
// This ensures consistent look across different execution paths (Execute, Run, RunStream).
func (s *Service) EmitDebugPrint(round int, debugType string, content string) {
	sep := strings.Repeat("─", 60)
	label := strings.ToUpper(debugType)

	fmt.Fprintf(os.Stderr, "\n\033[2m%s\n🐛 DEBUG [Round %d] %s\n%s\n%s\n%s\033[0m\n",
		sep, round, label, sep, content, sep)
}

func truncateGoal(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func getSkillVarTypeString(typ string) string {
	switch typ {
	case "number", "integer":
		return "number"
	case "boolean":
		return "boolean"
	default:
		return "string"
	}
}
