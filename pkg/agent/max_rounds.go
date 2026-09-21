package agent

// The round budget: how many times the loop may go back to the model.
//
// It used to be a constant in the middle of Runtime.loop, which meant both
// knobs pointing at it — WithMaxTurns on a run, WithAutonomy on a service —
// were dead: a caller could set either one, watch it land in RunConfig, and
// still get twenty rounds. Long-horizon work needs hundreds, so the budget
// has to be something a caller can actually raise.

// DefaultMaxRounds is the per-run tool-round budget the runtime uses when
// neither the run nor the service asks for a different one. It is sized for
// an interactive turn — a conversational answer that takes more than twenty
// tool rounds has usually gone wrong rather than gone deep.
//
// A long-horizon run is the other case, and it says so: WithMaxTurns(n) for
// one run, or WithAutonomy(AutonomyProfile{MaxRounds: n}) once for every run
// a service does.
const DefaultMaxRounds = 20

// UnlimitedRounds asks for no round budget at all: the loop runs until the
// model stops calling tools, the caller cancels, or the context ends.
//
// It is a distinct value rather than "zero means unlimited" because zero
// already means something load-bearing. RunConfig.MaxTurns starts life at zero
// and a caller who never touched it must get the default, not an unbounded
// run — see resolveMaxRounds. So asking for unlimited has to be something you
// can only do on purpose, and this is it.
//
// Nothing else stops a run that asks for this. A model looping on a failing
// tool will loop until the context is cancelled, and every round is a paid
// model call over a growing context, so the caller is taking on the job the
// budget was doing: a deadline, a spend ceiling, or a human watching.
const UnlimitedRounds = -1

// resolveMaxRounds picks this run's round budget, most specific first: the
// run's own WithMaxTurns, then the service's WithAutonomy default, then the
// framework default. It returns UnlimitedRounds when either level asked for
// no budget; every caller of this has to handle that.
//
// Zero means "not set" rather than "no rounds". Zero rounds is a run that
// cannot call a single tool and cannot answer, which is never what a caller
// means by leaving a field at its zero value — and RunConfig.MaxTurns starts
// life at zero for exactly that reason. Unlimited is UnlimitedRounds, which no
// zero value can be mistaken for.
func (r *Runtime) resolveMaxRounds() int {
	if r == nil {
		return DefaultMaxRounds
	}
	if r.cfg != nil && isRoundBudgetSet(r.cfg.MaxTurns) {
		return r.cfg.MaxTurns
	}
	if r.svc != nil && isRoundBudgetSet(r.svc.defaultMaxTurns) {
		return r.svc.defaultMaxTurns
	}
	return DefaultMaxRounds
}

// isRoundBudgetSet reports whether n is a budget a caller actually chose —
// a positive count, or the explicit request for none.
func isRoundBudgetSet(n int) bool { return n > 0 || n == UnlimitedRounds }

// roundsExhausted reports whether a run that has completed round rounds has
// used up budget max. An unlimited budget is never exhausted.
func roundsExhausted(round, max int) bool { return max != UnlimitedRounds && round >= max }
