package agent

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	ArchivistAgentName      = "Archivist"
	archivistVerifierPrefix = "VERIFIER_NEEDED:"
)

func (m *TeamManager) configureConciergeArchivistHook(concierge *Service) {
	m.ConfigureFollowUpAgentHook(concierge, FollowUpAgentPolicy{
		HookDescription: "concierge_archivist_async_review",
		AgentName:       ArchivistAgentName,
		Priority:        100,
		ScenarioTags:    []string{"memory_router", "save", "recall", "cleanup"},
		ShouldDispatch:  shouldDispatchArchivistFromHook,
		BuildPrompt:     buildArchivistReviewPromptFromHook,
	})
}

func shouldDispatchArchivistFromHook(data HookData) bool {
	goal := strings.TrimSpace(data.Goal)
	if goal == "" {
		return false
	}
	if looksLikeDelegatedAgentRequest(goal) {
		return false
	}
	if hookAlreadyDelegatedToAgent(data, ArchivistAgentName) {
		return false
	}
	return true
}

func buildArchivistReviewPromptFromHook(data HookData) string {
	reply := strings.TrimSpace(formatResultForContent(data.Result))
	if reply == "" {
		reply = "(empty response)"
	}

	intent := strings.TrimSpace(metadataString(data.Metadata, "intent"))
	if intent == "" {
		intent = "-"
	}
	toolsUsed := hookToolsUsed(data.Metadata)
	memories := hookMemoryContents(data.Metadata)
	toolSummary := "-"
	if len(toolsUsed) > 0 {
		toolSummary = strings.Join(toolsUsed, ", ")
	}
	memorySummary := "-"
	if len(memories) > 0 {
		memorySummary = strings.Join(limitStrings(memories, 5), "\n- ")
		memorySummary = "- " + memorySummary
	}

	return fmt.Sprintf(`You are Archivist, the background memory-routing agent for AgentGo.
Review this completed Concierge conversation turn and decide whether memory work is needed.

Your job:
- First decide whether this turn is memory-related.
- If it is memory-related, decide whether the correct follow-up action is primarily SAVE, RECALL, CLEANUP, or NONE.
- Then perform the needed memory action yourself using available memory tools when appropriate.
- SAVE means store a durable fact or preference extracted from the turn.
- RECALL means query memory to verify or improve the remembered answer.
- CLEANUP means delete or correct low-value, duplicate, or contradictory memory directly relevant to this turn.
- NONE means no memory action is useful.

Constraints:
- Only touch memories directly relevant to this conversation turn.
- Be conservative with deletions.
- Do not do unrelated filesystem, product, or web work.
- Never save the user's question text itself as memory.
- Prefer the shortest durable phrasing and avoid duplicate memories.

Output rule:
- If no memory action is needed, reply exactly: NO_MEMORY_ACTION_NEEDED
- If memory action is needed, do the memory work first, then reply with one short user-facing supplement message only, prefixed with "Supplement:".
- Keep the supplement under 2 sentences and do not repeat the full Concierge answer.

Conversation context:
- Session ID: %s
- Intent: %s
- Concierge tools used: %s
- User message: %s
- Concierge reply: %s
- Retrieved memories from Concierge turn:
%s
`, strings.TrimSpace(data.SessionID), intent, toolSummary, strings.TrimSpace(data.Goal), reply, memorySummary)
}

func hookMemoryContents(metadata map[string]interface{}) []string {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata["memory_summaries"]
	if !ok {
		return nil
	}
	items, ok := raw.([]map[string]interface{})
	if ok {
		out := make([]string, 0, len(items))
		for _, item := range items {
			content := strings.TrimSpace(metadataString(item, "content"))
			if content != "" {
				out = append(out, content)
			}
		}
		return out
	}
	if ifaceItems, ok := raw.([]interface{}); ok {
		out := make([]string, 0, len(ifaceItems))
		for _, item := range ifaceItems {
			if m, ok := item.(map[string]interface{}); ok {
				content := strings.TrimSpace(metadataString(m, "content"))
				if content != "" {
					out = append(out, content)
				}
			}
		}
		return out
	}
	return nil
}

func distinctMemoryContents(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func looksLikeDelegatedAgentRequest(goal string) bool {
	goal = strings.TrimSpace(goal)
	return strings.HasPrefix(goal, "@")
}

func metadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return value
}

func hookIntent(metadata map[string]interface{}) string {
	return strings.TrimSpace(strings.ToLower(metadataString(metadata, "intent")))
}

func hookToolsUsed(metadata map[string]interface{}) []string {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata["tools_used"]
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func containsStringFold(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func parseArchivistVerifierEscalation(text string) (displayText, verifierPrompt string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, archivistVerifierPrefix) {
		return "", "", false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, archivistVerifierPrefix))
	if payload == "" {
		payload = "Archivist found conflicting or low-confidence recall and requests verification."
	}
	candidate := parseVerifierEscalationField(payload, "candidate")
	reason := parseVerifierEscalationField(payload, "reason")
	if candidate == "" {
		candidate = payload
	}
	if reason == "" {
		reason = "Archivist found conflicting or low-confidence recall."
	}
	displayText = fmt.Sprintf("Supplement: Archivist requested verification for candidate %s.", candidate)
	verifierPrompt = fmt.Sprintf("Candidate answer from Archivist: %s\nReason for verification: %s\nVerify whether the candidate should stand, be qualified, or be corrected using memory. If the candidate is still the best answer, say so explicitly before any qualification.", candidate, reason)
	return displayText, verifierPrompt, true
}

func parseVerifierEscalationField(payload, field string) string {
	pattern := regexp.MustCompile(field + `=([^;]+)`)
	matches := pattern.FindStringSubmatch(payload)
	if len(matches) != 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func hookAlreadyDelegatedToAgent(data HookData, agentName string) bool {
	toolsUsed := hookToolsUsed(data.Metadata)
	if !containsStringFold(toolsUsed, "submit_agent_task") && !containsStringFold(toolsUsed, "submit_builtin_agent_task") {
		return false
	}
	resultText := strings.ToLower(strings.TrimSpace(formatResultForContent(data.Result)))
	agentName = strings.ToLower(strings.TrimSpace(agentName))
	if agentName == "" || resultText == "" {
		return false
	}
	return strings.Contains(resultText, agentName) && (strings.Contains(resultText, "queued") || strings.Contains(resultText, "background") || strings.Contains(resultText, "task id"))
}
