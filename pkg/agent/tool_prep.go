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
		SearchMode:            s.shouldExposeSearchTools(),
		ExposeSearchTools:     s.shouldExposeSearchTools(),
		HideNativeWebSearch:   s.shouldHideMCPWebSearchTools(),
		HideRegistryWebSearch: s.shouldHideRegistryWebSearchTool(),
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

// ensureRequiredToolsVisible puts back any tool the run is contractually
// required to call.
//
// Two policies narrow the schema for good reasons — skill-first (consult the
// relevant skill before reaching for anything else) and discovery layering (a
// huge catalogue goes behind a search step). Both are indifferent to what this
// particular run owes the user, and that indifference produced the worst
// outcome in the benchmark: a weak skill match on a question about drinking
// water stripped set_reminder out of the schema, so the model reported the
// reminder capability as absent — while the delivery contract stood waiting for
// a call to exactly that tool. The run then either burned rounds rediscovering
// the tool or gave up and told the user it could not be done.
//
// Hiding a tool the run must call is self-defeating whatever the reason for
// hiding it, so the constraint wins. Which tools those are was decided once, by
// the constraint extraction reading the run's own tool catalogue (satisfied_by
// in constraints.go); nothing is matched or guessed at here.
//
// An explicit allow/deny list still wins — it is applied after this — because
// that is the caller speaking directly.
func (s *Service) ensureRequiredToolsVisible(tools []domain.ToolDefinition, cfg *RunConfig) []domain.ToolDefinition {
	if s == nil || s.toolRegistry == nil || cfg == nil || cfg.resolvedConstraints == nil {
		return tools
	}
	constraints := *cfg.resolvedConstraints
	if constraints.ForbidTools {
		// A run with no tools at all cannot be owed one.
		return tools
	}
	present := make(map[string]bool, len(tools))
	for _, t := range tools {
		present[t.Function.Name] = true
	}
	for _, name := range requiredToolNames(constraints) {
		if name == "" || present[name] {
			continue
		}
		def, ok := s.toolRegistry.DefinitionOf(name)
		if !ok {
			continue
		}
		// Deferred definitions are hidden by construction; the point of putting
		// this one back is that the model can call it directly.
		def.DeferLoading = false
		tools = append(tools, def)
		present[name] = true
	}
	return tools
}

// requiredToolNames lists the tools this run's constraints name as the ones
// that carry out what the user asked for.
//
// Conditional actions count here even though the contract does not enforce
// them. "If it rains, remind me to take an umbrella" is not something the
// runtime can hold the model to — but the branch where it does rain needs the
// reminder tool, so it has to be reachable either way. Visibility and
// enforcement are different questions and get different answers.
func requiredToolNames(constraints RunConstraints) []string {
	out := make([]string, 0, len(constraints.Deliverables)+len(constraints.RequestedActions))
	for _, d := range constraints.Deliverables {
		out = append(out, strings.TrimSpace(d.SatisfiedBy))
	}
	for _, a := range constraints.RequestedActions {
		out = append(out, strings.TrimSpace(a.SatisfiedBy))
	}
	return out
}

// promoteRelevantSkillTools moves the matched skills' tools to the front of the
// schema. This is the whole of "skill-first" now, and the change from what it
// used to be is the point.
//
// It used to be subtractive: while a relevant skill was outstanding, every tool
// that was not that skill or a terminal was dropped from the schema. The
// intention — consult the skill before reaching for anything else — is a good
// one, but it rested on the match being right, and the matcher cannot carry
// that weight. It is lexical: skills-go scores a skill by testing whether each
// input word of four characters or more appears as a SUBSTRING of the skill's
// name, when_to_use or description, and any score above zero counted. So
// "Check the weather in Chicago…" matched a frontend design skill, because
// "check" appears inside "pre-flight check" in its description. That one word
// took web search out of the schema, and the model — reading the schema it was
// given — told the user no web search tool was available.
//
// The score is not exposed through pkg/skills, so there is no threshold to
// raise, and inventing one here would mean reimplementing the matcher. The
// honest conclusion is that a lexical guess is not grounds for removing a
// capability. It is fine grounds for a recommendation, so that is what it makes
// now: the skill goes first in the list, the <skill-discovery> reminder says it
// is relevant, and everything else stays reachable.
func promoteRelevantSkillTools(tools []domain.ToolDefinition, relevantSkillNames []string) []domain.ToolDefinition {
	if len(tools) == 0 || len(relevantSkillNames) == 0 {
		return tools
	}
	isRelevantSkill := func(name string) bool {
		if !strings.HasPrefix(name, "skill_") {
			return false
		}
		return slices.Contains(relevantSkillNames, strings.TrimPrefix(name, "skill_"))
	}
	front := make([]domain.ToolDefinition, 0, len(relevantSkillNames))
	rest := make([]domain.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		if isRelevantSkill(t.Function.Name) {
			front = append(front, t)
			continue
		}
		rest = append(rest, t)
	}
	return append(front, rest...)
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
	tools = s.ensureRequiredToolsVisible(tools, cfg)
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
	// Run-memory recall rides at the END of the system prompt: it is the most
	// run-specific content, and appending (rather than prepending) keeps the
	// static prompt prefix byte-stable across runs for provider prompt caches.
	if cfg != nil && cfg.recalledContext != "" {
		systemMsg += "\n\n## Recalled context (run memory)\n" +
			"Prior knowledge recalled for this task. Cite it when it answers the question; " +
			"say so when it conflicts with fresh evidence.\n\n" + cfg.recalledContext
	}
	// The plan an earlier run left behind, for the same reason and in the same
	// place. Persisting a plan and never telling the model about it is a
	// process that comes back holding the answer and starts over anyway.
	if cfg != nil && cfg.resumedPlan != "" {
		systemMsg += "\n\n## Work already done on this task\n" +
			"An earlier run was interrupted partway. This is where it got to, " +
			"and what each finished step produced.\n\n" + cfg.resumedPlan
	}
	// And what is on disk, which on a coding task is most of what an earlier
	// segment actually produced.
	if cfg != nil && cfg.resumedWorkspace != "" {
		systemMsg += "\n\n## Files already in the workspace\n" + cfg.resumedWorkspace
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

// ToolDiscoveryThreshold is the tool count above which the runtime stops
// putting everything in the schema and hides the bulk (MCP tools and skills)
// behind search_available_tools.
//
// Below it — which is where an ordinary agent lives — every registered tool
// goes straight into the schema and search_available_tools is not offered at
// all. Hiding tools behind a search step costs a round trip and, worse, invites
// the model to go shopping: a benchmark run made 35 search calls against an
// agent whose entire catalogue would have fitted in the schema, while a
// comparable agent with a static tool set made none and scored higher.
//
// Discovery still exists for genuinely large catalogues; it is no longer the
// default for small ones.
const ToolDiscoveryThreshold = 32

// collectAllAvailableToolsWithPolicy decides whether this turn gets a flat tool
// list or a layered one. It collects once with everything visible; only if that
// overflows the threshold does it collect again with the bulk deferred.
func (s *Service) collectAllAvailableToolsWithPolicy(ctx context.Context, currentAgent *Agent, policy toolPreparationPolicy) []domain.ToolDefinition {
	threshold := s.toolDiscoveryThreshold()

	flat := s.collectTools(ctx, currentAgent, policy, false)
	if len(flat) <= threshold || !policy.ExposeSearchTools {
		// Nothing is hidden, so the search tool has nothing to find. Offering it
		// anyway is a standing invitation to waste a round. The tool stays in
		// the registry (it is still callable if something activates it); it just
		// does not go into the schema.
		return withoutToolSearchTools(flat)
	}
	return s.collectTools(ctx, currentAgent, policy, true)
}

// toolDiscoveryThreshold reports the configured catalogue size above which
// discovery layering kicks in.
func (s *Service) toolDiscoveryThreshold() int {
	if s != nil && s.cfg != nil && s.cfg.Tooling.DiscoveryThreshold > 0 {
		return s.cfg.Tooling.DiscoveryThreshold
	}
	return ToolDiscoveryThreshold
}

// withoutToolSearchTools drops the discovery tools from a flat catalogue.
func withoutToolSearchTools(tools []domain.ToolDefinition) []domain.ToolDefinition {
	out := make([]domain.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		if t.Function.Name == "search_available_tools" || domain.IsToolSearchTool(t.Function.Name) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (s *Service) collectTools(ctx context.Context, currentAgent *Agent, policy toolPreparationPolicy, deferBulk bool) []domain.ToolDefinition {
	toolsMap := make(map[string]domain.ToolDefinition)
	sessionID := policy.SessionID
	searchMode := deferBulk
	relevantSkillNames := policy.RelevantSkillNames
	hasRelevantSkillFilter := len(relevantSkillNames) > 0

	// Helper to add tools with deduplication
	addTools := func(defs []domain.ToolDefinition) {
		for _, d := range defs {
			if policy.HideRegistryWebSearch && d.Function.Name == registryWebSearchToolName {
				continue
			}
			toolsMap[d.Function.Name] = d
		}
	}

	// 1. Add static tools and active deferred tools from Registry
	// This includes built-in tools like delegate_to_subagent and task_complete
	addTools(s.toolRegistry.ListForLLM(sessionID))

	// Search tools ride along only when something is actually hidden behind them.
	if deferBulk && policy.ExposeSearchTools {
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

	if policy.ForceSkillFirst {
		tools = promoteRelevantSkillTools(tools, relevantSkillNames)
	}
	return tools
}

// shouldExposeSearchTools reports whether discovery is permitted at all. The
// decision to actually use it is made by catalogue size in
// collectAllAvailableToolsWithPolicy; this only lets an operator switch the
// mechanism off entirely.
func (s *Service) shouldExposeSearchTools() bool {
	if s == nil || s.cfg == nil {
		return true
	}
	if s.cfg.Tooling.DisableToolSearch {
		return false
	}
	return true
}

func (s *Service) webSearchMode() domain.WebSearchMode {
	if s == nil || s.cfg == nil {
		return domain.WebSearchModeMCP
	}
	// An unset mode defaults to auto here — at the service level, where it
	// only shapes agent tool rounds — and deliberately not in
	// NormalizeWebSearchMode, whose zero value also covers bare per-request
	// options that must stay parameter-free.
	if strings.TrimSpace(s.cfg.Tooling.WebSearch.Mode) == "" {
		return domain.WebSearchModeAuto
	}
	return domain.NormalizeWebSearchMode(domain.WebSearchMode(s.cfg.Tooling.WebSearch.Mode))
}

// nativeWebSearchVerdict asks the generator what it has learned about the
// upstream's native web-search support. Generators that cannot report (mocks,
// non-pool providers) leave the verdict unknown.
func (s *Service) nativeWebSearchVerdict() (supported, known bool) {
	if s == nil {
		return false, false
	}
	if r, ok := s.llmService.(domain.NativeWebSearchReporter); ok {
		return r.NativeWebSearchVerdict()
	}
	return false, false
}

// webSearchSurfaceMode is the mode as the tool surface and prompt should see
// it: auto resolves to native once the provider has proven it searches, and
// to mcp once it has proven it rejects the parameters. While the verdict is
// unknown, auto stays auto — both routes remain available.
func (s *Service) webSearchSurfaceMode() domain.WebSearchMode {
	mode := s.webSearchMode()
	if mode != domain.WebSearchModeAuto {
		return mode
	}
	if supported, known := s.nativeWebSearchVerdict(); known {
		if supported {
			return domain.WebSearchModeNative
		}
		return domain.WebSearchModeMCP
	}
	return mode
}

// webSearchGenerationMode is the mode the generation options should carry.
// It differs from the surface mode in one deliberate way: a proven-native
// auto still reports auto, keeping the pool's strip-and-retry safety net
// armed in case a heterogeneous upstream later rejects the parameters. Only
// proven-unsupported downgrades, so doomed parameters stop being sent.
func (s *Service) webSearchGenerationMode() domain.WebSearchMode {
	mode := s.webSearchMode()
	if mode != domain.WebSearchModeAuto {
		return mode
	}
	if supported, known := s.nativeWebSearchVerdict(); known && !supported {
		return domain.WebSearchModeMCP
	}
	return mode
}

func (s *Service) webSearchContextSize() string {
	if s == nil || s.cfg == nil {
		return "medium"
	}
	return domain.NormalizeWebSearchContextSize(s.cfg.Tooling.WebSearch.SearchContextSize)
}

func (s *Service) shouldHideMCPWebSearchTools() bool {
	mode := s.webSearchSurfaceMode()
	return mode == domain.WebSearchModeNative || mode == domain.WebSearchModeOff
}

// registryWebSearchToolName is the built-in tool RegisterWebSearchTool adds.
const registryWebSearchToolName = "web_search"

// shouldHideRegistryWebSearchTool reports whether a locally registered
// web_search is redundant this turn.
//
// An embedder that registers the built-in web_search AND runs the MCP
// websearch server offers the model two ways to do one thing, and the model
// takes both: the efficiency audit found single questions answered with a
// web_search call and an mcp_websearch_basic call back to back, one task
// spending five search calls. Nothing was wrong with either route — there were
// just two of them.
//
// The configured mode already says which route this service means to use, so
// honour it symmetrically: HideNativeWebSearch drops the MCP tools when the
// mode is native, and this drops the registry tool when the mode is mcp. Only
// when the MCP route is actually present, or a service configured for mcp with
// no server running would be left with no way to search at all.
func (s *Service) shouldHideRegistryWebSearchTool() bool {
	if s == nil || s.webSearchSurfaceMode() != domain.WebSearchModeMCP {
		return false
	}
	if s.mcpService == nil {
		return false
	}
	for _, tool := range s.mcpService.ListTools() {
		if isMCPWebSearchToolName(tool.Function.Name) {
			return true
		}
	}
	return false
}

func isMCPWebSearchToolName(name string) bool {
	return strings.HasPrefix(name, "mcp_websearch_")
}

func (s *Service) toolGenerationOptions(temperature float64, maxTokens int, toolChoice string) *domain.GenerationOptions {
	opts := &domain.GenerationOptions{
		Temperature:          temperature,
		MaxTokens:            maxTokens,
		WebSearchMode:        s.webSearchGenerationMode(),
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
	// Forward the prompt-cache mode (set via WithPromptCache) onto every
	// tool round. A long run only benefits if every round is marked: one
	// unmarked round pays full prefill and leaves the next one cold.
	opts.PromptCache = s.promptCacheMode()
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
