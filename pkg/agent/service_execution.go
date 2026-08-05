package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/prompt"
)

type roundMetrics struct {
	round      int
	tokens     int
	toolCalls  int
	llmMs      int64
	toolMs     int64
	durationMs int64
}

type executionMetrics struct {
	toolCalls        int
	toolsUsed        []string
	estimatedTokens  int
	rounds           int
	roundStats       []roundMetrics
	totalDurationMs  int64
	estimatedCostUSD float64
}

// ============================================================
// Error Withholding - Recovery from API errors
// ============================================================

// IsWithholdable returns true if the error is a recoverable error
// that can be handled via compaction/retry.
func IsWithholdable(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, domain.ErrContextTooLong) ||
		errors.Is(err, domain.ErrMaxOutputTokens) ||
		errors.Is(err, domain.ErrRateLimited)
}

// IsContextTooLong returns true if the error indicates context length exceeded.
func IsContextTooLong(err error) bool {
	return err != nil && errors.Is(err, domain.ErrContextTooLong)
}

// IsMaxOutputTokens returns true if the error indicates max output tokens exceeded.
func IsMaxOutputTokens(err error) bool {
	return err != nil && errors.Is(err, domain.ErrMaxOutputTokens)
}

// CompactMessages compacts the conversation history using LLM summarization.
// Returns a new message list with summarized content.
func (s *Service) CompactMessages(ctx context.Context, messages []domain.Message) ([]domain.Message, error) {
	if len(messages) == 0 {
		return messages, nil
	}
	discoveredTools := extractDiscoveredToolNames(messages, "")

	// Build conversation text for summarization (similar to CompactSession)
	var conversationText strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			conversationText.WriteString(fmt.Sprintf("User: %s\n", stripDiscoveredToolsTag(msg.Content)))
		case "assistant":
			conversationText.WriteString(fmt.Sprintf("Responder: %s\n", msg.Content))
		}
	}

	// Get compact prompt template
	compactPrompt := s.promptManager.Get(prompt.LLMCompact)
	if compactPrompt == "" {
		compactPrompt = "You are a helpful assistant that summarizes long conversations. Your goal is to extract key points and important information from the conversation, keeping it concise but comprehensive."
	}

	// Build full prompt
	fullPrompt := fmt.Sprintf("%s\n\nConversation to summarize:\n%s\n\nPlease provide a concise summary of the key points:", compactPrompt, conversationText.String())

	// Generate summary using LLM
	summary, err := s.llmService.Generate(ctx, fullPrompt, nil)
	if err != nil {
		return nil, fmt.Errorf("compaction failed: %w", err)
	}

	// Rebuild messages with summary as the first user message
	compacted := []domain.Message{
		{Role: "user", Content: fmt.Sprintf("[Earlier conversation summarized: %s]", summary)},
	}
	if len(discoveredTools) > 0 {
		compacted = append(compacted, domain.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + appendDiscoveredToolsSnapshot("", discoveredTools) + "\n</system-reminder>",
		})
	}

	// Add recent messages (last 2 to maintain context)
	if len(messages) > 2 {
		compacted = append(compacted, messages[len(messages)-2:]...)
	}

	return compacted, nil
}

type ToolExecutionCallbacks struct {
	OnToolCall   func(name string, args map[string]interface{}, interruptBehavior string)
	OnToolResult func(name string, result interface{}, err error, interruptBehavior string)
	OnToolState  func(name string, state string, interruptBehavior string)
	EventSink    func(*Event)
	Debug        bool
}

const (
	recentConversationWindow  = 6
	olderConversationLimit    = 12
	toolUseNudgePrompt        = "Do not describe what you would do. You have tools available — call them now to accomplish the goal. Use the tool functions provided to you."
	toolResultsAnalysisPrompt = "Analyze the tool results above. If you have fulfilled the user's request, provide your final answer and call task_complete. If a concrete blocker prevents completion, call task_blocked. Otherwise continue executing directly with the available tools."
)

func (s *Service) prepareToolRound(ctx context.Context, messages *[]domain.Message, currentAgent *Agent, session *Session, result *domain.GenerationResult, prevToolCalls map[string]int, round int) (*Agent, interface{}, []domain.ToolCall, []ToolExecutionResult, string, bool) {
	result.ToolCalls = normalizeToolCalls(result.ToolCalls)

	filteredToolCalls, duplicateToolResults, fallback := s.handleDuplicateToolCalls(*messages, result, prevToolCalls)
	result.ToolCalls = filteredToolCalls
	return currentAgent, nil, filteredToolCalls, duplicateToolResults, fallback, false
}

func isPTCToolRound(ptcEnabled bool, filteredToolCalls []domain.ToolCall) bool {
	return ptcEnabled && len(filteredToolCalls) == 1 && filteredToolCalls[0].Function.Name == "execute_javascript"
}

func appendToolResultsAnalysisPrompt(messages []domain.Message) []domain.Message {
	return append(messages, domain.Message{
		Role:    "user",
		Content: toolResultsAnalysisPrompt,
	})
}

func terminalAnswerFromToolRound(ptcEnabled bool, filteredToolCalls []domain.ToolCall, toolResults []ToolExecutionResult) string {
	if !isPTCToolRound(ptcEnabled, filteredToolCalls) {
		return ""
	}
	return extractPTCTerminalAnswer(toolResults)
}

type toolRoundOutcome struct {
	Messages    []domain.Message
	ToolResults []ToolExecutionResult
	Terminal    string
	Blocked     bool
	AwaitAnswer bool
}

func (s *Service) buildToolRoundOutcome(messages []domain.Message, taskID string, result *domain.GenerationResult, duplicateToolResults, toolResults []ToolExecutionResult, ptcEnabled bool, filteredToolCalls []domain.ToolCall) toolRoundOutcome {
	allResults := append(append([]ToolExecutionResult(nil), duplicateToolResults...), toolResults...)
	nextMessages := s.appendToolRoundToMessages(messages, taskID, result, allResults)
	outcome := toolRoundOutcome{
		Messages:    nextMessages,
		ToolResults: allResults,
	}
	if blocked := blockedToolExecutionResult(allResults); blocked != "" {
		outcome.Terminal = blocked
		outcome.Blocked = true
		return outcome
	}
	if final, blocked := terminalToolResultFromToolRound(filteredToolCalls, allResults); final != "" {
		outcome.Terminal = final
		outcome.Blocked = blocked
		return outcome
	}
	if final := terminalAnswerFromToolRound(ptcEnabled, filteredToolCalls, allResults); final != "" {
		outcome.Terminal = final
		return outcome
	}
	if !isPTCToolRound(ptcEnabled, filteredToolCalls) {
		outcome.Messages = appendToolResultsAnalysisPrompt(outcome.Messages)
		outcome.AwaitAnswer = true
	}
	return outcome
}

func terminalToolResultFromToolRound(filteredToolCalls []domain.ToolCall, toolResults []ToolExecutionResult) (string, bool) {
	for _, toolCall := range filteredToolCalls {
		if !isTaskTerminalToolName(toolCall.Function.Name) {
			continue
		}
		for _, result := range toolResults {
			if strings.TrimSpace(result.ToolCallID) == strings.TrimSpace(toolCall.ID) {
				return strings.TrimSpace(toolResultToString(result.Result)), toolCall.Function.Name == "task_blocked"
			}
		}
		return taskTerminalToolResult(toolCall.Function.Name, toolCall.Function.Arguments, ""), toolCall.Function.Name == "task_blocked"
	}
	return "", false
}

func (s *Service) executePreparedToolRound(ctx context.Context, currentAgent *Agent, session *Session, messages []domain.Message, result *domain.GenerationResult, filteredToolCalls []domain.ToolCall, duplicateToolResults []ToolExecutionResult, callbacks ToolExecutionCallbacks, continueOnError bool) ([]domain.Message, []ToolExecutionResult, error) {
	result.ToolCalls = filteredToolCalls
	toolResults, err := s.executeToolCallsWithOptions(ctx, currentAgent, session, filteredToolCalls, callbacks, continueOnError)
	if err != nil {
		return messages, nil, err
	}
	messages = s.appendToolRoundToMessages(messages, currentTaskID(session), result, append(duplicateToolResults, toolResults...))
	return messages, toolResults, nil
}

// buildConversationMessages constructs the next-turn user message and prepends prior session history when available.
func buildSkillReminderMessage(session *Session, reminder *skillReminder) *domain.Message {
	if reminder == nil || strings.TrimSpace(reminder.Text) == "" {
		return nil
	}
	markRelevantSkillsSent(session, reminder.Names)
	return &domain.Message{
		Role:    "user",
		Content: "<system-reminder>\n" + strings.TrimSpace(reminder.Text) + "\n</system-reminder>",
		TaskID:  currentTaskID(session),
	}
}

func (s *Service) buildConversationMessages(session *Session, goal, ragContext, memoryContext string, skillReminder *skillReminder, summary string) []domain.Message {
	history := make([]domain.Message, 0)
	if session != nil {
		history = historyForTask(session.GetMessages(), currentTaskID(session))
	}

	olderMessages, recentMessages := splitConversationHistory(history, recentConversationWindow, olderConversationLimit)
	messages := make([]domain.Message, 0, len(olderMessages)+len(recentMessages)+4)
	if userCtxMsg := buildUserContextMetaMessage(s.buildUserContext()); userCtxMsg != nil {
		messages = append(messages, *userCtxMsg)
	}
	if skillMsg := buildSkillReminderMessage(session, skillReminder); skillMsg != nil {
		messages = append(messages, *skillMsg)
	}
	if contextMsg := buildConversationContextMessage(summary, memoryContext, ragContext); contextMsg != nil {
		messages = append(messages, *contextMsg)
	}
	messages = append(messages, olderMessages...)
	messages = append(messages, recentMessages...)
	messages = append(messages, withTaskID(domain.Message{Role: "user", Content: goal}, currentTaskID(session)))
	return messages
}

func historyForTask(history []domain.Message, taskID string) []domain.Message {
	if strings.TrimSpace(taskID) == "" || len(history) == 0 {
		return history
	}
	filtered := make([]domain.Message, 0, len(history))
	for _, msg := range history {
		if strings.TrimSpace(msg.TaskID) == taskID {
			filtered = append(filtered, msg)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
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
		TaskID:  "",
	}
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

func normalizeToolCalls(toolCalls []domain.ToolCall) []domain.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	normalized := make([]domain.ToolCall, len(toolCalls))
	copy(normalized, toolCalls)
	for i := range normalized {
		if normalized[i].ID == "" {
			normalized[i].ID = domain.NormalizeToolCallID(fmt.Sprintf("%s_%d", normalized[i].Function.Name, i))
			continue
		}
		normalized[i].ID = domain.NormalizeToolCallID(normalized[i].ID)
	}
	return normalized
}

func (s *Service) handleDuplicateToolCalls(messages []domain.Message, result *domain.GenerationResult, seen map[string]int) ([]domain.ToolCall, []ToolExecutionResult, string) {
	filtered := make([]domain.ToolCall, 0, len(result.ToolCalls))
	duplicates := make([]ToolExecutionResult, 0)

	for _, tc := range result.ToolCalls {
		// Search-tool keys are normalized here so casing/word-order variants
		// collapse. The discovery *budget* is enforced further down, at the
		// tool handlers themselves (see context_discovery_budget.go) — that is
		// the only point both chat-protocol calls and PTC sandbox callTool()
		// pass through.
		key := toolCallSignature(tc)
		seen[key]++
		if seen[key] <= 1 {
			filtered = append(filtered, tc)
			continue
		}

		if isTaskTerminalToolName(tc.Function.Name) {
			if final := taskTerminalToolResult(tc.Function.Name, tc.Function.Arguments, result.Content); final != "" {
				return nil, nil, final
			}
			return nil, nil, extractBestEffortAnswer(result.Content, messages)
		}

		if isSearchToolName(tc.Function.Name) {
			// Re-searching the internal tool registry is pointless; reuse the
			// prior results instead of executing again.
			log.Printf("[Agent] Duplicate tool search collapsed: %s", key)
			duplicates = append(duplicates, ToolExecutionResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				ToolType:   "tool_search",
				Result:     "This tool search was already executed. Use the previously returned tools or results directly instead of searching again.",
			})
			continue
		}

		// A read-only tool called again with identical arguments this turn cannot
		// return anything new, so re-executing it just wastes a backend round-trip
		// and lets a poorly-converging model spin (e.g. re-polling every resource's
		// status). Collapse it to a reuse hint — the earlier result is already in
		// context. Only tools explicitly flagged ReadOnly qualify; everything else
		// is still re-executed (a re-read after a write, an incrementing counter…).
		if s.toolRegistry != nil && s.toolRegistry.MetadataOf(tc.Function.Name).ReadOnly {
			log.Printf("[Agent] Duplicate read-only tool call collapsed: %s", key)
			duplicates = append(duplicates, ToolExecutionResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				ToolType:   "tool",
				Result:     "This read-only tool was already called with identical arguments this turn; its result is unchanged — reuse the earlier result instead of calling it again.",
			})
			continue
		}

		// Any other tool may be stateful: re-reading a file returns new content
		// after a write, and a repeated write is idempotent or intentional.
		// Re-executing is the only correct behavior — aborting here would kill
		// legitimate read-modify-write loops (e.g. an incrementing counter) and
		// could leak a raw tool result as the final answer. Genuine no-progress
		// loops are still bounded by MaxTurns.
		log.Printf("[Agent] Duplicate tool call re-executed (possibly stateful): %s", key)
		filtered = append(filtered, tc)
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

	// Only assistant text is a real answer. Tool-role messages hold raw tool
	// output (e.g. {"ok":true,"bytes":1}); surfacing that as the final answer
	// is never what the user asked for.
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "assistant" {
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

	// A machine-facing payload is not an answer. PTC's terminal short-circuit
	// uses this to decide whether a script's return value can stand as the final
	// reply and skip the summarising round; when the script returns a struct —
	// which is the normal thing for code to do — the user was shown raw JSON
	// instead of a sentence, and the system prompt's rules (tone, a required
	// trailing tag, a language) never got applied. Rejecting it here sends the
	// result back to the model for one more round, which is where a reply is
	// supposed to come from.
	if looksLikeMachinePayload(text) {
		return false
	}

	return true
}

// looksLikeMachinePayload reports whether text is a serialized value rather than
// prose: a JSON object or array, possibly fenced.
func looksLikeMachinePayload(text string) bool {
	t := strings.TrimSpace(text)
	// Unwrap a single fenced block so ```json {...} ``` is judged on content.
	if strings.HasPrefix(t, "```") {
		if end := strings.LastIndex(t, "```"); end > 3 {
			inner := t[3:end]
			if nl := strings.IndexByte(inner, '\n'); nl >= 0 {
				inner = inner[nl+1:]
			}
			t = strings.TrimSpace(inner)
		}
	}
	if len(t) < 2 {
		return false
	}
	first, last := t[0], t[len(t)-1]
	if (first == '{' && last == '}') || (first == '[' && last == ']') {
		return json.Valid([]byte(t))
	}
	return false
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
func (s *Service) appendToolRoundToMessages(messages []domain.Message, taskID string, result *domain.GenerationResult, toolResults []ToolExecutionResult) []domain.Message {
	messages = append(messages, withTaskID(domain.Message{
		Role:             "assistant",
		Content:          result.Content,
		ReasoningContent: result.ReasoningContent,
		ToolCalls:        result.ToolCalls,
		ResponseID:       result.ID,
	}, taskID))
	for _, tr := range toolResults {
		res := tr.Result
		// Multimodal: a tool result carrying image data is split into an
		// image part (attached to a follow-up user message the model can
		// actually see) and a cleaned text result. No-op for text results.
		imageParts, res := extractToolImageParts(res)
		resStr := toolResultToString(res)
		messages = append(messages, withTaskID(domain.Message{
			Role:       "tool",
			Content:    resStr,
			ToolCallID: tr.ToolCallID,
		}, taskID))
		// Vision: surface any image the tool produced as a follow-up user
		// message with an image part, since providers reject images in
		// tool-role messages. Only runs when WithVision is enabled and the
		// result actually carried an image.
		if len(imageParts) > 0 {
			parts := append([]domain.MessagePart{
				domain.TextPart("Image output from tool " + tr.ToolName + ":"),
			}, imageParts...)
			messages = append(messages, withTaskID(domain.Message{
				Role:  "user",
				Parts: parts,
			}, taskID))
		}
	}
	return messages
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

// executeToolCalls executes the tool calls decided by LLM and returns all results
func (s *Service) executeToolCalls(ctx context.Context, currentAgent *Agent, session *Session, toolCalls []domain.ToolCall) ([]ToolExecutionResult, error) {
	return s.executeToolCallsWithOptions(ctx, currentAgent, session, toolCalls, ToolExecutionCallbacks{}, false)
}

// ToolExecutionResult represents the result of a single tool execution
type ToolExecutionResult struct {
	ToolCallID string      `json:"tool_call_id"`
	ToolName   string      `json:"tool_name"`
	ToolType   string      `json:"tool_type"`
	Result     interface{} `json:"result"`
	Error      string      `json:"error,omitempty"`
	Blocked    bool        `json:"blocked,omitempty"`
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
