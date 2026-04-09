package agent

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/search"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

// SearchAndExecute searches for tools matching the query and optionally executes them.
// scope limits the search to a specific MCP server prefix or skill ID (empty = search all).
func (s *Service) SearchAndExecute(ctx context.Context, query string, instruction string, scope string) (interface{}, error) {
	query = strings.ToLower(query)
	keywords := strings.Fields(query)
	scope = strings.ToLower(scope)
	currentAgent := getCurrentAgent(ctx)
	if currentAgent == nil {
		currentAgent = s.agent
	}
	if redirect, ok := s.searchToolRedirect(query, instruction, scope); ok {
		return redirect, nil
	}

	// Search all registered tools (not just deferred)
	matches := s.filterToolDefinitionsForAgent(currentAgent, s.toolRegistry.SearchAllTools(query))

	// Search MCP tools if available
	if s.mcpService != nil {
		for _, t := range s.mcpService.ListTools() {
			if s.shouldHideMCPWebSearchTools() && isMCPWebSearchToolName(t.Function.Name) {
				continue
			}
			name := strings.ToLower(t.Function.Name)
			// Apply scope filter: skip tools that don't match the scope prefix
			if scope != "" && !strings.HasPrefix(name, scope) {
				continue
			}
			desc := strings.ToLower(t.Function.Description)
			matched := false
			for _, kw := range keywords {
				if strings.Contains(name, kw) || strings.Contains(desc, kw) {
					matched = true
					break
				}
			}
			if matched && s.isToolAllowedForAgent(currentAgent, t.Function.Name) {
				t.DeferLoading = true
				matches = append(matches, t)
			}
		}
	}

	// Search Skills if available
	if s.skillsService != nil {
		if skillsList, err := s.skillsService.ListSkills(ctx, skills.SkillFilter{}); err == nil {
			for _, sk := range skillsList {
				skillID := strings.ToLower(sk.ID)
				// Apply scope filter: only include if scope matches this skill's ID
				if scope != "" && !strings.HasPrefix(skillID, scope) && skillID != scope {
					continue
				}
				desc := strings.ToLower(sk.Description)
				matched := scope == skillID // exact skill scope match always included
				if !matched {
					for _, kw := range keywords {
						if strings.Contains(skillID, kw) || strings.Contains(desc, kw) {
							matched = true
							break
						}
					}
				}
				if matched {
					def := buildSkillToolDef(*sk)
					if !s.isToolAllowedForAgent(currentAgent, def.Function.Name) {
						continue
					}
					def.DeferLoading = true
					matches = append(matches, def)
				}
			}
		}
	}

	// Activate matching tools for current session
	sessionID := s.CurrentSessionID()
	for _, m := range matches {
		s.toolRegistry.ActivateForSession(sessionID, m.Function.Name)
	}

	// Automatic execution if instruction provided
	if instruction != "" {
		if s.llmService == nil {
			return nil, fmt.Errorf("LLM service not available for automatic execution")
		}

		messages := []domain.Message{
			{Role: "system", Content: "You are an intelligent executor. You MUST strictly call the appropriate tool to fulfill the user's instruction. Do not provide conversational filler, ONLY call the tool."},
			{Role: "user", Content: instruction},
		}

		opts := s.toolGenerationOptions(0.1, 0, "auto")

		result, err := s.llmService.GenerateWithTools(ctx, messages, matches, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to map instruction to tool: %w", err)
		}

		if len(result.ToolCalls) == 0 {
			return "Found tools: " + fmt.Sprintf("%v", getToolNames(matches)) + ". But could not map instruction to any of them.", nil
		}

		execResults, err := s.executeToolCalls(ctx, s.agent, nil, result.ToolCalls)
		if err != nil {
			return nil, fmt.Errorf("tool execution failed: %w", err)
		}

		var finalResults []string
		for _, r := range execResults {
			finalResults = append(finalResults, fmt.Sprintf("Tool '%s' executed successfully. Result: %v", r.ToolName, r.Result))
		}
		return strings.Join(finalResults, "\n") + "\n\nPlease provide a final summary of these results to the user.", nil
	}

	// Just return found tools metadata
	if len(matches) == 0 {
		return "No tools found matching the query.", nil
	}

	var result []map[string]interface{}
	for _, m := range matches {
		result = append(result, map[string]interface{}{
			"name":        m.Function.Name,
			"description": m.Function.Description,
			"parameters":  m.Function.Parameters,
		})
	}
	return result, nil
}

func (s *Service) searchToolRedirect(query string, instruction string, scope string) (string, bool) {
	mode := s.webSearchMode()
	if mode == domain.WebSearchModeMCP {
		return "", false
	}

	if !s.requestsHiddenMCPWebSearchTools(query, instruction, scope) {
		return "", false
	}

	switch mode {
	case domain.WebSearchModeOff:
		return "Web search is disabled for this run. Do not search for MCP web search tools.", true
	case domain.WebSearchModeNative:
		return "Native web search is enabled for this model. Do not search for MCP web search tools. Answer the user's request directly using the model's native web search capability.", true
	case domain.WebSearchModeAuto:
		return "", false
	default:
		return "", false
	}
}

func (s *Service) requestsHiddenMCPWebSearchTools(query string, instruction string, scope string) bool {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if strings.HasPrefix(scope, "mcp_websearch") {
		return true
	}
	if s.mcpService == nil {
		return false
	}

	keywords := toolSearchKeywords(query)
	if len(keywords) == 0 {
		keywords = toolSearchKeywords(instruction)
	}
	if len(keywords) == 0 {
		return false
	}

	for _, tool := range s.mcpService.ListTools() {
		if !isMCPWebSearchToolName(tool.Function.Name) {
			continue
		}
		if matchesToolSearchKeywords(tool, keywords) {
			return true
		}
	}
	return false
}

func toolSearchKeywords(text string) []string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return nil
	}
	raw := strings.Fields(normalized)
	keywords := make([]string, 0, len(raw))
	for _, token := range raw {
		token = strings.Trim(token, ".,!?;:()[]{}\"'")
		if len(token) < 3 {
			continue
		}
		keywords = append(keywords, token)
	}
	return keywords
}

func matchesToolSearchKeywords(tool domain.ToolDefinition, keywords []string) bool {
	if len(keywords) == 0 {
		return false
	}
	name := strings.ToLower(tool.Function.Name)
	desc := strings.ToLower(tool.Function.Description)
	for _, kw := range keywords {
		if strings.Contains(name, kw) || strings.Contains(desc, kw) {
			return true
		}
	}
	return false
}

func (s *Service) filterToolDefinitionsForAgent(currentAgent *Agent, defs []domain.ToolDefinition) []domain.ToolDefinition {
	if currentAgent == nil || len(defs) == 0 {
		return defs
	}

	filtered := make([]domain.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		if s.isToolAllowedForAgent(currentAgent, def.Function.Name) {
			filtered = append(filtered, def)
		}
	}
	return filtered
}

func (s *Service) isToolAllowedForAgent(currentAgent *Agent, toolName string) bool {
	if currentAgent == nil || toolName == "" {
		return true
	}

	if toolName == "search_available_tools" || domain.IsToolSearchTool(toolName) {
		return true
	}
	if strings.HasPrefix(toolName, "skill_") {
		skillID := strings.TrimPrefix(toolName, "skill_")
		return isAllAllowed(currentAgent.skills) || containsStr(currentAgent.skills, skillID)
	}
	if strings.HasPrefix(toolName, "mcp_") {
		return isAllAllowed(currentAgent.mcpTools) || containsStr(currentAgent.mcpTools, toolName)
	}
	return true
}

func getToolNames(defs []domain.ToolDefinition) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Function.Name
	}
	return names
}

func (s *Service) SearchDeferredCatalog(ctx context.Context, query, searchType string) ([]domain.ToolDefinition, error) {
	currentAgent := getCurrentAgent(ctx)
	if currentAgent == nil {
		currentAgent = s.agent
	}

	catalog := s.deferredToolCatalog(ctx, currentAgent)
	if len(catalog) == 0 {
		return nil, nil
	}

	var matches []domain.ToolDefinition
	switch strings.ToLower(strings.TrimSpace(searchType)) {
	case "bm25":
		matches = rankDeferredToolCatalog(query, catalog)
	default:
		matches = regexMatchDeferredToolCatalog(query, catalog)
	}

	sessionID := s.CurrentSessionID()
	for _, def := range matches {
		s.toolRegistry.ActivateForSession(sessionID, def.Function.Name)
	}
	if s.debug {
		names := make([]string, 0, len(matches))
		for _, def := range matches {
			names = append(names, def.Function.Name)
		}
		log.Printf("[DEBUG] PTC deferred tool search: type=%s query=%q session=%s matches=%v", searchType, query, sessionID, names)
	}
	return matches, nil
}

func (s *Service) deferredToolCatalog(ctx context.Context, currentAgent *Agent) []domain.ToolDefinition {
	toolsMap := make(map[string]domain.ToolDefinition)

	for _, def := range s.toolRegistry.ListDeferredTools() {
		if s.isToolAllowedForAgent(currentAgent, def.Function.Name) {
			toolsMap[def.Function.Name] = def
		}
	}

	if s.mcpService != nil {
		for _, tool := range s.mcpService.ListTools() {
			if s.shouldHideMCPWebSearchTools() && isMCPWebSearchToolName(tool.Function.Name) {
				continue
			}
			if !s.isToolAllowedForAgent(currentAgent, tool.Function.Name) {
				continue
			}
			t := tool
			t.DeferLoading = true
			toolsMap[t.Function.Name] = t
		}
	}

	if s.skillsService != nil {
		if skillsList, err := s.skillsService.ListSkills(ctx, skills.SkillFilter{}); err == nil {
			for _, sk := range skillsList {
				if !sk.Enabled || sk.DisableModelInvocation {
					continue
				}
				def := buildSkillToolDef(*sk)
				if !s.isToolAllowedForAgent(currentAgent, def.Function.Name) {
					continue
				}
				def.DeferLoading = true
				toolsMap[def.Function.Name] = def
			}
		}
	}

	out := make([]domain.ToolDefinition, 0, len(toolsMap))
	for _, def := range toolsMap {
		out = append(out, def)
	}
	return out
}

func rankDeferredToolCatalog(query string, catalog []domain.ToolDefinition) []domain.ToolDefinition {
	if strings.TrimSpace(query) == "" || len(catalog) == 0 {
		return nil
	}
	documents := make([]search.Document, 0, len(catalog))
	defsByName := make(map[string]domain.ToolDefinition, len(catalog))
	for _, def := range catalog {
		name := def.Function.Name
		defsByName[name] = def
		documents = append(documents, search.Document{
			ID: name,
			Text: strings.Join([]string{
				name,
				strings.ReplaceAll(name, "_", " "),
				def.Function.Description,
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

func regexMatchDeferredToolCatalog(pattern string, catalog []domain.ToolDefinition) []domain.ToolDefinition {
	if len(catalog) == 0 {
		return nil
	}
	var re *regexp.Regexp
	if strings.TrimSpace(pattern) != "" {
		if compiled, err := regexp.Compile("(?i)" + pattern); err == nil {
			re = compiled
		}
	}

	patternLower := strings.ToLower(strings.TrimSpace(pattern))
	results := make([]domain.ToolDefinition, 0)
	for _, def := range catalog {
		name := def.Function.Name
		desc := def.Function.Description
		matched := false
		if re != nil {
			matched = re.MatchString(name) || re.MatchString(desc)
		} else if patternLower != "" {
			nameLower := strings.ToLower(name)
			descLower := strings.ToLower(desc)
			matched = strings.Contains(nameLower, patternLower) || strings.Contains(descLower, patternLower)
		}
		if matched {
			results = append(results, def)
		}
	}
	if len(results) > 5 {
		results = results[:5]
	}
	return results
}

// buildSkillToolDef builds a ToolDefinition for a skill with proper parameter schema.
func buildSkillToolDef(sk skills.Skill) domain.ToolDefinition {
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
	if strings.TrimSpace(sk.WhenToUse) != "" {
		desc = desc + ". Use when: " + strings.TrimSpace(sk.WhenToUse)
	}

	// Use "skill_" prefix for consistency with other parts of the system
	toolName := "skill_" + sk.ID

	return domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        toolName,
			Description: "Skill workflow: " + desc,
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": properties,
				"required":   required,
			},
		},
	}
}
