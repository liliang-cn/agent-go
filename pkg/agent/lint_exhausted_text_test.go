package agent

import (
	"strings"
	"testing"
)

// Lint reasons are written to the model, not to the user. A run that exhausted
// its lint budget used to hand the reason straight to the blocked event, so the
// user's final answer was
//
//	"output lint requested_action_contract repeatedly rejected the response:
//	 the user explicitly asked you to carry out that action…"
//
// — the machinery arguing with itself. This pins that it never happens again.
func TestLintExhaustionNeverLeaksMachineTextToTheUser(t *testing.T) {
	t.Parallel()

	draft := "The average of 88, 92, 79 and 85 is 86."
	got := lintExhaustedUserText(draft)

	if !strings.Contains(got, draft) {
		t.Errorf("the model's substantive answer was dropped: %q", got)
	}
	for _, leak := range []string{
		"output lint",
		"requested_action_contract",
		"rejected the response",
		"you never called it",
		"task_blocked",
	} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(leak)) {
			t.Errorf("final text leaked machine wording %q: %q", leak, got)
		}
	}
	// The fallback (the model could not be asked to rewrite it) is the draft
	// alone: a fixed English sentence appended to an answer in another
	// language is worse than none, and the run's StopReason already says it
	// was not confirmed.
	if got != draft {
		t.Errorf("fallback changed the draft: %q", got)
	}
	if zh := "已把本周购物清单存到 /Documents/购物.txt。"; lintExhaustedUserText(zh) != zh {
		t.Errorf("an English caveat was appended to a Chinese answer: %q", lintExhaustedUserText(zh))
	}
}

// With no draft to carry, the caveat still has to stand on its own — an empty
// blocked event tells the user nothing at all.
func TestLintExhaustionWithoutADraftStillSaysSomething(t *testing.T) {
	t.Parallel()

	for _, empty := range []string{"", "   ", "\n\t"} {
		got := lintExhaustedUserText(empty)
		if strings.TrimSpace(got) == "" {
			t.Fatalf("empty draft produced an empty final answer")
		}
		if strings.Contains(got, "output lint") {
			t.Fatalf("machine wording leaked: %q", got)
		}
	}
}
