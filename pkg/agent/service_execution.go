package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	memorypkg "github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
	"golang.org/x/sync/errgroup"
)

type executionMetrics struct {
	toolCalls       int
	toolsUsed       []string
	estimatedTokens int
}

const (
	recentConversationWindow = 6
	olderConversationLimit   = 12
)

func isExplicitMemoryRecallQuery(goal string) bool {
	goal = normalizeTaskPrompt(goal)
	query := strings.ToLower(strings.TrimSpace(goal))
	if query == "" {
		return false
	}

	storePrefixes := []string{
		"remember:",
		"save to memory",
		"记住:",
		"记住：",
		"请记住",
	}
	for _, prefix := range storePrefixes {
		if strings.HasPrefix(query, prefix) {
			return false
		}
	}

	questionHints := []string{
		"what", "which", "who", "where", "when", "how",
		"什么", "哪个", "谁", "哪里", "什么时候", "怎么",
	}
	recallHints := []string{
		"remember", "recall", "memory", "from memory", "remind me",
		"i asked you to remember", "previously asked you to remember", "earlier asked you to remember",
		"you remember",
		"记得", "记忆", "从记忆里", "根据记忆", "我之前让你记住", "我让你记住", "之前说过",
	}

	hasQuestionHint := false
	for _, hint := range questionHints {
		if strings.Contains(query, hint) {
			hasQuestionHint = true
			break
		}
	}

	for _, hint := range recallHints {
		if strings.Contains(query, hint) {
			return hasQuestionHint || strings.Contains(query, "reply with only") || strings.Contains(query, "只回复") || strings.Contains(query, "只返回")
		}
	}

	scheduleTimeHints := []string{
		"today", "tomorrow", "tonight", "this afternoon", "this evening", "next week",
		"今天", "明天", "今晚", "下午", "上午", "早上", "晚上", "下周",
	}
	scheduleSubjectHints := []string{
		"schedule", "plan", "plans", "agenda", "appointment", "meeting", "todo",
		"安排", "计划", "日程", "行程", "约", "待办", "会议",
	}
	if looksLikeInformationSeekingQuery(goal) && containsAny(query, scheduleTimeHints) && containsAny(query, scheduleSubjectHints) {
		return true
	}

	return false
}

func looksLikeInformationSeekingQuery(goal string) bool {
	goal = normalizeTaskPrompt(goal)
	query := strings.ToLower(strings.TrimSpace(goal))
	if query == "" {
		return false
	}
	if strings.ContainsAny(goal, "?\n\r\t") || strings.Contains(goal, "？") {
		return true
	}
	prefixes := []string{
		"what ", "which ", "who ", "where ", "when ", "why ", "how ",
		"can you", "could you", "would you", "will you",
		"tell me", "explain", "describe", "list ", "show ", "find ", "search ", "compare ",
		"什么", "哪个", "谁", "哪里", "什么时候", "为什么", "怎么",
		"告诉我", "解释", "描述", "列出", "展示", "查找", "搜索", "比较",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(query, prefix) {
			return true
		}
	}
	return false
}

func isExplicitMemoryRecallIntent(goal string, intent *IntentRecognitionResult) bool {
	if intent != nil && strings.TrimSpace(intent.IntentType) == "memory_recall" {
		return true
	}
	return isExplicitMemoryRecallQuery(goal)
}

func isExplicitMemorySaveIntent(goal string, intent *IntentRecognitionResult) bool {
	goal = normalizeTaskPrompt(goal)
	if looksLikeInformationSeekingQuery(goal) {
		return false
	}
	if intent != nil && strings.TrimSpace(intent.IntentType) == "memory_save" {
		return true
	}

	goalLower := strings.ToLower(strings.TrimSpace(goal))
	return strings.HasPrefix(goalLower, "remember:") ||
		strings.HasPrefix(goalLower, "save to memory") ||
		strings.HasPrefix(goalLower, "my favorite") ||
		strings.HasPrefix(goalLower, "i prefer") ||
		strings.Contains(goalLower, "preference is") ||
		strings.HasPrefix(goalLower, "please remember") ||
		strings.HasPrefix(goalLower, "remember that") ||
		strings.HasPrefix(goalLower, "记住:") ||
		strings.HasPrefix(goalLower, "记住：") ||
		strings.HasPrefix(goalLower, "请记住")
}

func extractExplicitMemorySaveContent(goal string) string {
	trimmed := normalizeTaskPrompt(goal)
	lower := strings.ToLower(trimmed)

	prefixes := []string{
		"remember:",
		"save to memory",
		"please remember",
		"remember that",
		"store this in memory",
		"keep this in mind",
		"记住:",
		"记住：",
		"请记住",
		"帮我记住",
		"保存到记忆",
		"存到记忆",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			if len(trimmed) >= len(prefix) {
				return strings.TrimSpace(trimmed[len(prefix):])
			}
			return ""
		}
	}

	return trimmed
}

func (s *Service) answerExplicitMemoryRecall(ctx context.Context, goal string, intent *IntentRecognitionResult, memoryContext string, memories []*domain.MemoryWithScore, cfg *RunConfig) (string, bool, error) {
	if s == nil || s.llmService == nil || !isExplicitMemoryRecallIntent(goal, intent) || len(memories) == 0 {
		return "", false, nil
	}

	recalled := strings.TrimSpace(memoryContext)
	if recalled == "" {
		return "", false, nil
	}

	prompt := fmt.Sprintf(`You are answering a direct memory recall question.
Use only the recalled memory snippets below.
Follow the user's formatting instructions exactly.
If the user asks for only a token, ID, or short value, return only that value.
If the answer is not present in the recalled memories, reply exactly: I couldn't find that in memory.

Question:
%s

Recalled memories:
%s
`, goal, recalled)

	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 || maxTokens > 300 {
		maxTokens = 120
	}

	resp, err := s.llmService.Generate(ctx, prompt, &domain.GenerationOptions{
		Temperature: 0,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		return "", false, err
	}

	resp = strings.TrimSpace(resp)
	if resp == "" {
		return "", false, nil
	}
	return resp, true, nil
}

// executeWithLLM lets LLM decide which tool to use and executes with multi-round support
func (s *Service) executeWithLLM(ctx context.Context, goal string, intent *IntentRecognitionResult, session *Session, memoryContext string, ragContext string, cfg *RunConfig) (interface{}, *executionMetrics, error) {
	maxRounds := cfg.MaxTurns
	if maxRounds <= 0 {
		maxRounds = 20
	}

	metrics := &executionMetrics{}

	// Determine starting agent
	currentAgent := s.agent
	if session != nil && session.AgentID != "" && s.registry != nil {
		if a, ok := s.registry.GetAgent(session.AgentID); ok {
			currentAgent = a
		}
	}

	prevToolCalls := make(map[string]int)
	summary := ""
	if session != nil {
		summary = session.Summary
	}
	messages := s.buildConversationMessages(session, goal, ragContext, memoryContext, summary)

	if cfg.StoreHistory && s.historyStore != nil {
		s.historyStore.RecordMessage(ctx, session.GetID(), currentAgent.ID(), goal, messages[len(messages)-1], 0)
	}

	toolCallCount := 0

	// --- DEBUG: LOG AGENT CONFIGURATION ---
	if s.debug {
		var sb strings.Builder
		info := s.Info()
		fmt.Fprintf(&sb, "AGENT:    %s (%s)\n", info.Name, info.ID)
		fmt.Fprintf(&sb, "MODEL:    %s\n", info.Model)
		fmt.Fprintf(&sb, "BASEURL:  %s\n", info.BaseURL)
		fmt.Fprintf(&sb, "FEATURES: RAG:%v, MCP:%v, Skills:%v, PTC:%v, Memory:%v\n",
			info.RAGEnabled, info.MCPEnabled, info.SkillsEnabled, info.PTCEnabled, info.MemoryEnabled)
		s.EmitDebugPrint(0, "config", sb.String())
	}

	for round := 0; round < maxRounds; round++ {
		select {
		case <-ctx.Done():
			return nil, metrics, fmt.Errorf("execution cancelled by user")
		default:
		}

		s.emitProgress("thinking", fmt.Sprintf("[%s] Thinking...", currentAgent.Name()), round+1, "")

		result, turnTokens, err := s.runOneLLMTurn(ctx, currentAgent, messages, cfg, round, goal, intent)
		if err != nil {
			return nil, metrics, err
		}
		metrics.estimatedTokens += turnTokens

		if len(result.ToolCalls) > 0 {
			// Check for handoff first
			if newAgent, updated := s.applyHandoff(ctx, &messages, currentAgent, result, session, round); updated {
				currentAgent = newAgent
				continue
			}

			filteredToolCalls, duplicateToolResults, fallback := s.handleDuplicateToolCalls(messages, result, prevToolCalls)
			if fallback != "" {
				return fallback, metrics, nil
			}
			if len(filteredToolCalls) == 0 {
				if len(duplicateToolResults) > 0 {
					messages = s.appendToolRoundToMessages(messages, result, duplicateToolResults)
					s.recordToolResults(ctx, session, currentAgent, goal, duplicateToolResults, cfg, round)
					toolCallCount += len(duplicateToolResults)
					metrics.toolCalls += len(duplicateToolResults)
					metrics.toolsUsed = appendToolNames(metrics.toolsUsed, duplicateToolResults)
				}
				continue
			}

			// Execute tool calls and append results to messages
			s.emitProgress("tool_call", fmt.Sprintf("Calling %d tool(s)", len(filteredToolCalls)), round+1, "")
			toolResults, err := s.executeToolCalls(ctx, currentAgent, session, filteredToolCalls)
			if err != nil {
				messages = append(messages, domain.Message{
					Role:    "assistant",
					Content: fmt.Sprintf("Tool execution failed: %v", err),
				})
				continue
			}

			messages = s.appendToolRoundToMessages(messages, result, append(duplicateToolResults, toolResults...))
			s.recordToolResults(ctx, session, currentAgent, goal, toolResults, cfg, round)
			toolCallCount += len(toolResults)
			metrics.toolCalls += len(toolResults)
			metrics.toolsUsed = appendToolNames(metrics.toolsUsed, toolResults)
			continue
		}

		// No tool calls — done
		if cfg.StoreHistory && s.historyStore != nil {
			s.historyStore.CompleteSession(ctx, session.GetID(), currentAgent.ID(), goal, round+1, toolCallCount, true, 0)
		}
		return result.Content, metrics, nil
	}

	result, err := s.handleMaxTurnsExceeded(ctx, session, currentAgent, goal, maxRounds, toolCallCount, messages, cfg)
	return result, metrics, err
}

// buildConversationMessages constructs the next-turn user message and prepends prior session history when available.
func (s *Service) buildConversationMessages(session *Session, goal, ragContext, memoryContext, summary string) []domain.Message {
	history := make([]domain.Message, 0)
	if session != nil {
		history = session.GetMessages()
	}

	olderMessages, recentMessages := splitConversationHistory(history, recentConversationWindow, olderConversationLimit)
	messages := make([]domain.Message, 0, len(olderMessages)+len(recentMessages)+2)
	if contextMsg := buildConversationContextMessage(summary, memoryContext, ragContext); contextMsg != nil {
		messages = append(messages, *contextMsg)
	}
	messages = append(messages, olderMessages...)
	messages = append(messages, recentMessages...)
	messages = append(messages, domain.Message{Role: "user", Content: goal})
	return messages
}

func splitConversationHistory(history []domain.Message, recentWindow, olderLimit int) ([]domain.Message, []domain.Message) {
	if len(history) == 0 {
		return nil, nil
	}
	if recentWindow <= 0 {
		recentWindow = recentConversationWindow
	}
	if olderLimit < 0 {
		olderLimit = 0
	}

	if len(history) <= recentWindow {
		return nil, append([]domain.Message(nil), history...)
	}

	recentStart := len(history) - recentWindow
	recent := append([]domain.Message(nil), history[recentStart:]...)
	older := history[:recentStart]
	if olderLimit > 0 && len(older) > olderLimit {
		older = older[len(older)-olderLimit:]
	}
	return append([]domain.Message(nil), older...), recent
}

func buildConversationContextMessage(summary, memoryContext, ragContext string) *domain.Message {
	var sections []string

	if trimmed := strings.TrimSpace(summary); trimmed != "" {
		sections = append(sections, "--- Latest Summary / Key Info ---\n"+trimmed)
	}
	if trimmed := strings.TrimSpace(memoryContext); trimmed != "" {
		sections = append(sections, "--- Relevant Context From Memory ---\n"+trimmed)
	}
	if trimmed := strings.TrimSpace(ragContext); trimmed != "" {
		sections = append(sections, "--- Relevant Documents From Knowledge Base ---\n"+trimmed)
	}
	if len(sections) == 0 {
		return nil
	}

	content := strings.Join(sections, "\n\n") + "\n\nUse the context above when responding to the next user message."
	return &domain.Message{
		Role:    "user",
		Content: content,
	}
}

// runOneLLMTurn builds the prompt for this round and calls the LLM once.
func (s *Service) runOneLLMTurn(ctx context.Context, currentAgent *Agent, messages []domain.Message, cfg *RunConfig, round int, goal string, intent *IntentRecognitionResult) (*domain.GenerationResult, int, error) {
	tools := s.collectAllAvailableTools(ctx, currentAgent)
	if looksLikeInformationSeekingQuery(goal) {
		tools = filterToolDefinitions(tools, func(tool domain.ToolDefinition) bool {
			return tool.Function.Name != "memory_save"
		})
	}
	systemMsg := s.buildSystemPrompt(ctx, currentAgent)
	genMessages := append([]domain.Message{{Role: "system", Content: systemMsg}}, messages...)

	if s.debug || cfg.Debug {
		var promptBuilder strings.Builder
		info := s.Info()
		fmt.Fprintf(&promptBuilder, "MODEL: %s (%s)\n", info.Model, info.BaseURL)
		fmt.Fprintf(&promptBuilder, "=== TOOLS (%d) ===\n", len(tools))
		for _, t := range tools {
			fmt.Fprintf(&promptBuilder, "  • %s: %s\n", t.Function.Name, t.Function.Description)
		}
		fmt.Fprintf(&promptBuilder, "\n=== MESSAGES ===\n")
		for _, m := range genMessages {
			fmt.Fprintf(&promptBuilder, "[%s]:\n%s\n", strings.ToUpper(m.Role), m.Content)
		}
		s.EmitDebugPrint(round+1, "prompt", promptBuilder.String())
	}

	temperature := cfg.Temperature
	if temperature == 0 {
		temperature = 0.3
	}
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2000
	}

	result, err := s.llmService.GenerateWithTools(ctx, genMessages, tools, s.toolGenerationOptions(temperature, maxTokens, ""))
	if err != nil {
		return nil, 0, fmt.Errorf("LLM generation failed: %w", err)
	}
	if result == nil {
		return nil, 0, fmt.Errorf("LLM generation returned nil result")
	}

	if (s.debug || cfg.Debug) && err == nil {
		s.logDebugResponse(result, round)
	}
	return result, s.estimateGenerationTokens(genMessages, result), nil
}

func appendToolNames(existing []string, results []ToolExecutionResult) []string {
	for _, result := range results {
		if result.ToolName == "" {
			continue
		}
		existing = append(existing, result.ToolName)
	}
	return existing
}

// applyHandoff checks if any tool call is a handoff, applies it, and returns (newAgent, true) if so.
func (s *Service) applyHandoff(ctx context.Context, messages *[]domain.Message, currentAgent *Agent, result *domain.GenerationResult, session *Session, round int) (*Agent, bool) {
	for _, tc := range result.ToolCalls {
		if !strings.HasPrefix(tc.Function.Name, "transfer_to_") {
			continue
		}
		for _, h := range currentAgent.Handoffs() {
			if h.ToolName() != tc.Function.Name {
				continue
			}
			targetAgent := h.TargetAgent()
			reason := tc.Function.Arguments["reason"]
			s.emitProgress("tool_call", fmt.Sprintf("Transferring to %s", targetAgent.Name()), round+1, "handoff")

			if session != nil {
				session.AgentID = targetAgent.ID()
			}
			*messages = append(*messages,
				domain.Message{
					Role:             "assistant",
					Content:          result.Content,
					ReasoningContent: result.ReasoningContent,
					ToolCalls:        result.ToolCalls,
				},
				domain.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("Transferred to %s. Reason: %v", targetAgent.Name(), reason),
				},
			)
			return targetAgent, true
		}
	}
	return currentAgent, false
}

func (s *Service) handleDuplicateToolCalls(messages []domain.Message, result *domain.GenerationResult, seen map[string]int) ([]domain.ToolCall, []ToolExecutionResult, string) {
	filtered := make([]domain.ToolCall, 0, len(result.ToolCalls))
	duplicates := make([]ToolExecutionResult, 0)

	for _, tc := range result.ToolCalls {
		key := fmt.Sprintf("%s:%v", tc.Function.Name, tc.Function.Arguments)
		seen[key]++
		if seen[key] <= 1 {
			filtered = append(filtered, tc)
			continue
		}

		if tc.Function.Name == "task_complete" {
			if res, ok := tc.Function.Arguments["result"].(string); ok && strings.TrimSpace(res) != "" {
				return nil, nil, strings.TrimSpace(res)
			}
			return nil, nil, extractBestEffortAnswer(result.Content, messages)
		}

		log.Printf("[Agent] Duplicate tool call detected: %s", key)
		if isSearchToolName(tc.Function.Name) {
			duplicates = append(duplicates, ToolExecutionResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				ToolType:   "tool_search",
				Result:     "This tool search was already executed. Use the previously returned tools or results directly instead of searching again.",
			})
			continue
		}

		return nil, nil, extractBestEffortAnswer(result.Content, messages)
	}

	return filtered, duplicates, ""
}

func isSearchToolName(name string) bool {
	return name == "search_available_tools" || domain.IsToolSearchTool(name)
}

func extractBestEffortAnswer(currentContent string, messages []domain.Message) string {
	if isMeaningfulAnswerText(currentContent) {
		return strings.TrimSpace(currentContent)
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "assistant" && msg.Role != "tool" {
			continue
		}

		content := strings.TrimSpace(msg.Content)
		if !isMeaningfulAnswerText(content) {
			continue
		}
		return content
	}

	if strings.TrimSpace(currentContent) != "" {
		return strings.TrimSpace(currentContent)
	}

	return "Task stopped after repeating the same tool call before producing a substantive final answer."
}

func isMeaningfulAnswerText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}

	normalized := strings.ToLower(text)
	genericPrefixes := []string{
		"the task has been completed",
		"task complete",
		"done",
	}
	for _, prefix := range genericPrefixes {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+".") {
			return false
		}
	}

	return true
}

// toolResultToString converts a tool execution result to a string suitable for
// the LLM's "tool" role message. Strings are returned as-is; maps and slices
// are JSON-encoded so the LLM receives well-structured output rather than Go's
// fmt.Sprintf("%v") representation (e.g. "map[key:value]").
func toolResultToString(result interface{}) string {
	switch v := result.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

func filterToolDefinitions(tools []domain.ToolDefinition, keep func(tool domain.ToolDefinition) bool) []domain.ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	filtered := make([]domain.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if keep == nil || keep(tool) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// appendToolRoundToMessages appends the assistant message and tool result messages.
func (s *Service) appendToolRoundToMessages(messages []domain.Message, result *domain.GenerationResult, toolResults []ToolExecutionResult) []domain.Message {
	messages = append(messages, domain.Message{
		Role:             "assistant",
		Content:          result.Content,
		ReasoningContent: result.ReasoningContent,
		ToolCalls:        result.ToolCalls,
		ResponseID:       result.ID,
	})
	for _, tr := range toolResults {
		resStr := toolResultToString(tr.Result)
		messages = append(messages, domain.Message{
			Role:       "tool",
			Content:    resStr,
			ToolCallID: tr.ToolCallID,
		})
	}
	return messages
}

// recordToolResults writes tool results to history store if enabled.
func (s *Service) recordToolResults(ctx context.Context, session *Session, agent *Agent, goal string, toolResults []ToolExecutionResult, cfg *RunConfig, round int) {
	if !cfg.StoreHistory || s.historyStore == nil {
		return
	}
	for _, tr := range toolResults {
		success := true
		var errMsg string
		if errMap, ok := tr.Result.(map[string]interface{}); ok {
			if errVal, exists := errMap["error"]; exists && errVal != nil {
				success = false
				errMsg = fmt.Sprintf("%v", errVal)
			}
		}
		s.historyStore.RecordToolResult(ctx, session.GetID(), agent.ID(), goal,
			tr.ToolName, tr.ToolCallID, nil, tr.Result, success, errMsg, round+1)
	}
}

// handleMaxTurnsExceeded handles the case where max turns is reached.
func (s *Service) handleMaxTurnsExceeded(ctx context.Context, session *Session, agent *Agent, goal string, maxRounds, toolCallCount int, messages []domain.Message, cfg *RunConfig) (interface{}, error) {
	errExceeded := NewMaxTurnsExceeded(maxRounds, maxRounds, goal)
	if handler, ok := cfg.ErrorHandlers["max_turns"]; ok {
		handlerResult := handler(ErrorHandlerInput{
			Kind:         "max_turns",
			Round:        maxRounds,
			MaxTurns:     maxRounds,
			MessageCount: len(messages),
			Goal:         goal,
		})
		if cfg.StoreHistory && s.historyStore != nil {
			s.historyStore.CompleteSession(ctx, session.GetID(), agent.ID(), goal, maxRounds, toolCallCount, handlerResult.FinalOutput != nil, 0)
		}
		if handlerResult.FinalOutput != nil {
			return handlerResult.FinalOutput, nil
		}
		if handlerResult.Error != nil {
			return nil, handlerResult.Error
		}
	}
	if cfg.StoreHistory && s.historyStore != nil {
		s.historyStore.CompleteSession(ctx, session.GetID(), agent.ID(), goal, maxRounds, toolCallCount, false, 0)
	}
	return nil, errExceeded
}

// logDebugPrompt logs the full prompt for debugging.
func (s *Service) logDebugPrompt(genMessages []domain.Message, round int) {
	fmt.Println("\n" + strings.Repeat("=", 40))
	fmt.Printf("DEBUG: [ROUND %d] LLM FULL PROMPT\n", round+1)
	fmt.Println(strings.Repeat("-", 40))
	for _, m := range genMessages {
		fmt.Printf("[%s]:\n%s\n", strings.ToUpper(m.Role), m.Content)
		if len(m.ToolCalls) > 0 {
			fmt.Printf("  (ToolCalls: %d)\n", len(m.ToolCalls))
		}
	}
	fmt.Println(strings.Repeat("=", 40) + "\n")
}

// logDebugResponse logs the raw LLM response for debugging.
func (s *Service) logDebugResponse(result *domain.GenerationResult, round int) {
	fmt.Println("\n" + strings.Repeat("=", 40))
	fmt.Printf("DEBUG: [ROUND %d] LLM RAW RESPONSE\n", round+1)
	fmt.Println(strings.Repeat("-", 40))
	if result.ReasoningContent != "" {
		fmt.Printf("REASONING: %s\n", result.ReasoningContent)
	}
	fmt.Printf("CONTENT: %s\n", result.Content)
	if len(result.ToolCalls) > 0 {
		fmt.Println("TOOL CALLS:")
		for _, tc := range result.ToolCalls {
			fmt.Printf("  - %s(%v)\n", tc.Function.Name, tc.Function.Arguments)
		}
	}
	fmt.Println(strings.Repeat("=", 40) + "\n")
}

// verifyResult verifies the result with LLM
// Returns: (verified bool, reason string, correctedResult interface{}, err error)
func (s *Service) verifyResult(ctx context.Context, goal string, result interface{}) (bool, string, interface{}, error) {
	resultStr := formatResultForContent(result)

	data := map[string]interface{}{
		"Goal":   goal,
		"Result": resultStr,
	}

	rendered, err := s.promptManager.Render(prompt.AgentVerification, data)
	if err != nil {
		return true, "Render failed, assume verified", result, nil
	}

	verifyResp, err := s.llmService.Generate(ctx, rendered, &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   300,
	})
	if err != nil {
		return true, "", result, nil // Return original on error, assume verified
	}

	// Try to parse as JSON verification
	var verifyRespJSON struct {
		Verified   bool   `json:"verified"`
		Reason     string `json:"reason"`
		NeedsRetry bool   `json:"needs_retry"`
	}

	// Simple JSON extraction
	if err := extractJSON(verifyResp, &verifyRespJSON); err == nil {
		if verifyRespJSON.Verified {
			return true, "Verified", result, nil
		}
		return false, verifyRespJSON.Reason, nil, fmt.Errorf("verification failed: %s", verifyRespJSON.Reason)
	}

	// If parsing failed, assume verified to avoid infinite loops
	return true, "Parse OK, assume verified", result, nil
}

// extractJSON extracts JSON from LLM response (handles markdown code blocks)
func extractJSON(resp string, target interface{}) error {
	// Try direct parse first
	if err := json.Unmarshal([]byte(resp), target); err == nil {
		return nil
	}

	// Try to find JSON in markdown code blocks
	if strings.Contains(resp, "```json") {
		start := strings.Index(resp, "```json")
		if start >= 0 {
			jsonStart := start + 7
			end := strings.Index(resp[jsonStart:], "```")
			if end >= 0 {
				jsonStr := strings.TrimSpace(resp[jsonStart : jsonStart+end])
				return json.Unmarshal([]byte(jsonStr), target)
			}
		}
	}

	// Try to find plain code block
	if strings.Contains(resp, "```") {
		start := strings.Index(resp, "```")
		if start >= 0 {
			jsonStart := start + 3
			end := strings.Index(resp[jsonStart:], "```")
			if end >= 0 {
				jsonStr := strings.TrimSpace(resp[jsonStart : jsonStart+end])
				return json.Unmarshal([]byte(jsonStr), target)
			}
		}
	}

	return fmt.Errorf("no JSON found in response")
}

// executeToolCalls executes the tool calls decided by LLM and returns all results
func (s *Service) executeToolCalls(ctx context.Context, currentAgent *Agent, session *Session, toolCalls []domain.ToolCall) ([]ToolExecutionResult, error) {
	results := make([]ToolExecutionResult, len(toolCalls))

	// Create an errgroup to run tools in parallel
	g, groupCtx := errgroup.WithContext(ctx)

	for i, tc := range toolCalls {
		// Capture index and tool call for the goroutine
		idx, toolCall := i, tc

		g.Go(func() error {
			toolCtx := withCurrentAgent(groupCtx, currentAgent)

			// Format tool name for display
			toolName := toolCall.Function.Name
			toolDesc := toolName
			if strings.HasPrefix(toolName, "mcp_") {
				toolDesc = strings.TrimPrefix(toolName, "mcp_")
			}

			s.emitProgress("tool_call", fmt.Sprintf("→ %s", toolDesc), 0, toolName)

			if s.debug {
				s.EmitDebugPrint(0, "tool_call", fmt.Sprintf("TOOL: %s\nARGS: %v", toolName, toolCall.Function.Arguments))
			}

			s.logger.Debug("Executing Tool",
				slog.String("tool", toolName),
				slog.Any("arguments", toolCall.Function.Arguments))

			// Delegate to SubAgent
			result, err, _ := s.executeToolViaSubAgent(toolCtx, currentAgent, session, toolCall)

			if err != nil {
				s.logger.Error("Tool execution failed",
					slog.String("tool", toolName),
					slog.Any("error", err))

				if s.debug {
					s.EmitDebugPrint(0, "tool_result", fmt.Sprintf("TOOL: %s\nERROR: %v", toolName, err))
				}

				return fmt.Errorf("Tool %s failed: %w", toolCall.Function.Name, err)
			}

			s.logger.Debug("Tool Result",
				slog.String("tool", toolName),
				slog.Any("result", result))

			if s.debug {
				s.EmitDebugPrint(0, "tool_result", fmt.Sprintf("TOOL: %s\nRESULT: %v", toolName, result))
			}

			// Emit tool result progress
			s.emitProgress("tool_result", fmt.Sprintf("✓ %s Done", toolDesc), 0, toolName)

			results[idx] = ToolExecutionResult{
				ToolCallID: toolCall.ID,
				ToolName:   toolCall.Function.Name,
				Result:     result,
			}
			return nil
		})
	}

	// Wait for all tools to finish
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return results, nil
}

// ToolExecutionResult represents the result of a single tool execution
type ToolExecutionResult struct {
	ToolCallID string      `json:"tool_call_id"`
	ToolName   string      `json:"tool_name"`
	ToolType   string      `json:"tool_type"`
	Result     interface{} `json:"result"`
}

// formatToolResults formats tool execution results for LLM consumption
func (s *Service) formatToolResults(results []ToolExecutionResult) string {
	var sb strings.Builder

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("Tool %d: %s (%s)\n", i+1, r.ToolName, r.ToolType))

		// Format result based on type
		switch v := r.Result.(type) {
		case string:
			if len(v) > 5000 {
				sb.WriteString(fmt.Sprintf("Result: %s...\n", v[:5000]))
			} else {
				sb.WriteString(fmt.Sprintf("Result: %s\n", v))
			}
		case []interface{}:
			// Handle array results (e.g., search results)
			for j, item := range v {
				sb.WriteString(fmt.Sprintf("  [%d] %v\n", j+1, item))
			}
		default:
			sb.WriteString(fmt.Sprintf("Result: %v\n", r.Result))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// executeWithDynamicToolSelection uses LLM's native function calling to decide which MCP tools to use
func (s *Service) executeWithDynamicToolSelection(ctx context.Context, goal string, intent *IntentRecognitionResult, availableTools []domain.ToolDefinition, memoryContext, ragResult string) (interface{}, error) {
	systemPrompt, err := s.promptManager.Render(prompt.AgentDynamicToolSelection, nil)
	if err != nil {
		systemPrompt = "You are a helpful assistant with access to tools. Use tools when appropriate to help the user."
	}

	// Build messages - let LLM decide which tools to call
	messages := []domain.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: goal},
	}

	// Add context if available
	if memoryContext != "" || ragResult != "" {
		contextMsg := "\n\nRelevant context:\n"
		if memoryContext != "" {
			contextMsg += memoryContext + "\n"
		}
		if ragResult != "" {
			contextMsg += ragResult + "\n"
		}
		messages[len(messages)-1].Content += contextMsg
	}

	// Use GenerateWithTools - let LLM natively decide which tools to call
	result, err := s.llmService.GenerateWithTools(ctx, messages, availableTools, s.toolGenerationOptions(0.3, 1000, ""))

	if err != nil {
		return nil, fmt.Errorf("tool execution failed: %w", err)
	}

	// If LLM made tool calls, execute them
	if len(result.ToolCalls) > 0 {
		return s.executeLLMToolCalls(ctx, result.ToolCalls, goal, memoryContext, ragResult)
	}

	// No tool calls needed, return the text response
	return result.Content, nil
}

// executeLLMToolCalls executes tool calls decided by LLM
func (s *Service) executeLLMToolCalls(ctx context.Context, toolCalls []domain.ToolCall, goal, memoryContext, ragResult string) (interface{}, error) {
	var results []interface{}

	for _, tc := range toolCalls {
		log.Printf("[Agent] Calling tool: %s", tc.Function.Name)

		// Route through ToolRegistry first (covers custom, RAG, Memory tools).
		if s.toolRegistry.Has(tc.Function.Name) {
			result, err := s.toolRegistry.Call(ctx, tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				results = append(results, fmt.Sprintf("Tool %s failed: %v", tc.Function.Name, err))
			} else {
				results = append(results, result)
			}
			continue
		}

		// MCP tools — handled by mcpService.
		result, err := s.mcpService.CallTool(ctx, tc.Function.Name, tc.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("tool call failed: %w", err)
		}
		results = append(results, result)
	}

	// If results were obtained, format them
	if len(results) == 1 {
		return results[0], nil
	}
	return results, nil
}

// finalizeExecution finalizes the execution result
func (s *Service) finalizeExecution(ctx context.Context, session *Session, goal string, intent *IntentRecognitionResult, memoryMemories []*domain.MemoryWithScore, memoryLogic string, ragResult string, finalResult interface{}) (*ExecutionResult, error) {
	queryContext := s.resolveMemoryQueryContext(session)

	// Store to memory after completion
	if s.memoryService != nil {
		// Auto-store for explicit memory request patterns
		goalLower := strings.ToLower(goal)
		if isExplicitMemorySaveIntent(goal, intent) {

			if !s.hasRunMemorySaved() {
				// Direct storage for explicit memory requests
				content := extractExplicitMemorySaveContent(goal)
				if strings.TrimSpace(content) == "" {
					content = goal
				}

				scope := memorypkg.AgentScope(queryContext.AgentID)
				if scope.ID == "" && queryContext.SessionID != "" {
					scope = memorypkg.SessionScope(queryContext.SessionID)
				}

				memType := domain.MemoryTypeFact
				if strings.HasPrefix(goalLower, "my favorite") ||
					strings.HasPrefix(goalLower, "i prefer") ||
					strings.Contains(goalLower, "preference is") {
					memType = domain.MemoryTypePreference
				}

				if err := s.memoryService.Add(ctx, &domain.Memory{
					Type:       memType,
					SessionID:  memorypkg.ToBankID(scope),
					ScopeType:  scope.Type,
					ScopeID:    scope.ID,
					Content:    content,
					Importance: 0.8,
					Metadata: map[string]interface{}{
						"source": "user_direct",
					},
				}); err != nil {
					s.logger.Warn("failed to store preference memory", slog.String("error", err.Error()))
				} else {
					log.Printf("[Agent] Stored to memory: %s", content)
				}
			}
		}

		// LLM-based extraction for complex memories
		if err := s.memoryService.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
			SessionID:  session.GetID(),
			AgentID:    queryContext.AgentID,
			TeamID:     queryContext.TeamID,
			UserID:     queryContext.UserID,
			TaskGoal:   goal,
			TaskResult: formatResultForContent(finalResult),
			ExecutionLog: fmt.Sprintf("Intent: %s\nMemory: %d items\nRAG: %d chars",
				intent.IntentType, len(memoryMemories), len(ragResult)),
		}); err != nil {
			s.logger.Warn("failed to store memory", slog.String("error", err.Error()))
		}
	}

	// Save session
	if err := s.store.SaveSession(session); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	res := &ExecutionResult{
		PlanID:      uuid.New().String(),
		SessionID:   session.GetID(),
		Success:     true,
		StepsTotal:  1,
		StepsDone:   1,
		StepsFailed: 0,
		FinalResult: finalResult,
		Memories:    memoryMemories,
		MemoryLogic: memoryLogic,
		Duration:    "completed",
		Metadata: map[string]interface{}{
			"memory_scope_chain": []string{
				fmt.Sprintf("session:%s", strings.TrimSpace(queryContext.SessionID)),
				fmt.Sprintf("agent:%s", strings.TrimSpace(queryContext.AgentID)),
				fmt.Sprintf("team:%s", strings.TrimSpace(queryContext.TeamID)),
				fmt.Sprintf("user:%s", strings.TrimSpace(queryContext.UserID)),
				"global",
			},
			"memory_scope_context": map[string]interface{}{
				"session_id": queryContext.SessionID,
				"agent_id":   queryContext.AgentID,
				"team_id":    queryContext.TeamID,
				"user_id":    queryContext.UserID,
			},
		},
	}

	// Collect RAG sources
	s.ragSourcesMu.RLock()
	if len(s.ragSources) > 0 {
		res.Sources = append([]domain.Chunk{}, s.ragSources...)
	}
	s.ragSourcesMu.RUnlock()

	// Clear sources for next run
	s.ragSourcesMu.Lock()
	s.ragSources = nil
	s.ragSourcesMu.Unlock()

	// Emit PostExecution Hook on per-service registry
	hookResults := s.hooks.Emit(HookEventPostExecution, HookData{
		SessionID: session.GetID(),
		AgentID:   session.AgentID,
		Goal:      goal,
		Result:    finalResult,
		Metadata: map[string]interface{}{
			"intent":             intent.IntentType,
			"tools_used":         append([]string(nil), res.ToolsUsed...),
			"memory_summaries":   summarizeMemoriesForHooks(memoryMemories),
			"memory_scope_chain": res.Metadata["memory_scope_chain"],
		},
	})
	if len(hookResults) > 0 {
		appendPostExecutionHookResults(res.Metadata, hookResults)
	}

	return res, nil
}

func summarizeMemoriesForHooks(memories []*domain.MemoryWithScore) []map[string]interface{} {
	if len(memories) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(memories))
	for _, mem := range memories {
		if mem == nil || mem.Memory == nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":      mem.ID,
			"type":    string(mem.Type),
			"content": strings.TrimSpace(mem.Content),
			"score":   mem.Score,
			"scope":   formatMemoryScopeString(mem.ScopeType, mem.ScopeID, mem.SessionID),
			"source":  string(mem.SourceType),
		})
	}
	return out
}

func formatMemoryScopeString(scopeType domain.MemoryScopeType, scopeID, sessionID string) string {
	scopeTypeStr := strings.TrimSpace(string(scopeType))
	scopeID = strings.TrimSpace(scopeID)
	sessionID = strings.TrimSpace(sessionID)
	if scopeTypeStr == "" {
		switch {
		case sessionID == "", sessionID == "global", sessionID == "default":
			scopeTypeStr = "global"
		case strings.Contains(sessionID, ":"):
			parts := strings.SplitN(sessionID, ":", 2)
			scopeTypeStr = strings.TrimSpace(parts[0])
			if scopeID == "" && len(parts) == 2 {
				scopeID = strings.TrimSpace(parts[1])
			}
		default:
			scopeTypeStr = "session"
			if scopeID == "" {
				scopeID = sessionID
			}
		}
	}
	if scopeTypeStr == "" {
		return ""
	}
	if scopeTypeStr == "global" || scopeID == "" {
		return scopeTypeStr
	}
	return scopeTypeStr + ":" + scopeID
}

func appendPostExecutionHookResults(metadata map[string]interface{}, hookResults []interface{}) {
	if len(hookResults) == 0 {
		return
	}
	taskIDs, _ := metadata["async_task_ids"].([]string)
	for _, item := range hookResults {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(m["type"])) != "async_agent_task" {
			continue
		}
		taskID := strings.TrimSpace(fmt.Sprint(m["task_id"]))
		if taskID == "" {
			continue
		}
		taskIDs = append(taskIDs, taskID)
	}
	if len(taskIDs) > 0 {
		metadata["async_task_ids"] = uniqueTaskStrings(taskIDs)
	}
}

func uniqueTaskStrings(values []string) []string {
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

// performRAGQuery performs a RAG query to get relevant documents
func (s *Service) performRAGQuery(ctx context.Context, query string) (string, error) {
	if s.ragProcessor == nil {
		return "", nil
	}

	// Use the RAG processor to query
	request := domain.QueryRequest{
		Query:        query,
		TopK:         5, // Get top 5 results
		Temperature:  0.3,
		ShowThinking: false,
		ShowSources:  true,
	}

	results, err := s.ragProcessor.Query(ctx, request)
	if err != nil {
		return "", err
	}

	// Format results as context
	if results.Answer == "" && len(results.Sources) == 0 {
		return "", nil
	}

	// Collect sources for final result (deduplicated)
	s.addRAGSources(results.Sources)

	var context strings.Builder
	context.WriteString("## Relevant Documents\n\n")

	// Add answer if available
	if results.Answer != "" {
		context.WriteString(fmt.Sprintf("**Answer:** %s\n\n", results.Answer))
	}

	// Add sources
	for i, source := range results.Sources {
		context.WriteString(fmt.Sprintf("### Document %d\n", i+1))
		if source.DocumentID != "" {
			context.WriteString(fmt.Sprintf("**Source:** %s\n", source.DocumentID))
		}
		if source.Score > 0 {
			context.WriteString(fmt.Sprintf("**Score:** %.2f\n", source.Score))
		}
		if source.Content != "" {
			context.WriteString(fmt.Sprintf("**Content:** %s\n", source.Content))
		}
		context.WriteString("\n---\n\n")
	}

	return context.String(), nil
}

// countDocuments counts the number of documents in RAG context
func countDocuments(ragContext string) int {
	if ragContext == "" {
		return 0
	}
	// Count "### Document" occurrences
	count := strings.Count(ragContext, "### Document")
	return count
}

// executeSubAgentDelegation handles the delegate_to_subagent tool call.
// It creates a SubAgent with the specified configuration, runs it, and returns the result.
func (s *Service) executeSubAgentDelegation(ctx context.Context, currentAgent *Agent, args map[string]interface{}) (interface{}, error) {
	goal, _ := args["goal"].(string)
	if goal == "" {
		return nil, fmt.Errorf("delegate_to_subagent: 'goal' argument is required")
	}

	maxTurns := 5
	if mt, ok := args["max_turns"].(float64); ok {
		maxTurns = int(mt)
	} else if mt, ok := args["max_turns"].(int); ok {
		maxTurns = mt
	}

	timeoutSeconds := 60
	if ts, ok := args["timeout_seconds"].(float64); ok {
		timeoutSeconds = int(ts)
	} else if ts, ok := args["timeout_seconds"].(int); ok {
		timeoutSeconds = ts
	}

	var allowlist, denylist []string
	if al, ok := args["tools_allowlist"].([]interface{}); ok {
		for _, v := range al {
			if s, ok := v.(string); ok {
				allowlist = append(allowlist, s)
			}
		}
	}
	if dl, ok := args["tools_denylist"].([]interface{}); ok {
		for _, v := range dl {
			if s, ok := v.(string); ok {
				denylist = append(denylist, s)
			}
		}
	}

	var contextData map[string]interface{}
	if ctxData, ok := args["context"].(map[string]interface{}); ok {
		contextData = ctxData
	}

	s.emitProgress("tool_call", fmt.Sprintf("→ Delegating to SubAgent: %s", truncateGoal(goal, 50)), 0, "delegate_to_subagent")

	subAgent := s.CreateSubAgent(currentAgent, goal,
		WithSubAgentMaxTurns(maxTurns),
		WithSubAgentTimeout(time.Duration(timeoutSeconds)*time.Second),
		WithSubAgentToolAllowlist(allowlist),
		WithSubAgentToolDenylist(denylist),
		WithSubAgentContext(contextData),
	)
	subAgent.config.Debug = runDebugFromContext(ctx)

	sink := eventSinkFromContext(ctx)
	var (
		result interface{}
		err    error
	)
	if sink == nil {
		result, err = subAgent.Run(ctx)
	} else {
		for evt := range subAgent.RunAsync(ctx) {
			sink(evt)
		}
		result, err = subAgent.GetResult()
	}
	if err != nil {
		return nil, fmt.Errorf("sub-agent execution failed: %w", err)
	}

	s.emitProgress("tool_result", "✓ SubAgent completed", 0, "delegate_to_subagent")

	return map[string]interface{}{
		"subagent_id":   subAgent.ID(),
		"subagent_name": subAgent.Name(),
		"state":         string(subAgent.GetState()),
		"turns_used":    subAgent.GetCurrentTurn(),
		"duration_ms":   subAgent.GetDuration().Milliseconds(),
		"result":        result,
	}, nil
}
