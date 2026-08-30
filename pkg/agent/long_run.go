package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Running one task across many segments.
//
// Everything else in this package makes a single run survive longer. None of
// it makes a run that ended start again, and a task measured in hours cannot
// be one run: the context window fills, the round budget runs out, the process
// gets restarted, the machine reboots. So a long task is many runs, and this is
// the loop that drives them.
//
// The continuity is deliberately not the conversation. Each segment starts a
// fresh session, so its context length starts at zero rather than inheriting a
// summary of a summary of a summary. What carries across is what was actually
// established:
//
//   - the plan, with PlanItem.Note saying what each finished step produced,
//     injected into the next segment's system prompt (see planSummaryForRun);
//   - the workspace, which is the same sandbox on the same Service;
//   - run memory, recalled at the start of every segment like any other run.
//
// That is the whole trick, and it is why the segments do not degrade: segment
// forty reads the same kind of prompt segment two did.
//
// This is not orchestration and there is no second engine. RunSegments calls
// Run, reads why it stopped, and calls it again.

// LongRunConfig bounds a segmented run. The zero value is usable: every field
// falls back to a default.
type LongRunConfig struct {
	// MaxSegments caps how many times the task is picked back up. It is the
	// backstop against a task that can never finish quietly costing money
	// forever.
	MaxSegments int

	// RoundsPerSegment is each segment's round budget. Smaller segments
	// compact less and hand over more often; larger ones keep more in one
	// conversation. It is not the total: MaxSegments × RoundsPerSegment is.
	RoundsPerSegment int

	// MaxConsecutiveFailures ends the long run after this many segments fail
	// in a row. Consecutive is the point — an outage that has swallowed three
	// segments back to back is not going to be fixed by a fourth, while
	// failures scattered over forty segments are just a flaky provider.
	MaxConsecutiveFailures int

	// RequirePlanComplete keeps going when a segment reports it is done but
	// the plan it kept still has unchecked steps. Default true; set
	// AllowIncompletePlan to turn it off.
	AllowIncompletePlan bool

	// PlanKey is the scratchpad list the plan lives under. Empty means the
	// one the scratchpad tools write to when the model does not name one.
	PlanKey string

	// MaxDuration stops starting new segments once the task has been running
	// this long. It is not a deadline on the work in flight — a segment that
	// has started is allowed to finish, so the task ends at a hand-off point
	// with its plan and workspace consistent, rather than being cut in half.
	// Zero = no limit; the context's own deadline still applies and does cut.
	MaxDuration time.Duration

	// MaxTotalCostUSD caps what the whole task may spend, summed over every
	// segment. RunConfig.MaxBudgetUSD only ever bounded one run, which on a
	// task made of forty of them bounds nothing.
	//
	// It is enforced twice: no new segment starts once the total is reached,
	// and each segment is given the remainder as its own MaxBudgetUSD so it
	// stops mid-flight rather than overrunning the ceiling by however much a
	// segment happens to cost. Checking only between segments made the real
	// bound "the limit, plus one whole segment" — fine at sixty rounds,
	// meaningless at six hundred.
	// Zero = no limit.
	MaxTotalCostUSD float64

	// MaxUnproductiveSegments ends the task after this many segments in a row
	// change nothing.
	//
	// A segment that runs out of rounds having called only read-only tools has
	// not failed — it succeeded at doing nothing, and the failure budget never
	// sees it. Measured on a real run: with segments of 15 rounds, segments 3
	// and 4 spent every round re-reading the workspace and wrote not one byte,
	// and the supervisor would happily have bought seventy-five more of them.
	//
	// Below some segment size, rediscovery costs more than the segment is
	// worth. This is how the task notices rather than paying for it eighty
	// times. Default 3; 0 = the default.
	MaxUnproductiveSegments int

	// SegmentRetryBackoff is how long to wait after a failed segment before
	// starting the next one, doubling with each consecutive failure.
	//
	// It exists because a failed segment used to restart instantly, which
	// spent the whole MaxConsecutiveFailures budget in seconds and made it
	// useless against the outage it was meant for: a provider cooldown is
	// measured in tens of minutes, not seconds. Zero = the default ladder.
	SegmentRetryBackoff time.Duration
}

// Defaults for a segmented run.
const (
	defaultMaxSegments            = 20
	defaultRoundsPerSegment       = 100
	defaultMaxConsecutiveFailures = 3

	// defaultMaxUnproductiveSegments is how many segments in a row may change
	// nothing before the task gives up on the segment size it was given.
	defaultMaxUnproductiveSegments = 3

	// defaultSegmentRetryBackoff starts the wait after a failed segment.
	// Doubling from five minutes, three consecutive failures sit out roughly
	// half an hour of provider trouble before the task gives up — the right
	// order of magnitude for a cooldown, where the per-call retry ladder
	// (capped at a minute) is not.
	defaultSegmentRetryBackoff = 5 * time.Minute

	// segmentRetryMaxBackoff caps that doubling.
	segmentRetryMaxBackoff = 30 * time.Minute
)

// SegmentOutcome is what one segment of a long run did.
type SegmentOutcome struct {
	Index      int
	SessionID  string
	StopReason StopReason
	Text       string
	Error      string
	Rounds     int
	Duration   time.Duration
	// Productive is false when the segment called nothing that could change
	// state — no writes, no commands, only reads. Such a segment did not fail;
	// it succeeded at doing nothing, which the failure budget cannot see.
	Productive bool

	// WaitedBefore is how long the supervisor sat out a provider outage
	// before starting this segment. Non-zero only after a failure, and worth
	// reporting: it is the difference between a task that took eleven hours
	// and one that took eleven hours of which two were waiting.
	WaitedBefore time.Duration
}

// LongRunStop says why the supervisor stopped starting segments. It is
// deliberately distinct from a run's StopReason: "the task finished" and "I
// stopped asking" are different statements, and conflating them is how a
// budget exhaustion gets reported as a result.
type LongRunStop string

const (
	// LongRunStopFinished is the only outcome that means the work is done.
	LongRunStopFinished LongRunStop = "finished"
	// LongRunStopSegmentBudget means MaxSegments ran out with work left.
	LongRunStopSegmentBudget LongRunStop = "segment_budget_exhausted"
	// LongRunStopFailing means MaxConsecutiveFailures segments failed in a row.
	LongRunStopFailing LongRunStop = "consecutive_failures"
	// LongRunStopCancelled means the caller stopped it. An outcome, not a fault.
	LongRunStopCancelled LongRunStop = "cancelled"
	// LongRunStopBlocked means a segment concluded the task cannot proceed.
	// Retrying a considered refusal in a fresh segment would just spend the
	// budget arriving at it again.
	LongRunStopBlocked LongRunStop = "blocked"
	// LongRunStopTimeLimit means MaxDuration ran out with work left.
	LongRunStopTimeLimit LongRunStop = "time_limit"
	// LongRunStopCostLimit means MaxTotalCostUSD ran out with work left.
	LongRunStopCostLimit LongRunStop = "cost_limit"
	// LongRunStopUnproductive means several segments in a row changed nothing,
	// which usually means each one is too small to get past rediscovery.
	LongRunStopUnproductive LongRunStop = "unproductive_segments"
)

// LongRunResult is the whole task's outcome.
type LongRunResult struct {
	TaskID   string
	Segments []SegmentOutcome
	// Stop says why the supervisor stopped. Only LongRunStopFinished means
	// the task is done.
	Stop LongRunStop
	// Text is the last segment's answer, which is the task's answer only when
	// Stop is LongRunStopFinished.
	Text string
	// PlanSummary is what the plan looked like at the end — the honest report
	// of how far an unfinished task got.
	PlanSummary string
	Duration    time.Duration
	// TotalCostUSD is what every segment cost together, which is the only
	// figure that means anything for a task made of dozens of runs.
	TotalCostUSD float64
	// TotalUsage sums the provider-reported tokens across segments. Nil when
	// no segment's provider reported any.
	TotalUsage *domain.TokenUsage
}

// Done reports whether the task actually finished.
func (r *LongRunResult) Done() bool {
	return r != nil && r.Stop == LongRunStopFinished
}

func (c LongRunConfig) resolved() LongRunConfig {
	if c.MaxSegments <= 0 {
		c.MaxSegments = defaultMaxSegments
	}
	if c.RoundsPerSegment <= 0 {
		c.RoundsPerSegment = defaultRoundsPerSegment
	}
	if c.MaxConsecutiveFailures <= 0 {
		c.MaxConsecutiveFailures = defaultMaxConsecutiveFailures
	}
	if strings.TrimSpace(c.PlanKey) == "" {
		c.PlanKey = scratchpadDefaultKey
	}
	if c.MaxUnproductiveSegments <= 0 {
		c.MaxUnproductiveSegments = defaultMaxUnproductiveSegments
	}
	if c.SegmentRetryBackoff <= 0 {
		c.SegmentRetryBackoff = defaultSegmentRetryBackoff
	}
	return c
}

// segmentRetryDelay is how long to wait before the next attempt after n
// consecutive failures, doubling and capped.
func segmentRetryDelay(base time.Duration, consecutiveFailures int) time.Duration {
	if consecutiveFailures < 1 {
		consecutiveFailures = 1
	}
	d := base
	for i := 1; i < consecutiveFailures && d < segmentRetryMaxBackoff; i++ {
		d *= 2
	}
	if d > segmentRetryMaxBackoff {
		d = segmentRetryMaxBackoff
	}
	return d
}

// RunSegments drives one goal across as many runs as it takes.
//
// One task id spans the whole thing, so every segment's checkpoints and the
// plan accumulate under it and Tasks().ResumeFromCheckpoint still addresses a
// single task. Each segment gets its own session id, which is what stops the
// conversation from growing across segments.
//
// Extra RunOptions are applied to every segment. WithMaxTurns and WithSessionID
// are overridden — those are the supervisor's to set — so pass
// RoundsPerSegment instead of the former.
func (s *Service) RunSegments(ctx context.Context, goal string, cfg LongRunConfig, opts ...RunOption) (*LongRunResult, error) {
	return s.runSegments(ctx, goal, cfg, nil, opts...)
}

// runSegments is RunSegments with somewhere to forward its segments' events.
// sink may be nil; RunSegmentsStream supplies one.
func (s *Service) runSegments(ctx context.Context, goal string, cfg LongRunConfig, sink chan<- *Event, opts ...RunOption) (*LongRunResult, error) {
	if s == nil {
		return nil, fmt.Errorf("agent: RunSegments on a nil service")
	}
	callerNamedPlanKey := strings.TrimSpace(cfg.PlanKey) != ""
	cfg = cfg.resolved()

	began := time.Now()
	taskID := uuid.NewString()
	// A caller who named the task keeps their name: the segments should land
	// under the id the host is already tracking.
	probe := &RunConfig{}
	for _, opt := range opts {
		opt(probe)
	}
	if named := strings.TrimSpace(probe.TaskID); named != "" {
		taskID = named
	}

	// Scope the plan to the task. PlanKey defaulted to a single shared
	// constant, which was harmless while a plan lived in memory for the
	// lifetime of one Service — and became cross-task contamination the
	// moment plans were persisted: two long tasks against one database would
	// read and overwrite each other's steps. A caller who names PlanKey keeps
	// their name, including when they name the default one.
	if !callerNamedPlanKey {
		cfg.PlanKey = scratchpadDefaultKey + ":" + taskID
	}

	out := &LongRunResult{TaskID: taskID}
	consecutiveFailures := 0
	unproductive := 0
	// What the task has done so far, across every segment. Contract lints
	// judge the task, not the segment that happens to be running.
	toolsUsed := map[string]struct{}{}

	for i := 0; i < cfg.MaxSegments; i++ {
		if ctx.Err() != nil {
			out.Stop = LongRunStopCancelled
			break
		}
		// Budgets are checked between segments, never inside one. A segment
		// that has started is allowed to finish so the task stops at a
		// hand-off point, with its plan and workspace consistent, rather than
		// being cut in half by a clock.
		if cfg.MaxDuration > 0 && time.Since(began) >= cfg.MaxDuration {
			out.Stop = LongRunStopTimeLimit
			break
		}
		if cfg.MaxTotalCostUSD > 0 && out.TotalCostUSD >= cfg.MaxTotalCostUSD {
			out.Stop = LongRunStopCostLimit
			break
		}

		// Sit out a provider outage rather than spending the failure budget
		// on it in seconds.
		var waited time.Duration
		if consecutiveFailures > 0 {
			delay := segmentRetryDelay(cfg.SegmentRetryBackoff, consecutiveFailures)
			if cfg.MaxDuration > 0 {
				if left := cfg.MaxDuration - time.Since(began); left < delay {
					delay = left
				}
			}
			if delay > 0 {
				if !waitBeforeLLMRetry(ctx, delay) {
					out.Stop = LongRunStopCancelled
					break
				}
				waited = delay
			}
		}

		sessionID := uuid.NewString()
		segmentOpts := append([]RunOption{}, opts...)
		prior := make([]string, 0, len(toolsUsed))
		for name := range toolsUsed {
			prior = append(prior, name)
		}
		sort.Strings(prior)
		segmentOpts = append(segmentOpts,
			WithPriorToolCalls(prior),
			WithTaskID(taskID),
			// A fresh session per segment is the whole point: the next
			// segment's history starts empty and the hand-off carries the
			// state instead.
			WithSessionID(sessionID),
			WithMaxTurns(cfg.RoundsPerSegment),
		)
		// Hand the segment what is left of the task's budget, so the ceiling
		// holds inside a segment and not merely between them.
		if left, ok := segmentCostCeiling(cfg, out.TotalCostUSD); ok {
			segmentOpts = append(segmentOpts, WithMaxBudgetUSD(left))
		}

		segStart := time.Now()
		s.emitSegmentObserved(ctx, SegmentInfo{
			TaskID: taskID, Index: i, Total: cfg.MaxSegments, SessionID: sessionID,
		})
		segCfg := DefaultRunConfig()
		for _, opt := range segmentOpts {
			opt(segCfg)
		}
		result, err := s.runWithConfigTee(ctx, goal, segCfg, sink)
		seg := SegmentOutcome{
			Index:        i,
			SessionID:    sessionID,
			Duration:     time.Since(segStart),
			WaitedBefore: waited,
		}
		if result != nil {
			seg.Productive = s.segmentChangedSomething(result)
			for _, name := range result.ToolsUsed {
				toolsUsed[name] = struct{}{}
			}
			out.TotalCostUSD += result.EstimatedCostUSD
			out.TotalUsage = addUsage(out.TotalUsage, result.Usage)
			seg.StopReason = result.StopReason
			seg.Text = result.Text()
			seg.Error = result.Error
			seg.Rounds = result.ToolCalls
		}
		if err != nil && seg.Error == "" {
			seg.Error = err.Error()
		}
		out.Segments = append(out.Segments, seg)
		out.Text = seg.Text
		s.emitSegmentObserved(ctx, SegmentInfo{
			TaskID: taskID, Index: i, Total: cfg.MaxSegments, SessionID: sessionID,
			Ending: true, StopReason: seg.StopReason, Duration: seg.Duration,
			Productive: seg.Productive, CostUSD: out.TotalCostUSD, Err: seg.Error,
		})

		switch {
		case ctx.Err() != nil || (result != nil && result.Cancelled):
			out.Stop = LongRunStopCancelled
		case err != nil || result == nil || (!result.Success && !result.Blocked):
			// A failed segment is not the end: the plan and the workspace
			// survived it, so the next segment picks up where this one died.
			consecutiveFailures++
			if consecutiveFailures >= cfg.MaxConsecutiveFailures {
				out.Stop = LongRunStopFailing
			}
		case result != nil && result.StopReason == StopReasonMaxBudgetUSD:
			// The segment stopped because the task ran out of money, which is
			// the task's outcome and not the segment's verdict. Before the
			// remainder was handed down, this could only happen between
			// segments and the loop labelled it itself; now that a segment can
			// hit the ceiling mid-flight, the label has to survive the trip
			// back up or a task that spent its budget reports "blocked".
			out.Stop = LongRunStopCostLimit
		case result.Blocked && result.StopReason != StopReasonMaxTurns:
			// A considered "I cannot proceed" is an answer. Starting another
			// segment would spend the budget arriving at it again.
			//
			// The stop reason is what separates that from running out of road.
			// A segment that exhausts its rounds is forced to synthesise an
			// answer, and if that answer fails a final lint the runtime blocks
			// it — with StopReasonMaxTurns, because the budget is what ran
			// out. Reading that as a refusal ended a soak run at 9 of 13
			// milestones while it was still making progress: it had not decided
			// anything, it had simply not finished.
			out.Stop = LongRunStopBlocked
		default:
			consecutiveFailures = 0
			if seg.Productive {
				unproductive = 0
			} else {
				unproductive++
			}
			switch {
			case s.segmentFinishedTheTask(result, cfg):
				out.Stop = LongRunStopFinished
			case unproductive >= cfg.MaxUnproductiveSegments:
				// Each segment is spending everything it has on working out
				// where it is. Buying more of them buys more of that.
				out.Stop = LongRunStopUnproductive
			}
		}
		if out.Stop != "" {
			break
		}
	}

	if out.Stop == "" {
		out.Stop = LongRunStopSegmentBudget
	}
	out.PlanSummary = s.PlanSummary(cfg.PlanKey)
	out.Duration = time.Since(began)
	return out, nil
}

// segmentFinishedTheTask decides whether a successful segment ended the work
// or merely ended.
//
// Two things have to be true. The run must have stopped because the model
// concluded, not because it ran out of rounds — those are indistinguishable in
// ExecutionResult.Success, and the second one carries a synthesised answer
// assembled from however far it got. And if the agent kept a plan, that plan
// must not still have unchecked steps.
//
// The plan check reads Done flags, not text. A run that never used the
// scratchpad has no plan, so the check is vacuously true and the stop reason
// decides alone — which is the right degradation, not a special case.
func (s *Service) segmentFinishedTheTask(result *ExecutionResult, cfg LongRunConfig) bool {
	if result == nil || !result.Success {
		return false
	}
	if result.StopReason == StopReasonMaxTurns {
		return false
	}
	if cfg.AllowIncompletePlan {
		return true
	}
	return !s.planHasUnfinishedSteps(cfg.PlanKey)
}

// planHasUnfinishedSteps reports whether the stored plan still has work in it.
func (s *Service) planHasUnfinishedSteps(key string) bool {
	if s == nil {
		return false
	}
	for _, item := range s.scratchpadStore().get(key) {
		if !item.Done {
			return true
		}
	}
	return false
}

// addUsage sums a segment's token accounting into the task's running total.
// Nil in means nothing to add; nil out stays nil, so a task whose providers
// never reported usage reports none rather than a fabricated zero.
func addUsage(total, seg *domain.TokenUsage) *domain.TokenUsage {
	if seg == nil {
		return total
	}
	if total == nil {
		copied := *seg
		return &copied
	}
	total.PromptTokens += seg.PromptTokens
	total.CompletionTokens += seg.CompletionTokens
	total.CachedPromptTokens += seg.CachedPromptTokens
	total.CacheWriteTokens += seg.CacheWriteTokens
	return total
}

// segmentChangedSomething reports whether a segment called anything that could
// change state.
//
// Read-only is declared by the tool, so this asks the registry rather than
// guessing from names — the same question the duplicate-call check asks, for
// the same reason. A segment that only read did not fail and did not finish;
// it did nothing, and that is a third outcome the supervisor has to be able to
// see.
func (s *Service) segmentChangedSomething(result *ExecutionResult) bool {
	if result == nil {
		return false
	}
	for _, name := range result.ToolsUsed {
		if s.toolCallChangesState(name) {
			return true
		}
	}
	return false
}

// emitSegmentObserved reports a segment boundary.
//
// RunSegments emitted nothing at all before this: a supervisor driving a task
// across eleven segments and an hour was one opaque call from outside, and its
// boundaries had to be reverse-engineered from round numbers restarting at 1.
func (s *Service) emitSegmentObserved(ctx context.Context, info SegmentInfo) {
	if s == nil {
		return
	}
	s.emitObserver(func(o Observer) { o.OnSegment(ctx, info) })
}

// segmentCostCeiling is what one segment may spend: whatever the task has left.
//
// Reported as (amount, ok) rather than a bare float so "no ceiling configured"
// and "nothing left" stay distinguishable — the first must not cap a segment,
// and the second never reaches here because the loop stops first.
func segmentCostCeiling(cfg LongRunConfig, spent float64) (float64, bool) {
	if cfg.MaxTotalCostUSD <= 0 {
		return 0, false
	}
	left := cfg.MaxTotalCostUSD - spent
	if left <= 0 {
		return 0, false
	}
	return left, true
}
