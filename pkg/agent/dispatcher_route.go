package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
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
	Blocked            bool
	DispatchTaskID     string
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
	result, err := routeBuiltInRequestWithDispatcher(ctx, prompt, queryContext, mcpToolNames, func(ctx context.Context, agentName, instruction string, opts []RunOption) (string, error) {
		return m.dispatchTaskWithOptions(ctx, agentName, instruction, "", opts)
	})
	if err != nil || result == nil {
		return result, err
	}
	if strings.TrimSpace(result.DispatchTaskID) != "" {
		if task, getErr := m.store.GetTask(result.DispatchTaskID); getErr == nil && task != nil {
			result.Blocked = task.Status == "blocked"
		}
	}
	return result, nil
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
		WithPTCEnabled(false),
	}
	parentTaskID := uuid.NewString()
	runOptions = append(runOptions, WithParentTaskID(parentTaskID))

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
		routerRaw, routerErr = dispatch(ctx, defaultIntentRouterAgentName, buildIntentRouterTaskPrompt(prompt, availableMCPTools), append(runOptions, WithTaskID(parentTaskID+":router")))
	}()

	go func() {
		defer wg.Done()
		optimizerRaw, optimizerErr = dispatch(ctx, defaultPromptOptimizerAgentName, buildPromptOptimizerTaskPrompt(prompt), append(runOptions, WithTaskID(parentTaskID+":optimizer")))
	}()

	wg.Wait()

	decision := parseIntentRouterDecision(routerRaw)
	fallbackDecision := fallbackBuiltInRouteDecision(prompt)
	decision = stabilizeBuiltInMemoryRouteDecision(prompt, decision, fallbackDecision)
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
	dispatchTaskID := parentTaskID + ":dispatch"
	result, err := dispatch(ctx, decision.TargetAgent, finalPrompt, append(runOptions, WithTaskID(dispatchTaskID)))
	if err != nil {
		return nil, err
	}
	verificationResult := ""
	if shouldVerifyBuiltInDispatchCompletion(prompt, decision) {
		verifyPrompt := buildBuiltInCompletionVerificationPrompt(prompt, finalPrompt, result, decision)
		if strings.TrimSpace(verifyPrompt) != "" {
			if verifyRaw, verifyErr := dispatch(ctx, defaultVerifierAgentName, verifyPrompt, append(runOptions, WithTaskID(parentTaskID+":verify"))); verifyErr == nil {
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
		DispatchTaskID:     dispatchTaskID,
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
Valid TARGET_AGENT values: Responder, Operator, Evaluator, Archivist, Verifier.
Rules:
- Use Archivist for memory_save, memory_recall, preferences, schedules, durable facts, and planned events.
- Also use Archivist for recalling internal project/team facts such as code names, owner mappings, label semantics, recurring plans, and the meaning of named internal terms.
- Use Operator for files, commands, execution, validation, environment inspection, MCP-backed actions, desktop automation, local app control, and device control.
- If the user is asking the system to do something through a configured tool or server, prefer Operator.
- Use Evaluator for product, business, prioritization, requirements, scope, and acceptance criteria.
- Use Verifier for checking or validating a candidate answer, especially conflicts or corrections.
- Use Responder for everything else.
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
	case strings.ToLower(defaultResponderAgentName):
		return defaultResponderAgentName
	case strings.ToLower(defaultOperatorAgentName):
		return defaultOperatorAgentName
	case strings.ToLower(defaultEvaluatorAgentName):
		return defaultEvaluatorAgentName
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
		IntentType:        strings.TrimSpace(intent.IntentType),
		Reason:            "fallback heuristic route",
		NeedsOptimization: strings.TrimSpace(intent.Transition) == "text_first" && intent.Confidence < 0.75,
	}

	if strings.TrimSpace(intent.PreferredAgent) != "" {
		decision.TargetAgent = strings.TrimSpace(intent.PreferredAgent)
	}

	switch intent.IntentType {
	case "memory_save", "memory_recall":
		decision.TargetAgent = defaultArchivistAgentName
	case "file_create", "file_read", "file_edit":
		decision.TargetAgent = defaultOperatorAgentName
	case "analysis", "general_qa", "rag_query":
		decision.TargetAgent = defaultResponderAgentName
	}

	if decision.TargetAgent == "" {
		decision.TargetAgent = defaultResponderAgentName
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
	if fallback.TargetAgent == defaultArchivistAgentName && strings.EqualFold(strings.TrimSpace(fallback.IntentType), "memory_recall") {
		return decision.TargetAgent != defaultArchivistAgentName
	}
	if fallback.TargetAgent == defaultOperatorAgentName && promptLooksLikeOperatorControlRequest(prompt) {
		return decision.TargetAgent == defaultResponderAgentName
	}
	return false
}

func stabilizeBuiltInMemoryRouteDecision(prompt string, decision, fallback builtInRouteDecision) builtInRouteDecision {
	fallbackIntent := strings.TrimSpace(fallback.IntentType)
	if fallback.TargetAgent != defaultArchivistAgentName {
		return decision
	}
	if fallbackIntent != "memory_save" && fallbackIntent != "memory_recall" {
		return decision
	}
	if strings.TrimSpace(decision.TargetAgent) == defaultArchivistAgentName && !decision.NeedsOptimization {
		return decision
	}

	decision.TargetAgent = defaultArchivistAgentName
	decision.IntentType = fallbackIntent
	// Memory intents should preserve the user's original wording. Let Archivist
	// distill/save or answer from memory directly instead of letting optimizer
	// rewrite the request into a different question.
	decision.NeedsOptimization = false
	if strings.TrimSpace(decision.Reason) == "" {
		decision.Reason = "deterministic memory route"
	}
	return decision
}

func promptLooksLikeOperatorControlRequest(prompt string) bool {
	decision := fallbackBuiltInRouteDecision(normalizeTaskPrompt(prompt))
	return strings.EqualFold(decision.TargetAgent, defaultOperatorAgentName)
}

func DefaultEntryAgentForPrompt(prompt string) string {
	intent := (&Planner{}).fallbackIntentRecognition(normalizeTaskPrompt(prompt))
	if looksLikeDirectExecutionPrompt(prompt, intent) {
		return defaultOperatorAgentName
	}
	if preferred := preferredEntryAgentForIntent(intent); strings.EqualFold(preferred, defaultOperatorAgentName) {
		return defaultOperatorAgentName
	}
	decision := fallbackBuiltInRouteDecision(normalizeTaskPrompt(prompt))
	if strings.EqualFold(decision.TargetAgent, defaultOperatorAgentName) {
		return defaultOperatorAgentName
	}
	return defaultDispatcherAgentName
}

func looksLikeDirectExecutionPrompt(prompt string, intent *IntentRecognitionResult) bool {
	prompt = normalizeTaskPrompt(prompt)
	if strings.TrimSpace(prompt) == "" || looksLikeInformationSeekingQuery(prompt) {
		return false
	}
	if intent != nil && (intent.IntentType == "memory_save" || intent.IntentType == "memory_recall") {
		return false
	}
	lower := strings.ToLower(prompt)
	return containsAny(lower, []string{
		"run", "start", "stop", "execute", "launch", "open", "click", "turn on", "turn off", "make",
		"让", "启动", "停止", "执行", "打开", "点击", "运行", "关掉", "开启",
	})
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
	if strings.EqualFold(decision.TargetAgent, defaultArchivistAgentName) &&
		strings.EqualFold(strings.TrimSpace(decision.IntentType), "memory_recall") {
		subqueries := dispatcherMemoryRecallQueries(originalPrompt)
		var subqueryHint string
		if len(subqueries) > 1 {
			subqueryHint = "\nSuggested memory_recall subqueries:"
			for _, query := range subqueries {
				subqueryHint += "\n- " + query
			}
		}
		return strings.TrimSpace(optimizedPrompt + "\n\nRecall contract:\n- Answer from memory.\n- If the question asks for multiple facts, split it into multiple focused memory_recall queries before answering.\n- Merge the recalled facts into one concise final answer.\n- If one part is missing, say “信息不足” for that part instead of mixing in 'I couldn't find that in memory.'" + subqueryHint)
	}
	if strings.EqualFold(decision.TargetAgent, defaultOperatorAgentName) && !looksLikeInformationSeekingQuery(originalPrompt) {
		return strings.TrimSpace(optimizedPrompt + "\n\nExecution contract:\n- Execute the request directly using available tools or MCP capabilities when possible.\n- Do not bounce the task back to Dispatcher or say you are routing it.\n- If the request is actionable and reasonable defaults suffice, choose practical defaults instead of asking unnecessary follow-up questions.\n- Before giving your final answer, explicitly verify from tool results or observed effects whether the request is actually complete, and state the concrete evidence in your answer.\n- If blocked, call task_blocked with exactly what blocked execution and what was attempted.\n\n" + FinishOrBlockContract)
	}
	return optimizedPrompt
}

func shouldVerifyBuiltInDispatchCompletion(originalPrompt string, decision builtInRouteDecision) bool {
	if !strings.EqualFold(decision.TargetAgent, defaultOperatorAgentName) {
		return false
	}
	intentType := strings.TrimSpace(decision.IntentType)
	return strings.EqualFold(intentType, "tool_execution") || strings.EqualFold(intentType, "device_or_robot_control")
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
