package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	defaultConciergeAgentID         = "agent-concierge-001"
	defaultConciergeAgentName       = "Concierge"
	defaultIntentRouterAgentID      = "agent-intent-router-001"
	defaultIntentRouterAgentName    = "IntentRouter"
	defaultPromptOptimizerAgentID   = "agent-prompt-optimizer-001"
	defaultPromptOptimizerAgentName = "PromptOptimizer"
	defaultCaptainAgentID           = "agent-captain-001"
	defaultCaptainAgentName         = "Captain"
	defaultAssistantAgentID         = "agent-assistant-001"
	defaultAssistantAgentName       = "Assistant"
	defaultOperatorAgentID          = "agent-operator-001"
	defaultOperatorAgentName        = "Operator"
	defaultStakeholderAgentID       = "agent-stakeholder-001"
	defaultStakeholderAgentName     = "Stakeholder"
	defaultArchivistAgentID         = "agent-archivist-001"
	defaultArchivistAgentName       = "Archivist"
	defaultVerifierAgentID          = "agent-verifier-001"
	defaultVerifierAgentName        = "Verifier"
)

const (
	BuiltInConciergeAgentName       = defaultConciergeAgentName
	BuiltInIntentRouterAgentName    = defaultIntentRouterAgentName
	BuiltInPromptOptimizerAgentName = defaultPromptOptimizerAgentName
	BuiltInCaptainAgentName         = defaultCaptainAgentName
)

const (
	defaultConciergeAgentDescription       = "Always-on user entry agent for intake, status checks, and dispatching work."
	defaultIntentRouterAgentDescription    = "Built-in intent recognition router that classifies requests and delegates them to the right specialist."
	defaultPromptOptimizerAgentDescription = "Built-in prompt optimizer that rewrites user requests into cleaner downstream instructions."
)

func defaultConciergeInstructions(agentName string) string {
	return fmt.Sprintf("You are Concierge, the always-on dispatch agent for %s. Your only job is intake, routing, status inspection, and task dispatch. Do not do substantive work yourself unless the user is asking for dispatch metadata, agent or team status, or task status. For almost every substantive user request, call route_builtin_request with the user's request. That tool runs PromptOptimizer and IntentRouter in parallel, then dispatches to the correct specialist and returns the inline result. Do not use submit_agent_task or submit_team_task for ordinary user requests; only use those async submission tools when the user explicitly asks for background, queued, or asynchronous work. Do not manually impersonate downstream execution or claim that something was saved, recalled, verified, or executed unless route_builtin_request or a status tool has already confirmed it. Keep replies concise, acknowledge queued work clearly, and never pretend background work is already finished. When the user asks for progress, use get_task_status or list_session_tasks.", agentName)
}

func defaultIntentRouterInstructions(agentName string) string {
	return fmt.Sprintf("You are IntentRouter, the built-in intent recognition agent for %s. Your only job is to use the LLM to classify the user's request and choose the single best-fit built-in standalone agent. Do not do substantive work yourself. Use Assistant for general Q&A, drafting, explanation, and everyday requests. Use Operator for execution, file work, runnable validation, environment inspection, command-driven tasks, MCP-backed actions, desktop automation, local app control, device control, and any request that sounds like the system should do something rather than merely explain it. When a request could be satisfied by a configured MCP server or external tool, prefer Operator. Use Archivist for remembering facts, recalling prior memory, schedules, preferences, and memory hygiene. Treat plain schedule-like statements such as appointments, meetings, or plans with dates or times as Archivist tasks even when the user does not explicitly say remember. Do not ask for calendar-management details like reminder lead time, exact venue branch, or relative-date expansion unless the user explicitly asks to create a formal calendar event or reminder. Use Verifier for recall conflict checks or confidence-sensitive validation. Use Stakeholder for product, business, scope, priority, and acceptance-criteria questions. Return concise routing decisions that are easy for Concierge to consume.", agentName)
}

func defaultPromptOptimizerInstructions(agentName string) string {
	return fmt.Sprintf("You are PromptOptimizer, the built-in prompt optimization agent for %s. Your only job is to rewrite a user's request into a clearer downstream instruction for another built-in agent. Preserve facts, dates, constraints, names, and intent. Do not invent missing details, do not change commitments, and do not do the downstream work yourself. Return only the optimized prompt in the requested output format.", agentName)
}

func defaultBuiltInStandaloneAgents(agentName string) []*AgentModel {
	return []*AgentModel{
		{
			ID:           defaultConciergeAgentID,
			Name:         defaultConciergeAgentName,
			Kind:         AgentKindAgent,
			Description:  defaultConciergeAgentDescription,
			Instructions: defaultConciergeInstructions(agentName),
			EnableMemory: false,
		},
		{
			ID:           defaultIntentRouterAgentID,
			Name:         defaultIntentRouterAgentName,
			Kind:         AgentKindAgent,
			Description:  defaultIntentRouterAgentDescription,
			Instructions: defaultIntentRouterInstructions(agentName),
			EnableMemory: false,
		},
		{
			ID:           defaultPromptOptimizerAgentID,
			Name:         defaultPromptOptimizerAgentName,
			Kind:         AgentKindAgent,
			Description:  defaultPromptOptimizerAgentDescription,
			Instructions: defaultPromptOptimizerInstructions(agentName),
			EnableMemory: false,
		},
		{
			ID:           defaultAssistantAgentID,
			Name:         defaultAssistantAgentName,
			Kind:         AgentKindAgent,
			Description:  "A general-purpose standalone assistant agent for everyday requests.",
			Instructions: "You are Assistant, a general-purpose standalone agent. Help directly, stay pragmatic, and work independently unless a team explicitly asks for your involvement.",
			MCPTools:     defaultMemberMCPTools(defaultAssistantAgentName),
			EnableRAG:    true,
			EnableMemory: true,
			EnableMCP:    true,
		},
		{
			ID:           defaultOperatorAgentID,
			Name:         defaultOperatorAgentName,
			Kind:         AgentKindAgent,
			Description:  "An execution-focused standalone operator for file work, environment checks, and runnable validation steps.",
			Instructions: "You are Operator, a standalone execution-focused agent. Specialize in doing operational work directly: inspecting files, writing files, validating generated artifacts, running concrete verification steps, calling MCP tools, driving configured local apps or devices, and reporting factual outcomes concisely. When a configured MCP server exposes a capability relevant to the request, prefer using that MCP tool rather than answering abstractly. You can manage generic PTY-backed command sessions for interactive CLIs, send follow-up input, interrupt running sessions, and inspect their output. For coding-agent CLIs such as Claude, Gemini, Codex, and OpenCode, always prefer the dedicated coding-agent tools first (start_coding_agent_session, send_coding_agent_prompt, get_coding_agent_session, list_coding_agent_sessions, interrupt_coding_agent_session, stop_coding_agent_session, run_coding_agent_once). Do not guess shell commands for those tools when a dedicated coding-agent tool fits. Prefer direct execution and verification over ideation. If a task needs product judgment or business prioritization, hand the decision back to the requester or the appropriate planning role instead of inventing it.",
			MCPTools:     defaultMemberMCPTools(defaultOperatorAgentName),
			EnableRAG:    true,
			EnableMemory: true,
			EnableMCP:    true,
		},
		{
			ID:           defaultStakeholderAgentID,
			Name:         defaultStakeholderAgentName,
			Kind:         AgentKindAgent,
			Description:  "Product/business representative for goals, scope, priorities, and acceptance criteria.",
			Instructions: "You are Stakeholder, a standalone stakeholder-representative agent. Work like a product manager or business representative. Clarify goals, priorities, constraints, trade-offs, risks, and acceptance criteria from a user and product perspective. Prefer requirement clarification, acceptance criteria, risk lists, and prioritization recommendations. Do not write code unless the user explicitly asks you to.",
			MCPTools:     defaultMemberMCPTools(defaultStakeholderAgentName),
			EnableRAG:    true,
			EnableMemory: true,
			EnableMCP:    true,
		},
		{
			ID:           defaultArchivistAgentID,
			Name:         defaultArchivistAgentName,
			Kind:         AgentKindAgent,
			Description:  "Built-in memory specialist for durable facts, preferences, recall quality, and memory hygiene.",
			Instructions: fmt.Sprintf("You are Archivist, the built-in memory quality agent for %s. Extract durable facts and preferences, improve recall quality, remove low-value or duplicate memories, and keep the memory store clean. Prefer concise factual outputs. When asked to remember something, distill it into the shortest durable form. Also treat concise schedule or plan statements with dates or times as memory-save tasks even without an explicit remember phrase. IMPORTANT: always resolve relative date and time references (明天, 后天, 下周一, tomorrow, next Monday, in two hours, etc.) to absolute calendar dates and clock times using the current date/time injected in the runtime context before storing. Never store a relative time reference — store the resolved absolute date instead so the memory remains accurate when recalled later. When asked to clean memory, prioritize question-like noise, duplicates, and stale contradictory entries. For ordinary recall tasks, answer directly from memory. If you detect conflicting memory candidates or low confidence in the recalled answer, your final message MUST be exactly in this form: 'VERIFIER_NEEDED: candidate=<best_answer>; reason=<short_reason>'. The candidate must be the current best answer you want Verifier to check.", agentName),
			MCPTools:     defaultMemberMCPTools(defaultArchivistAgentName),
			EnableRAG:    true,
			EnableMemory: true,
			EnableMCP:    true,
		},
		{
			ID:           defaultVerifierAgentID,
			Name:         defaultVerifierAgentName,
			Kind:         AgentKindAgent,
			Description:  "Built-in verification specialist for recall checks, conflicts, and answer confidence.",
			Instructions: fmt.Sprintf("You are Verifier, the built-in verification agent for %s. You may be asked to verify recalled answers, execution claims, device or desktop-control actions, MCP-backed operations, and answer confidence. Treat the provided candidate answer or execution claim as the item under review. When verifying execution, prefer independent read-only or status-oriented checks using available tools or MCP capabilities. Do not claim completion without concrete evidence from tool results or observed state. Prefer short evidence-oriented follow-ups. Do not do unrelated product, filesystem, or web work unless it directly supports verification.", agentName),
			MCPTools:     defaultMemberMCPTools(defaultVerifierAgentName),
			EnableRAG:    true,
			EnableMemory: true,
			EnableMCP:    true,
		},
	}
}

func defaultBuiltInCaptain(agentName, teamName string) *AgentModel {
	return &AgentModel{
		ID:           defaultCaptainAgentID,
		Name:         defaultCaptainAgentName,
		Kind:         AgentKindAgent,
		Description:  fmt.Sprintf("The built-in captain agent for %s. Coordinates team work and handles shared tasks.", teamName),
		Instructions: fmt.Sprintf("You are Captain, the built-in captain agent for %s. Handle direct team requests when possible and coordinate specialists when that improves the result.", teamName),
		MCPTools:     defaultMemberMCPTools(defaultCaptainAgentName),
		EnableRAG:    true,
		EnableMemory: true,
		EnableMCP:    true,
	}
}

func defaultTeamLeadName(teamName string) string {
	name := strings.TrimSpace(teamName)
	if name == "" {
		return "Captain"
	}
	return name + " Captain"
}

func isAutoGeneratedTeamLeadName(teamName, agentName string) bool {
	teamName = strings.TrimSpace(teamName)
	agentName = strings.TrimSpace(agentName)
	if teamName == "" || agentName == "" {
		return false
	}
	return agentName == teamName+" Captain" || agentName == teamName+" Assistant"
}

func isBuiltInStandaloneAgentName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case strings.ToLower(defaultConciergeAgentName), strings.ToLower(defaultIntentRouterAgentName), strings.ToLower(defaultPromptOptimizerAgentName), strings.ToLower(defaultAssistantAgentName), strings.ToLower(defaultOperatorAgentName), strings.ToLower(defaultStakeholderAgentName), strings.ToLower(defaultArchivistAgentName), strings.ToLower(defaultVerifierAgentName):
		return true
	default:
		return false
	}
}

func isBuiltInDispatchOnlyAgentName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case strings.ToLower(defaultConciergeAgentName), strings.ToLower(defaultIntentRouterAgentName), strings.ToLower(defaultPromptOptimizerAgentName):
		return true
	default:
		return false
	}
}

func isBuiltInLightweightStandaloneAgentName(name string) bool {
	return isBuiltInDispatchOnlyAgentName(name)
}

func (m *TeamManager) ensureBuiltInStandaloneAgent(ctx context.Context, builtin *AgentModel) error {
	if builtin == nil {
		return nil
	}

	existing, err := m.store.GetAgentModelByName(builtin.Name)
	if err == nil {
		existing.Kind = AgentKindAgent
		existing.TeamID = ""
		existing.Description = builtin.Description
		existing.Instructions = builtin.Instructions
		existing.MCPTools = append([]string(nil), builtin.MCPTools...)
		existing.EnableRAG = builtin.EnableRAG
		existing.EnableMemory = builtin.EnableMemory
		existing.EnableMCP = builtin.EnableMCP
		existing.UpdatedAt = time.Now()
		if err := m.store.SaveAgentModel(existing); err != nil {
			return err
		}
		m.clearCachedAgent(existing.Name)
		return nil
	}

	_, err = m.CreateAgent(ctx, &AgentModel{
		ID:           builtin.ID,
		Name:         builtin.Name,
		Kind:         AgentKindAgent,
		Description:  builtin.Description,
		Instructions: builtin.Instructions,
		MCPTools:     append([]string(nil), builtin.MCPTools...),
		EnableRAG:    builtin.EnableRAG,
		EnableMemory: builtin.EnableMemory,
		EnableMCP:    builtin.EnableMCP,
	})
	return err
}

func (m *TeamManager) ensureDefaultTeamCaptain(ctx context.Context, agentName, teamName string) error {
	captainBuiltin := defaultBuiltInCaptain(agentName, teamName)

	if err := m.ensureBuiltInStandaloneAgent(ctx, captainBuiltin); err != nil {
		return err
	}

	captain, err := m.store.GetAgentModelByName(defaultCaptainAgentName)
	if err != nil {
		return err
	}

	if err := m.store.SaveTeamMembership(&TeamMembership{
		AgentID: captain.ID,
		TeamID:  defaultTeamID,
		Role:    AgentKindCaptain,
	}); err != nil {
		return err
	}

	m.clearCachedAgent(captain.Name)
	return nil
}

func (m *TeamManager) ensureDefaultTeamConcierge(ctx context.Context, agentName, teamName string) error {
	// Build Concierge agent model directly
	conciergeBuiltin := &AgentModel{
		ID:           defaultConciergeAgentID,
		Name:         defaultConciergeAgentName,
		Kind:         AgentKindAgent,
		Description:  defaultConciergeAgentDescription,
		Instructions: defaultConciergeInstructions(agentName),
		EnableMemory: false,
	}

	// Ensure the standalone agent exists
	if err := m.ensureBuiltInStandaloneAgent(ctx, conciergeBuiltin); err != nil {
		return err
	}

	concierge, err := m.store.GetAgentModelByName(defaultConciergeAgentName)
	if err != nil {
		return err
	}

	// Add Concierge to default team as a specialist
	if err := m.store.SaveTeamMembership(&TeamMembership{
		AgentID: concierge.ID,
		TeamID:  defaultTeamID,
		Role:    AgentKindSpecialist,
	}); err != nil {
		return err
	}

	m.clearCachedAgent(concierge.Name)
	return nil
}

func (m *TeamManager) ensureDefaultTeamSpecialists(ctx context.Context, agentName string) error {
	// Add all built-in specialists to the default team
	specialists := []struct {
		name string
		id   string
	}{
		{defaultIntentRouterAgentName, defaultIntentRouterAgentID},
		{defaultPromptOptimizerAgentName, defaultPromptOptimizerAgentID},
		{defaultAssistantAgentName, defaultAssistantAgentID},
		{defaultOperatorAgentName, defaultOperatorAgentID},
		{defaultStakeholderAgentName, defaultStakeholderAgentID},
		{defaultArchivistAgentName, defaultArchivistAgentID},
		{defaultVerifierAgentName, defaultVerifierAgentID},
	}

	for _, spec := range specialists {
		model, err := m.store.GetAgentModelByName(spec.name)
		if err != nil {
			// Agent doesn't exist yet, create it from built-in defaults
			builtins := defaultBuiltInStandaloneAgents(agentName)
			var found *AgentModel
			for _, b := range builtins {
				if b.Name == spec.name {
					found = b
					break
				}
			}
			if found == nil {
				continue
			}
			if err := m.ensureBuiltInStandaloneAgent(ctx, found); err != nil {
				return err
			}
			model, err = m.store.GetAgentModelByName(spec.name)
			if err != nil {
				continue
			}
		}

		// Check if already a member
		for _, team := range model.Teams {
			if team.TeamID == defaultTeamID {
				continue
			}
		}

		if err := m.store.SaveTeamMembership(&TeamMembership{
			AgentID: model.ID,
			TeamID:  defaultTeamID,
			Role:    AgentKindSpecialist,
		}); err != nil {
			return err
		}
		m.clearCachedAgent(model.Name)
	}

	return nil
}

func (m *TeamManager) detachBuiltInStandaloneAgentsFromDefaultTeam(names ...string) error {
	for _, name := range names {
		model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
		if err != nil {
			continue
		}
		if err := m.store.DeleteTeamMembership(model.ID, defaultTeamID); err != nil {
			return err
		}
		m.clearCachedAgent(model.Name)
	}
	return nil
}
