package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

const (
	optimizedPromptBeginMarker = "OPTIMIZED_PROMPT_BEGIN"
	optimizedPromptEndMarker   = "OPTIMIZED_PROMPT_END"
)

type builtInDispatchFunc func(context.Context, string, string, []RunOption) (string, error)

type builtInRouteDecision struct {
	TargetAgent       string
	IntentType        string
	Reason            string
	NeedsOptimization bool
}

type builtInRouteResult struct {
	TargetAgent        string
	IntentType         string
	Reason             string
	OptimizedPrompt    string
	Result             string
	VerificationResult string
	RouterRaw          string
	OptimizerRaw       string
}

func (m *TeamManager) routeBuiltInRequest(ctx context.Context, prompt string, queryContext domain.MemoryQueryContext) (*builtInRouteResult, error) {
	var mcpToolNames []string
	if svc, err := m.getOrBuildService(defaultOperatorAgentName); err == nil && svc != nil && svc.mcpService != nil {
		for _, t := range svc.mcpService.ListTools() {
			mcpToolNames = append(mcpToolNames, t.Function.Name+": "+t.Function.Description)
		}
	}
	return routeBuiltInRequestWithDispatcher(ctx, prompt, queryContext, mcpToolNames, func(ctx context.Context, agentName, instruction string, opts []RunOption) (string, error) {
		return m.dispatchTaskWithOptions(ctx, agentName, instruction, "", opts)
	})
}

func routeBuiltInRequestWithDispatcher(ctx context.Context, prompt string, queryContext domain.MemoryQueryContext, availableMCPTools []string, dispatch builtInDispatchFunc) (*builtInRouteResult, error) {
	prompt = normalizeTaskPrompt(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if dispatch == nil {
		return nil, fmt.Errorf("dispatch function is required")
	}

	runOptions := []RunOption{
		WithInheritedMemoryScope(queryContext.AgentID, queryContext.TeamID, queryContext.UserID),
	}

	var (
		routerRaw    string
		routerErr    error
		optimizerRaw string
		optimizerErr error
	)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		routerRaw, routerErr = dispatch(ctx, defaultIntentRouterAgentName, buildIntentRouterTaskPrompt(prompt, availableMCPTools), runOptions)
	}()

	go func() {
		defer wg.Done()
		optimizerRaw, optimizerErr = dispatch(ctx, defaultPromptOptimizerAgentName, buildPromptOptimizerTaskPrompt(prompt), runOptions)
	}()

	wg.Wait()

	decision := parseIntentRouterDecision(routerRaw)
	fallbackDecision := fallbackBuiltInRouteDecision(prompt)
	if shouldOverrideBuiltInRouteDecision(prompt, decision, fallbackDecision) {
		decision = fallbackDecision
	}
	if decision.TargetAgent == "" {
		decision = fallbackDecision
		if routerErr != nil && strings.TrimSpace(decision.Reason) == "" {
			decision.Reason = routerErr.Error()
		}
	}

	optimizedPrompt := prompt
	if decision.NeedsOptimization && optimizerErr == nil {
		if parsed := parseOptimizedPrompt(optimizerRaw, ""); parsed != "" {
			optimizedPrompt = parsed
		}
	}

	finalPrompt := buildFinalBuiltInDispatchPrompt(prompt, optimizedPrompt, decision)
	result, err := dispatch(ctx, decision.TargetAgent, finalPrompt, runOptions)
	if err != nil {
		return nil, err
	}
	verificationResult := ""
	if shouldVerifyBuiltInDispatchCompletion(prompt, decision) {
		verifyPrompt := buildBuiltInCompletionVerificationPrompt(prompt, finalPrompt, result, decision)
		if strings.TrimSpace(verifyPrompt) != "" {
			if verifyRaw, verifyErr := dispatch(ctx, defaultVerifierAgentName, verifyPrompt, runOptions); verifyErr == nil {
				verificationResult = strings.TrimSpace(verifyRaw)
				if verified := applyBuiltInVerificationResult(result, verificationResult); strings.TrimSpace(verified) != "" {
					result = verified
				}
			}
		}
	}

	return &builtInRouteResult{
		TargetAgent:        decision.TargetAgent,
		IntentType:         decision.IntentType,
		Reason:             decision.Reason,
		OptimizedPrompt:    finalPrompt,
		Result:             result,
		VerificationResult: verificationResult,
		RouterRaw:          routerRaw,
		OptimizerRaw:       optimizerRaw,
	}, nil
}

func buildIntentRouterTaskPrompt(userPrompt string, availableMCPTools []string) string {
	toolsSection := ""
	if len(availableMCPTools) > 0 {
		toolsSection = "\nCurrently available MCP tools for Operator:\n"
		for _, t := range availableMCPTools {
			toolsSection += "  - " + t + "\n"
		}
		toolsSection += "If any of these tools could satisfy the user's request, use Operator.\n"
	}
	return strings.TrimSpace(fmt.Sprintf(`Classify the user's request and choose exactly one built-in target agent.
Valid TARGET_AGENT values: Assistant, Operator, Stakeholder, Archivist, Verifier.
Rules:
- Use Archivist for memory_save, memory_recall, preferences, schedules, durable facts, and planned events.
- Use Operator for files, commands, execution, validation, environment inspection, MCP-backed actions, desktop automation, local app control, and device control.
- If the user is asking the system to do something through a configured tool or server, prefer Operator.
- Use Stakeholder for product, business, prioritization, requirements, scope, and acceptance criteria.
- Use Verifier for checking or validating a candidate answer, especially conflicts or corrections.
- Use Assistant for everything else.
%sAlso decide if the request needs prompt optimization before dispatch.
NEEDS_OPTIMIZATION: yes — only when the request is vague, ambiguous, or missing key context that a rewrite would meaningfully clarify.
NEEDS_OPTIMIZATION: no — when the request is already clear, direct, or contains specific facts (dates, names, numbers).
Return exactly:
TARGET_AGENT: <one value>
INTENT_TYPE: <short intent label>
REASON: <one short sentence>
NEEDS_OPTIMIZATION: yes|no

User request:
%s`, toolsSection, strings.TrimSpace(userPrompt)))
}

func buildPromptOptimizerTaskPrompt(userPrompt string) string {
	return strings.TrimSpace(fmt.Sprintf(`Rewrite the user's request into a clean downstream instruction for another built-in agent.
Rules:
- Preserve facts, dates, names, constraints, and intent.
- Do not invent missing details.
- Keep relative dates and times exactly as given unless the user already provided an absolute date.
- Do not answer the request yourself.
Return exactly:
%s
<optimized prompt>
%s

User request:
%s`, optimizedPromptBeginMarker, optimizedPromptEndMarker, strings.TrimSpace(userPrompt)))
}

func parseIntentRouterDecision(text string) builtInRouteDecision {
	text = strings.TrimSpace(text)
	if text == "" {
		return builtInRouteDecision{}
	}

	var decision builtInRouteDecision
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "TARGET_AGENT:"):
			decision.TargetAgent = normalizeBuiltInTargetAgent(strings.TrimSpace(line[len("TARGET_AGENT:"):]))
		case strings.HasPrefix(upper, "INTENT_TYPE:"):
			decision.IntentType = strings.TrimSpace(line[len("INTENT_TYPE:"):])
		case strings.HasPrefix(upper, "REASON:"):
			decision.Reason = strings.TrimSpace(line[len("REASON:"):])
		case strings.HasPrefix(upper, "NEEDS_OPTIMIZATION:"):
			val := strings.ToLower(strings.TrimSpace(line[len("NEEDS_OPTIMIZATION:"):]))
			decision.NeedsOptimization = val == "yes"
		}
	}
	return decision
}

func parseOptimizedPrompt(text, fallback string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return strings.TrimSpace(fallback)
	}

	start := strings.Index(text, optimizedPromptBeginMarker)
	end := strings.Index(text, optimizedPromptEndMarker)
	if start >= 0 && end > start {
		content := strings.TrimSpace(text[start+len(optimizedPromptBeginMarker) : end])
		if content != "" {
			return content
		}
	}

	return firstNonEmpty(text, fallback)
}

func normalizeBuiltInTargetAgent(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case strings.ToLower(defaultAssistantAgentName):
		return defaultAssistantAgentName
	case strings.ToLower(defaultOperatorAgentName):
		return defaultOperatorAgentName
	case strings.ToLower(defaultStakeholderAgentName):
		return defaultStakeholderAgentName
	case strings.ToLower(defaultArchivistAgentName):
		return defaultArchivistAgentName
	case strings.ToLower(defaultVerifierAgentName):
		return defaultVerifierAgentName
	default:
		return ""
	}
}

func fallbackBuiltInRouteDecision(prompt string) builtInRouteDecision {
	intent := (&Planner{}).fallbackIntentRecognition(prompt)
	decision := builtInRouteDecision{
		IntentType: strings.TrimSpace(intent.IntentType),
		Reason:     "fallback heuristic route",
	}

	switch intent.IntentType {
	case "memory_save", "memory_recall":
		decision.TargetAgent = defaultArchivistAgentName
	case "file_create", "file_read", "file_edit":
		decision.TargetAgent = defaultOperatorAgentName
	case "analysis", "general_qa", "web_search", "rag_query":
		decision.TargetAgent = defaultAssistantAgentName
	}

	if decision.TargetAgent == "" {
		decision.TargetAgent = defaultAssistantAgentName
	}
	return decision
}

func shouldOverrideBuiltInRouteDecision(prompt string, decision, fallback builtInRouteDecision) bool {
	if strings.TrimSpace(decision.TargetAgent) == "" || strings.TrimSpace(fallback.TargetAgent) == "" {
		return false
	}
	if decision.TargetAgent == fallback.TargetAgent {
		return false
	}
	if fallback.TargetAgent == defaultOperatorAgentName && promptLooksLikeOperatorControlRequest(prompt) {
		return decision.TargetAgent == defaultAssistantAgentName
	}
	return false
}

func promptLooksLikeOperatorControlRequest(prompt string) bool {
	decision := fallbackBuiltInRouteDecision(normalizeTaskPrompt(prompt))
	return strings.EqualFold(decision.TargetAgent, defaultOperatorAgentName)
}

func DefaultEntryAgentForPrompt(prompt string) string {
	decision := fallbackBuiltInRouteDecision(normalizeTaskPrompt(prompt))
	if strings.EqualFold(decision.TargetAgent, defaultOperatorAgentName) {
		return defaultOperatorAgentName
	}
	return defaultConciergeAgentName
}

func buildFinalBuiltInDispatchPrompt(originalPrompt, optimizedPrompt string, decision builtInRouteDecision) string {
	originalPrompt = normalizeTaskPrompt(originalPrompt)
	optimizedPrompt = firstNonEmpty(strings.TrimSpace(optimizedPrompt), strings.TrimSpace(originalPrompt))
	if strings.EqualFold(decision.TargetAgent, defaultArchivistAgentName) &&
		(strings.EqualFold(strings.TrimSpace(decision.IntentType), "memory_save") ||
			strings.EqualFold(strings.TrimSpace(decision.IntentType), "schedule_event") ||
			looksLikeImplicitMemorySavePrompt(originalPrompt)) {
		if hasExplicitMemorySavePrefix(optimizedPrompt) {
			return optimizedPrompt
		}
		return "记住：" + optimizedPrompt
	}
	if strings.EqualFold(decision.TargetAgent, defaultOperatorAgentName) && !looksLikeInformationSeekingQuery(originalPrompt) {
		return strings.TrimSpace(optimizedPrompt + "\n\nExecution contract:\n- Execute the request directly using available tools or MCP capabilities when possible.\n- Do not bounce the task back to Concierge or say you are routing it.\n- If the request is actionable and reasonable defaults suffice, choose practical defaults instead of asking unnecessary follow-up questions.\n- Before giving your final answer, explicitly verify from tool results or observed effects whether the request is actually complete, and state the concrete evidence in your answer.\n- If blocked, say exactly what blocked execution.")
	}
	return optimizedPrompt
}

func shouldVerifyBuiltInDispatchCompletion(originalPrompt string, decision builtInRouteDecision) bool {
	if !strings.EqualFold(decision.TargetAgent, defaultOperatorAgentName) {
		return false
	}
	return promptLooksLikeOperatorControlRequest(originalPrompt) || strings.EqualFold(strings.TrimSpace(decision.IntentType), "tool_execution") || strings.EqualFold(strings.TrimSpace(decision.IntentType), "device_or_robot_control")
}

func buildBuiltInCompletionVerificationPrompt(originalPrompt, executedPrompt, previousResult string, decision builtInRouteDecision) string {
	if !shouldVerifyBuiltInDispatchCompletion(originalPrompt, decision) {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf(`You are verifying a completed execution claim from Operator.

Original user request:
%s

Instruction Operator executed:
%s

Operator's reported result:
%s

Your task:
- Independently verify whether the requested action is complete.
- Use the available tools or MCP capabilities to gather independent evidence.
- Do not repeat the primary action unless verification genuinely requires it or the action is clearly safe and idempotent.
- If the request is complete, reply exactly: VERIFIED_COMPLETE: <one concise sentence with concrete evidence>.
- If the request is still blocked, incomplete, or unverifiable, reply exactly: VERIFIED_BLOCKED: <one concise sentence with the blocker or missing evidence>.
`, strings.TrimSpace(originalPrompt), strings.TrimSpace(executedPrompt), strings.TrimSpace(previousResult)))
}

func applyBuiltInVerificationResult(previousResult, verification string) string {
	previousResult = strings.TrimSpace(previousResult)
	verification = strings.TrimSpace(verification)
	switch {
	case strings.HasPrefix(verification, "VERIFIED_COMPLETE:"):
		evidence := strings.TrimSpace(strings.TrimPrefix(verification, "VERIFIED_COMPLETE:"))
		if evidence == "" {
			return previousResult
		}
		if previousResult == "" {
			return evidence
		}
		return strings.TrimSpace(previousResult + "\n\nVerifier confirmation: " + evidence)
	case strings.HasPrefix(verification, "VERIFIED_BLOCKED:"):
		blocker := strings.TrimSpace(strings.TrimPrefix(verification, "VERIFIED_BLOCKED:"))
		if blocker == "" {
			return previousResult
		}
		if previousResult == "" {
			return blocker
		}
		return strings.TrimSpace(previousResult + "\n\nVerifier warning: " + blocker)
	default:
		return previousResult
	}
}

func looksLikeImplicitMemorySavePrompt(prompt string) bool {
	intent := (&Planner{}).fallbackIntentRecognition(prompt)
	return strings.EqualFold(strings.TrimSpace(intent.IntentType), "memory_save") && !looksLikeInformationSeekingQuery(prompt)
}

func hasExplicitMemorySavePrefix(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	prefixes := []string{
		"remember:",
		"save to memory",
		"please remember",
		"remember that",
		"记住:",
		"记住：",
		"请记住",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
