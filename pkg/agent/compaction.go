package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// Default knobs for in-loop history compaction. Override per-run with
// WithAutoCompaction(threshold, keep).
const (
	// CompactionDefaultThresholdTokens is the context budget the runtime
	// compacts at when nothing better is known: no explicit threshold on the
	// run, and no context window for the model (see pool.RegisterModelWindow).
	// A run that falls back to it says so — a log warning once per run, the
	// threshold source on every CompactionInfo, and a Doctor check.
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
	CompactionDefaultThresholdTokens = 60000

	// CompactionDefaultKeepRecent is the number of trailing messages
	// preserved verbatim. Six covers a typical "tool call → tool result
	// → assistant text" cluster plus one full round of follow-up,
	// keeping the model's working state intact.
	CompactionDefaultKeepRecent = 6

	// CompactionDefaultClipAfterRounds is how many of the most recent tool
	// rounds keep their results verbatim when compaction first clips. Older
	// results become a one-line stub (see clipOldToolResults).
	CompactionDefaultClipAfterRounds = 4

	// compactionWindowRatio is the share of a model's usable window (window
	// minus the output reserve) a run fills before compacting on a large
	// window. compactionWindowFloor is the least a run is ever compacted at
	// when the window allows it, and compactionSmallWindowRatio caps the
	// floor on a window too small to hold it.
	compactionWindowRatio      = 0.50
	compactionWindowFloor      = 64000
	compactionSmallWindowRatio = 0.80
)

// CompactionThresholdForWindow derives the compaction threshold from a model's
// context window and the output reserve a turn needs. It returns 0 when the
// window is unknown (contextTokens <= 0).
//
//	usable    = window − min(maxOutput, window/4)
//	threshold = max(usable × 50%, min(64k, usable × 80%))
//
// Large windows compact at half of what they can hold; nothing that can hold
// 80k is compacted below 64k; a small window compacts at 80% of itself. The
// curve is continuous, so a slightly smaller window never compacts later than
// a slightly larger one. A 200k window with an 8k reserve compacts at ~96k, a
// 128k window at 64k, a 32k window at ~19.7k.
func CompactionThresholdForWindow(contextTokens, maxOutput int) int {
	if contextTokens <= 0 {
		return 0
	}
	// The output reserve is capped at a quarter of the window: a per-turn
	// budget sized for a large model must not leave a small one with nothing,
	// and the cap keeps the threshold rising with the window.
	if maxOutput < 0 {
		maxOutput = 0
	}
	if maxOutput > contextTokens/4 {
		maxOutput = contextTokens / 4
	}
	usable := float64(contextTokens - maxOutput)
	t := usable * compactionWindowRatio
	floor := float64(compactionWindowFloor)
	if small := usable * compactionSmallWindowRatio; small < floor {
		floor = small
	}
	if t < floor {
		t = floor
	}
	return int(t)
}

// Where a run's compaction threshold came from. Reported on CompactionInfo.
const (
	CompactionThresholdConfigured  = "configured"
	CompactionThresholdModelWindow = "model_window"
	// CompactionThresholdDefault means the model's window is unknown and the
	// run fell back to CompactionDefaultThresholdTokens.
	CompactionThresholdDefault = "default_unknown_window"
)

// resolveCompactionThreshold picks a run's threshold: an explicit one wins,
// then the model's window, then the fixed default. The source says which, so
// a caller can tell a deliberate 60000 from a fallback one.
func resolveCompactionThreshold(configured int, model string, maxOutput int) (int, string) {
	if configured > 0 {
		return configured, CompactionThresholdConfigured
	}
	if w, ok := pool.LookupModelWindow(model); ok {
		out := maxOutput
		if w.MaxOutputTokens > 0 && (out <= 0 || out > w.MaxOutputTokens) {
			out = w.MaxOutputTokens
		}
		if t := CompactionThresholdForWindow(w.ContextTokens, out); t > 0 {
			return t, CompactionThresholdModelWindow
		}
	}
	return CompactionDefaultThresholdTokens, CompactionThresholdDefault
}

// compactionTrigger labels why the runtime decided to compact. Surfaced
// via HookData.TriggerReason and the autocompact analytics event.
type compactionTrigger string

const (
	compactionTriggerTokenThreshold     compactionTrigger = "token_threshold"
	compactionTriggerDiminishingReturns compactionTrigger = "diminishing_returns"
)

// shouldCompactByTokens reports whether the estimated context tokens for
// msgs has crossed the runtime's threshold. Threshold of zero means
// "use the default". The loop itself uses Runtime.contextTokens, which
// anchors on provider-reported usage; this is the pure-estimate form.
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

// estimateConversationTokens sizes a message slice with the tokenizer
// estimate alone. The loop prefers Runtime.contextTokens, which anchors on
// what the provider last reported; this is what it falls back to before
// anything has been reported.
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

// compactMessages summarizes older history while keeping the protected head
// (leading system messages and the first user message) and the tail intact.
// Returns the rewritten slice plus a non-nil error only when the summary LLM
// call fails — callers should keep the original messages on error rather
// than dropping context.
//
// Layout produced:
//
//	[ leading system messages... ] +
//	[ first user message (goal)  ] +
//	[ summary user message       ] +
//	[ last keepRecent messages   ]
//
// keepRecent <= 0 falls back to CompactionDefaultKeepRecent.
func (s *Service) compactMessages(ctx context.Context, msgs []domain.Message, keepRecent int) ([]domain.Message, error) {
	if s == nil || len(msgs) == 0 {
		return msgs, nil
	}
	headEnd, tailStart := compactionBounds(msgs, keepRecent)
	// Nothing meaningful to compact when head + tail already covers
	// everything or the middle is too small to be worth summarizing.
	if tailStart-headEnd < 2 {
		return msgs, nil
	}
	summary, err := s.summarizeForCompaction(ctx, msgs[headEnd:tailStart])
	if err != nil {
		return msgs, err
	}
	return foldHistory(msgs, headEnd, tailStart, summary, false), nil
}

// compactionBounds returns where the protected head ends and the verbatim
// tail begins; msgs[headEnd:tailStart] is what compaction may fold.
func compactionBounds(msgs []domain.Message, keepRecent int) (headEnd, tailStart int) {
	if keepRecent <= 0 {
		keepRecent = CompactionDefaultKeepRecent
	}
	headEnd = protectedHeadEnd(msgs)
	tailStart = pickTailStart(msgs, headEnd, keepRecent)
	return headEnd, tailStart
}

// foldHistory replaces msgs[headEnd:tailStart] with one summary turn.
func foldHistory(msgs []domain.Message, headEnd, tailStart int, summary string, degraded bool) []domain.Message {
	head := msgs[:headEnd]
	tail := msgs[tailStart:]
	out := make([]domain.Message, 0, len(head)+1+len(tail))
	out = append(out, head...)
	note := ""
	if degraded {
		// Said to the model too: a mechanical record is thinner than a
		// written summary, and a model that knows it re-checks instead of
		// trusting a gap.
		note = "It was assembled mechanically because the summariser was unavailable; " +
			"re-read anything you need rather than relying on it. "
	}
	// The summary is a user turn, not a system message. It stands in for the
	// folded user/assistant turns, and the verbatim tail often begins with an
	// assistant tool_calls message. Gemini's turn validator rejects a
	// function call that does not immediately follow a user turn or a
	// function response, so [system, system(summary), assistant(tool_calls),
	// ...] is a hard 400 there; [system, user(summary), assistant(tool_calls),
	// ...] is valid everywhere. (CompactMessages, the session-level
	// compactor, already emits its summary as a user turn for the same
	// reason.)
	out = append(out, domain.Message{
		Role: "user",
		Content: fmt.Sprintf(
			"=== COMPACTED CONVERSATION SUMMARY ===\n%s\n=== END SUMMARY ===\n"+
				"The above replaces %d earlier message(s) to stay within the context budget. %s"+
				"The most recent %d message(s) follow verbatim.",
			strings.TrimSpace(summary),
			tailStart-headEnd,
			note,
			len(tail),
		),
	})
	out = append(out, tail...)
	return out
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

// protectedHeadEnd is where compaction's untouchable head ends: the leading
// system messages plus the first user message right after them. That message
// is normally the run's goal — the one thing every later turn is measured
// against — and folding it into a summary is how an agent ends up working
// from its own paraphrase of the task. Neither the clipper nor the
// summariser touches anything before this index.
func protectedHeadEnd(msgs []domain.Message) int {
	end := leadingSystemCount(msgs)
	if end < len(msgs) && msgs[end].Role == "user" {
		end++
	}
	return end
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
