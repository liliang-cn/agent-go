package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Default knobs for in-loop history compaction. Override per-run with
// WithAutoCompaction(threshold, keep).
const (
	// CompactionDefaultThresholdTokens is the rough context-budget the
	// runtime starts compacting at. Sized for ~16K-window models with
	// generous headroom; bump for larger models via WithAutoCompaction.
	CompactionDefaultThresholdTokens = 8000

	// CompactionDefaultKeepRecent is the number of trailing messages
	// preserved verbatim. Six covers a typical "tool call → tool result
	// → assistant text" cluster plus one full round of follow-up,
	// keeping the model's working state intact.
	CompactionDefaultKeepRecent = 6
)

// compactionTrigger labels why the runtime decided to compact. Surfaced
// via HookData.TriggerReason and the autocompact analytics event.
type compactionTrigger string

const (
	compactionTriggerTokenThreshold     compactionTrigger = "token_threshold"
	compactionTriggerDiminishingReturns compactionTrigger = "diminishing_returns"
)

// shouldCompactByTokens reports whether the estimated context tokens for
// msgs has crossed the runtime's threshold. Threshold of zero means
// "use the default".
func (s *Service) shouldCompactByTokens(msgs []domain.Message, model string, threshold int) bool {
	if threshold <= 0 {
		threshold = CompactionDefaultThresholdTokens
	}
	if s == nil || s.tokenCounter == nil {
		return false
	}
	tokens := s.tokenCounter.EstimateConversationTokens(toUsageMessages(msgs), model)
	return tokens >= threshold
}

// compactMessages summarizes older history while keeping leading system
// messages and the tail intact. Returns the rewritten slice plus a
// non-nil error only when the summary LLM call fails — callers should
// keep the original messages on error rather than dropping context.
//
// Layout produced:
//
//	[ leading system messages... ] +
//	[ summary system message     ] +
//	[ last keepRecent messages   ]
//
// keepRecent <= 0 falls back to CompactionDefaultKeepRecent.
func (s *Service) compactMessages(ctx context.Context, msgs []domain.Message, keepRecent int) ([]domain.Message, error) {
	if s == nil || len(msgs) == 0 {
		return msgs, nil
	}
	if keepRecent <= 0 {
		keepRecent = CompactionDefaultKeepRecent
	}

	headEnd := leadingSystemCount(msgs)
	tailStart := pickTailStart(msgs, headEnd, keepRecent)

	// Nothing meaningful to compact when head + tail already covers
	// everything or the middle is too small to be worth summarizing.
	if tailStart-headEnd < 2 {
		return msgs, nil
	}

	head := msgs[:headEnd]
	middle := msgs[headEnd:tailStart]
	tail := msgs[tailStart:]

	summary, err := s.summarizeForCompaction(ctx, middle)
	if err != nil {
		return msgs, err
	}

	out := make([]domain.Message, 0, len(head)+1+len(tail))
	out = append(out, head...)
	out = append(out, domain.Message{
		Role: "system",
		Content: fmt.Sprintf(
			"=== COMPACTED CONVERSATION SUMMARY ===\n%s\n=== END SUMMARY ===\n"+
				"The above replaces %d earlier message(s) to stay within the context budget. "+
				"The most recent %d message(s) follow verbatim.",
			strings.TrimSpace(summary),
			tailStart-headEnd,
			len(tail),
		),
	})
	out = append(out, tail...)
	return out, nil
}

// leadingSystemCount returns the index of the first non-system message.
// Used to keep the agent's system prompt(s) intact when compacting.
func leadingSystemCount(msgs []domain.Message) int {
	for i, m := range msgs {
		if m.Role != "system" {
			return i
		}
	}
	return len(msgs)
}

// pickTailStart chooses the index where the verbatim tail begins. It
// starts from len-keepRecent and walks backward so the tail never starts
// on an orphaned "tool" role response — the matching assistant
// "tool_calls" must be in the tail too or the model sees a dangling
// tool_result with no call.
func pickTailStart(msgs []domain.Message, headEnd, keepRecent int) int {
	start := len(msgs) - keepRecent
	if start < headEnd {
		start = headEnd
	}
	// Walk back to a safe boundary: never start on a "tool" message,
	// since the preceding assistant message owns the tool_call it's
	// answering. Also never start on a system message — those belong
	// with the head.
	for start > headEnd && start < len(msgs) {
		m := msgs[start]
		if m.Role == "tool" || m.Role == "system" {
			start--
			continue
		}
		break
	}
	if start < headEnd {
		start = headEnd
	}
	return start
}

// summarizeForCompaction asks the service's LLM to produce a terse
// summary of the supplied messages. Format is intentionally plain text
// — the runtime wraps it in a system message at the call site.
func (s *Service) summarizeForCompaction(ctx context.Context, middle []domain.Message) (string, error) {
	if s == nil || s.llmService == nil {
		return "", fmt.Errorf("compaction: no LLM available to summarize")
	}
	transcript := renderMessagesForSummary(middle)
	prompt := "Summarize the following conversation slice into a compact, factual " +
		"record that preserves: (1) the user's goal and any open subgoals, " +
		"(2) decisions made and their rationale, (3) concrete results from " +
		"tool calls (file paths, identifiers, values), (4) outstanding " +
		"questions or blockers. Omit pleasantries, restatement, and meta " +
		"commentary. Use short bullet points. Do not invent details that " +
		"are not in the transcript.\n\n" +
		"<transcript>\n" + transcript + "\n</transcript>"

	summary, err := s.llmService.Generate(ctx, prompt, &domain.GenerationOptions{
		Temperature: 0.2,
		MaxTokens:   800,
	})
	if err != nil {
		return "", fmt.Errorf("compaction: summary LLM call failed: %w", err)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", fmt.Errorf("compaction: summary LLM returned empty content")
	}
	return summary, nil
}

// renderMessagesForSummary flattens a slice of domain.Message into a
// plain-text transcript suitable for feeding to the summary prompt.
// Truncates very long contents per-message so a runaway tool result
// doesn't dominate the summary prompt.
func renderMessagesForSummary(msgs []domain.Message) string {
	const perMessageMaxRunes = 2000
	var b strings.Builder
	for _, m := range msgs {
		role := strings.ToUpper(m.Role)
		if role == "" {
			role = "MESSAGE"
		}
		content := strings.TrimSpace(m.Content)
		if r := []rune(content); len(r) > perMessageMaxRunes {
			content = string(r[:perMessageMaxRunes]) + "... (truncated)"
		}
		if content == "" && len(m.ToolCalls) > 0 {
			// Surface tool calls explicitly when the assistant message
			// is content-empty.
			names := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			content = "(tool_calls: " + strings.Join(names, ", ") + ")"
		}
		b.WriteString("[")
		b.WriteString(role)
		b.WriteString("] ")
		b.WriteString(content)
		b.WriteString("\n")
	}
	return b.String()
}
