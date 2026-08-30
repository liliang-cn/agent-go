package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Default knobs for in-loop history compaction. Override per-run with
// WithAutoCompaction(threshold, keep).
const (
	// CompactionDefaultThresholdTokens is the rough context-budget the
	// runtime starts compacting at.
	//
	// This was 8000, chosen for 16K-window models — and then chosen again by
	// nobody, because the estimator it is compared against counted only
	// message content and read about 1.5% of a tool-using agent's history.
	// For years of runs it was effectively a threshold of half a million and
	// compaction never fired at all.
	//
	// With the estimator fixed the number became real, and 8000 turned out to
	// be far too small: a coding agent crossed it every round and spent a
	// summary call folding thirteen messages into nine, over and over,
	// freeing almost nothing. Meanwhile two runs that completed a
	// thirteen-milestone task in an hour peaked at 60-65k prompt tokens with
	// compaction switched off entirely and were fine.
	//
	// 60000 is sized from that measurement: it leaves a working set that big
	// intact, and still bounds a run that would otherwise grow without limit.
	// Models with smaller windows should lower it via WithAutoCompaction.
	CompactionDefaultThresholdTokens = 60000

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
	tokens := s.tokenCounter.EstimateConversationTokens(msgs, model)
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
// estimateConversationTokens sizes a message slice the same way the
// compaction trigger does, so a reported number and the decision that
// produced it can never disagree.
func (s *Service) estimateConversationTokens(msgs []domain.Message) int {
	if s == nil || s.tokenCounter == nil {
		return 0
	}
	model := ""
	if info := s.Info(); info.Model != "" {
		model = info.Model
	}
	return s.tokenCounter.EstimateConversationTokens(msgs, model)
}

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
	// The summary is a user turn, not a system message. It stands in for the
	// folded user/assistant turns — which crucially include the original goal
	// — and the verbatim tail often begins with an assistant tool_calls
	// message. Gemini's turn validator rejects a function call that does not
	// immediately follow a user turn or a function response, so
	// [system, system(summary), assistant(tool_calls), ...] is a hard 400
	// there; [system, user(summary), assistant(tool_calls), ...] is valid
	// everywhere. (CompactMessages, the session-level compactor, already
	// emits its summary as a user turn for the same reason.)
	out = append(out, domain.Message{
		Role: "user",
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

	// Ask, and ask again with more room if nothing came back.
	//
	// This call had a fixed 800-token budget and treated an empty answer as a
	// hard error. On a model that reasons before it writes, 800 tokens is
	// spent thinking and the visible answer is empty — the same failure the
	// main loop already handles by escalating (see token_budget.go), except
	// here the consequence was worse: compaction returned an error, the
	// runtime kept the unfolded history, and the context grew without bound.
	//
	// It was also invisible. The failure went to EventTypeError, which had no
	// observer, so a run compacting on every round and failing on every round
	// looked exactly like a run that never needed to compact. A synthetic
	// transcript summarised fine at 800; the real ones did not.
	budget := compactionSummaryMaxTokens
	var lastErr error
	for attempt := 0; attempt <= compactionSummaryEscalations; attempt++ {
		summary, err := s.llmService.Generate(ctx, prompt, &domain.GenerationOptions{
			Temperature: 0.2,
			MaxTokens:   budget,
		})
		if err != nil {
			return "", fmt.Errorf("compaction: summary LLM call failed: %w", err)
		}
		if summary = strings.TrimSpace(summary); summary != "" {
			return summary, nil
		}
		lastErr = fmt.Errorf("compaction: summary LLM returned empty content at %d tokens", budget)
		budget *= 4
	}
	return "", lastErr
}

const (
	// compactionSummaryMaxTokens is the first budget a summary is asked for.
	// A folded slice summarises into bullet points; what needs the room is a
	// reasoning model's thinking, which is billed against the same cap.
	compactionSummaryMaxTokens = 4096
	// compactionSummaryEscalations is how many times to quadruple it before
	// giving up — 4k to 64k, past any single summary a model should need.
	compactionSummaryEscalations = 2
)

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
