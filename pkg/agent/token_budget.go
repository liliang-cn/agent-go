// A truncated turn is not an empty answer.
//
// The loop capped every response at defaultRunMaxTokens and nothing looked at
// what the provider said about why generation stopped. On a model that reasons
// before it writes, that cap is spent on reasoning the caller never sees:
// measured against deepseek-v4-flash, an open-ended turn returned
// finish_reason="length" with zero characters of content at 400, 2000 and even
// 8000 tokens — the whole budget went to reasoning_tokens.
//
// What reached the deterministic layer was an empty draft, and
// non_empty_final_answer read it exactly as designed: the model refused to
// answer. Three rejections later the retry budget was gone and the run was
// blocked. The model had not refused anything. We cut it off mid-sentence and
// then judged it for saying nothing.
//
// So the runtime treats truncation as its own outcome: raise the budget and
// ask again, rather than hand a severed turn to the lints.
package agent

import (
	"context"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

const (
	// maxTokenEscalations is how many times one turn may grow its budget.
	// Two steps of 4x take the default from 8k to 128k, past any reasoning
	// budget a current model spends on a single turn; a model that still
	// produces nothing is not being truncated, it is failing, and the
	// ordinary terminal paths should say so.
	maxTokenEscalations = 2
	// tokenEscalationFactor multiplies the budget on each escalation.
	tokenEscalationFactor = 4
)

// truncatedWithNothingToShow reports whether a turn was cut off before it
// produced anything a caller could use.
//
// Both halves matter. finish_reason alone is not enough — a turn that wrote
// two paragraphs and then hit the cap has content worth keeping, and asking
// again would throw it away. Emptiness alone is not enough either: a model
// that genuinely returns nothing while the provider says "stop" is a different
// problem, and one the lints are right to catch.
func truncatedWithNothingToShow(result *domain.GenerationResult, err error) bool {
	if err != nil || result == nil {
		return false
	}
	if strings.TrimSpace(result.Content) != "" || len(result.ToolCalls) > 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(result.FinishReason)) {
	case "length", "max_tokens":
		return true
	}
	return false
}

// escalateMaxTokens returns the budget to retry a truncated turn with, and
// whether to retry at all.
func escalateMaxTokens(result *domain.GenerationResult, err error, current, escalation int) (int, bool) {
	if escalation >= maxTokenEscalations || current <= 0 {
		return current, false
	}
	if !truncatedWithNothingToShow(result, err) {
		return current, false
	}
	return current * tokenEscalationFactor, true
}

// emitTokenBudgetEscalation records that a turn was retried with a larger
// budget. It is an analytics event rather than a silent retry because a run
// that escalates every round is paying for reasoning it never uses, and that
// is a configuration problem its operator should be able to see.
//
// spanID names the turn being re-asked, so the escalation lands on that turn's
// span instead of on the run — the difference between seeing which round paid
// for the extra budget and knowing only that some round did.
func (r *Runtime) emitTokenBudgetEscalation(spanID string, round, from, to int, finishReason string) {
	if r == nil || r.eventChan == nil {
		return
	}
	r.eventChan <- NewAnalyticsEvent(AnalyticsLLMRetry, map[string]interface{}{
		"round":         round,
		"reason":        "max_tokens_truncation",
		"finish_reason": finishReason,
		"from_tokens":   from,
		"to_tokens":     to,
	})
	r.emitModelRetryObserved(ModelRetryInfo{
		Round:         round,
		SpanID:        spanID,
		Kind:          "max_tokens_truncation",
		Attempt:       1,
		Reason:        finishReason,
		MaxTokensFrom: from,
		MaxTokensTo:   to,
	})
}

// emitModelRetryObserved fills in the run-scoped identity every observer
// callback carries and fans the retry out.
func (r *Runtime) emitModelRetryObserved(info ModelRetryInfo) {
	if r == nil || r.svc == nil {
		return
	}
	info.TaskID = currentTaskID(r.session)
	info.RunID = r.runID()
	info.SessionID = r.sessionID()
	info.AgentName = r.currentAgentName()
	ctx := context.Background()
	r.svc.emitObserver(func(o Observer) { o.OnModelRetry(ctx, info) })
}
