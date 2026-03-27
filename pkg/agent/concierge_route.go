package agent

import (
	"context"
	"fmt"
	"strings"

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
	TargetAgent     string
	IntentType      string
	Reason          string
	OptimizedPrompt string
	Result          string
	RouterRaw       string
	OptimizerRaw    string
}

func (m *SquadManager) routeBuiltInRequest(ctx context.Context, prompt string, queryContext domain.MemoryQueryContext) (*builtInRouteResult, error) {
	return routeBuiltInRequestWithDispatcher(ctx, prompt, queryContext, func(ctx context.Context, agentName, instruction string, opts []RunOption) (string, error) {
		return m.dispatchTaskWithOptions(ctx, agentName, instruction, "", opts)
	})
}

func routeBuiltInRequestWithDispatcher(ctx context.Context, prompt string, queryContext domain.MemoryQueryContext, dispatch builtInDispatchFunc) (*builtInRouteResult, error) {
	prompt = normalizeTaskPrompt(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if dispatch == nil {
		return nil, fmt.Errorf("dispatch function is required")
	}

	runOptions := []RunOption{
		WithInheritedMemoryScope(queryContext.AgentID, queryContext.SquadID, queryContext.UserID),
	}

	// Always run IntentRouter. PromptOptimizer only runs when IntentRouter says it's needed.
	routerRaw, routerErr := dispatch(ctx, defaultIntentRouterAgentName, buildIntentRouterTaskPrompt(prompt), runOptions)

	decision := parseIntentRouterDecision(routerRaw)
	if decision.TargetAgent == "" {
		decision = fallbackBuiltInRouteDecision(prompt)
		if routerErr != nil && strings.TrimSpace(decision.Reason) == "" {
			decision.Reason = routerErr.Error()
		}
	}

	optimizedPrompt := prompt
	var optimizerRaw string
	if decision.NeedsOptimization {
		optimizerRaw, _ = dispatch(ctx, defaultPromptOptimizerAgentName, buildPromptOptimizerTaskPrompt(prompt), runOptions)
		if parsed := parseOptimizedPrompt(optimizerRaw, ""); parsed != "" {
			optimizedPrompt = parsed
		}
	}

	finalPrompt := buildFinalBuiltInDispatchPrompt(prompt, optimizedPrompt, decision)
	result, err := dispatch(ctx, decision.TargetAgent, finalPrompt, runOptions)
	if err != nil {
		return nil, err
	}

	return &builtInRouteResult{
		TargetAgent:     decision.TargetAgent,
		IntentType:      decision.IntentType,
		Reason:          decision.Reason,
		OptimizedPrompt: finalPrompt,
		Result:          result,
		RouterRaw:       routerRaw,
		OptimizerRaw:    optimizerRaw,
	}, nil
}

func buildIntentRouterTaskPrompt(userPrompt string) string {
	return strings.TrimSpace(fmt.Sprintf(`Classify the user's request and choose exactly one built-in target agent.
Valid TARGET_AGENT values: Assistant, Operator, Stakeholder, Archivist, Verifier.
Rules:
- Use Archivist for memory_save, memory_recall, preferences, schedules, durable facts, and planned events.
- Use Operator for files, commands, execution, validation, and environment inspection.
- Use Stakeholder for product, business, prioritization, requirements, scope, and acceptance criteria.
- Use Verifier for checking or validating a candidate answer, especially conflicts or corrections.
- Use Assistant for everything else.
Also decide if the request needs prompt optimization before dispatch.
NEEDS_OPTIMIZATION: yes — only when the request is vague, ambiguous, or missing key context that a rewrite would meaningfully clarify.
NEEDS_OPTIMIZATION: no — when the request is already clear, direct, or contains specific facts (dates, names, numbers).
Return exactly:
TARGET_AGENT: <one value>
INTENT_TYPE: <short intent label>
REASON: <one short sentence>
NEEDS_OPTIMIZATION: yes|no

User request:
%s`, strings.TrimSpace(userPrompt)))
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

	lower := strings.ToLower(strings.TrimSpace(prompt))
	switch {
	case containsAny(lower, []string{"priorit", "roadmap", "acceptance criteria", "scope", "business", "product", "requirement", "需求", "优先级", "范围", "产品", "验收标准"}):
		decision.TargetAgent = defaultStakeholderAgentName
		if decision.IntentType == "" {
			decision.IntentType = "product_judgment"
		}
	case containsAny(lower, []string{"verify", "verification", "check whether", "validate", "double-check", "correct or not", "冲突", "核对", "验证", "校验"}):
		decision.TargetAgent = defaultVerifierAgentName
		if decision.IntentType == "" {
			decision.IntentType = "verification"
		}
	}

	if decision.TargetAgent == "" {
		decision.TargetAgent = defaultAssistantAgentName
	}
	return decision
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
	return optimizedPrompt
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
