package scheduler

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	storepkg "github.com/liliang-cn/agent-go/v2/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageInitialization(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"

	storage, err := NewStorage(dbPath)
	require.NoError(t, err)
	require.NotNil(t, storage)
	defer storage.Close()

	// Verify tables were created by trying to query them
	var taskCount int
	err = storage.db.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&taskCount)
	require.NoError(t, err)

	var execCount int
	err = storage.db.QueryRow("SELECT COUNT(*) FROM task_executions").Scan(&execCount)
	require.NoError(t, err)
}

func TestStorageTaskOperations(t *testing.T) {
	tempDir := t.TempDir()
	storage, err := NewStorage(tempDir + "/test.db")
	require.NoError(t, err)
	defer storage.Close()

	// Create task
	task := &Task{
		ID:          "test-task-1",
		Type:        string(TaskTypeQuery),
		Schedule:    "0 * * * *",
		Parameters:  map[string]string{"query": "test"},
		Description: "Test task",
		Priority:    1,
		Enabled:     true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	err = storage.CreateTask(task)
	require.NoError(t, err)

	// Get task
	retrieved, err := storage.GetTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, task.ID, retrieved.ID)
	assert.Equal(t, task.Type, retrieved.Type)
	assert.Equal(t, task.Description, retrieved.Description)

	// Update task
	task.Description = "Updated task"
	err = storage.UpdateTask(task)
	require.NoError(t, err)

	updated, err := storage.GetTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated task", updated.Description)

	// List tasks
	tasks, err := storage.ListTasks(false)
	require.NoError(t, err)
	assert.Len(t, tasks, 1)

	// Disable task
	err = storage.EnableTask(task.ID, false)
	require.NoError(t, err)

	disabled, err := storage.GetTask(task.ID)
	require.NoError(t, err)
	assert.False(t, disabled.Enabled)

	// List should not include disabled tasks
	tasks, err = storage.ListTasks(false)
	require.NoError(t, err)
	assert.Empty(t, tasks)

	// List with disabled should include them
	tasks, err = storage.ListTasks(true)
	require.NoError(t, err)
	assert.Len(t, tasks, 1)

	// Delete task
	err = storage.DeleteTask(task.ID)
	require.NoError(t, err)

	_, err = storage.GetTask(task.ID)
	assert.Error(t, err)
}

func TestStorageMirrorsCanonicalTasks(t *testing.T) {
	tempDir := t.TempDir()
	canonical, err := storepkg.NewAgentGoDB(filepath.Join(tempDir, "agentgo.db"))
	require.NoError(t, err)
	defer canonical.Close()

	storage, err := NewStorageWithCanonical(filepath.Join(tempDir, "scheduler.db"), canonical)
	require.NoError(t, err)
	defer storage.Close()

	task := &Task{
		ID:          "scheduler-task-1",
		Type:        string(TaskTypeQuery),
		Schedule:    "0 * * * *",
		Parameters:  map[string]string{"query": "test"},
		Description: "Canonical scheduler mirror",
		Priority:    1,
		Enabled:     true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	require.NoError(t, storage.CreateTask(task))

	canonicalTask, err := canonical.GetTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "scheduler", string(canonicalTask.Kind))
	assert.Equal(t, "queued", string(canonicalTask.Status))
	assert.Equal(t, task.Description, canonicalTask.Input)

	exec := &TaskExecution{TaskID: task.ID, StartTime: time.Now(), Status: TaskStatusRunning}
	require.NoError(t, storage.CreateExecution(exec))
	canonicalTask, err = canonical.GetTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", string(canonicalTask.Status))
}

func TestStorageExecutionOperations(t *testing.T) {
	tempDir := t.TempDir()
	storage, err := NewStorage(tempDir + "/test.db")
	require.NoError(t, err)
	defer storage.Close()

	// Create task first
	task := &Task{
		ID:        "test-task-1",
		Type:      string(TaskTypeQuery),
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	err = storage.CreateTask(task)
	require.NoError(t, err)

	// Create execution
	execution := &TaskExecution{
		TaskID:    task.ID,
		StartTime: time.Now(),
		Status:    TaskStatusRunning,
	}
	err = storage.CreateExecution(execution)
	require.NoError(t, err)
	assert.NotZero(t, execution.ID)

	// Update execution
	endTime := time.Now()
	execution.EndTime = &endTime
	execution.Duration = time.Second * 5
	execution.Status = TaskStatusCompleted
	execution.Output = "Task completed"
	err = storage.UpdateExecution(execution)
	require.NoError(t, err)

	// Get task executions
	executions, err := storage.GetTaskExecutions(task.ID, 10)
	require.NoError(t, err)
	assert.Len(t, executions, 1)
	assert.Equal(t, TaskStatusCompleted, executions[0].Status)
}

func TestStorageTaskScheduling(t *testing.T) {
	tempDir := t.TempDir()
	storage, err := NewStorage(tempDir + "/test.db")
	require.NoError(t, err)
	defer storage.Close()

	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	// Create tasks with different schedules
	tasks := []*Task{
		{
			ID:        "due-task",
			Type:      string(TaskTypeQuery),
			Enabled:   true,
			NextRun:   &past,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "future-task",
			Type:      string(TaskTypeQuery),
			Enabled:   true,
			NextRun:   &future,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "disabled-task",
			Type:      string(TaskTypeQuery),
			Enabled:   false,
			NextRun:   &past,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	for _, task := range tasks {
		err = storage.CreateTask(task)
		require.NoError(t, err)
	}

	// Get tasks due for execution
	dueTasks, err := storage.GetTasksDueForExecution()
	require.NoError(t, err)
	assert.Len(t, dueTasks, 1)
	assert.Equal(t, "due-task", dueTasks[0].ID)

	// Update next run
	newNextRun := now.Add(2 * time.Hour)
	err = storage.UpdateTaskNextRun("due-task", &newNextRun)
	require.NoError(t, err)

	updated, err := storage.GetTask("due-task")
	require.NoError(t, err)
	assert.NotNil(t, updated.NextRun)
	assert.Equal(t, newNextRun.Unix(), updated.NextRun.Unix())

	// Update last run
	err = storage.UpdateTaskLastRun("due-task", now)
	require.NoError(t, err)

	updated, err = storage.GetTask("due-task")
	require.NoError(t, err)
	assert.NotNil(t, updated.LastRun)
}

func TestStorageCleanup(t *testing.T) {
	tempDir := t.TempDir()
	storage, err := NewStorage(tempDir + "/test.db")
	require.NoError(t, err)
	defer storage.Close()

	// Create task
	task := &Task{
		ID:        "test-task",
		Type:      string(TaskTypeQuery),
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	err = storage.CreateTask(task)
	require.NoError(t, err)

	// Create multiple executions
	now := time.Now()
	for i := 0; i < 10; i++ {
		startTime := now.Add(-time.Hour * time.Duration(i*24))
		endTime := startTime.Add(time.Minute)
		execution := &TaskExecution{
			TaskID:    task.ID,
			StartTime: startTime,
			EndTime:   &endTime,
			Duration:  time.Minute,
			Status:    TaskStatusCompleted,
		}
		err = storage.CreateExecution(execution)
		require.NoError(t, err)
	}

	// Verify all executions exist
	executions, err := storage.GetTaskExecutions(task.ID, 100)
	require.NoError(t, err)
	assert.Len(t, executions, 10)

	// Cleanup old executions (keep only 5 days)
	err = storage.CleanupOldExecutions(5*24*time.Hour, 5)
	require.NoError(t, err)

	// Verify cleanup worked
	executions, err = storage.GetTaskExecutions(task.ID, 100)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(executions), 5)
}

func TestStorageParameterSerialization(t *testing.T) {
	tempDir := t.TempDir()
	storage, err := NewStorage(tempDir + "/test.db")
	require.NoError(t, err)
	defer storage.Close()

	// Create task with complex parameters
	params := map[string]string{
		"query":        "test query",
		"top-k":        "10",
		"show-sources": "true",
		"special":      `{"nested": "json"}`,
	}

	task := &Task{
		ID:         "param-test",
		Type:       string(TaskTypeQuery),
		Parameters: params,
		Enabled:    true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	err = storage.CreateTask(task)
	require.NoError(t, err)

	// Retrieve and verify parameters
	retrieved, err := storage.GetTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, params, retrieved.Parameters)
}

func TestStorageConcurrency(t *testing.T) {
	tempDir := t.TempDir()
	storage, err := NewStorage(tempDir + "/test.db")
	require.NoError(t, err)
	defer storage.Close()

	// Create multiple tasks with some concurrency
	// Use smaller batches to avoid too many lock conflicts
	const numTasks = 5
	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < numTasks; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Retry logic for SQLite busy errors
			var err error
			for retry := 0; retry < 3; retry++ {
				task := &Task{
					ID:        fmt.Sprintf("task-%d", id),
					Type:      string(TaskTypeQuery),
					Enabled:   true,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				err = storage.CreateTask(task)
				if err == nil {
					mu.Lock()
					successCount++
					mu.Unlock()
					break
				}
				// Wait a bit before retry if database is locked
				if retry < 2 {
					time.Sleep(time.Millisecond * 10)
				}
			}
		}(i)
	}

	// Wait for all goroutines
	wg.Wait()

	// Verify at least some tasks were created
	tasks, err := storage.ListTasks(true)
	require.NoError(t, err)
	assert.Greater(t, len(tasks), 0, "At least some tasks should be created")
	assert.Equal(t, successCount, len(tasks), "Created tasks should match success count")
}
