package agent

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// One compaction step, in the order that costs least:
//
//  1. Clip — tool results older than the last few rounds become one-line
//     stubs. No model call, cannot fail, keeps every call/result pair. If
//     that brings the context under the threshold, stop here.
//  2. Summarise — the old middle is folded into a model-written summary.
//  3. Fall back — after compactionFailuresBeforeFallback consecutive summary
//     failures the middle is folded into a mechanical summary instead, and
//     the summariser is left alone for compactionFailureCooldownRounds rounds.
//
// Before step 3 existed a summariser that kept failing kept the full history
// and was asked again every round: the context grew without bound and each
// round paid for one more failed call. A run must be able to shrink its
// context whether or not a model will help it.

const (
	// Compaction modes, reported on CompactionInfo.Mode.
	CompactionModeClip     = "clip"
	CompactionModeSummary  = "summary"
	CompactionModeFallback = "fallback"

	// compactionFailuresBeforeFallback is how many consecutive summariser
	// failures are tolerated before folding without it.
	compactionFailuresBeforeFallback = 2
	// compactionFailureCooldownRounds is how long a failing summariser is
	// left alone once the fallback has taken over.
	compactionFailureCooldownRounds = 5

	// compactionMinProgress is the least share of the context a compaction
	// must free to count as progress. A step that frees less arms the
	// backoff below, and a summary whose foldable middle is smaller than
	// this share is not asked for at all: the model call would cost more
	// than it could ever save.
	compactionMinProgress = 0.10
	// compactionBackoffShare is how far past the size a no-progress
	// compaction left behind the context must grow, as a share of the
	// threshold, before compaction is tried again.
	compactionBackoffShare = 0.10
	// compactionTrimKeepNewestRounds is how many of the newest tool rounds
	// keep oversized results whole. One: the newest round's results have not
	// been shown to the model yet.
	compactionTrimKeepNewestRounds = 1
)

// compactionTracker is one run's memory of how compaction has gone.
type compactionTracker struct {
	// failures counts consecutive summariser failures; a success resets it.
	failures int
	// cooldownUntil is the first round the summariser may be asked again
	// once failures has reached the fallback limit.
	cooldownUntil int
	// summaryCalls counts summariser attempts over the run.
	summaryCalls int
	// backoffUntilTokens suppresses compaction until the context grows past
	// it. Set when a compaction freed less than compactionMinProgress —
	// asking again on the same history would free as little again, and on a
	// run whose bulk sits in the recent rounds that is a summary call per
	// round rewriting the prompt for nothing (seen live: two summaries left
	// 92k of 104k and 93k of 94k).
	backoffUntilTokens int
}

// clipAfterRounds is how many recent tool rounds keep their results verbatim.
func (r *Runtime) clipAfterRounds() int {
	if r != nil && r.cfg != nil && r.cfg.CompactionClipAfterRounds != 0 {
		return r.cfg.CompactionClipAfterRounds
	}
	return CompactionDefaultClipAfterRounds
}

// shouldAutoCompact returns true when the runtime should try to compact
// older history before continuing. Two triggers:
//   - the history's size, anchored on provider-reported usage, reaches the
//     run's threshold (explicit, from the model's window, or the default)
//   - the budget signals diminishing returns (queryLoopState.shouldContinue)
//
// Disabled when RunConfig.DisableAutoCompaction is set or no LLM is
// available. The compactor itself is a no-op when there is nothing to clip
// and the middle slice is too small to summarize, so callers can ask freely.
func (r *Runtime) shouldAutoCompact(state *queryLoopState, messages []domain.Message) bool {
	if r == nil || r.svc == nil || r.svc.llmService == nil {
		return false
	}
	if r.cfg != nil && r.cfg.DisableAutoCompaction {
		return false
	}
	if state == nil || len(messages) == 0 {
		return false
	}
	threshold, _ := r.compactionThreshold()
	tokens := r.contextTokens(messages)
	if r.compaction.backoffUntilTokens > 0 && tokens < r.compaction.backoffUntilTokens {
		return false
	}
	if tokens >= threshold {
		return true
	}
	return state.shouldContinue() == budgetCompact
}

// runCompactionRound performs one compaction step. Returns the new message
// slice and ok=true when the history changed; the original messages and
// ok=false when nothing could be done this round.
func (r *Runtime) runCompactionRound(ctx context.Context, state *queryLoopState, messages []domain.Message, goal string) ([]domain.Message, bool) {
	keepRecent := 0
	if r.cfg != nil {
		keepRecent = r.cfg.CompactionKeepRecent
	}

	reason := compactionTriggerTokenThreshold
	if state.shouldContinue() == budgetCompact {
		reason = compactionTriggerDiminishingReturns
	}

	hookData := HookData{
		SessionID:      r.session.GetID(),
		AgentID:        currentAgentID(r.currentAgent, r.svc.agent),
		Goal:           goal,
		MessagesBefore: append([]domain.Message(nil), messages...),
		TriggerReason:  string(reason),
		Metadata: map[string]interface{}{
			"round":            state.CurrentRound,
			"message_count":    len(messages),
			"estimated_tokens": state.Budget.EstimatedTokens,
		},
	}
	if r.svc != nil && r.svc.hooks != nil {
		r.svc.hooks.Emit(HookEventPreCompact, hookData)
	}

	threshold, source := r.compactionThreshold()
	info := CompactionInfo{
		TaskID:          currentTaskID(r.session),
		RunID:           r.runID(),
		SessionID:       r.sessionID(),
		AgentName:       r.currentAgentName(),
		Round:           state.CurrentRound,
		Trigger:         string(reason),
		MessagesBefore:  len(messages),
		ContextTokens:   r.contextTokens(messages),
		EstimatedTokens: state.Budget.EstimatedTokens,
		Threshold:       threshold,
		ThresholdSource: source,
	}

	// 1. Clip old results to stubs, and trim oversized ones to head and
	// tail wherever they sit outside the newest round.
	working := messages
	clipped, trimmed := 0, 0
	if r.clipAfterRounds() >= 0 {
		working, clipped = clipOldToolResults(working, protectedHeadEnd(working), r.clipAfterRounds())
		working, trimmed = trimOversizedToolResults(working, protectedHeadEnd(working), compactionTrimKeepNewestRounds, clipOversizeChars)
	}
	reduced := clipped+trimmed > 0
	if reduced {
		r.shiftAnchorForClip(messages, working)
		info.ClippedResults = clipped
		info.TrimmedResults = trimmed
	}
	if reduced && r.contextTokens(working) < threshold {
		info.Mode = CompactionModeClip
		return r.finishCompaction(ctx, info, messages, working), true
	}

	// 2. Summarise, or 3. fall back — only when folding the middle can free
	// a meaningful share. When the bulk sits in the verbatim tail, a summary
	// rewrites the prompt and frees nothing.
	headEnd, tailStart := compactionBounds(working, keepRecent)
	foldable := tailStart-headEnd >= 2
	if foldable && r.svc.tokenCounter != nil {
		middleTokens := r.svc.tokenCounter.EstimateConversationTokens(working[headEnd:tailStart], r.svc.Info().Model)
		if float64(middleTokens) < compactionMinProgress*float64(r.contextTokens(working)) {
			foldable = false
		}
	}
	if !foldable {
		if reduced {
			info.Mode = CompactionModeClip
			return r.finishCompaction(ctx, info, messages, working), true
		}
		r.armCompactionBackoff(r.contextTokens(messages), threshold)
		return messages, false
	}
	middle := working[headEnd:tailStart]

	summary := ""
	degraded := false
	coolingDown := r.compaction.failures >= compactionFailuresBeforeFallback &&
		state.CurrentRound < r.compaction.cooldownUntil
	if coolingDown {
		degraded = true
	} else {
		r.compaction.summaryCalls++
		info.SummaryCalls = 1
		s, err := r.svc.summarizeForCompaction(ctx, middle)
		switch {
		case err == nil:
			summary = s
			r.compaction.failures = 0
		default:
			r.compaction.failures++
			info.SummaryError = err.Error()
			r.emit(EventTypeError, fmt.Sprintf("auto-compaction failed: %v", err))
			if r.compaction.failures < compactionFailuresBeforeFallback {
				// First failure: keep the history (clipped, if clipping
				// found anything) and try the summariser again next time.
				if clipped > 0 {
					info.Mode = CompactionModeClip
					return r.finishCompaction(ctx, info, messages, working), true
				}
				return messages, false
			}
			degraded = true
			r.compaction.cooldownUntil = state.CurrentRound + compactionFailureCooldownRounds
		}
	}
	if degraded {
		summary = buildFallbackSummary(fallbackSummaryInput{
			Goal:   goal,
			Plan:   r.svc.PlanSummary(r.planKey()),
			Middle: middle,
		})
		info.Degraded = true
		info.Mode = CompactionModeFallback
	} else {
		info.Mode = CompactionModeSummary
	}
	out := foldHistory(working, headEnd, tailStart, summary, degraded)
	// A fold removes messages; no provider report covers the result.
	r.dropAnchor()
	return r.finishCompaction(ctx, info, messages, out), true
}

// armCompactionBackoff holds compaction off until the context has grown a
// share of the threshold past after.
func (r *Runtime) armCompactionBackoff(after, threshold int) {
	r.compaction.backoffUntilTokens = after + int(compactionBackoffShare*float64(threshold))
}

// finishCompaction reports one compaction and returns its result.
func (r *Runtime) finishCompaction(ctx context.Context, info CompactionInfo, before, after []domain.Message) []domain.Message {
	info.MessagesAfter = len(after)
	info.ContextTokensAfter = r.contextTokens(after)
	if info.ContextTokens > 0 && float64(info.ContextTokens-info.ContextTokensAfter) < compactionMinProgress*float64(info.ContextTokens) {
		info.NoProgress = true
		r.armCompactionBackoff(info.ContextTokensAfter, info.Threshold)
	} else {
		r.compaction.backoffUntilTokens = 0
	}
	r.eventChan <- NewAnalyticsEvent(AnalyticsAutocompactTriggered, map[string]interface{}{
		"round":            info.Round,
		"trigger":          info.Trigger,
		"mode":             info.Mode,
		"messages_before":  len(before),
		"messages_after":   len(after),
		"clipped_results":  info.ClippedResults,
		"trimmed_results":  info.TrimmedResults,
		"no_progress":      info.NoProgress,
		"context_tokens":   info.ContextTokens,
		"context_after":    info.ContextTokensAfter,
		"degraded":         info.Degraded,
		"estimated_tokens": info.EstimatedTokens,
	})
	r.emit(EventTypeCompactBoundary, fmt.Sprintf(
		"Compacted %d → %d messages, ~%d → ~%d tokens (%s, %s)",
		len(before), len(after), info.ContextTokens, info.ContextTokensAfter, info.Trigger, info.Mode,
	))
	r.svc.emitObserver(func(o Observer) { o.OnCompaction(ctx, info) })
	return after
}
