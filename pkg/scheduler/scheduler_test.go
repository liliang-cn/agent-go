package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockExecutor is a test executor
type MockExecutor struct {
	taskType   TaskType
	validateFn func(map[string]string) error
	executeFn  func(context.Context, map[string]string) (*TaskResult, error)
}

func (m *MockExecutor) Type() TaskType {
	return m.taskType
}

func (m *MockExecutor) Validate(params map[string]string) error {
	if m.validateFn != nil {
		return m.validateFn(params)
	}
	return nil
}

func (m *MockExecutor) Execute(ctx context.Context, params map[string]string) (*TaskResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, params)
	}
	return &TaskResult{
		Success: true,
		Output:  "mock execution",
	}, nil
}

func TestNewScheduler(t *testing.T) {
	cfg := &config.Config{
		Home: "/tmp",
	}
	cfg.ApplyHomeLayout()

	scheduler := NewScheduler(cfg)
	require.NotNil(t, scheduler)
	assert.NotNil(t, scheduler.config)
	assert.NotNil(t, scheduler.cronParser)
	assert.NotNil(t, scheduler.executors)
	assert.False(t, scheduler.running)
	assert.Equal(t, "/tmp/data/cortex.db", scheduler.config.DatabasePath)
}

func TestSchedulerStartStop(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{
		Home: tempDir,
	}
	cfg.ApplyHomeLayout()

	scheduler := NewScheduler(cfg)
	require.NotNil(t, scheduler)

	// Start scheduler
	err := scheduler.Start()
	require.NoError(t, err)
	assert.True(t, scheduler.running)

	// Try to start again - should fail
	err = scheduler.Start()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	// Stop scheduler
	err = scheduler.Stop()
	require.NoError(t, err)
	assert.False(t, scheduler.running)

	// Stop again - should be idempotent
	err = scheduler.Stop()
	assert.NoError(t, err)
}

func TestRegisterExecutor(t *testing.T) {
	scheduler := NewScheduler(nil)

	mockExecutor := &MockExecutor{
		taskType: TaskTypeQuery,
	}

	scheduler.RegisterExecutor(mockExecutor)

	scheduler.mu.RLock()
	executor, exists := scheduler.executors[TaskTypeQuery]
	scheduler.mu.RUnlock()

	assert.True(t, exists)
	assert.Equal(t, mockExecutor, executor)
}

func TestCreateTask(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{
		Home: tempDir,
	}
	cfg.ApplyHomeLayout()

	scheduler := NewScheduler(cfg)

	// Register mock executor
	mockExecutor := &MockExecutor{
		taskType: TaskTypeQuery,
		validateFn: func(params map[string]string) error {
			if _, ok := params["query"]; !ok {
				return fmt.Errorf("query parameter required")
			}
			return nil
		},
	}
	scheduler.RegisterExecutor(mockExecutor)

	// Start scheduler
	err := scheduler.Start()
	require.NoError(t, err)
	defer scheduler.Stop()

	tests := []struct {
		name    string
		task    *Task
		wantErr bool
		errMsg  string
	}{
		{
			name: "Valid task with schedule",
			task: &Task{
				Type:        string(TaskTypeQuery),
				Schedule:    "0 * * * *",
				Parameters:  map[string]string{"query": "test"},
				Description: "Test task",
				Enabled:     true,
			},
			wantErr: false,
		},
		{
			name: "Valid task without schedule",
			task: &Task{
				Type:       string(TaskTypeQuery),
				Parameters: map[string]string{"query": "test"},
			},
			wantErr: false,
		},
		{
			name: "Unknown task type",
			task: &Task{
				Type:       "unknown",
				Parameters: map[string]string{},
			},
			wantErr: true,
			errMsg:  "unknown task type",
		},
		{
			name: "Invalid parameters",
			task: &Task{
				Type:       string(TaskTypeQuery),
				Parameters: map[string]string{},
			},
			wantErr: true,
			errMsg:  "invalid task parameters",
		},
		{
			name: "Invalid schedule",
			task: &Task{
				Type:       string(TaskTypeQuery),
				Schedule:   "invalid",
				Parameters: map[string]string{"query": "test"},
			},
			wantErr: true,
			errMsg:  "invalid schedule",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := scheduler.CreateTask(tt.task)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, id)
			}
		})
	}
}

func TestExecuteTask(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{
		Home: tempDir,
	}
	cfg.ApplyHomeLayout()

	scheduler := NewScheduler(cfg)

	// Register mock executor
	executeCount := 0
	mockExecutor := &MockExecutor{
		taskType: TaskTypeQuery,
		executeFn: func(ctx context.Context, params map[string]string) (*TaskResult, error) {
			executeCount++
			if params["fail"] == "true" {
				return nil, fmt.Errorf("execution failed")
			}
			return &TaskResult{
				Success: true,
				Output:  fmt.Sprintf("Executed query: %s", params["query"]),
			}, nil
		},
	}
	scheduler.RegisterExecutor(mockExecutor)

	// Start scheduler
	err := scheduler.Start()
	require.NoError(t, err)
	defer scheduler.Stop()

	// Create and execute successful task
	task := &Task{
		Type:       string(TaskTypeQuery),
		Parameters: map[string]string{"query": "test query"},
	}
	id, err := scheduler.CreateTask(task)
	require.NoError(t, err)

	result, err := scheduler.RunTask(id)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "Executed query: test query", result.Output)
	assert.Equal(t, 1, executeCount)

	// Create and execute failing task
	failTask := &Task{
		Type:       string(TaskTypeQuery),
		Parameters: map[string]string{"query": "test", "fail": "true"},
	}
	failID, err := scheduler.CreateTask(failTask)
	require.NoError(t, err)

	result, err = scheduler.RunTask(failID)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "execution failed")
	assert.Equal(t, 2, executeCount)
}

func TestSchedulerNotRunning(t *testing.T) {
	scheduler := NewScheduler(nil)

	// All operations should fail when scheduler not running
	_, err := scheduler.CreateTask(&Task{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")

	_, err = scheduler.GetTask("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")

	_, err = scheduler.ListTasks(false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")

	err = scheduler.UpdateTask(&Task{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")

	err = scheduler.DeleteTask("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")

	err = scheduler.EnableTask("test", true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")

	_, err = scheduler.RunTask("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")

	_, err = scheduler.GetTaskExecutions("test", 10)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
}

func TestTaskLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{
		Home: tempDir,
	}
	cfg.ApplyHomeLayout()

	scheduler := NewScheduler(cfg)
	scheduler.RegisterExecutor(&MockExecutor{taskType: TaskTypeQuery})

	err := scheduler.Start()
	require.NoError(t, err)
	defer scheduler.Stop()

	// Create task
	task := &Task{
		Type:        string(TaskTypeQuery),
		Parameters:  map[string]string{"query": "test"},
		Description: "Test task",
		Enabled:     true,
	}
	id, err := scheduler.CreateTask(task)
	require.NoError(t, err)

	// Get task
	retrieved, err := scheduler.GetTask(id)
	require.NoError(t, err)
	assert.Equal(t, id, retrieved.ID)
	assert.Equal(t, "Test task", retrieved.Description)

	// Update task
	retrieved.Description = "Updated task"
	err = scheduler.UpdateTask(retrieved)
	require.NoError(t, err)

	updated, err := scheduler.GetTask(id)
	require.NoError(t, err)
	assert.Equal(t, "Updated task", updated.Description)

	// Disable task
	err = scheduler.EnableTask(id, false)
	require.NoError(t, err)

	disabled, err := scheduler.GetTask(id)
	require.NoError(t, err)
	assert.False(t, disabled.Enabled)

	// List tasks
	tasks, err := scheduler.ListTasks(false)
	require.NoError(t, err)
	assert.Empty(t, tasks) // Disabled task not shown

	tasks, err = scheduler.ListTasks(true)
	require.NoError(t, err)
	assert.Len(t, tasks, 1)

	// Delete task
	err = scheduler.DeleteTask(id)
	require.NoError(t, err)

	_, err = scheduler.GetTask(id)
	assert.Error(t, err)
}

func TestSchedulerLoop(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{
		Home: tempDir,
	}
	cfg.ApplyHomeLayout()

	scheduler := NewScheduler(cfg)

	// Track executions
	executions := make(chan string, 10)
	mockExecutor := &MockExecutor{
		taskType: TaskTypeQuery,
		executeFn: func(ctx context.Context, params map[string]string) (*TaskResult, error) {
			executions <- params["query"]
			return &TaskResult{Success: true}, nil
		},
	}
	scheduler.RegisterExecutor(mockExecutor)

	err := scheduler.Start()
	require.NoError(t, err)
	defer scheduler.Stop()

	// Create a task that should run immediately
	now := time.Now().Add(-time.Minute)
	task := &Task{
		Type:       string(TaskTypeQuery),
		Parameters: map[string]string{"query": "scheduled"},
		Enabled:    true,
		NextRun:    &now,
	}
	id, err := scheduler.CreateTask(task)
	require.NoError(t, err)

	// Manually trigger check for due tasks
	scheduler.checkAndExecuteDueTasks()

	// Wait for execution
	select {
	case query := <-executions:
		assert.Equal(t, "scheduled", query)
	case <-time.After(2 * time.Second):
		t.Fatal("Task was not executed")
	}

	// Wait a bit for the execution to be recorded in the database
	time.Sleep(100 * time.Millisecond)

	// Verify execution was recorded
	execHistory, err := scheduler.GetTaskExecutions(id, 10)
	require.NoError(t, err)
	assert.NotEmpty(t, execHistory)
}
