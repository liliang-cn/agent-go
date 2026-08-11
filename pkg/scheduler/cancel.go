package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Cancelling one execution.
//
// Every execution used to be handed s.ctx — the scheduler's own root context —
// so the only cancel in the package was Stop(), which tears down every timer
// and closes the store. "Stop this one run" had no expression at all: a
// scheduled prompt that turned out to be a fifteen-minute mistake ran to its
// timeout while the user watched.
//
// So each execution now gets a context of its own, derived from the root and
// registered under a run ID. Stop() still cancels everything through the root;
// CancelRun / CancelTaskRuns cancel exactly one thing and leave the timers
// alone. The task itself is untouched either way — a cancelled run is one
// execution ending early, not a schedule being deleted or disabled.

// RunningTask is one execution currently in flight.
type RunningTask struct {
	// RunID identifies this execution for CancelRun. Also returned by
	// RunTaskAsync.
	RunID string `json:"run_id"`
	// TaskID is the schedule this execution belongs to.
	TaskID string `json:"task_id"`
	// StartedAt is when the executor was invoked.
	StartedAt time.Time `json:"started_at"`
	// Manual reports a run started by RunTask/RunTaskAsync rather than by
	// the cron loop.
	Manual bool `json:"manual"`
}

// taskRun is one in-flight execution plus the means to stop it.
type taskRun struct {
	RunningTask
	cancel context.CancelFunc

	mu sync.Mutex
	// cancelled records that CancelRun pulled the plug, rather than the
	// context expiring for some other reason. Inferring it from ctx.Err()
	// afterwards cannot distinguish a user's stop from a Stop() shutdown or
	// an executor's own timeout, and those are three different outcomes.
	cancelled bool
}

func (r *taskRun) markCancelled() {
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
}

func (r *taskRun) wasCancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled
}

// beginRun derives a cancellable context for one execution and registers it.
// The returned release func must be called when the execution finishes.
func (s *TaskScheduler) beginRun(taskID string, manual bool) (context.Context, *taskRun, func()) {
	s.mu.RLock()
	parent := s.ctx
	s.mu.RUnlock()
	if parent == nil {
		parent = context.Background()
	}

	runCtx, cancel := context.WithCancel(parent)
	run := &taskRun{
		RunningTask: RunningTask{
			RunID:     uuid.New().String(),
			TaskID:    taskID,
			StartedAt: time.Now(),
			Manual:    manual,
		},
		cancel: cancel,
	}

	s.runsMu.Lock()
	if s.activeRuns == nil {
		s.activeRuns = make(map[string]*taskRun)
	}
	s.activeRuns[run.RunID] = run
	s.runsMu.Unlock()

	var once sync.Once
	return runCtx, run, func() {
		once.Do(func() {
			s.runsMu.Lock()
			delete(s.activeRuns, run.RunID)
			s.runsMu.Unlock()
			cancel()
		})
	}
}

// CancelRun stops one execution by its run ID. Returns false when no such
// execution is in flight — the caller needs to be able to tell "stopped it"
// from "there was nothing to stop", because a stop that lands after the run
// finished is not an error.
func (s *TaskScheduler) CancelRun(runID string) bool {
	if s == nil || runID == "" {
		return false
	}
	s.runsMu.Lock()
	run := s.activeRuns[runID]
	if run != nil {
		run.markCancelled()
	}
	s.runsMu.Unlock()

	if run == nil {
		return false
	}
	// Outside the lock: cancel runs the context's own callbacks, and a
	// second caller should not queue behind them.
	run.cancel()
	return true
}

// CancelTaskRuns stops every execution of one task and returns how many were
// stopped. Zero means the task was not running. The schedule itself keeps its
// timer and can be run again — cancelling a run is not disabling a task.
func (s *TaskScheduler) CancelTaskRuns(taskID string) int {
	if s == nil || taskID == "" {
		return 0
	}
	s.runsMu.Lock()
	pending := make([]context.CancelFunc, 0, 2)
	for _, run := range s.activeRuns {
		if run.TaskID != taskID {
			continue
		}
		run.markCancelled()
		pending = append(pending, run.cancel)
	}
	s.runsMu.Unlock()

	for _, cancel := range pending {
		cancel()
	}
	return len(pending)
}

// RunningTasks lists the executions currently in flight.
func (s *TaskScheduler) RunningTasks() []RunningTask {
	if s == nil {
		return nil
	}
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	out := make([]RunningTask, 0, len(s.activeRuns))
	for _, run := range s.activeRuns {
		out = append(out, run.RunningTask)
	}
	return out
}

// IsTaskRunning reports whether any execution of a task is in flight. A UI
// needs it to decide whether to offer "run now" or "cancel".
func (s *TaskScheduler) IsTaskRunning(taskID string) bool {
	if s == nil || taskID == "" {
		return false
	}
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	for _, run := range s.activeRuns {
		if run.TaskID == taskID {
			return true
		}
	}
	return false
}
