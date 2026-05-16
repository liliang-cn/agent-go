package agent

import (
	"strings"
)

// StopReason explains *why* a run terminated. It is independent of the
// task's status (completed / blocked / failed) and gives library callers
// a stable, machine-readable signal alongside the human-readable final
// content.
//
// Mirrors the role of `stop_reason` in the Claude Agent SDK so callers
// porting code between the two have a familiar surface.
type StopReason string

const (
	// StopReasonEndTurn is the normal "model finished" outcome — the
	// task produced a final answer without any anomaly.
	StopReasonEndTurn StopReason = "end_turn"

	// StopReasonMaxTurns means the runtime hit its turn ceiling before
	// the model could finish. Use a higher MaxTurns or resume the task.
	StopReasonMaxTurns StopReason = "max_turns"

	// StopReasonMaxBudgetUSD means the cumulative estimated cost
	// (input + output tokens × model pricing) crossed
	// RunConfig.MaxBudgetUSD. Raise the budget or resume.
	StopReasonMaxBudgetUSD StopReason = "max_budget_usd"

	// StopReasonRefusal means the model declined the request — either
	// the provider surfaced finish_reason="refusal" / "content_filter"
	// or the final text matches a refusal phrase heuristic. Distinct
	// from a generic block because the *model* chose to stop, not the
	// runtime.
	StopReasonRefusal StopReason = "refusal"

	// StopReasonMaxTokens means the provider stopped because the model
	// hit its per-response token cap (finish_reason="length"). Often
	// resolved by raising MaxTokens; the answer may be truncated.
	StopReasonMaxTokens StopReason = "max_tokens"

	// StopReasonLintExhausted means the post-output lint registry
	// rejected the response repeatedly until the per-task retry budget
	// was spent.
	StopReasonLintExhausted StopReason = "lint_exhausted"

	// StopReasonStopHook means a Stop hook returned
	// PreventContinuation=true.
	StopReasonStopHook StopReason = "stop_hook"

	// StopReasonErrorDuringExecution means an underlying step failed
	// (provider error, tool crash, context cancellation, ...). The
	// task's blocker text carries the specific error message.
	StopReasonErrorDuringExecution StopReason = "error_during_execution"
)

// classifyFinishReason maps a provider-side finish_reason string to a
// runtime StopReason. Returns "" when no special handling applies — the
// runtime then chooses end_turn or another reason based on flow.
func classifyFinishReason(finishReason string) StopReason {
	switch strings.ToLower(strings.TrimSpace(finishReason)) {
	case "refusal":
		return StopReasonRefusal
	case "content_filter", "content-filter", "safety":
		return StopReasonRefusal
	case "length", "max_tokens":
		return StopReasonMaxTokens
	}
	return ""
}

// looksLikeRefusalText is a conservative heuristic for refusal phrases
// in a model's final text. Used only when the provider doesn't surface a
// finish_reason — many open-source / OpenAI-compat endpoints don't.
//
// Kept short and high-precision on purpose: false positives are worse
// than false negatives here (a generic block is better than mislabelling
// a partial answer as a refusal).
func looksLikeRefusalText(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	// Anchor on the first ~200 chars so a long answer that happens to
	// contain the phrase mid-paragraph doesn't trip the heuristic.
	prefix := t
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	needles := []string{
		"i can't help",
		"i cannot help",
		"i'm not able to help",
		"i am not able to help",
		"i won't help",
		"i will not help",
		"i'm sorry, but i can't",
		"i am sorry, but i can't",
		"i'm sorry, i can't",
		"i am sorry, i cannot",
		"i cannot assist",
		"i can't assist",
		"i cannot provide",
		"i can't provide",
		"i'm unable to",
		"i am unable to",
		"我无法",
		"我不能",
		"抱歉，我无法",
		"抱歉，我不能",
	}
	for _, n := range needles {
		if strings.Contains(prefix, n) {
			return true
		}
	}
	return false
}
