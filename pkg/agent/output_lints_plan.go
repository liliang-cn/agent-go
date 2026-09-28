package agent

import (
	"fmt"
	"strings"
)

// PlanReadyStepsDone rejects a final answer while the run's plan still has
// steps that could be worked on now and are not checked.
//
// It reads the plan, not the answer's wording: the measured failure was a
// model writing a four-step plan, doing two, and finishing cleanly — the
// prose said "done" and nothing on the output side could tell. The plan
// could. It fires only when LintContext.Plan is filled, which the runtime
// does only for a run that touched its plan through the scratchpad tools, so
// a later, unrelated turn in the same session is not held to an old plan.
//
// The feedback leaves a way out on purpose. A step that turns out to be
// unnecessary or impossible can be checked with a note saying so; without
// that, a model that is right to stop could only be rejected until the run
// blocks.
func PlanReadyStepsDone() OutputLint {
	return LintFunc{
		NameValue: "plan_ready_steps_done",
		Fn: func(_ string, ctx LintContext) (bool, string) {
			if ctx.Plan.Empty() || len(ctx.Plan.Ready) == 0 {
				return true, ""
			}
			labels := make([]string, 0, len(ctx.Plan.Ready))
			for _, i := range ctx.Plan.Ready {
				if i < 0 || i >= len(ctx.Plan.Items) {
					continue
				}
				labels = append(labels, fmt.Sprintf("%d (%s)", i, ctx.Plan.Items[i].Text))
			}
			if len(labels) == 0 {
				return true, ""
			}
			return false, "your plan still has steps that can be done now and are not checked off: " +
				strings.Join(labels, ", ") + ". Do them and check each one off before giving the final answer. " +
				"If a step turns out to be unnecessary or impossible, check it off with a note saying why."
		},
	}
}

// planViewForLint is the plan the final-answer lints see: the run's own plan,
// and only when this run used the scratchpad tools. A session that planned
// on an earlier turn and is now asked something else is not rejected over a
// plan it has stopped working on.
func (r *Runtime) planViewForLint() PlanView {
	if r == nil || r.svc == nil {
		return PlanView{}
	}
	touched := false
	for _, name := range r.toolNamesUsedSnapshot() {
		if strings.HasPrefix(name, "scratchpad_") {
			touched = true
			break
		}
	}
	if !touched {
		return PlanView{}
	}
	key := r.planKey()
	return newPlanView(key, r.svc.scratchpadStore().get(key))
}
