package scheduler

import (
	"context"
	"time"
)

// TaskType represents the type of task
type TaskType string

const (
	TaskTypeQuery  TaskType = "query"
	TaskTypeIngest TaskType = "ingest"
	TaskTypeMCP    TaskType = "mcp"
	TaskTypeScript TaskType = "script"
)

// TaskStatus represents the status of a task execution
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

// Task represents a scheduled task
type Task struct {
	ID          string            `json:"id" db:"id"`
	Type        string            `json:"type" db:"type"`
	Schedule    string            `json:"schedule" db:"schedule"`
	Parameters  map[string]string `json:"parameters" db:"parameters"`
	Description string            `json:"description" db:"description"`
	Priority    int               `json:"priority" db:"priority"`
	Enabled     bool              `json:"enabled" db:"enabled"`
	CreatedAt   time.Time         `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at" db:"updated_at"`
	NextRun     *time.Time        `json:"next_run,omitempty" db:"next_run"`
	LastRun     *time.Time        `json:"last_run,omitempty" db:"last_run"`
}

// TaskExecution represents a single execution of a task
type TaskExecution struct {
	ID        int64         `json:"id" db:"id"`
	TaskID    string        `json:"task_id" db:"task_id"`
	StartTime time.Time     `json:"start_time" db:"start_time"`
	EndTime   *time.Time    `json:"end_time,omitempty" db:"end_time"`
	Duration  time.Duration `json:"duration" db:"duration"`
	Status    TaskStatus    `json:"status" db:"status"`
	Output    string        `json:"output,omitempty" db:"output"`
	Error     string        `json:"error,omitempty" db:"error"`
}

// TaskResult represents the result of a task execution
type TaskResult struct {
	Success  bool          `json:"success"`
	Output   string        `json:"output,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

// Executor interface for task executors
type Executor interface {
	// Execute runs the task with given parameters
	Execute(ctx context.Context, parameters map[string]string) (*TaskResult, error)

	// Validate checks if the parameters are valid for this executor
	Validate(parameters map[string]string) error

	// Type returns the task type this executor handles
	Type() TaskType
}

// Scheduler interface defines the main scheduler operations
type Scheduler interface {
	// Start the scheduler
	Start() error

	// Stop the scheduler
	Stop() error

	// CreateTask creates a new task
	CreateTask(task *Task) (string, error)

	// GetTask retrieves a task by ID
	GetTask(id string) (*Task, error)

	// ListTasks lists all tasks (optionally including disabled ones)
	ListTasks(includeDisabled bool) ([]*Task, error)

	// UpdateTask updates an existing task
	UpdateTask(task *Task) error

	// DeleteTask deletes a task
	DeleteTask(id string) error

	// EnableTask enables or disables a task
	EnableTask(id string, enabled bool) error

	// RunTask runs a task immediately
	RunTask(id string) (*TaskResult, error)

	// GetTaskExecutions retrieves execution history for a task
	GetTaskExecutions(taskID string, limit int) ([]*TaskExecution, error)
}

// SchedulerConfig holds configuration for the scheduler
type SchedulerConfig struct {
	// DatabasePath is the path to the SQLite database
	DatabasePath string `mapstructure:"database_path"`
	// CanonicalDatabasePath mirrors scheduler tasks into AgentGo's canonical tasks table.
	CanonicalDatabasePath string `mapstructure:"canonical_database_path"`

	// MaxConcurrentTasks is the maximum number of tasks that can run concurrently
	MaxConcurrentTasks int `mapstructure:"max_concurrent_tasks"`

	// RetryAttempts is the number of retry attempts for failed tasks
	RetryAttempts int `mapstructure:"retry_attempts"`

	// RetryDelay is the delay between retry attempts
	RetryDelay time.Duration `mapstructure:"retry_delay"`

	// CleanupInterval is how often to clean up old execution records
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`

	// MaxExecutionHistory is the maximum number of execution records to keep per task
	MaxExecutionHistory int `mapstructure:"max_execution_history"`
}

// DefaultSchedulerConfig returns default configuration
func DefaultSchedulerConfig() *SchedulerConfig {
	return &SchedulerConfig{
		DatabasePath:        "./data/scheduler.db",
		MaxConcurrentTasks:  5,
		RetryAttempts:       3,
		RetryDelay:          time.Minute * 5,
		CleanupInterval:     time.Hour * 24,
		MaxExecutionHistory: 100,
	}
}
