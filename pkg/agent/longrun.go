package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
	"github.com/robfig/cron/v3"
)

// LongRunMode enables autonomous agent operation with heartbeat scheduling
// Similar to OpenClaw's daemon mode

// LongRunConfig configures the LongRun mode
type LongRunConfig struct {
	// Enabled enables LongRun mode
	Enabled bool `json:"enabled"`

	// HeartbeatInterval is the interval between heartbeat checks (default: 30m)
	HeartbeatInterval time.Duration `json:"heartbeat_interval"`

	// HeartbeatFile is the path to HEARTBEAT.md checklist
	HeartbeatFile string `json:"heartbeat_file"`

	// MemoryFile is the path to MEMORY.md for persistent context
	MemoryFile string `json:"memory_file"`

	// MaxAutonomousActions limits autonomous actions per heartbeat (default: 5)
	MaxAutonomousActions int `json:"max_autonomous_actions"`

	// RequireApproval for high-risk actions (delete, send email, payments)
	RequireApproval bool `json:"require_approval"`

	// WorkDir is the working directory for LongRun
	WorkDir string `json:"work_dir"`

	// LogFile is the path to LongRun logs
	LogFile string `json:"log_file"`

	// NotificationWebhook is an optional webhook URL for notifications
	NotificationWebhook string `json:"notification_webhook,omitempty"`
}

// DefaultLongRunConfig returns default configuration
func DefaultLongRunConfig() *LongRunConfig {
	homeDir, _ := os.UserHomeDir()
	workDir := filepath.Join(homeDir, ".agentgo", "longrun")

	return &LongRunConfig{
		Enabled:              false,
		HeartbeatInterval:    30 * time.Minute,
		HeartbeatFile:        filepath.Join(workDir, "HEARTBEAT.md"),
		MemoryFile:           filepath.Join(workDir, "MEMORY.md"),
		MaxAutonomousActions: 5,
		RequireApproval:      true,
		WorkDir:              workDir,
	}
}

// LongRunService manages autonomous agent operation
type LongRunService struct {
	config      *LongRunConfig
	agent       *Service
	scheduler   *cron.Cron
	queue       *TaskQueue
	memory      *MemoryManager       // file-based: SOUL.md / AGENTS.md / TOOLS.md persona config
	memSvc      domain.MemoryService // DB-based: same store as agent (may be nil)
	coordinator *SubAgentCoordinator // Manages concurrent SubAgent execution
	logger      *slog.Logger

	mu       sync.RWMutex
	running  bool
	lastRun  time.Time
	stopChan chan struct{}
}

// LongRunBuilder provides a fluent interface for creating LongRunService
type LongRunBuilder struct {
	agent  *Service
	config *LongRunConfig
	err    error
}

// NewLongRun creates a new LongRun builder with the given Agent Service
func NewLongRun(agent *Service) *LongRunBuilder {
	return &LongRunBuilder{
		agent:  agent,
		config: DefaultLongRunConfig(),
	}
}

// WithInterval sets the heartbeat interval
func (b *LongRunBuilder) WithInterval(interval time.Duration) *LongRunBuilder {
	if b.err != nil {
		return b
	}
	b.config.HeartbeatInterval = interval
	return b
}

// WithWorkDir sets the working directory for LongRun
func (b *LongRunBuilder) WithWorkDir(dir string) *LongRunBuilder {
	if b.err != nil {
		return b
	}
	b.config.WorkDir = dir
	b.config.HeartbeatFile = filepath.Join(dir, "HEARTBEAT.md")
	b.config.MemoryFile = filepath.Join(dir, "MEMORY.md")
	return b
}

// WithMaxActions sets the maximum autonomous actions per heartbeat
func (b *LongRunBuilder) WithMaxActions(max int) *LongRunBuilder {
	if b.err != nil {
		return b
	}
	b.config.MaxAutonomousActions = max
	return b
}

// WithApproval sets whether high-risk actions require approval
func (b *LongRunBuilder) WithApproval(require bool) *LongRunBuilder {
	if b.err != nil {
		return b
	}
	b.config.RequireApproval = require
	return b
}

// WithConfig sets the full configuration
func (b *LongRunBuilder) WithConfig(cfg *LongRunConfig) *LongRunBuilder {
	if b.err != nil {
		return b
	}
	b.config = cfg
	return b
}

// Build creates the LongRunService
func (b *LongRunBuilder) Build() (*LongRunService, error) {
	if b.err != nil {
		return nil, b.err
	}
	if b.agent == nil {
		return nil, fmt.Errorf("agent service is required")
	}
	return NewLongRunService(b.agent, b.config)
}

// MustBuild creates the LongRunService or panics
func (b *LongRunBuilder) MustBuild() *LongRunService {
	svc, err := b.Build()
	if err != nil {
		panic(err)
	}
	return svc
}

// NewLongRunService creates a new LongRun service (deprecated: use NewLongRun builder)
func NewLongRunService(agent *Service, cfg *LongRunConfig) (*LongRunService, error) {
	if cfg == nil {
		cfg = DefaultLongRunConfig()
	}

	// Ensure work directory exists
	if err := os.MkdirAll(cfg.WorkDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create work directory: %w", err)
	}

	// Initialize memory manager
	memory, err := NewMemoryManager(cfg.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create memory manager: %w", err)
	}

	// Use heartbeat file from memory manager
	cfg.HeartbeatFile = filepath.Join(cfg.WorkDir, "HEARTBEAT.md")

	queue, err := NewTaskQueue(filepath.Join(cfg.WorkDir, "tasks.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to create task queue: %w", err)
	}

	svc := &LongRunService{
		config:      cfg,
		agent:       agent,
		queue:       queue,
		memory:      memory,
		memSvc:      agent.MemoryService(), // may be nil; used for DB-backed memory
		coordinator: NewSubAgentCoordinator(),
		logger:      slog.Default().With("module", "longrun"),
		scheduler:   cron.New(cron.WithSeconds()),
		stopChan:    make(chan struct{}),
	}

	return svc, nil
}

// Start starts the LongRun service
func (s *LongRunService) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("LongRun service is already running")
	}

	// Schedule heartbeat with detached context
	// This ensures heartbeats continue even if the original context is cancelled
	schedule := fmt.Sprintf("@every %s", s.config.HeartbeatInterval)
	_, err := s.scheduler.AddFunc(schedule, func() {
		// Create a fresh context for each heartbeat to prevent cancellation issues
		heartbeatCtx := context.Background()
		s.executeHeartbeat(heartbeatCtx)
	})
	if err != nil {
		return fmt.Errorf("failed to schedule heartbeat: %w", err)
	}

	s.scheduler.Start()
	s.running = true

	s.logger.Info("LongRun service started", "interval", s.config.HeartbeatInterval)

	return nil
}

// Stop stops the LongRun service
func (s *LongRunService) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	// Stop scheduler
	ctx := s.scheduler.Stop()
	<-ctx.Done()

	s.running = false
	s.logger.Info("LongRun service stopped")

	return nil
}

// IsRunning returns whether the service is running
func (s *LongRunService) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// executeHeartbeat executes a single heartbeat cycle
func (s *LongRunService) executeHeartbeat(ctx context.Context) {
	s.mu.Lock()
	s.lastRun = time.Now()
	s.mu.Unlock()

	s.logger.Info("Executing heartbeat")

	actionsTaken := 0

	// 1. Process pending tasks from queue FIRST (user-added tasks have priority)
	tasks, err := s.queue.GetPendingTasks(ctx, s.config.MaxAutonomousActions)
	if err != nil {
		s.logger.Error("Failed to get pending tasks", "error", err)
	} else {
		for _, task := range tasks {
			if actionsTaken >= s.config.MaxAutonomousActions {
				break
			}

			s.logger.Info("Processing task", "id", task.ID, "goal", task.Goal)

			// Mark task as running immediately to prevent duplicate processing
			task.Status = TaskStatusRunning
			now := time.Now()
			task.StartedAt = &now
			if err := s.queue.UpdateTask(ctx, task); err != nil {
				s.logger.Error("Failed to mark task as running", "task_id", task.ID, "error", err)
				continue
			}

			if err := s.executeTask(ctx, task); err != nil {
				s.logger.Error("Failed to execute task", "task_id", task.ID, "error", err)
				task.Status = TaskStatusFailed
				task.Error = err.Error()
			} else {
				task.Status = TaskStatusCompleted
			}

			completedAt := time.Now()
			task.CompletedAt = &completedAt

			if err := s.queue.UpdateTask(ctx, task); err != nil {
				s.logger.Error("Failed to update task", "task_id", task.ID, "error", err)
			}
			actionsTaken++
		}
	}

	// 2. If quota remaining, process HEARTBEAT.md checklist
	if actionsTaken < s.config.MaxAutonomousActions {
		checklist, err := s.readChecklist()
		if err != nil {
			s.logger.Error("Failed to read checklist", "error", err)
		} else {
			for _, item := range checklist.Items {
				if actionsTaken >= s.config.MaxAutonomousActions {
					break
				}

				if item.Status == ChecklistItemPending {
					if err := s.processChecklistItem(ctx, item); err != nil {
						s.logger.Error("Failed to process item", "item", item.ID, "error", err)
					} else {
						actionsTaken++
					}
				}
			}
		}
	}

	s.logger.Info("Heartbeat completed", "actions_taken", actionsTaken)
}

// processChecklistItem processes a single checklist item
func (s *LongRunService) processChecklistItem(ctx context.Context, item ChecklistItem) error {
	s.logger.Info("Processing checklist item", "id", item.ID, "description", item.Description)

	// Check if approval is required
	if s.config.RequireApproval && item.RequiresApproval {
		// TODO: Send notification and wait for approval
		s.logger.Info("Item requires approval, skipping", "id", item.ID)
		return nil
	}

	// Build context from memory files
	contextPrompt, err := s.buildContextPrompt(item.Description)
	if err != nil {
		s.logger.Warn("Failed to build context", "error", err)
	}

	// Execute using agent with full integration (MCP, Skills, Memory)
	result, err := s.agent.Run(ctx, contextPrompt,
		WithMaxTurns(10),
		WithStoreHistory(true),
	)
	if err != nil {
		return err
	}

	s.logger.Info("Item processed", "id", item.ID, "success", result.Success)

	// Save result to unified memory
	if result.Success && result.FinalResult != nil {
		entry := fmt.Sprintf("Checklist item '%s': %s", item.Description, result.Text())
		if err := s.saveMemory(context.Background(), entry); err != nil {
			s.logger.Warn("Failed to save to memory", "error", err)
		}
	}

	// Update checklist
	return s.updateChecklistItem(item.ID, ChecklistItemDone)
}

// executeTask executes a queued task in a separate goroutine
func (s *LongRunService) executeTask(ctx context.Context, task *Task) error {
	s.logger.Info("Executing task", "id", task.ID, "goal", task.Goal)

	// Build context from memory files
	contextPrompt, err := s.buildContextPrompt(task.Goal)
	if err != nil {
		s.logger.Warn("Failed to build context", "error", err)
		contextPrompt = task.Goal
	}

	// Create a SubAgent for isolated execution
	subAgent := NewSubAgent(SubAgentConfig{
		Agent:   NewAgentWithConfig("task-runner", contextPrompt, nil),
		Goal:    contextPrompt,
		Service: s.agent,
		Timeout: 5 * time.Minute,
	}, WithSubAgentMaxTurns(15))

	// Run in coordinator (separate goroutine)
	resultChan := s.coordinator.RunAsync(ctx, subAgent)

	// Wait for result
	select {
	case result, ok := <-resultChan:
		if !ok {
			return fmt.Errorf("task channel closed unexpectedly")
		}
		if result.Error != nil {
			return result.Error
		}
		if execResult, ok := result.Result.(*ExecutionResult); ok {
			task.Result = execResult.Text()
		} else {
			task.Result = fmt.Sprintf("%v", result.Result)
		}

	case <-ctx.Done():
		s.coordinator.Cancel(task.ID)
		return ctx.Err()
	}

	// Log the result for visibility
	s.logger.Info("Task completed",
		"id", task.ID,
		"goal", task.Goal,
	)

	// Print result to console for immediate feedback
	fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("✅ Task Completed: %s\n", task.Goal)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("%s\n", task.Result)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// Save result to unified memory
	if task.Result != "" {
		entry := fmt.Sprintf("Task '%s': %s", task.Goal, task.Result)
		if err := s.saveMemory(context.Background(), entry); err != nil {
			s.logger.Warn("Failed to save to memory", "error", err)
		}
	}

	return nil
}

// extractCleanResult extracts the clean answer from the result
// Removes markdown formatting and thinking blocks
func extractCleanResult(result string) string {
	// Remove thinking blocks
	result = strings.TrimSpace(result)

	// Remove markdown code blocks
	result = strings.TrimPrefix(result, "```")
	result = strings.TrimSuffix(result, "```")

	// Remove leading/trailing newlines
	result = strings.TrimSpace(result)

	// If there's a thinking block at the start, skip to the actual answer
	if idx := strings.Index(result, ""); idx != -1 {
		// Find the end of thinking block
		if endIdx := strings.Index(result[idx:], ""); endIdx != -1 {
			result = strings.TrimSpace(result[idx+endIdx+2:])
		} else {
			// No end tag, take everything after start tag
			result = strings.TrimSpace(result[idx+2:])
		}
	}

	// Remove markdown bold/italic
	result = strings.ReplaceAll(result, "**", "")
	result = strings.ReplaceAll(result, "__", "")
	result = strings.ReplaceAll(result, "*", "")
	result = strings.ReplaceAll(result, "_", "")

	// Take first meaningful line if multi-line
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "") {
			return line
		}
	}

	return result
}

// truncateString truncates a string to max length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// saveMemory saves a memory entry, preferring DB memory when available,
// falling back to the file-based MEMORY.md.
func (s *LongRunService) saveMemory(ctx context.Context, content string) error {
	if s.memSvc != nil {
		return s.memSvc.Add(ctx, &domain.Memory{
			ID:         uuid.New().String(),
			SessionID:  "longrun",
			Type:       domain.MemoryTypeContext,
			Content:    content,
			Importance: 0.7,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		})
	}
	return s.memory.AppendMemory(content)
}

// buildContextPrompt builds a prompt with unified memory context.
// Persona files (SOUL/AGENTS/TOOLS) come from file-based memory.
// Recent relevant memories come from DB memory when available.
func (s *LongRunService) buildContextPrompt(goal string) (string, error) {
	var sb strings.Builder

	// 1. Persona & static config from files (SOUL.md / AGENTS.md / TOOLS.md)
	//    Exclude MEMORY.md and HEARTBEAT.md (DB handles memories; heartbeat is runtime state)
	for _, name := range []string{"SOUL.md", "AGENTS.md", "TOOLS.md"} {
		file, err := s.memory.Get(name)
		if err == nil && file.Content != "" {
			sb.WriteString(fmt.Sprintf("\n\n# %s\n\n%s", name, file.Content))
		}
	}

	// 2. Relevant memories from DB (semantic search), or fall back to file MEMORY.md
	if s.memSvc != nil {
		memContext, _, err := s.memSvc.RetrieveAndInject(context.Background(), goal, "longrun")
		if err == nil && memContext != "" {
			sb.WriteString("\n\n# Relevant Memory\n\n")
			sb.WriteString(memContext)
		}
	} else {
		// fall back to file-based MEMORY.md
		if file, err := s.memory.Get("MEMORY.md"); err == nil && file.Content != "" {
			sb.WriteString("\n\n# MEMORY.md\n\n")
			sb.WriteString(file.Content)
		}
	}

	// 3. Available skills
	if s.agent.Skills != nil {
		skillList, _ := s.agent.Skills.ListSkills(context.Background(), skills.SkillFilter{})
		if len(skillList) > 0 {
			sb.WriteString("\n\n## Available Skills\n")
			for _, sk := range skillList {
				sb.WriteString(fmt.Sprintf("- /%s: %s\n", sk.Name, sk.Description))
			}
		}
	}

	// 4. Available MCP tools
	if s.agent.MCP != nil {
		tools := s.agent.MCP.GetAvailableTools(context.Background())
		if len(tools) > 0 {
			sb.WriteString("\n\n## Available MCP Tools\n")
			for _, tool := range tools {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", tool.Name, tool.Description))
			}
		}
	}

	// 5. RAG availability hint
	if s.agent.RAG != nil {
		sb.WriteString("\n\n## RAG Knowledge Base\n")
		sb.WriteString("You have access to a RAG knowledge base. Use rag_query tool to search for relevant information when needed.\n")
	}

	sb.WriteString("\n\n## Current Task\n\n")
	sb.WriteString(goal)

	return sb.String(), nil
}

// AddTask adds a new task to the queue
func (s *LongRunService) AddTask(ctx context.Context, goal string, scheduledAt *time.Time) (*Task, error) {
	task := &Task{
		Goal:        goal,
		Status:      TaskStatusPending,
		ScheduledAt: scheduledAt,
		CreatedAt:   time.Now(),
	}

	if err := s.queue.AddTask(ctx, task); err != nil {
		return nil, err
	}

	s.logger.Info("Task added", "id", task.ID, "goal", goal)
	return task, nil
}

// GetStatus returns the current status of LongRun
func (s *LongRunService) GetStatus() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pendingCount, _ := s.queue.CountByStatus(context.Background(), TaskStatusPending)

	return map[string]interface{}{
		"running":            s.running,
		"last_run":           s.lastRun,
		"heartbeat_interval": s.config.HeartbeatInterval.String(),
		"pending_tasks":      pendingCount,
		"work_dir":           s.config.WorkDir,
	}
}

// GetMemory returns the file-based memory manager (persona files: SOUL/AGENTS/TOOLS/HEARTBEAT).
func (s *LongRunService) GetMemory() *MemoryManager {
	return s.memory
}

// GetMemoryService returns the DB-backed memory service (same store as the agent).
// Returns nil if the agent was built without WithMemory().
func (s *LongRunService) GetMemoryService() domain.MemoryService {
	return s.memSvc
}

// createDefaultHeartbeatFile creates a default HEARTBEAT.md
func createDefaultHeartbeatFile(path string) error {
	content := `# HEARTBEAT.md

This file contains tasks for the LongRun agent to check periodically.

## Checklist

- [ ] Check for urgent emails
- [ ] Review calendar for upcoming events
- [ ] Check system status

## Instructions

Mark items as done by changing [ ] to [x].
Add new items as needed.
`
	return os.WriteFile(path, []byte(content), 0644)
}
