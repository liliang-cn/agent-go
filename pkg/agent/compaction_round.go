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
	if r.contextTokens(messages) >= threshold {
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

	// 1. Clip.
	working := messages
	clippedMsgs, clipped := clipOldToolResults(messages, protectedHeadEnd(messages), r.clipAfterRounds())
	if clipped > 0 {
		r.shiftAnchorForClip(messages, clippedMsgs)
		working = clippedMsgs
		info.ClippedResults = clipped
	}
	if clipped > 0 && r.contextTokens(working) < threshold {
		info.Mode = CompactionModeClip
		return r.finishCompaction(ctx, info, messages, working), true
	}

	// 2. Summarise, or 3. fall back.
	headEnd, tailStart := compactionBounds(working, keepRecent)
	if tailStart-headEnd < 2 {
		if clipped > 0 {
			info.Mode = CompactionModeClip
			return r.finishCompaction(ctx, info, messages, working), true
		}
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

// finishCompaction reports one compaction and returns its result.
func (r *Runtime) finishCompaction(ctx context.Context, info CompactionInfo, before, after []domain.Message) []domain.Message {
	info.MessagesAfter = len(after)
	info.ContextTokensAfter = r.contextTokens(after)
	r.eventChan <- NewAnalyticsEvent(AnalyticsAutocompactTriggered, map[string]interface{}{
		"round":            info.Round,
		"trigger":          info.Trigger,
		"mode":             info.Mode,
		"messages_before":  len(before),
		"messages_after":   len(after),
		"clipped_results":  info.ClippedResults,
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
