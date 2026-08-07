package agent

import (
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Answering the part you can, when the part you can't has no tool.
//
// A task like "work out the interest and email it to me" run against an agent
// with no mail tool has a right answer and a wrong one. The right answer is the
// interest figure plus a plain statement that it could not be sent and why. The
// wrong answer is task_blocked, which scores nothing and tells the user nothing
// they can use.
//
// The delivery-contract lint already declines to punish a missing tool — an
// agent cannot be faulted for a capability it never had. But declining to
// punish is not the same as steering, and models reliably chose to give up.
//
// This is the steer, and it is deterministic rather than a prompt: the runtime
// already knows both facts it needs — the user asked for a delivery, and no
// registered tool can perform it — so when the model reaches for task_blocked
// it gets one structured nudge to answer the computable part instead.

// undeliverableRequirements returns the deliverables this run owes that no
// available tool can satisfy.
//
// "Can no tool do this?" is answered by the constraint extraction, which sees
// the run's tool catalog and names the tool it picked (satisfied_by). An empty
// pick — or a pick that is not actually registered — means the capability is
// missing, which is exactly the case this fallback steers.
func undeliverableRequirements(constraints RunConstraints, availableTools []string) []DeliverableRequirement {
	var out []DeliverableRequirement
	for _, want := range constraints.Deliverables {
		if want.SatisfiedBy != "" && toolIsAvailable(availableTools, want.SatisfiedBy) {
			continue
		}
		// A file deliverable with a named path is verified against the artifact
		// by the delivery contract, not steered toward a partial answer.
		if strings.EqualFold(strings.TrimSpace(want.Kind), "file") && want.Path != "" {
			continue
		}
		out = append(out, want)
	}
	return out
}

// deliverableFallbackPrompt is the feedback injected when the model gives up on
// a task whose delivery step has no tool behind it.
func deliverableFallbackPrompt(missing []DeliverableRequirement) string {
	var b strings.Builder
	b.WriteString("[system] You are about to stop without answering. This run has no tool that can perform ")
	parts := make([]string, 0, len(missing))
	for _, m := range missing {
		label := strings.TrimSpace(m.Description)
		if label == "" {
			label = m.Kind
		}
		parts = append(parts, label)
	}
	b.WriteString(strings.Join(parts, "; "))
	b.WriteString(" — that part is genuinely impossible here, and stopping is not the answer.\n")
	b.WriteString("Do the part you can: work out and state the result the user asked for, ")
	b.WriteString("then say plainly which part you could not carry out and why (no such tool is available). ")
	b.WriteString("Finish with task_complete, not task_blocked.")
	return b.String()
}

// redirectBlockedToPartialAnswer intercepts a model-initiated block on a run
// whose only obstacle is a missing delivery tool. It injects the nudge once per
// run and asks the loop to take another turn; a second attempt to block is
// allowed through, so the model can still stop if it truly has nothing.
//
// Returns true when the loop should continue instead of terminating.
func (r *Runtime) redirectBlockedToPartialAnswer(messages *[]domain.Message, state *queryLoopState) bool {
	if r == nil || r.deliverableFallbackUsed {
		return false
	}
	missing := undeliverableRequirements(r.runConstraints(), r.availableToolNamesSnapshot())
	if len(missing) == 0 {
		return false
	}
	r.deliverableFallbackUsed = true

	*messages = append(*messages, domain.Message{
		Role:    "user",
		Content: deliverableFallbackPrompt(missing),
	})
	if state != nil {
		state.Messages = *messages
		state.setLoopTransition(queryLoopTransitionNextTurn,
			"blocked redirected: deliverable has no tool, answer the computable part")
		state.noteRoundCompleted()
	}
	return true
}

// DeliverableBlockMustCarryWork rejects a blocked run that gave up on a missing
// delivery tool without reporting the work it could still do.
//
// The redirect above asks the model to answer the computable part. If it blocks
// anyway that is allowed — but the blocker text then has to contain something
// the user can use, not just "I cannot send the email". A bare refusal on a task
// with a computable half scores nothing and helps nobody.
func DeliverableBlockMustCarryWork() OutputLint {
	return LintFunc{
		NameValue: "deliverable_block_must_carry_work",
		Fn: func(text string, ctx LintContext) (bool, string) {
			missing := undeliverableRequirements(
				RunConstraints{Deliverables: ctx.Deliverables}, ctx.AvailableTools)
			if len(missing) == 0 {
				return true, ""
			}
			if len(strings.TrimSpace(text)) >= minimumPartialAnswerChars {
				return true, ""
			}
			return false, "you stopped because a delivery tool is missing, but said nothing the user can use. " +
				"State the result you were able to work out, then say which part could not be carried out and why."
		},
	}
}

// minimumPartialAnswerChars is the length below which a blocker reads as a bare
// refusal rather than a report of partial work.
const minimumPartialAnswerChars = 80
