package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Tool rounds — the execution half of concept 3.
//
// One turn's worth of tool work: normalising the calls the model emitted,
// collapsing duplicates that cannot return anything new, executing them, and
// deciding whether the round produced a terminal answer or another turn's
// input. runtime.go drives the loop; this file owns what happens inside a
// single tool round.

type ToolExecutionCallbacks struct {
	OnToolCall   func(name string, args map[string]interface{}, interruptBehavior string)
	OnToolResult func(name string, result interface{}, err error, interruptBehavior string)
	OnToolState  func(name string, state string, interruptBehavior string)
	EventSink    func(*Event)
	Debug        bool
}

func (s *Service) prepareToolRound(ctx context.Context, messages *[]domain.Message, currentAgent *Agent, session *Session, result *domain.GenerationResult, prevToolCalls map[string]int, round int) (*Agent, interface{}, []domain.ToolCall, []ToolExecutionResult, string, bool) {
	result.ToolCalls = normalizeToolCalls(result.ToolCalls)

	filteredToolCalls, duplicateToolResults, fallback := s.handleDuplicateToolCalls(*messages, result, prevToolCalls)
	result.ToolCalls = filteredToolCalls
	return currentAgent, nil, filteredToolCalls, duplicateToolResults, fallback, false
}

type toolRoundOutcome struct {
	Messages    []domain.Message
	ToolResults []ToolExecutionResult
	Terminal    string
	Blocked     bool
	AwaitAnswer bool
}

func (s *Service) buildToolRoundOutcome(messages []domain.Message, taskID string, result *domain.GenerationResult, duplicateToolResults, toolResults []ToolExecutionResult, filteredToolCalls []domain.ToolCall) toolRoundOutcome {
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
	outcome.Messages = appendToolResultsAnalysisPrompt(outcome.Messages)
	outcome.AwaitAnswer = true
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

	// A read only repeats an earlier read while nothing has changed underneath
	// it. `seen` persists for the whole run, so without this a re-read after a
	// write was collapsed and the agent was handed its own pre-edit content,
	// with the claim that it was current — the worst of both.
	//
	// So a batch containing anything that is not declared read-only clears the
	// record afterwards, and every read is open again.
	//
	// Coarse on purpose: any write reopens every read, not just reads of the
	// same path. Working out which argument of an arbitrary tool is a path, and
	// whether another tool's write aliases it, is knowledge the framework does
	// not have and should not guess at. The cost of being coarse is an
	// occasional re-read; the cost of being clever and wrong is an agent
	// building on a file it thinks it has, and has not.
	sawWrite := false

	for _, tc := range result.ToolCalls {
		// Search-tool keys are normalized here so casing/word-order variants
		// collapse. The discovery *budget* is enforced further down, at the
		// tool handlers themselves (see context_discovery_budget.go) — that is
		// the only point both chat-protocol calls and PTC sandbox callTool()
		// pass through.
		key := toolCallSignature(tc)
		seen[key]++
		if s.toolCallChangesState(tc.Function.Name) {
			// This call may change what every later read would see.
			sawWrite = true
		}
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
		if s.toolCallIsReadOnly(tc.Function.Name) {
			log.Printf("[Agent] Duplicate read-only tool call collapsed: %s (x%d)", key, seen[key])
			duplicates = append(duplicates, ToolExecutionResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				ToolType:   "tool",
				Result: fmt.Sprintf(
					"You have called %s with these exact arguments %d times, and nothing has been "+
						"written since the first one, so the answer has not changed. Its result is "+
						"already above — read it there. Repeating this call cannot make progress: "+
						"take a different action, or say what is blocking you.",
					tc.Function.Name, seen[key]),
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

	if sawWrite {
		// Done after the batch rather than during it, so a repeat inside this
		// same batch is still collapsed — two identical reads in one turn
		// cannot have a write between them.
		clear(seen)
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

// toolCallIsReadOnly reports whether a tool is declared as making no changes.
// An unregistered tool is not: assuming a tool nobody described is safe to
// skip is the wrong way round.
func (s *Service) toolCallIsReadOnly(name string) bool {
	if s == nil || s.toolRegistry == nil {
		return false
	}
	return s.toolRegistry.MetadataOf(name).ReadOnly
}

// toolCallChangesState reports whether a call could change what a later read
// would see, which is the question that decides whether earlier collapses may
// still be reused.
//
// Searching the tool catalogue and signalling the task is over change nothing
// on disk whatever the registry says about them — the first is a lookup, the
// second is a control signal — so neither reopens the reads. Everything else
// that is not declared read-only does, including a tool nobody registered.
func (s *Service) toolCallChangesState(name string) bool {
	if isSearchToolName(name) || isTaskTerminalToolName(name) {
		return false
	}
	return !s.toolCallIsReadOnly(name)
}
