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

// resolveMaxRounds picks this run's round budget, most specific first: the
// run's own WithMaxTurns, then the service's WithAutonomy default, then the
// framework default.
//
// A non-positive value at either level means "not set" rather than "no
// rounds". Zero rounds is a run that cannot call a single tool and cannot
// answer, which is never what a caller means by leaving a field at its zero
// value — and RunConfig.MaxTurns starts life at zero for exactly that reason.
func (r *Runtime) resolveMaxRounds() int {
	if r == nil {
		return DefaultMaxRounds
	}
	if r.cfg != nil && r.cfg.MaxTurns > 0 {
		return r.cfg.MaxTurns
	}
	if r.svc != nil && r.svc.defaultMaxTurns > 0 {
		return r.svc.defaultMaxTurns
	}
	return DefaultMaxRounds
}
