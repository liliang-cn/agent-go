package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingExecutor counts its runs and, while hold is open, stays inside
// Execute the way a prompt handed to another machine does.
type countingExecutor struct {
	taskType TaskType
	runs     atomic.Int32
	hold     chan struct{}
}

func (c *countingExecutor) Type() TaskType                   { return c.taskType }
func (c *countingExecutor) Validate(map[string]string) error { return nil }
func (c *countingExecutor) Execute(ctx context.Context, _ map[string]string) (*TaskResult, error) {
	c.runs.Add(1)
	if c.hold != nil {
		select {
		case <-c.hold:
		case <-ctx.Done():
		}
	}
	return &TaskResult{Success: true}, nil
}

// dueTask creates a task on a daily schedule whose slot has come.
func dueTask(t *testing.T, s *TaskScheduler, typ TaskType) (string, time.Time) {
	t.Helper()
	id, err := s.CreateTask(&Task{Type: string(typ), Schedule: "0 20 * * *", Enabled: true})
	require.NoError(t, err)
	slot := time.Now().Add(-30 * time.Second).Truncate(time.Second)
	require.NoError(t, s.storage.UpdateTaskNextRun(id, &slot))
	return id, slot
}

func waitIdle(t *testing.T, s *TaskScheduler, id string) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.slotsMu.Lock()
		defer s.slotsMu.Unlock()
		return !s.firing[id] && !s.taskRunning(id)
	}, 5*time.Second, 10*time.Millisecond)
}

// The 2026-10-29 incident: the execution record failed to save and the slot
// was not moved, so every following tick fired the daily post again. With
// every scheduler write to the database failing, the next tick must still
// leave the slot alone.
func TestSlotFiresOnceWhenBookkeepingFails(t *testing.T) {
	s := newTestScheduler(t)
	exec := &countingExecutor{taskType: "slot-once"}
	s.RegisterExecutor(exec)
	id, slot := dueTask(t, s, exec.taskType)

	_, err := s.storage.db.Exec(`DROP TABLE task_executions`)
	require.NoError(t, err)
	_, err = s.storage.db.Exec(`CREATE TRIGGER no_task_updates BEFORE UPDATE ON tasks
		BEGIN SELECT RAISE(ABORT, 'forced write failure'); END`)
	require.NoError(t, err)

	s.checkAndExecuteDueTasks()
	waitIdle(t, s, id)
	require.EqualValues(t, 1, exec.runs.Load())

	// The write really did fail: the store still says the slot is due.
	task, err := s.storage.GetTask(id)
	require.NoError(t, err)
	require.NotNil(t, task.NextRun)
	require.True(t, task.NextRun.Equal(slot), "next_run moved; the test no longer covers a failed write")

	for i := 0; i < 3; i++ {
		s.checkAndExecuteDueTasks()
		waitIdle(t, s, id)
	}
	assert.EqualValues(t, 1, exec.runs.Load(), "the same slot fired again")
}

// With the store healthy, the slot is claimed before the run starts, so the
// task is not due on the next tick even while its run is still going.
func TestSlotIsClaimedBeforeTheRun(t *testing.T) {
	s := newTestScheduler(t)
	exec := &countingExecutor{taskType: "slot-claim", hold: make(chan struct{})}
	s.RegisterExecutor(exec)
	id, slot := dueTask(t, s, exec.taskType)

	s.checkAndExecuteDueTasks()
	require.Eventually(t, func() bool { return exec.runs.Load() == 1 }, 5*time.Second, 10*time.Millisecond)

	task, err := s.storage.GetTask(id)
	require.NoError(t, err)
	require.NotNil(t, task.NextRun)
	assert.True(t, task.NextRun.After(slot.Add(time.Hour)), "next_run still at the slot while the run goes on")

	due, err := s.storage.GetTasksDueForExecution()
	require.NoError(t, err)
	for _, d := range due {
		assert.NotEqual(t, id, d.ID)
	}

	close(exec.hold)
	waitIdle(t, s, id)
	assert.EqualValues(t, 1, exec.runs.Load())
}

// A task that is still running is not fired again, whether its run came from
// the cron loop or from RunTaskAsync.
func TestRunningTaskIsNotReentered(t *testing.T) {
	s := newTestScheduler(t)
	exec := &countingExecutor{taskType: "slot-reentry", hold: make(chan struct{})}
	s.RegisterExecutor(exec)

	// No schedule and no next_run: due on every tick, so only the running
	// guard stands between it and a second run.
	id, err := s.CreateTask(&Task{Type: string(exec.taskType), Enabled: true})
	require.NoError(t, err)

	s.checkAndExecuteDueTasks()
	require.Eventually(t, func() bool { return exec.runs.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	for i := 0; i < 3; i++ {
		s.checkAndExecuteDueTasks()
	}
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, exec.runs.Load(), "cron fired a task that was still running")

	close(exec.hold)
	waitIdle(t, s, id)

	// A manual run in flight blocks the cron firing too.
	exec.hold = make(chan struct{})
	_, err = s.RunTaskAsync(id)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return exec.runs.Load() == 2 }, 5*time.Second, 10*time.Millisecond)
	s.checkAndExecuteDueTasks()
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 2, exec.runs.Load(), "cron fired over a manual run")
	close(exec.hold)
	waitIdle(t, s, id)
}

func TestRetryBusy(t *testing.T) {
	saved := busyRetryDelays
	busyRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { busyRetryDelays = saved })

	calls := 0
	err := retryBusy(func() error {
		calls++
		if calls < 3 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 3, calls)

	calls = 0
	err = retryBusy(func() error { calls++; return errors.New("no such table") })
	assert.Error(t, err)
	assert.Equal(t, 1, calls, "a non-busy error is not retried")
}
