package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Work that outlives the turn that started it.
//
// Everything else here is synchronous by construction: a turn calls a tool,
// the tool returns, the loop goes round. That is right for almost every tool
// and wrong for the ones a person would never stand and wait for — a crawl,
// a build, a long piece of research, a report over a week of logs. Making
// the model wait for those does two bad things at once: the conversation
// stops, and the round budget burns on a tool that is merely slow.
//
// What existed before this was half of the answer. A HOST could start work
// in the background (Manager.SubmitAgentTask, PromptScheduler.Schedule). The
// AGENT could not: there was no way for a model to say "start this, I will
// come back to it", so every framework consumer that wanted it wrote its own
// tool — superai's schedule_prompt is exactly that, and it is a scheduler,
// not a background task.
//
// The rules this follows, so it stays inside the seven concepts:
//
//   - **It is a tool.** Sub-agents are tools, skills are tools, MCP servers
//     are tools; background work is `background_start` and friends. Nothing
//     new appears in the loop.
//   - **It is the same loop.** A background task is another run on the same
//     Service, with its own session and its own run id. There is no second
//     engine, and every observer, lint, hook and extension applies to it
//     exactly as to any other run.
//   - **It does not inherit the caller's context.** This is the one thing
//     that makes it different from a sub-agent, and the whole point: a
//     sub-agent runs under its parent's context and dies with it. A
//     background task must survive the turn that started it, so it derives
//     from Background() and is cancellable only through the registry — or by
//     Close, which does cancel it.

// BackgroundStatus is where a background task has got to.
type BackgroundStatus string

const (
	// BackgroundRunning means it is still going.
	BackgroundRunning BackgroundStatus = "running"
	// BackgroundCompleted means it finished and produced an answer.
	BackgroundCompleted BackgroundStatus = "completed"
	// BackgroundBlocked means it stopped on a concrete blocker.
	BackgroundBlocked BackgroundStatus = "blocked"
	// BackgroundCancelled means somebody stopped it.
	BackgroundCancelled BackgroundStatus = "cancelled"
	// BackgroundFailed means it ended with an error.
	BackgroundFailed BackgroundStatus = "failed"
)

// Done reports whether this status is terminal.
func (s BackgroundStatus) Done() bool { return s != BackgroundRunning }

// BackgroundTask is one piece of detached work.
type BackgroundTask struct {
	// ID is what background_check and CancelBackgroundTask take.
	ID string `json:"id"`
	// Goal is what it was asked to do.
	Goal string `json:"goal"`
	// Label is an optional short name the caller gave it, so a model
	// checking several can tell them apart without re-reading the goals.
	Label string `json:"label,omitempty"`

	Status BackgroundStatus `json:"status"`
	// StopReason is the run's own stop reason, once it has one.
	StopReason StopReason `json:"stop_reason,omitempty"`
	// Result is the final text. Empty until the task finishes.
	Result string `json:"result,omitempty"`
	// Err is why it failed, when it did.
	Err string `json:"error,omitempty"`

	// SessionID and RunID name the run, so its events, trace lines and
	// checkpoints can be found the same way any other run's can.
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id,omitempty"`
	// ParentSessionID is the conversation that started it, so a host can
	// show a person the work their own chat kicked off.
	ParentSessionID string `json:"parent_session_id,omitempty"`
	// Tenant is inherited from the run that started it: work started on
	// somebody's behalf stays theirs, for limits and for cancellation.
	Tenant string `json:"tenant,omitempty"`

	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`

	cancel context.CancelFunc
}

// Duration is how long the task ran, or has been running.
func (t BackgroundTask) Duration() time.Duration {
	if t.EndedAt != nil {
		return t.EndedAt.Sub(t.StartedAt)
	}
	return time.Since(t.StartedAt)
}

// ErrTooManyBackgroundTasks is returned when the ceiling is reached.
var ErrTooManyBackgroundTasks = errors.New("too many background tasks in flight")

// backgroundRegistry holds a service's detached work.
type backgroundRegistry struct {
	mu    sync.RWMutex
	tasks map[string]*BackgroundTask
	order []string
	max   int
	// keep bounds how many finished tasks are remembered. A result nobody
	// collected still has to be collectable, but a process that runs for
	// weeks must not accumulate every one it ever ran.
	keep int
	wg   sync.WaitGroup
}

const (
	// defaultMaxBackgroundTasks bounds how many run at once. Each one is a
	// full run with its own history, so this is a memory ceiling as much as a
	// concurrency one.
	defaultMaxBackgroundTasks = 8
	// defaultKeepFinishedTasks bounds how many finished tasks are kept for
	// collection.
	defaultKeepFinishedTasks = 64
)

func (s *Service) background() *backgroundRegistry {
	s.backgroundOnce.Do(func() {
		max := s.maxBackgroundTasks
		if max <= 0 {
			max = defaultMaxBackgroundTasks
		}
		s.backgroundReg = &backgroundRegistry{
			tasks: map[string]*BackgroundTask{},
			max:   max,
			keep:  defaultKeepFinishedTasks,
		}
	})
	return s.backgroundReg
}

// StartBackgroundTask runs a goal detached from the caller, and returns its
// id immediately.
//
// The task does not inherit ctx: it is meant to outlive the turn that
// started it, and a task cancelled because a chat message finished would be
// no use to anyone. ctx is still read for the tenant and the parent session,
// which the task inherits.
//
// Stop one with CancelBackgroundTask; Close stops them all.
func (s *Service) StartBackgroundTask(ctx context.Context, goal string, opts ...RunOption) (*BackgroundTask, error) {
	if s == nil {
		return nil, ErrServiceClosed
	}
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return nil, fmt.Errorf("background task needs a goal")
	}
	if s.Closed() {
		return nil, ErrServiceClosed
	}

	reg := s.background()
	reg.mu.Lock()
	running := 0
	for _, t := range reg.tasks {
		if t.Status == BackgroundRunning {
			running++
		}
	}
	if running >= reg.max {
		reg.mu.Unlock()
		return nil, fmt.Errorf("%w: %d of %d", ErrTooManyBackgroundTasks, running, reg.max)
	}
	reg.mu.Unlock()

	cfg := DefaultRunConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	// Its own session: a background task is not part of the conversation
	// that started it, and appending its turns to that history would put
	// work the user never sees into the next thing they say.
	if strings.TrimSpace(cfg.SessionID) == "" {
		cfg.SessionID = uuid.NewString()
	}
	// Whose work this is travels with it.
	if strings.TrimSpace(cfg.Tenant) == "" {
		cfg.Tenant = currentRunTenant(ctx)
	}

	task := &BackgroundTask{
		ID:              uuid.NewString(),
		Goal:            goal,
		Label:           strings.TrimSpace(cfg.BackgroundLabel),
		Status:          BackgroundRunning,
		SessionID:       cfg.SessionID,
		ParentSessionID: currentRunSessionID(ctx),
		Tenant:          cfg.Tenant,
		StartedAt:       time.Now(),
	}

	// Background(), deliberately, not ctx. See the note at the top of this
	// file: inheriting the caller's context is what a sub-agent does, and it
	// is exactly what a background task must not do.
	runCtx, cancel := context.WithCancel(context.Background())
	task.cancel = cancel

	reg.mu.Lock()
	reg.tasks[task.ID] = task
	reg.order = append(reg.order, task.ID)
	reg.mu.Unlock()

	reg.wg.Add(1)
	go func() {
		defer reg.wg.Done()
		defer cancel()
		result, err := s.Run(runCtx, goal, func(c *RunConfig) { *c = *cfg })
		reg.finish(task.ID, result, err)
	}()

	snapshot := *task
	snapshot.cancel = nil
	return &snapshot, nil
}

// finish records how a task ended and prunes old ones.
func (r *backgroundRegistry) finish(id string, result *ExecutionResult, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	task, ok := r.tasks[id]
	if !ok {
		return
	}
	now := time.Now()
	task.EndedAt = &now
	switch {
	case err != nil:
		task.Status = BackgroundFailed
		task.Err = err.Error()
	case result == nil:
		task.Status = BackgroundFailed
		task.Err = "the run produced no result"
	case result.Cancelled:
		task.Status = BackgroundCancelled
		task.StopReason = result.StopReason
		task.Result = result.Text()
	case result.Blocked:
		task.Status = BackgroundBlocked
		task.StopReason = result.StopReason
		task.Result = result.Text()
	default:
		task.Status = BackgroundCompleted
		task.StopReason = result.StopReason
		task.Result = result.Text()
	}
	r.pruneLocked()
}

// pruneLocked forgets the oldest finished tasks past the keep limit. A
// running task is never pruned: a handle that stops answering while the work
// is still going is worse than none.
func (r *backgroundRegistry) pruneLocked() {
	finished := 0
	for _, id := range r.order {
		if t, ok := r.tasks[id]; ok && t.Status.Done() {
			finished++
		}
	}
	if finished <= r.keep {
		return
	}
	drop := finished - r.keep
	kept := make([]string, 0, len(r.order))
	for _, id := range r.order {
		t, ok := r.tasks[id]
		if ok && t.Status.Done() && drop > 0 {
			delete(r.tasks, id)
			drop--
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}

// BackgroundTask returns one task by id.
func (s *Service) BackgroundTask(id string) (*BackgroundTask, bool) {
	if s == nil {
		return nil, false
	}
	reg := s.background()
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	t, ok := reg.tasks[strings.TrimSpace(id)]
	if !ok {
		return nil, false
	}
	snapshot := *t
	snapshot.cancel = nil
	return &snapshot, true
}

// BackgroundTasks lists every task this service remembers, newest first.
func (s *Service) BackgroundTasks() []*BackgroundTask {
	if s == nil {
		return nil
	}
	reg := s.background()
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]*BackgroundTask, 0, len(reg.tasks))
	for _, t := range reg.tasks {
		snapshot := *t
		snapshot.cancel = nil
		out = append(out, &snapshot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// CancelBackgroundTask stops one task and reports whether it stopped
// anything. A task that already finished is not an error.
func (s *Service) CancelBackgroundTask(id string) bool {
	if s == nil {
		return false
	}
	reg := s.background()
	reg.mu.Lock()
	t, ok := reg.tasks[strings.TrimSpace(id)]
	if !ok || t.Status.Done() || t.cancel == nil {
		reg.mu.Unlock()
		return false
	}
	cancel := t.cancel
	reg.mu.Unlock()
	cancel()
	return true
}

// stopBackgroundTasks cancels everything in flight and waits for it, bounded.
// Called by Close: a background task holds the store this service is about to
// release, and one still running through a closed store is the bug
// service_close.go exists to prevent.
func (s *Service) stopBackgroundTasks(timeout time.Duration) {
	if s == nil || s.backgroundReg == nil {
		return
	}
	reg := s.backgroundReg
	reg.mu.Lock()
	for _, t := range reg.tasks {
		if !t.Status.Done() && t.cancel != nil {
			t.cancel()
		}
	}
	reg.mu.Unlock()

	done := make(chan struct{})
	go func() { reg.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}
