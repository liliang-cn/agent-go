package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestScheduler(t *testing.T) *TaskScheduler {
	t.Helper()
	cfg := &config.Config{Home: t.TempDir()}
	cfg.ApplyHomeLayout()
	s := NewScheduler(cfg)
	require.NoError(t, s.Start())
	t.Cleanup(func() { _ = s.Stop() })
	return s
}

// blockingExecutor stays inside Execute until its context is cancelled, which
// is the only honest stand-in for an agent turn: the thing a user wants to
// cancel is one that has not come back yet.
type blockingExecutor struct {
	taskType TaskType
	entered  chan struct{}
	once     sync.Once
}

func (b *blockingExecutor) Type() TaskType                   { return b.taskType }
func (b *blockingExecutor) Validate(map[string]string) error { return nil }
func (b *blockingExecutor) Execute(ctx context.Context, _ map[string]string) (*TaskResult, error) {
	b.once.Do(func() { close(b.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// The whole point: a run that was stopped must be recorded as cancelled, not
// as failed. A scheduled prompt the user aborted is not a broken schedule, and
// a history list that cannot tell the two apart teaches people to ignore it.
func TestCancelTaskRuns_RecordsCancelledNotFailed(t *testing.T) {
	s := newTestScheduler(t)
	exec := &blockingExecutor{taskType: TaskType("blocking"), entered: make(chan struct{})}
	s.RegisterExecutor(exec)

	id, err := s.CreateTask(&Task{Type: string(exec.taskType), Schedule: "@daily", Enabled: true})
	require.NoError(t, err)

	runID, err := s.RunTaskAsync(id)
	require.NoError(t, err)
	require.NotEmpty(t, runID)

	select {
	case <-exec.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("executor never started")
	}

	assert.True(t, s.IsTaskRunning(id), "the task should report as running while the executor is inside Execute")
	require.Equal(t, 1, s.CancelTaskRuns(id), "expected exactly one execution to be cancelled")

	// The run must actually end, and it must end soon: a cancel that only
	// flips a flag is the fake button this exists to avoid.
	deadline := time.After(5 * time.Second)
	for s.IsTaskRunning(id) {
		select {
		case <-deadline:
			t.Fatal("execution did not stop after cancel")
		case <-time.After(10 * time.Millisecond):
		}
	}

	var execution *TaskExecution
	for i := 0; i < 200; i++ {
		list, err := s.GetTaskExecutions(id, 5)
		require.NoError(t, err)
		if len(list) > 0 && list[0].Status != TaskStatusRunning {
			execution = list[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotNil(t, execution, "no finished execution was recorded")
	assert.Equal(t, TaskStatusCancelled, execution.Status)
	assert.Empty(t, execution.Error, "a cancelled run must not carry an error message")

	// The schedule itself survives — cancelling a run is not deleting a task.
	task, err := s.GetTask(id)
	require.NoError(t, err)
	assert.True(t, task.Enabled)
}

func TestCancelRun_ByRunID(t *testing.T) {
	s := newTestScheduler(t)
	exec := &blockingExecutor{taskType: TaskType("blocking-run-id"), entered: make(chan struct{})}
	s.RegisterExecutor(exec)

	id, err := s.CreateTask(&Task{Type: string(exec.taskType), Enabled: true})
	require.NoError(t, err)

	runID, err := s.RunTaskAsync(id)
	require.NoError(t, err)
	<-exec.entered

	running := s.RunningTasks()
	require.Len(t, running, 1)
	assert.Equal(t, runID, running[0].RunID)
	assert.Equal(t, id, running[0].TaskID)
	assert.True(t, running[0].Manual)

	assert.True(t, s.CancelRun(runID))

	deadline := time.After(5 * time.Second)
	for len(s.RunningTasks()) > 0 {
		select {
		case <-deadline:
			t.Fatal("execution did not stop after CancelRun")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// An id that is over must say so rather than answering "ok" — the caller
	// needs to tell "stopped it" from "there was nothing to stop".
	assert.False(t, s.CancelRun(runID))
	assert.False(t, s.CancelRun("no-such-run"))
	assert.Equal(t, 0, s.CancelTaskRuns(id))
}

// A cancel racing against the run finishing on its own must not deadlock,
// double-close, or leave a ghost in the registry. Run with -race.
func TestCancelRuns_Concurrent(t *testing.T) {
	s := newTestScheduler(t)
	exec := &MockExecutor{
		taskType: TaskType("racy"),
		executeFn: func(ctx context.Context, _ map[string]string) (*TaskResult, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(20 * time.Millisecond):
				return &TaskResult{Success: true}, nil
			}
		},
	}
	s.RegisterExecutor(exec)

	ids := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		id, err := s.CreateTask(&Task{Type: string(exec.taskType), Enabled: true})
		require.NoError(t, err)
		ids = append(ids, id)
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := s.RunTaskAsync(id); err != nil {
					return
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				s.CancelTaskRuns(id)
				_ = s.IsTaskRunning(id)
				_ = s.RunningTasks()
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()

	for _, id := range ids {
		s.CancelTaskRuns(id)
	}
	deadline := time.After(10 * time.Second)
	for len(s.RunningTasks()) > 0 {
		select {
		case <-deadline:
			t.Fatal("runs never drained")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRunTaskAsync_UnknownExecutorIsReportedToCaller(t *testing.T) {
	s := newTestScheduler(t)
	s.RegisterExecutor(&MockExecutor{taskType: TaskType("known")})

	// A stored task whose type has no executor — the shape you get when a
	// task outlives the code that used to run it. RunTaskAsync must report it
	// to the caller instead of swallowing it in a goroutine nobody watches.
	require.NoError(t, s.storage.CreateTask(&Task{ID: "orphan", Type: "gone", Enabled: true}))

	_, err := s.RunTaskAsync("orphan")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no executor")
}
