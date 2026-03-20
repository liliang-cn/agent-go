package agent

import (
	"fmt"
	"strings"
)

const VerifierAgentName = "Verifier"

func (m *SquadManager) configureConciergeVerifierHook(concierge *Service) {
	m.ConfigureFollowUpAgentHook(concierge, FollowUpAgentPolicy{
		HookDescription: "concierge_verifier_async_review",
		AgentName:       VerifierAgentName,
		Priority:        200,
		ScenarioTags:    []string{"recall", "conflict", "verification"},
		ShouldDispatch:  shouldDispatchVerifierFromHook,
		BuildPrompt:     buildVerifierReviewPromptFromHook,
		BuildSupplement: buildDeterministicVerifierSupplement,
	})
}

func shouldDispatchVerifierFromHook(data HookData) bool {
	goal := strings.TrimSpace(data.Goal)
	if goal == "" || looksLikeDelegatedAgentRequest(goal) {
		return false
	}
	if hookAlreadyDelegatedToAgent(data, VerifierAgentName) {
		return false
	}
	intent := hookIntent(data.Metadata)
	toolsUsed := hookToolsUsed(data.Metadata)
	return intent == "memory_recall" || containsStringFold(toolsUsed, "memory_recall")
}

func buildVerifierReviewPromptFromHook(data HookData) string {
	reply := strings.TrimSpace(formatResultForContent(data.Result))
	if reply == "" {
		reply = "(empty response)"
	}
	return fmt.Sprintf(`Review this completed Concierge recall turn for verification quality.
If no verification follow-up is needed, reply exactly: NO_MEMORY_ACTION_NEEDED

Your job:
- Check whether the recalled answer appears consistent with the retrieved memories.
- Flag stale or conflicting remembered values.
- Produce one short user-facing supplement message prefixed with "Supplement:" when useful.
- Prefer evidence-oriented wording and avoid repeating the full Concierge answer.

Conversation context:
- Session ID: %s
- User message: %s
- Concierge reply: %s
`, strings.TrimSpace(data.SessionID), strings.TrimSpace(data.Goal), reply)
}

func buildDeterministicVerifierSupplement(data HookData) (string, bool) {
	intent := hookIntent(data.Metadata)
	toolsUsed := hookToolsUsed(data.Metadata)
	if intent != "memory_recall" && !containsStringFold(toolsUsed, "memory_recall") {
		return "", false
	}

	answer := strings.TrimSpace(formatResultForContent(data.Result))
	if answer == "" {
		return "", false
	}
	memories := hookMemoryContents(data.Metadata)
	if len(memories) == 0 {
		return "", false
	}
	distinct := distinctMemoryContents(memories)
	if len(distinct) > 1 {
		return fmt.Sprintf("Supplement: Verifier sees multiple historical candidates, so treat %s as the current best answer rather than the only remembered value.", answer), true
	}
	return fmt.Sprintf("Supplement: Verifier found a single dominant recalled answer and no competing memory candidate stronger than %s.", answer), true
}
