package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

// TaskScheduler implements the Scheduler interface
type TaskScheduler struct {
	config     *SchedulerConfig
	storage    *Storage
	cronParser *CronParser
	executors  map[TaskType]Executor

	// Runtime state
	running bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
	mu      sync.RWMutex

	// Concurrency control
	semaphore chan struct{}

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
}

// NewScheduler creates a new task scheduler
func NewScheduler(cfg *config.Config) *TaskScheduler {
	// Extract scheduler config or use defaults
	schedulerConfig := DefaultSchedulerConfig()

	// Use the unified database path if available
	if cfg != nil {
		schedulerConfig.DatabasePath = cfg.CortexDBPath()
	}

	ctx, cancel := context.WithCancel(context.Background())

	scheduler := &TaskScheduler{
		config:     schedulerConfig,
		cronParser: NewCronParser(),
		executors:  make(map[TaskType]Executor),
		stopCh:     make(chan struct{}),
		semaphore:  make(chan struct{}, schedulerConfig.MaxConcurrentTasks),
		ctx:        ctx,
		cancel:     cancel,
	}

	return scheduler
}

// RegisterExecutor registers a task executor for a specific task type
func (s *TaskScheduler) RegisterExecutor(executor Executor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executors[executor.Type()] = executor
}

// Start starts the scheduler
func (s *TaskScheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("scheduler is already running")
	}

	// Initialize storage
	storage, err := NewStorage(s.config.DatabasePath)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}
	s.storage = storage

	// Register default executors
	s.registerDefaultExecutors()

	s.running = true

	// Start the main scheduler loop
	s.wg.Add(1)
	go s.schedulerLoop()

	// Start cleanup routine
	s.wg.Add(1)
	go s.cleanupLoop()

	log.Printf("Task scheduler started with database: %s", s.config.DatabasePath)
	return nil
}

// Stop stops the scheduler
func (s *TaskScheduler) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.running = false
	close(s.stopCh)
	s.cancel()

	// Wait for all goroutines to finish
	s.wg.Wait()

	// Close storage
	if s.storage != nil {
		if err := s.storage.Close(); err != nil {
			log.Printf("Error closing storage: %v", err)
		}
	}

	log.Println("Task scheduler stopped")
	return nil
}

// CreateTask creates a new task
func (s *TaskScheduler) CreateTask(task *Task) (string, error) {
	if !s.running {
		return "", fmt.Errorf("scheduler is not running")
	}

	// Generate ID if not provided
	if task.ID == "" {
		task.ID = uuid.New().String()
	}

	// Validate task type
	s.mu.RLock()
	executor, exists := s.executors[TaskType(task.Type)]
	s.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("unknown task type: %s", task.Type)
	}

	// Validate parameters
	if err := executor.Validate(task.Parameters); err != nil {
		return "", fmt.Errorf("invalid task parameters: %w", err)
	}

	// Validate and calculate next run time
	if task.Schedule != "" {
		if err := s.cronParser.Validate(task.Schedule); err != nil {
			return "", fmt.Errorf("invalid schedule: %w", err)
		}

		nextRun, err := s.cronParser.ParseAndNext(task.Schedule, time.Now())
		if err != nil {
			return "", fmt.Errorf("failed to calculate next run: %w", err)
		}
		task.NextRun = nextRun
	}

	// Set timestamps
	now := time.Now()
	task.CreatedAt = now
	task.UpdatedAt = now

	// Store task
	if err := s.storage.CreateTask(task); err != nil {
		return "", fmt.Errorf("failed to store task: %w", err)
	}

	log.Printf("Created task %s (%s) with schedule: %s", task.ID[:8], task.Type, task.Schedule)
	return task.ID, nil
}

// GetTask retrieves a task by ID
func (s *TaskScheduler) GetTask(id string) (*Task, error) {
	if !s.running {
		return nil, fmt.Errorf("scheduler is not running")
	}

	return s.storage.GetTask(id)
}

// ListTasks lists all tasks
func (s *TaskScheduler) ListTasks(includeDisabled bool) ([]*Task, error) {
	if !s.running {
		return nil, fmt.Errorf("scheduler is not running")
	}

	return s.storage.ListTasks(includeDisabled)
}

// UpdateTask updates an existing task
func (s *TaskScheduler) UpdateTask(task *Task) error {
	if !s.running {
		return fmt.Errorf("scheduler is not running")
	}

	// Validate task type and parameters
	s.mu.RLock()
	executor, exists := s.executors[TaskType(task.Type)]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("unknown task type: %s", task.Type)
	}

	if err := executor.Validate(task.Parameters); err != nil {
		return fmt.Errorf("invalid task parameters: %w", err)
	}

	// Recalculate next run if schedule changed
	if task.Schedule != "" {
		if err := s.cronParser.Validate(task.Schedule); err != nil {
			return fmt.Errorf("invalid schedule: %w", err)
		}

		nextRun, err := s.cronParser.ParseAndNext(task.Schedule, time.Now())
		if err != nil {
			return fmt.Errorf("failed to calculate next run: %w", err)
		}
		task.NextRun = nextRun
	} else {
		task.NextRun = nil
	}

	return s.storage.UpdateTask(task)
}

// DeleteTask deletes a task
func (s *TaskScheduler) DeleteTask(id string) error {
	if !s.running {
		return fmt.Errorf("scheduler is not running")
	}

	return s.storage.DeleteTask(id)
}

// EnableTask enables or disables a task
func (s *TaskScheduler) EnableTask(id string, enabled bool) error {
	if !s.running {
		return fmt.Errorf("scheduler is not running")
	}

	return s.storage.EnableTask(id, enabled)
}

// RunTask runs a task immediately
func (s *TaskScheduler) RunTask(id string) (*TaskResult, error) {
	if !s.running {
		return nil, fmt.Errorf("scheduler is not running")
	}

	task, err := s.storage.GetTask(id)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

	return s.executeTask(task)
}

// GetTaskExecutions retrieves execution history for a task
func (s *TaskScheduler) GetTaskExecutions(taskID string, limit int) ([]*TaskExecution, error) {
	if !s.running {
		return nil, fmt.Errorf("scheduler is not running")
	}

	return s.storage.GetTaskExecutions(taskID, limit)
}

// schedulerLoop is the main scheduling loop
func (s *TaskScheduler) schedulerLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Minute) // Check every minute
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkAndExecuteDueTasks()
		}
	}
}

// checkAndExecuteDueTasks checks for and executes tasks that are due
func (s *TaskScheduler) checkAndExecuteDueTasks() {
	tasks, err := s.storage.GetTasksDueForExecution()
	if err != nil {
		log.Printf("Error getting due tasks: %v", err)
		return
	}

	for _, task := range tasks {
		// Skip if we're at capacity
		select {
		case s.semaphore <- struct{}{}:
			// Got semaphore, proceed
			s.wg.Add(1)
			go s.executeTaskAsync(task)
		default:
			// At capacity, skip this execution
			log.Printf("Skipping task %s - at capacity", task.ID[:8])
		}
	}
}

// executeTaskAsync executes a task asynchronously
func (s *TaskScheduler) executeTaskAsync(task *Task) {
	defer s.wg.Done()
	defer func() { <-s.semaphore }() // Release semaphore

	result, err := s.executeTask(task)
	if err != nil {
		log.Printf("Task %s execution error: %v", task.ID[:8], err)
	} else if result != nil && !result.Success {
		log.Printf("Task %s failed: %s", task.ID[:8], result.Error)
	} else {
		log.Printf("Task %s completed successfully in %v", task.ID[:8], result.Duration)
	}

	// Update next run time if this is a scheduled task
	if task.Schedule != "" {
		nextRun, err := s.cronParser.ParseAndNext(task.Schedule, time.Now())
		if err != nil {
			log.Printf("Error calculating next run for task %s: %v", task.ID[:8], err)
		} else {
			if err := s.storage.UpdateTaskNextRun(task.ID, nextRun); err != nil {
				log.Printf("Error updating next run for task %s: %v", task.ID[:8], err)
			}
		}
	}
}

// executeTask executes a single task
func (s *TaskScheduler) executeTask(task *Task) (*TaskResult, error) {
	s.mu.RLock()
	executor, exists := s.executors[TaskType(task.Type)]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("no executor for task type: %s", task.Type)
	}

	// Create execution record
	execution := &TaskExecution{
		TaskID:    task.ID,
		StartTime: time.Now(),
		Status:    TaskStatusRunning,
	}

	// Store execution start
	if err := s.storage.CreateExecution(execution); err != nil {
		log.Printf("Failed to create execution record: %v", err)
	}

	// Execute task
	start := time.Now()
	result, err := executor.Execute(s.ctx, task.Parameters)
	duration := time.Since(start)

	// Update execution record
	endTime := time.Now()
	execution.EndTime = &endTime
	execution.Duration = duration

	if err != nil {
		execution.Status = TaskStatusFailed
		execution.Error = err.Error()
		result = &TaskResult{
			Success:  false,
			Error:    err.Error(),
			Duration: duration,
		}
	} else if result == nil {
		execution.Status = TaskStatusCompleted
		result = &TaskResult{
			Success:  true,
			Duration: duration,
		}
	} else {
		if result.Success {
			execution.Status = TaskStatusCompleted
		} else {
			execution.Status = TaskStatusFailed
		}
		execution.Output = result.Output
		execution.Error = result.Error
		result.Duration = duration
	}

	// Update execution in database
	if err := s.storage.UpdateExecution(execution); err != nil {
		log.Printf("Failed to update execution record: %v", err)
	}

	// Update task last run time
	if err := s.storage.UpdateTaskLastRun(task.ID, start); err != nil {
		log.Printf("Failed to update task last run: %v", err)
	}

	return result, nil
}

// cleanupLoop periodically cleans up old execution records
func (s *TaskScheduler) cleanupLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.performCleanup()
		}
	}
}

// performCleanup cleans up old execution records
func (s *TaskScheduler) performCleanup() {
	maxAge := time.Hour * 24 * 30 // Keep 30 days of history

	if err := s.storage.CleanupOldExecutions(maxAge, s.config.MaxExecutionHistory); err != nil {
		log.Printf("Error during cleanup: %v", err)
	} else {
		log.Println("Completed scheduled cleanup of old execution records")
	}
}

// registerDefaultExecutors registers the built-in task executors
func (s *TaskScheduler) registerDefaultExecutors() {
	// We'll register executors externally to avoid import cycles
	// This method is now just a placeholder
	log.Println("Default executors registration placeholder - will be done externally")
}
