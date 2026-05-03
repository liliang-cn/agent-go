package scheduler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	storepkg "github.com/liliang-cn/agent-go/v2/pkg/store"
	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
	_ "modernc.org/sqlite"
)

// NullTime is a nullable time type for SQLite
type NullTime struct {
	Time  time.Time
	Valid bool
}

// Scan implements the Scanner interface
func (nt *NullTime) Scan(value interface{}) error {
	if value == nil {
		nt.Time, nt.Valid = time.Time{}, false
		return nil
	}
	nt.Valid = true
	switch v := value.(type) {
	case time.Time:
		nt.Time = v
		return nil
	case string:
		var err error
		nt.Time, err = time.Parse(time.RFC3339, v)
		return err
	default:
		return fmt.Errorf("cannot scan %T into NullTime", value)
	}
}

// Storage handles task persistence in SQLite
type Storage struct {
	db        *sql.DB
	canonical *storepkg.AgentGoDB
	mu        sync.Mutex
}

// NewStorage creates a new storage instance
func NewStorage(dbPath string) (*Storage, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath) // modernc.org/sqlite uses "sqlite" not "sqlite3"
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to set synchronous mode: %w", err)
	}

	storage := &Storage{db: db}
	if err := storage.initTables(); err != nil {
		if err := db.Close(); err != nil {
			fmt.Printf("Warning: failed to close database during cleanup: %v\n", err)
		}
		return nil, fmt.Errorf("failed to initialize tables: %w", err)
	}

	// Run migrations
	if err := storage.migrate(); err != nil {
		if err := db.Close(); err != nil {
			fmt.Printf("Warning: failed to close database during cleanup: %v\n", err)
		}
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return storage, nil
}

func NewStorageWithCanonical(dbPath string, canonical *storepkg.AgentGoDB) (*Storage, error) {
	storage, err := NewStorage(dbPath)
	if err != nil {
		return nil, err
	}
	storage.canonical = canonical
	return storage, nil
}

// Close closes the database connection
func (s *Storage) Close() error {
	if s.canonical != nil {
		_ = s.canonical.Close()
	}
	return s.db.Close()
}

// initTables creates the necessary tables
func (s *Storage) initTables() error {
	// Create tasks table
	tasksSQL := `
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		schedule TEXT,
		parameters TEXT,
		description TEXT,
		priority INTEGER DEFAULT 0,
		enabled BOOLEAN DEFAULT TRUE,
		created_at DATETIME,
		updated_at DATETIME,
		next_run DATETIME,
		last_run DATETIME
	);`

	if _, err := s.db.Exec(tasksSQL); err != nil {
		return fmt.Errorf("failed to create tasks table: %w", err)
	}

	// Create task_executions table
	executionsSQL := `
	CREATE TABLE IF NOT EXISTS task_executions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT,
		start_time DATETIME,
		end_time DATETIME,
		duration INTEGER,
		status TEXT,
		output TEXT,
		error TEXT,
		FOREIGN KEY (task_id) REFERENCES tasks (id) ON DELETE CASCADE
	);`

	if _, err := s.db.Exec(executionsSQL); err != nil {
		return fmt.Errorf("failed to create task_executions table: %w", err)
	}

	// Create indexes for better performance
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_tasks_enabled ON tasks(enabled);",
		"CREATE INDEX IF NOT EXISTS idx_tasks_next_run ON tasks(next_run);",
		"CREATE INDEX IF NOT EXISTS idx_executions_task_id ON task_executions(task_id);",
		"CREATE INDEX IF NOT EXISTS idx_executions_start_time ON task_executions(start_time);",
	}

	for _, indexSQL := range indexes {
		if _, err := s.db.Exec(indexSQL); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	return nil
}

// migrate runs database migrations
func (s *Storage) migrate() error {
	// Check if priority column exists
	var columnExists bool
	err := s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name='priority'").Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check priority column: %w", err)
	}

	// Add priority column if it doesn't exist
	if !columnExists {
		_, err = s.db.Exec("ALTER TABLE tasks ADD COLUMN priority INTEGER DEFAULT 0")
		if err != nil {
			return fmt.Errorf("failed to add priority column: %w", err)
		}
	}

	return nil
}

// CreateTask inserts a new task
func (s *Storage) CreateTask(task *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	parametersJSON, err := json.Marshal(task.Parameters)
	if err != nil {
		return fmt.Errorf("failed to marshal parameters: %w", err)
	}

	sql := `
	INSERT INTO tasks (id, type, schedule, parameters, description, priority, enabled, created_at, updated_at, next_run, last_run)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err = s.db.Exec(sql,
		task.ID,
		task.Type,
		task.Schedule,
		string(parametersJSON),
		task.Description,
		task.Priority,
		task.Enabled,
		task.CreatedAt,
		task.UpdatedAt,
		task.NextRun,
		task.LastRun,
	)

	if err != nil {
		return fmt.Errorf("failed to insert task: %w", err)
	}

	_ = s.mirrorTask(task, taskpkg.StatusQueued, "", "")
	return nil
}

// GetTask retrieves a task by ID
func (s *Storage) GetTask(id string) (*Task, error) {
	sql := `
	SELECT id, type, schedule, parameters, description, priority, enabled, created_at, updated_at, next_run, last_run
	FROM tasks WHERE id = ?`

	row := s.db.QueryRow(sql, id)

	task := &Task{}
	var parametersJSON string
	var nextRun, lastRun NullTime

	err := row.Scan(
		&task.ID,
		&task.Type,
		&task.Schedule,
		&parametersJSON,
		&task.Description,
		&task.Priority,
		&task.Enabled,
		&task.CreatedAt,
		&task.UpdatedAt,
		&nextRun,
		&lastRun,
	)

	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, fmt.Errorf("task not found: %s", id)
		}
		return nil, fmt.Errorf("failed to scan task: %w", err)
	}

	// Parse parameters JSON
	if err := json.Unmarshal([]byte(parametersJSON), &task.Parameters); err != nil {
		return nil, fmt.Errorf("failed to unmarshal parameters: %w", err)
	}

	// Handle nullable timestamps
	if nextRun.Valid {
		task.NextRun = &nextRun.Time
	}
	if lastRun.Valid {
		task.LastRun = &lastRun.Time
	}

	return task, nil
}

// ListTasks retrieves all tasks, optionally including disabled ones
func (s *Storage) ListTasks(includeDisabled bool) ([]*Task, error) {
	sql := `
	SELECT id, type, schedule, parameters, description, priority, enabled, created_at, updated_at, next_run, last_run
	FROM tasks`

	if !includeDisabled {
		sql += " WHERE enabled = TRUE"
	}

	sql += " ORDER BY priority DESC, created_at DESC"

	rows, err := s.db.Query(sql)
	if err != nil {
		return nil, fmt.Errorf("failed to query tasks: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			fmt.Printf("Warning: failed to close rows: %v\n", err)
		}
	}()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		var parametersJSON string
		var nextRun, lastRun NullTime

		err := rows.Scan(
			&task.ID,
			&task.Type,
			&task.Schedule,
			&parametersJSON,
			&task.Description,
			&task.Priority,
			&task.Enabled,
			&task.CreatedAt,
			&task.UpdatedAt,
			&nextRun,
			&lastRun,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan task: %w", err)
		}

		// Parse parameters JSON
		if err := json.Unmarshal([]byte(parametersJSON), &task.Parameters); err != nil {
			return nil, fmt.Errorf("failed to unmarshal parameters: %w", err)
		}

		// Handle nullable timestamps
		if nextRun.Valid {
			task.NextRun = &nextRun.Time
		}
		if lastRun.Valid {
			task.LastRun = &lastRun.Time
		}

		tasks = append(tasks, task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tasks: %w", err)
	}

	return tasks, nil
}

// UpdateTask updates an existing task
func (s *Storage) UpdateTask(task *Task) error {
	parametersJSON, err := json.Marshal(task.Parameters)
	if err != nil {
		return fmt.Errorf("failed to marshal parameters: %w", err)
	}

	task.UpdatedAt = time.Now()

	sql := `
	UPDATE tasks SET 
		type = ?, schedule = ?, parameters = ?, description = ?, enabled = ?, 
		updated_at = ?, next_run = ?, last_run = ?
	WHERE id = ?`

	result, err := s.db.Exec(sql,
		task.Type,
		task.Schedule,
		string(parametersJSON),
		task.Description,
		task.Enabled,
		task.UpdatedAt,
		task.NextRun,
		task.LastRun,
		task.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update task: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("task not found: %s", task.ID)
	}

	_ = s.mirrorTask(task, schedulerStatusForTask(task), "", "")
	return nil
}

// DeleteTask deletes a task and its execution history
func (s *Storage) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Start transaction
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Delete executions first (foreign key constraint)
	if _, err := tx.Exec("DELETE FROM task_executions WHERE task_id = ?", id); err != nil {
		return fmt.Errorf("failed to delete task executions: %w", err)
	}

	// Delete task
	result, err := tx.Exec("DELETE FROM tasks WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("task not found: %s", id)
	}

	_ = s.deleteCanonicalTask(id)
	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true

	return nil
}

// EnableTask enables or disables a task
func (s *Storage) EnableTask(id string, enabled bool) error {
	sql := "UPDATE tasks SET enabled = ?, updated_at = ? WHERE id = ?"

	result, err := s.db.Exec(sql, enabled, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update task enabled status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("task not found: %s", id)
	}

	if task, err := s.GetTask(id); err == nil {
		_ = s.mirrorTask(task, schedulerStatusForTask(task), "", "")
	}
	return nil
}

// CreateExecution records a task execution
func (s *Storage) CreateExecution(execution *TaskExecution) error {
	sql := `
	INSERT INTO task_executions (task_id, start_time, end_time, duration, status, output, error)
	VALUES (?, ?, ?, ?, ?, ?, ?)`

	var durationMs int64
	if execution.Duration > 0 {
		durationMs = execution.Duration.Nanoseconds() / 1000000
	}

	result, err := s.db.Exec(sql,
		execution.TaskID,
		execution.StartTime,
		execution.EndTime,
		durationMs,
		execution.Status,
		execution.Output,
		execution.Error,
	)

	if err != nil {
		return fmt.Errorf("failed to insert execution: %w", err)
	}

	// Get the auto-generated ID
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get execution ID: %w", err)
	}

	execution.ID = id
	if task, err := s.GetTask(execution.TaskID); err == nil {
		_ = s.mirrorTask(task, taskpkg.StatusRunning, execution.Output, execution.Error)
	}
	return nil
}

// UpdateExecution updates an existing task execution record
func (s *Storage) UpdateExecution(execution *TaskExecution) error {
	sql := `
	UPDATE task_executions 
	SET end_time = ?, duration = ?, status = ?, output = ?, error = ?
	WHERE id = ?`

	var durationMs int64
	if execution.Duration > 0 {
		durationMs = execution.Duration.Nanoseconds() / 1000000
	}

	_, err := s.db.Exec(sql,
		execution.EndTime,
		durationMs,
		execution.Status,
		execution.Output,
		execution.Error,
		execution.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update execution: %w", err)
	}

	if task, getErr := s.GetTask(execution.TaskID); getErr == nil {
		_ = s.mirrorTask(task, taskStatusFromExecution(execution.Status), execution.Output, execution.Error)
	}
	return nil
}

// GetTaskExecutions retrieves execution history for a task
func (s *Storage) GetTaskExecutions(taskID string, limit int) ([]*TaskExecution, error) {
	sql := `
	SELECT id, task_id, start_time, end_time, duration, status, output, error
	FROM task_executions 
	WHERE task_id = ? 
	ORDER BY start_time DESC`

	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.Query(sql, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to query executions: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			fmt.Printf("Warning: failed to close rows: %v\n", err)
		}
	}()

	var executions []*TaskExecution
	for rows.Next() {
		execution := &TaskExecution{}
		var endTime NullTime
		var durationMs int64

		err := rows.Scan(
			&execution.ID,
			&execution.TaskID,
			&execution.StartTime,
			&endTime,
			&durationMs,
			&execution.Status,
			&execution.Output,
			&execution.Error,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan execution: %w", err)
		}

		// Handle nullable end time
		if endTime.Valid {
			execution.EndTime = &endTime.Time
		}

		// Convert duration from milliseconds
		if durationMs > 0 {
			execution.Duration = time.Duration(durationMs) * time.Millisecond
		}

		executions = append(executions, execution)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating executions: %w", err)
	}

	return executions, nil
}

func (s *Storage) mirrorTask(t *Task, status taskpkg.Status, output, errText string) error {
	if s == nil || s.canonical == nil || t == nil || t.ID == "" {
		return nil
	}
	if status == "" {
		status = schedulerStatusForTask(t)
	}
	return s.canonical.SaveTask(&taskpkg.Task{
		ID:         t.ID,
		Kind:       taskpkg.KindScheduler,
		Status:     status,
		QueueClass: taskpkg.QueueClassTask,
		Input:      firstNonEmptySchedulerString(t.Description, t.Type),
		Output:     output,
		Error:      errText,
		CreatedAt:  firstNonZeroSchedulerTime(t.CreatedAt, time.Now()),
		StartedAt:  cloneSchedulerTimePtr(t.LastRun),
		Source:     "scheduler",
		SourceID:   t.ID,
	})
}

func (s *Storage) deleteCanonicalTask(id string) error {
	return nil
}

func schedulerStatusForTask(t *Task) taskpkg.Status {
	if t == nil {
		return taskpkg.StatusQueued
	}
	if !t.Enabled {
		return taskpkg.StatusCancelled
	}
	return taskpkg.StatusQueued
}

func taskStatusFromExecution(status TaskStatus) taskpkg.Status {
	switch status {
	case TaskStatusRunning:
		return taskpkg.StatusRunning
	case TaskStatusCompleted:
		return taskpkg.StatusCompleted
	case TaskStatusFailed:
		return taskpkg.StatusFailed
	case TaskStatusCancelled:
		return taskpkg.StatusCancelled
	default:
		return taskpkg.StatusQueued
	}
}

func firstNonEmptySchedulerString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonZeroSchedulerTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Now()
}

func cloneSchedulerTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// UpdateTaskNextRun updates the next run time for a task
func (s *Storage) UpdateTaskNextRun(id string, nextRun *time.Time) error {
	sql := "UPDATE tasks SET next_run = ?, updated_at = ? WHERE id = ?"

	_, err := s.db.Exec(sql, nextRun, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update task next run: %w", err)
	}

	return nil
}

// UpdateTaskLastRun updates the last run time for a task
func (s *Storage) UpdateTaskLastRun(id string, lastRun time.Time) error {
	sql := "UPDATE tasks SET last_run = ?, updated_at = ? WHERE id = ?"

	_, err := s.db.Exec(sql, lastRun, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update task last run: %w", err)
	}

	return nil
}

// GetTasksDueForExecution gets tasks that are due for execution
func (s *Storage) GetTasksDueForExecution() ([]*Task, error) {
	sql := `
	SELECT id, type, schedule, parameters, description, priority, enabled, created_at, updated_at, next_run, last_run
	FROM tasks 
	WHERE enabled = TRUE AND (next_run IS NULL OR next_run <= ?)
	ORDER BY priority DESC, next_run ASC`

	rows, err := s.db.Query(sql, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to query due tasks: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			fmt.Printf("Warning: failed to close rows: %v\n", err)
		}
	}()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		var parametersJSON string
		var nextRun, lastRun NullTime

		err := rows.Scan(
			&task.ID,
			&task.Type,
			&task.Schedule,
			&parametersJSON,
			&task.Description,
			&task.Priority,
			&task.Enabled,
			&task.CreatedAt,
			&task.UpdatedAt,
			&nextRun,
			&lastRun,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan task: %w", err)
		}

		// Parse parameters JSON
		if err := json.Unmarshal([]byte(parametersJSON), &task.Parameters); err != nil {
			return nil, fmt.Errorf("failed to unmarshal parameters: %w", err)
		}

		// Handle nullable timestamps
		if nextRun.Valid {
			task.NextRun = &nextRun.Time
		}
		if lastRun.Valid {
			task.LastRun = &lastRun.Time
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}

// CleanupOldExecutions removes old execution records
func (s *Storage) CleanupOldExecutions(maxAge time.Duration, maxPerTask int) error {
	// Delete old executions beyond maxAge
	cutoffTime := time.Now().Add(-maxAge)

	_, err := s.db.Exec("DELETE FROM task_executions WHERE start_time < ?", cutoffTime)
	if err != nil {
		return fmt.Errorf("failed to delete old executions: %w", err)
	}

	// Keep only the latest maxPerTask executions per task
	if maxPerTask > 0 {
		sql := `
		DELETE FROM task_executions 
		WHERE id NOT IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY task_id ORDER BY start_time DESC) as rn
				FROM task_executions
			) WHERE rn <= ?
		)`

		_, err := s.db.Exec(sql, maxPerTask)
		if err != nil {
			return fmt.Errorf("failed to cleanup excess executions: %w", err)
		}
	}

	return nil
}
