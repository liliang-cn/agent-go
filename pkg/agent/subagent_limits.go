package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// DefaultSubagentMaxDepth is how deep sub-agents may nest when nothing says
// otherwise: the top-level run may hand work to a sub-agent, and that
// sub-agent may not hand it on again.
//
// One, not two, because every level multiplies spend and hides work from the
// person watching: a child's events reach the host nested once already, and
// its tool budget, lints and checkpoints are its own. A child that wants to
// delegate further is almost always a child that was given too big a job,
// and the cheap correction is to do the work itself. It is also what a child
// could do before this bound existed only by accident — the `task` tool was
// in every child's tool list, so nesting was unbounded, not deliberate.
// WithSubagentMaxDepth(2) is there for a caller who has actually designed a
// two-level tree.
const DefaultSubagentMaxDepth = 1

// DefaultSubagentMaxParallel caps how many parallel-safe sub-agents one turn
// runs at once. Reference harnesses settle on three or four: past that the
// provider's rate limit, not the loop, decides the wall clock, and every
// child holds a whole conversation in memory.
const DefaultSubagentMaxParallel = 4

// subagentLimits is the Service's configuration for the `task` tool's
// fan-out and nesting. The zero value means "use the defaults".
type subagentLimits struct {
	mu          sync.RWMutex
	maxDepth    int
	maxParallel int
	// parallel holds the lower-cased names of the specs that declared
	// Parallel. Membership is what lets a task call leave its own batch.
	parallel map[string]bool
}

func (l *subagentLimits) setParallel(name string, parallel bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.parallel == nil {
		l.parallel = make(map[string]bool)
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if parallel {
		l.parallel[key] = true
	} else {
		delete(l.parallel, key)
	}
}

func (l *subagentLimits) isParallel(name string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.parallel[strings.ToLower(strings.TrimSpace(name))]
}

// SetSubagentMaxDepth bounds how deep `task` / `delegate_to_subagent` may
// nest. n < 1 restores DefaultSubagentMaxDepth. Safe to call while runs are
// in flight; a run reads it each time it delegates.
func (s *Service) SetSubagentMaxDepth(n int) {
	s.subagentLimits.mu.Lock()
	s.subagentLimits.maxDepth = n
	s.subagentLimits.mu.Unlock()
}

// SetSubagentMaxParallel bounds how many parallel-safe sub-agents one turn
// runs at the same time. n < 1 restores DefaultSubagentMaxParallel.
func (s *Service) SetSubagentMaxParallel(n int) {
	s.subagentLimits.mu.Lock()
	s.subagentLimits.maxParallel = n
	s.subagentLimits.mu.Unlock()
}

// SubagentMaxDepth reports the nesting bound in force.
func (s *Service) SubagentMaxDepth() int {
	s.subagentLimits.mu.RLock()
	defer s.subagentLimits.mu.RUnlock()
	if s.subagentLimits.maxDepth < 1 {
		return DefaultSubagentMaxDepth
	}
	return s.subagentLimits.maxDepth
}

// SubagentMaxParallel reports the per-turn fan-out bound in force.
func (s *Service) SubagentMaxParallel() int {
	s.subagentLimits.mu.RLock()
	defer s.subagentLimits.mu.RUnlock()
	if s.subagentLimits.maxParallel < 1 {
		return DefaultSubagentMaxParallel
	}
	return s.subagentLimits.maxParallel
}

// isParallelSubagentCall reports whether a tool call is a `task` call naming a
// sub-agent that declared itself safe to run beside others.
//
// It reads the call's own agent_name argument — a field of the tool's schema,
// constrained by an enum to the configured names — not anything the user
// wrote, so it decides nothing from wording.
func (s *Service) isParallelSubagentCall(tc domain.ToolCall) bool {
	if s == nil || !strings.EqualFold(strings.TrimSpace(tc.Function.Name), subagentTaskToolName) {
		return false
	}
	name, _ := tc.Function.Arguments["agent_name"].(string)
	return name != "" && s.subagentLimits.isParallel(name)
}

// subagentTaskToolName is the one tool WithSubagents registers.
const subagentTaskToolName = "task"

// delegationToolNames are the tools that start another sub-agent run in this
// process. A child at the depth limit is not offered any of them.
var delegationToolNames = []string{subagentTaskToolName, "delegate_to_subagent", "delegate_async", "subagent_send_message"}

type subagentDepthKey struct{}

// withSubagentDepth records how many sub-agent levels sit above the code
// running under ctx. The top-level run is depth 0.
func withSubagentDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, subagentDepthKey{}, depth)
}

// subagentDepth reads the nesting level; 0 outside any sub-agent.
func subagentDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	d, _ := ctx.Value(subagentDepthKey{}).(int)
	return d
}

// subagentDepthRefusal is what a delegation beyond the limit returns instead
// of running anything. It is a result, not an error: an error would reach the
// model as a failed tool it may retry, and at worst end the run, when the
// only useful thing to say is "you have the tools — do it yourself".
func subagentDepthRefusal(depth, maxDepth int, agentName string) map[string]interface{} {
	out := map[string]interface{}{
		"ok":        false,
		"refused":   true,
		"reason":    "subagent_depth_limit",
		"depth":     depth,
		"max_depth": maxDepth,
		"message": fmt.Sprintf("Sub-agents cannot be nested deeper than %d level(s), and this run is already at depth %d. "+
			"Do not delegate this work: carry it out yourself with the tools you have, then give your answer.", maxDepth, depth),
	}
	if agentName != "" {
		out["agent_name"] = agentName
	}
	return out
}

// toolCallGate orders the tool calls of one streamed turn.
//
// The streaming loop starts each call the moment its arguments are complete,
// which used to mean every call in a turn ran at once whatever it declared:
// two writes to one file, a bash command beside the edit it depends on, four
// sub-agents working the same checkout. The batch executor has always
// partitioned by isConcurrencySafeToolCall; the path real runs take did not.
//
// The gate applies the same partition incrementally, in the order the model
// emitted the calls: a concurrency-safe call waits only for the last
// exclusive call before it; an exclusive call waits for everything before it,
// and everything after it waits for it. Consecutive safe calls therefore
// overlap, exactly as one batch would, and nothing overtakes a write.
//
// Parallel-safe sub-agents additionally share a bounded number of slots.
type toolCallGate struct {
	mu             sync.Mutex
	lastExclusive  chan struct{}
	sinceExclusive []chan struct{}
	subagentSlots  chan struct{}
}

func newToolCallGate(maxParallelSubagents int) *toolCallGate {
	if maxParallelSubagents < 1 {
		maxParallelSubagents = DefaultSubagentMaxParallel
	}
	return &toolCallGate{subagentSlots: make(chan struct{}, maxParallelSubagents)}
}

// toolCallTicket is one call's place in the order.
type toolCallTicket struct {
	waitFor []chan struct{}
	done    chan struct{}
	slots   chan struct{} // non-nil when the call needs a sub-agent slot
	holding bool
}

// admit records a call in emission order. It never blocks: the stream
// callback that calls it must keep reading the provider.
func (g *toolCallGate) admit(safe, subagentSlot bool) *toolCallTicket {
	g.mu.Lock()
	defer g.mu.Unlock()
	t := &toolCallTicket{done: make(chan struct{})}
	if subagentSlot {
		t.slots = g.subagentSlots
	}
	if safe {
		if g.lastExclusive != nil {
			t.waitFor = []chan struct{}{g.lastExclusive}
		}
		g.sinceExclusive = append(g.sinceExclusive, t.done)
		return t
	}
	t.waitFor = append(t.waitFor, g.sinceExclusive...)
	if g.lastExclusive != nil {
		t.waitFor = append(t.waitFor, g.lastExclusive)
	}
	g.lastExclusive = t.done
	g.sinceExclusive = nil
	return t
}

// wait blocks until the call may start. It returns ctx's error when the run
// was stopped first, in which case the call must not run.
func (t *toolCallTicket) wait(ctx context.Context) error {
	for _, ch := range t.waitFor {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if t.slots != nil {
		select {
		case t.slots <- struct{}{}:
			t.holding = true
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// finish releases whatever the call held and lets its successors start.
func (t *toolCallTicket) finish() {
	if t.holding {
		<-t.slots
		t.holding = false
	}
	close(t.done)
}
