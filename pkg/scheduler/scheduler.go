package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	storepkg "github.com/liliang-cn/agent-go/v3/pkg/store"
)

// TaskScheduler implements the Scheduler interface
type TaskScheduler struct {
	config  *SchedulerConfig
	storage *Storage
	// canonical is the AgentGoDB handle start() opened so scheduler tasks are
	// mirrored into AgentGo's canonical tasks table. Opened here, closed here:
	// the Storage that writes through it only borrows it.
	canonical  *storepkg.AgentGoDB
	cronParser *CronParser
	executors  map[TaskType]Executor

	// Runtime state
	running bool
	// executing distinguishes a scheduler that fires tasks from one opened only
	// to manage them; see StartManageOnly.
	executing bool
	stopCh    chan struct{}
	wg        sync.WaitGroup
	mu        sync.RWMutex

	// Concurrency control
	semaphore chan struct{}

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// activeRuns holds the executions currently in flight, keyed by run ID,
	// so one of them can be cancelled without stopping the scheduler. See
	// cancel.go. Its own lock: cancelling must not queue behind whatever
	// holds mu, since the moment a stop is worth pressing is the moment the
	// scheduler is busy.
	runsMu     sync.Mutex
	activeRuns map[string]*taskRun
}

// NewScheduler creates a new task scheduler
func NewScheduler(cfg *config.Config) *TaskScheduler {
	// Extract scheduler config or use defaults
	schedulerConfig := DefaultSchedulerConfig()

	// Use the unified database path if available
	if cfg != nil {
		schedulerConfig.DatabasePath = cfg.CortexDBPath()
		schedulerConfig.CanonicalDatabasePath = cfg.AgentDBPath()
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
		activeRuns: make(map[string]*taskRun),
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
func (s *TaskScheduler) Start() error { return s.start(true) }

// StartManageOnly opens the store and accepts task management, without running
// the cron loop — nothing fires.
//
// Two processes sharing a home directory (a desktop app and a background daemon,
// say) both need to create and list tasks, but only one may execute them, or
// every task runs twice. Since Start couples opening the store to starting the
// loop, the process that does not own execution had no way to manage tasks at
// all. This is that way.
func (s *TaskScheduler) StartManageOnly() error { return s.start(false) }

// IsExecuting reports whether this scheduler fires tasks, as opposed to only
// managing them.
func (s *TaskScheduler) IsExecuting() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running && s.executing
}

func (s *TaskScheduler) start(execute bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("scheduler is already running")
	}

	// Initialize storage. The canonical handle is opened here and therefore
	// closed here (Stop), not by the Storage that borrows it.
	var canonical *storepkg.AgentGoDB
	if s.config.CanonicalDatabasePath != "" {
		canonical, _ = storepkg.NewAgentGoDB(s.config.CanonicalDatabasePath)
	}
	storage, err := NewStorageWithCanonical(s.config.DatabasePath, canonical)
	if err != nil {
		if canonical != nil {
			_ = canonical.Close()
		}
		return fmt.Errorf("failed to initialize storage: %w", err)
	}
	s.storage = storage
	s.canonical = canonical

	// Register default executors
	s.registerDefaultExecutors()

	s.running = true
	s.executing = execute

	if !execute {
		log.Printf("Task scheduler opened for management only (no execution): %s", s.config.DatabasePath)
		return nil
	}

	// Start the main scheduler loop
	s.wg.Add(1)
	go s.schedulerLoop()

	// Start cleanup routine
	s.wg.Add(1)
	go s.cleanupLoop()

	log.Printf("Task scheduler started with database: %s", s.config.DatabasePath)
	return nil
}

// Stop stops the scheduler. Every in-flight execution is cancelled through the
// root context and waited for; the semantics are unchanged by the arrival of
// per-execution cancel, which is the narrower operation.
func (s *TaskScheduler) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	close(s.stopCh)
	s.cancel()
	// Released before waiting: an execution goroutine takes mu.RLock to look
	// up its executor, so holding the write lock across wg.Wait() deadlocks
	// against any task that started in the same instant as the shutdown.
	s.mu.Unlock()

	// Wait for all goroutines to finish
	s.wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	// Close storage, then the canonical handle start() opened for it — in that
	// order, so nothing mirrors a task into a database that is already gone.
	if s.storage != nil {
		if err := s.storage.Close(); err != nil {
			log.Printf("Error closing storage: %v", err)
		}
	}
	if s.canonical != nil {
		if err := s.canonical.Close(); err != nil {
			log.Printf("Error closing canonical database: %v", err)
		}
		s.canonical = nil
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

	log.Printf("Created task %s (%s) with schedule: %s", shortID(task.ID), task.Type, task.Schedule)
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

// RunTask runs a task immediately and blocks until it finishes.
//
// A prompt task can legitimately take minutes, so a UI thread should prefer
// RunTaskAsync: this call cannot be interrupted by its caller, only by
// CancelTaskRuns from another goroutine.
func (s *TaskScheduler) RunTask(id string) (*TaskResult, error) {
	if !s.running {
		return nil, fmt.Errorf("scheduler is not running")
	}

	task, err := s.storage.GetTask(id)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

	return s.executeTask(task, true)
}

// RunTaskAsync starts a task immediately in the background and returns the run
// ID of the execution, which CancelRun accepts.
//
// It exists because RunTask blocks for as long as the task takes, which for an
// agent prompt is minutes: a host that calls it from a UI handler freezes, and
// a frozen UI cannot offer the cancel button that would end the wait. The
// outcome lands in the execution history (GetTaskExecutions) the same way a
// cron-fired run's does.
func (s *TaskScheduler) RunTaskAsync(id string) (string, error) {
	if !s.running {
		return "", fmt.Errorf("scheduler is not running")
	}

	task, err := s.storage.GetTask(id)
	if err != nil {
		return "", fmt.Errorf("failed to get task: %w", err)
	}

	// Resolved up front so an unknown task type is reported to the caller
	// rather than buried in a log line from a goroutine nobody is watching.
	s.mu.RLock()
	_, exists := s.executors[TaskType(task.Type)]
	s.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("no executor for task type: %s", task.Type)
	}

	runCtx, run, release := s.beginRun(task.ID, true)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer release()
		if _, err := s.executeTaskWithRun(task, runCtx, run); err != nil {
			log.Printf("Task %s execution error: %v", shortID(task.ID), err)
		}
	}()

	return run.RunID, nil
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
			log.Printf("Skipping task %s - at capacity", shortID(task.ID))
		}
	}
}

// executeTaskAsync executes a task asynchronously
func (s *TaskScheduler) executeTaskAsync(task *Task) {
	defer s.wg.Done()
	defer func() { <-s.semaphore }() // Release semaphore

	result, err := s.executeTask(task, false)
	switch {
	case err != nil:
		log.Printf("Task %s execution error: %v", shortID(task.ID), err)
	case result != nil && result.Cancelled:
		log.Printf("Task %s cancelled after %v", shortID(task.ID), result.Duration)
	case result != nil && !result.Success:
		log.Printf("Task %s failed: %s", shortID(task.ID), result.Error)
	default:
		log.Printf("Task %s completed successfully in %v", shortID(task.ID), result.Duration)
	}

	// Update next run time if this is a scheduled task
	if task.Schedule != "" {
		nextRun, err := s.cronParser.ParseAndNext(task.Schedule, time.Now())
		if err != nil {
			log.Printf("Error calculating next run for task %s: %v", shortID(task.ID), err)
		} else {
			if err := s.storage.UpdateTaskNextRun(task.ID, nextRun); err != nil {
				log.Printf("Error updating next run for task %s: %v", shortID(task.ID), err)
			}
		}
	}
}

// executeTask executes a single task, registering a cancellable context for
// the execution so CancelRun / CancelTaskRuns can stop just this one.
func (s *TaskScheduler) executeTask(task *Task, manual bool) (*TaskResult, error) {
	runCtx, run, release := s.beginRun(task.ID, manual)
	defer release()
	return s.executeTaskWithRun(task, runCtx, run)
}

// executeTaskWithRun is executeTask once the execution has been registered.
// Split out so RunTaskAsync can own the registration across a goroutine
// boundary and hand its run ID back to the caller before the work starts.
func (s *TaskScheduler) executeTaskWithRun(task *Task, runCtx context.Context, run *taskRun) (*TaskResult, error) {
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

	// Execute task with the execution's own context, not the scheduler root:
	// that is what makes cancelling one run possible without stopping the
	// scheduler.
	start := time.Now()
	result, err := executor.Execute(runCtx, task.Parameters)
	duration := time.Since(start)

	// Update execution record
	endTime := time.Now()
	execution.EndTime = &endTime
	execution.Duration = duration

	// A run somebody stopped is cancelled, not failed. Checked before the
	// error branch because a cancelled executor usually does return an error
	// (context.Canceled, or whatever it wrapped it in), and recording that as
	// a failure is how a stop button ends up looking like a crash in the
	// history list.
	cancelled := run != nil && run.wasCancelled()
	if cancelled || (result != nil && result.Cancelled) {
		execution.Status = TaskStatusCancelled
		execution.Error = ""
		if result != nil {
			execution.Output = result.Output
		}
		out := ""
		if result != nil {
			out = result.Output
		}
		result = &TaskResult{
			Success:   false,
			Cancelled: true,
			Output:    out,
			Duration:  duration,
		}
	} else if err != nil {
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
