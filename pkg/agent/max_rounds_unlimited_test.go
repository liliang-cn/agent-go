package agent

import "testing"

// The whole point of UnlimitedRounds is that it is not zero, so these pin the
// two apart at every level that reads the value.

func TestZeroStillMeansUnsetNotUnlimited(t *testing.T) {
	r := &Runtime{cfg: &RunConfig{MaxTurns: 0}}
	if got := r.resolveMaxRounds(); got != DefaultMaxRounds {
		t.Fatalf("an untouched MaxTurns resolved to %d; a caller who set nothing must get the default (%d)", got, DefaultMaxRounds)
	}
}

func TestUnlimitedIsCarriedThroughFromTheRun(t *testing.T) {
	r := &Runtime{cfg: &RunConfig{MaxTurns: UnlimitedRounds}}
	if got := r.resolveMaxRounds(); got != UnlimitedRounds {
		t.Fatalf("a run asking for no budget resolved to %d", got)
	}
}

func TestUnlimitedIsCarriedThroughFromTheService(t *testing.T) {
	r := &Runtime{cfg: &RunConfig{}, svc: &Service{defaultMaxTurns: UnlimitedRounds}}
	if got := r.resolveMaxRounds(); got != UnlimitedRounds {
		t.Fatalf("a service configured for no budget resolved to %d", got)
	}
}

func TestTheRunsOwnBudgetStillWinsOverTheServices(t *testing.T) {
	r := &Runtime{cfg: &RunConfig{MaxTurns: 7}, svc: &Service{defaultMaxTurns: UnlimitedRounds}}
	if got := r.resolveMaxRounds(); got != 7 {
		t.Fatalf("the run asked for 7 and got %d", got)
	}
	r = &Runtime{cfg: &RunConfig{MaxTurns: UnlimitedRounds}, svc: &Service{defaultMaxTurns: 7}}
	if got := r.resolveMaxRounds(); got != UnlimitedRounds {
		t.Fatalf("the run asked for none and got %d", got)
	}
}

func TestANegativeThatIsNotTheSentinelIsTreatedAsUnset(t *testing.T) {
	// Guards the obvious slip of writing "< 0" somewhere and turning every
	// stray negative into an unbounded run.
	r := &Runtime{cfg: &RunConfig{MaxTurns: -42}}
	if got := r.resolveMaxRounds(); got != DefaultMaxRounds {
		t.Fatalf("MaxTurns=-42 resolved to %d, want the default %d", got, DefaultMaxRounds)
	}
}

func TestAnUnlimitedBudgetIsNeverExhausted(t *testing.T) {
	for _, round := range []int{0, 1, 20, 1_000_000} {
		if roundsExhausted(round, UnlimitedRounds) {
			t.Fatalf("an unlimited run was stopped at round %d", round)
		}
	}
}

func TestAFiniteBudgetStopsExactlyAtItsCount(t *testing.T) {
	// The loop is `for round := 0; !roundsExhausted(round, max); round++`, so
	// a budget of 3 must allow rounds 0, 1, 2 and stop at 3.
	var ran int
	for round := 0; !roundsExhausted(round, 3); round++ {
		ran++
		if ran > 10 {
			t.Fatal("a budget of 3 did not stop the loop")
		}
	}
	if ran != 3 {
		t.Fatalf("a budget of 3 ran %d rounds", ran)
	}
}

func TestUnlimitedRunsAreNotToldTheyAreOutOfRounds(t *testing.T) {
	// The model reads remaining_rounds. Reporting 0 to a run that has no limit
	// would make it start wrapping up work it was told it had time for.
	s := newQueryLoopState("goal", nil, UnlimitedRounds)
	for i := 0; i < 5; i++ {
		s.beginRound()
		s.noteRoundCompleted()
	}
	if s.Budget.RemainingRounds != UnlimitedRounds {
		t.Fatalf("after 5 rounds an unlimited run reports %d rounds remaining", s.Budget.RemainingRounds)
	}
}

func TestAFiniteRunCountsItsRemainingRoundsDown(t *testing.T) {
	s := newQueryLoopState("goal", nil, 3)
	s.beginRound()
	s.noteRoundCompleted()
	if s.Budget.RemainingRounds != 2 {
		t.Fatalf("after 1 of 3 rounds, %d remaining, want 2", s.Budget.RemainingRounds)
	}
}

func TestTheProgressLabelSaysUnlimitedRatherThanMinusOne(t *testing.T) {
	if got := formatBudgetProgress(4, UnlimitedRounds); got != "4/∞" {
		t.Fatalf("progress label is %q", got)
	}
	if got := formatBudgetProgress(4, 20); got != "4/20" {
		t.Fatalf("a finite budget's label is %q", got)
	}
}

func TestTheServiceKnobAcceptsUnlimited(t *testing.T) {
	// isRoundBudgetSet gates whether WithAutonomy's value reaches the service
	// at all; if it only accepted positives, asking for unlimited there would
	// be silently dropped.
	if !isRoundBudgetSet(UnlimitedRounds) {
		t.Fatal("UnlimitedRounds does not count as a budget anyone asked for")
	}
	if isRoundBudgetSet(0) {
		t.Fatal("zero counts as a chosen budget, so an untouched field would override the default")
	}
	if !isRoundBudgetSet(200) {
		t.Fatal("200 does not count as a chosen budget")
	}
}
