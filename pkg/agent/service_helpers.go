package agent

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/skills"
)

const sessionContextSentSkillReminders = "skills.sent_relevant_names"

type skillReminder struct {
	// Names are the skills newly named in this reminder — the ones the
	// session has not been told about yet.
	Names []string
	// All are every skill that cleared the relevance floor this turn,
	// whether or not it was named again. It is what the tool-preparation
	// policy reads, so a caller assembling a turn without writing to the
	// service (Service.Preview) can hand the same list to the policy.
	All  []string
	Text string
}

type toolPreparationPolicy struct {
	SessionID           string
	TaskID              string
	SearchMode          bool
	ExposeSearchTools   bool
	HideNativeWebSearch bool
	// HideRegistryWebSearch drops a locally registered `web_search` tool when
	// the MCP route is the configured one and is actually present. Offering
	// both is how one question turned into two searches.
	HideRegistryWebSearch bool
	// HideDelegationTools drops the three built-in sub-agent tools when the
	// service has nothing to delegate to. See Service.offersDelegationTools for
	// why an unconfigured delegate_to_subagent is schema bytes for nothing.
	HideDelegationTools bool
	RelevantSkillNames  []string
	// ForceSkillFirst promotes the matched skills' tools to the front of the
	// schema while they are still outstanding. It used to remove every other
	// tool instead; see promoteRelevantSkillTools for why a lexical match is
	// not grounds for taking a capability away.
	ForceSkillFirst bool
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
			if strings.TrimSpace(cfg.ParentTaskID) != "" {
				session.SetContext("runtime.parent_task_id", strings.TrimSpace(cfg.ParentTaskID))
			}
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

func (s *Service) buildWebSearchPromptNote(currentAgent *Agent) string {
	switch s.webSearchSurfaceMode() {
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

// containsStr checks if a string slice contains a string
func containsStr(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
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

// skillMinScore is the relevance a skill must reach before the runtime acts on
// the match — surfacing it in a <skill-discovery> reminder, activating its
// tool, or promoting it up the schema.
//
// Below the floor the match is an incidental word overlap rather than a
// judgement about the task, and acting on one used to be expensive: it spent
// context on an irrelevant skill and, while skill-first was subtractive, took
// unrelated tools away from the model entirely.
func (s *Service) skillMinScore() float64 {
	if s == nil || s.cfg == nil || s.cfg.Skills.MinRelevance == 0 {
		return skills.DefaultSkillMinScore
	}
	if s.cfg.Skills.MinRelevance < 0 {
		return 0
	}
	return s.cfg.Skills.MinRelevance
}
