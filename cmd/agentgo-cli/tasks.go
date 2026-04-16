package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	agentpkg "github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/scheduler"
	storepkg "github.com/liliang-cn/agent-go/v2/pkg/store"
	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
	"github.com/spf13/cobra"
)

var tasksCmd = &cobra.Command{
	Use:   "task",
	Short: "Manage background tasks",
	Long:  `Manage and monitor background tasks (ingestion, mcp, etc).`,
}

var (
	textOutput    bool
	jsonOutput    bool
	schedulerOnly bool
	inspectFrames bool
	inspectEvents bool
	inspectJSON   bool
)

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks",
	Long:  `Manage and monitor background tasks (ingestion, mcp, etc).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if schedulerOnly {
			return listSchedulerTasksSimple(textOutput, jsonOutput)
		}
		// Check for text/json output mode (non-interactive)
		if textOutput || jsonOutput || !isInteractive() {
			return listUnifiedTasksSimple(jsonOutput)
		}
		// Interactive mode with bubbletea
		p := tea.NewProgram(initialModel())
		if _, err := p.Run(); err != nil {
			return fmt.Errorf("error running task list UI: %w", err)
		}
		return nil
	},
}

func init() {
	// Note: tasksCmd is added to RootCmd in root.go's init() to avoid duplication
	tasksCmd.AddCommand(taskListCmd)
	tasksCmd.AddCommand(taskGetCmd)
	tasksCmd.AddCommand(taskInspectCmd)
	tasksCmd.AddCommand(taskTraceCmd)
	tasksCmd.AddCommand(taskYieldCmd)
	tasksCmd.AddCommand(taskResumeCmd)
	tasksCmd.AddCommand(taskCancelCmd)
	taskListCmd.Flags().BoolVar(&textOutput, "text", false, "Output as plain text")
	taskListCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	taskListCmd.Flags().BoolVar(&schedulerOnly, "scheduler-only", false, "Show only legacy scheduler tasks")
	taskInspectCmd.Flags().BoolVar(&inspectFrames, "frames", false, "Include task LLM/tool frames")
	taskInspectCmd.Flags().BoolVar(&inspectEvents, "events", false, "Include task events")
	taskInspectCmd.Flags().BoolVar(&inspectJSON, "json", false, "Output as JSON")
}

var taskGetCmd = &cobra.Command{
	Use:   "get [task-id]",
	Short: "Get a unified task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := taskManager()
		if err != nil {
			return err
		}
		task, err := manager.GetUnifiedTask(args[0])
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(task, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	},
}

var taskInspectCmd = &cobra.Command{
	Use:   "inspect [task-id]",
	Short: "Inspect a task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		task, err := loadUnifiedTask(args[0])
		if err != nil {
			return err
		}
		if inspectJSON {
			if !inspectFrames {
				task.Frames = nil
			}
			if !inspectEvents {
				task.Events = nil
			}
			return printJSON(task)
		}
		printTaskHeader(task)
		if inspectFrames {
			printTaskFrames(task)
		}
		if inspectEvents {
			printTaskEvents(task)
		}
		return nil
	},
}

var taskTraceCmd = &cobra.Command{
	Use:   "trace [task-id]",
	Short: "Show a task execution trace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		task, err := loadUnifiedTask(args[0])
		if err != nil {
			return err
		}
		printTaskHeader(task)
		printTaskEvents(task)
		printTaskFrames(task)
		return nil
	},
}

var taskYieldCmd = &cobra.Command{
	Use:   "yield [task-id] [reason]",
	Short: "Mark a task yielded/resumable",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := taskManager()
		if err != nil {
			return err
		}
		reason := ""
		if len(args) > 1 {
			reason = args[1]
		}
		task, err := manager.YieldTask(cmd.Context(), args[0], reason)
		if err != nil {
			return err
		}
		fmt.Printf("Task %s yielded (%s).\n", task.ID, task.Status)
		return nil
	},
}

var taskResumeCmd = &cobra.Command{
	Use:   "resume [task-id] [input]",
	Short: "Mark a yielded task as resuming",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := taskManager()
		if err != nil {
			return err
		}
		input := ""
		if len(args) > 1 {
			input = args[1]
		}
		task, err := manager.ResumeTask(cmd.Context(), args[0], input)
		if err != nil {
			return err
		}
		fmt.Printf("Task %s resumed (%s).\n", task.ID, task.Status)
		return nil
	},
}

var taskCancelCmd = &cobra.Command{
	Use:   "cancel [task-id]",
	Short: "Cancel a task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := taskManager()
		if err != nil {
			return err
		}
		task, err := manager.CancelTask(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		fmt.Printf("Task %s status: %s\n", task.ID, task.Status)
		return nil
	},
}

// isInteractive checks if we're running in an interactive terminal
func isInteractive() bool {
	// Check if stdout is a TTY
	return isStdoutTTY()
}

// isStdoutTTY returns true if stdout is a terminal
func isStdoutTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// listTasksSimple outputs tasks in simple text or JSON format
func listSchedulerTasksSimple(textOutput, jsonOutput bool) error {
	// Determine DB path using unified DataDir
	dbPath := filepath.Join(Cfg.DataDir(), "scheduler.db")

	store, err := scheduler.NewStorage(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer store.Close()

	tasks, err := store.ListTasks(true)
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	if jsonOutput {
		// JSON output
		data, err := json.MarshalIndent(struct {
			Tasks []*scheduler.Task `json:"tasks"`
		}{Tasks: tasks}, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
	} else {
		// Plain text output
		if len(tasks) == 0 {
			fmt.Println("No tasks found.")
			return nil
		}

		fmt.Printf("Tasks (%d):\n\n", len(tasks))
		for _, t := range tasks {
			nextRun := "N/A"
			if t.NextRun != nil {
				nextRun = t.NextRun.Format("2006-01-02 15:04:05")
			}
			fmt.Printf("ID:       %s\n", t.ID)
			fmt.Printf("Type:     %s\n", t.Type)
			fmt.Printf("Enabled:  %v\n", t.Enabled)
			fmt.Printf("Priority: %d\n", t.Priority)
			fmt.Printf("Schedule: %s\n", t.Schedule)
			fmt.Printf("Next Run: %s\n", nextRun)
			fmt.Println(strings.Repeat("-", 40))
		}
	}

	return nil
}

func listUnifiedTasksSimple(jsonOutput bool) error {
	tasks, err := listUnifiedTasks()
	if err != nil {
		return err
	}

	if jsonOutput {
		data, err := json.MarshalIndent(struct {
			Tasks []*agentpkg.UnifiedTask `json:"tasks"`
		}{Tasks: tasks}, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	if len(tasks) == 0 {
		fmt.Println("No tasks found.")
		return nil
	}

	fmt.Printf("Tasks (%d):\n\n", len(tasks))
	for _, task := range tasks {
		fmt.Printf("ID:      %s\n", task.ID)
		fmt.Printf("Kind:    %s\n", task.Kind)
		fmt.Printf("Status:  %s\n", task.Status)
		if task.SessionID != "" {
			fmt.Printf("Session: %s\n", task.SessionID)
		}
		if task.TeamName != "" || task.TeamID != "" {
			fmt.Printf("Team:    %s (%s)\n", valueOrDash(task.TeamName), valueOrDash(task.TeamID))
		}
		if task.AgentName != "" {
			fmt.Printf("Agent:   %s\n", task.AgentName)
		}
		if task.Input != "" {
			fmt.Printf("Input:   %s\n", trimTaskText(task.Input, 120))
		}
		if task.Output != "" {
			fmt.Printf("Output:  %s\n", trimTaskText(task.Output, 120))
		}
		if task.Error != "" {
			fmt.Printf("Error:   %s\n", trimTaskText(task.Error, 120))
		}
		fmt.Printf("Frames:  %d | Events: %d | ToolCalls: %d\n", len(task.Frames), len(task.Events), countUnifiedTaskToolCalls(task))
		if task.Stats != nil {
			fmt.Printf("Stats:   Rounds:%d Tokens:%d Duration:%dms\n",
				task.Stats.Rounds, task.Stats.TotalTokens, task.Stats.DurationMs)
		}
		fmt.Printf("Created: %s\n", task.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Println(strings.Repeat("-", 40))
	}
	return nil
}

func loadUnifiedTask(taskID string) (*agentpkg.UnifiedTask, error) {
	manager, err := taskManager()
	if err != nil {
		return nil, err
	}
	return manager.GetUnifiedTask(taskID)
}

func printJSON(value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func printTaskHeader(task *agentpkg.UnifiedTask) {
	fmt.Printf("Task:    %s\n", task.ID)
	fmt.Printf("Kind:    %s\n", task.Kind)
	fmt.Printf("Status:  %s\n", task.Status)
	if task.QueueClass != "" {
		fmt.Printf("Queue:   %s\n", task.QueueClass)
	}
	if task.SessionID != "" {
		fmt.Printf("Session: %s\n", task.SessionID)
	}
	if task.RuntimeSessionID != "" && task.RuntimeSessionID != task.SessionID {
		fmt.Printf("Runtime: %s\n", task.RuntimeSessionID)
	}
	if task.TeamName != "" || task.TeamID != "" {
		fmt.Printf("Team:    %s (%s)\n", valueOrDash(task.TeamName), valueOrDash(task.TeamID))
	}
	if task.AgentName != "" {
		fmt.Printf("Agent:   %s\n", task.AgentName)
	}
	if task.ContinuationID != "" {
		fmt.Printf("Cont:    %s\n", task.ContinuationID)
	}
	if task.Awaiting != nil {
		fmt.Printf("Awaiting:%s %s\n", valueOrDash(task.Awaiting.Type), trimTaskText(task.Awaiting.Reason, 120))
	}
	if task.Input != "" {
		fmt.Printf("Input:   %s\n", trimTaskText(task.Input, 200))
	}
	if task.Output != "" {
		fmt.Printf("Output:  %s\n", trimTaskText(task.Output, 200))
	}
	if task.Error != "" {
		fmt.Printf("Error:   %s\n", trimTaskText(task.Error, 200))
	}
	fmt.Printf("Frames:  %d | Events: %d | ToolCalls: %d\n", len(task.Frames), len(task.Events), countUnifiedTaskToolCalls(task))
	if task.Stats != nil {
		fmt.Printf("Stats:   Rounds:%d Tokens:%d Duration:%dms\n",
			task.Stats.Rounds, task.Stats.TotalTokens, task.Stats.DurationMs)
		if len(task.Stats.RoundBreakdown) > 0 {
			for _, rs := range task.Stats.RoundBreakdown {
				fmt.Printf("  Round %d: tokens=%d tools=%d llm=%dms tool=%dms total=%dms\n",
					rs.Round, rs.TokensUsed, rs.ToolCalls, rs.LLMMs, rs.ToolMs, rs.DurationMs)
			}
		}
	}
	fmt.Printf("Created: %s\n", task.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Println(strings.Repeat("-", 40))
}

func printTaskEvents(task *agentpkg.UnifiedTask) {
	fmt.Println("Events:")
	if len(task.Events) == 0 {
		fmt.Println("  (none)")
		return
	}
	for i, event := range task.Events {
		when := event.Timestamp.Format("15:04:05")
		dur := ""
		if event.DurationMs > 0 {
			dur = fmt.Sprintf(" (%dms)", event.DurationMs)
		}
		dupFlag := ""
		if event.Runtime != nil && event.Runtime.Duplicate {
			dupFlag = " [DUPLICATE]"
		}
		roundTag := ""
		if event.Runtime != nil && event.Runtime.Round > 0 {
			roundTag = fmt.Sprintf(" R%d", event.Runtime.Round)
		}
		message := trimTaskText(event.Message, 160)
		if message == "" && event.Runtime != nil {
			message = trimTaskText(event.Runtime.ToolName, 160)
		}
		fmt.Printf("  [%d] %s %s%s%s%s %s\n", i+1, when, event.Type, roundTag, dur, dupFlag, message)
	}
}

func printTaskFrames(task *agentpkg.UnifiedTask) {
	fmt.Println("Frames:")
	if len(task.Frames) == 0 {
		fmt.Println("  (none)")
		return
	}
	for i, frame := range task.Frames {
		msg := frame.Message
		label := msg.Role
		if msg.ToolCallID != "" {
			label += " tool_call_id=" + msg.ToolCallID
		}
		fmt.Printf("  [%d] %s session=%s task=%s\n", i+1, label, valueOrDash(frame.SessionID), valueOrDash(msg.TaskID))
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				fmt.Printf("      tool_call %s %s\n", call.ID, call.Function.Name)
			}
		}
		if strings.TrimSpace(msg.Content) != "" {
			fmt.Printf("      %s\n", trimTaskText(msg.Content, 240))
		}
	}
}

func countUnifiedTaskToolCalls(task *agentpkg.UnifiedTask) int {
	if task == nil {
		return 0
	}
	count := 0
	for _, msg := range task.Frames {
		count += len(msg.Message.ToolCalls)
		if strings.TrimSpace(msg.Message.ToolCallID) != "" {
			count++
		}
	}
	for _, evt := range task.Events {
		if evt.Type == string(agentpkg.EventTypeToolCall) {
			count++
		}
	}
	return count
}

func listUnifiedTasks() ([]*agentpkg.UnifiedTask, error) {
	var unified []*agentpkg.UnifiedTask

	manager, err := taskManager()
	if err == nil {
		unified = append(unified, manager.ListUnifiedTasks(0)...)
	}

	schedulerTasks, err := loadSchedulerTasks()
	if err != nil {
		return nil, err
	}
	for _, task := range schedulerTasks {
		if task := unifiedTaskFromScheduler(task); task != nil {
			unified = append(unified, task)
		}
	}

	slices.SortFunc(unified, func(a, b *agentpkg.UnifiedTask) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return unified, nil
}

func taskManager() (*agentpkg.TeamManager, error) {
	agentStore, err := agentpkg.NewStore(Cfg.AgentDBPath())
	if err != nil {
		return nil, err
	}
	manager := agentpkg.NewTeamManager(agentStore)
	manager.SetConfig(Cfg)
	_ = manager.SeedDefaultMembers()
	return manager, nil
}

func valueOrDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func trimTaskText(value string, maxLen int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen] + "..."
}

func loadSchedulerTasks() ([]*scheduler.Task, error) {
	dbPath := filepath.Join(Cfg.DataDir(), "scheduler.db")
	canonical, _ := storepkg.NewAgentGoDB(Cfg.AgentDBPath())
	store, err := scheduler.NewStorageWithCanonical(dbPath, canonical)
	if err != nil {
		if canonical != nil {
			_ = canonical.Close()
		}
		return nil, fmt.Errorf("failed to open scheduler database: %w", err)
	}
	defer store.Close()
	return store.ListTasks(true)
}

func unifiedTaskFromScheduler(task *scheduler.Task) *agentpkg.UnifiedTask {
	if task == nil {
		return nil
	}
	status := taskpkg.StatusQueued
	if !task.Enabled {
		status = taskpkg.StatusCancelled
	}
	return &agentpkg.UnifiedTask{
		ID:        task.ID,
		Kind:      agentpkg.TaskKindScheduler,
		Status:    status,
		Input:     firstNonEmptyString(task.Description, task.Type),
		CreatedAt: task.CreatedAt,
		Source:    "scheduler",
		SourceID:  task.ID,
	}
}

// Bubble Tea Model
type model struct {
	tasks    []*scheduler.Task
	loading  bool
	err      error
	quitting bool
	url      string
}

type taskMsg []*scheduler.Task
type errMsg error

func initialModel() model {
	// Determine API URL
	host := Cfg.Server.Host
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	port := Cfg.Server.Port
	url := fmt.Sprintf("http://%s:%d/api/v1/tasks", host, port)

	return model{
		tasks:   []*scheduler.Task{},
		loading: true,
		url:     url,
	}
}

func (m model) Init() tea.Cmd {
	return fetchTasks(m.url)
}

func fetchTasks(url string) tea.Cmd {
	return func() tea.Msg {
		// 1. Try API first
		client := http.Client{
			Timeout: 2 * time.Second,
		}
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				body, _ := io.ReadAll(resp.Body)
				var result struct {
					Tasks []*scheduler.Task `json:"tasks"`
				}
				if err := json.Unmarshal(body, &result); err == nil {
					return taskMsg(result.Tasks)
				}
			}
		}

		// 2. Fallback: Direct DB Access using unified DataDir
		dbPath := filepath.Join(Cfg.DataDir(), "scheduler.db")

		// Initialize storage directly
		store, err := scheduler.NewStorage(dbPath)
		if err != nil {
			return errMsg(fmt.Errorf("API unavailable and DB access failed: %v", err))
		}
		defer store.Close()

		tasks, err := store.ListTasks(true)
		if err != nil {
			return errMsg(fmt.Errorf("API unavailable and DB list failed: %v", err))
		}

		return taskMsg(tasks)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if msg.String() == "r" {
			m.loading = true
			return m, fetchTasks(m.url)
		}

	case taskMsg:
		m.tasks = msg
		m.loading = false

	case errMsg:
		m.err = msg
		m.loading = false
	}

	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return "Bye!\n"
	}

	s := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("AgentGo Task Monitor") + "\n\n"

	if m.loading {
		s += "Loading tasks...\n"
	} else if m.err != nil {
		s += fmt.Sprintf("Error: %v\n", m.err)
		s += "\nRunning in offline mode. Task data from local database.\n"
	} else {
		if len(m.tasks) == 0 {
			s += "No tasks found.\n"
		} else {
			for _, t := range m.tasks {
				// Task struct has NextRun/LastRun but not "Status" of execution.
				// We need to fetch executions or infer.
				// For now, simple list.

				s += fmt.Sprintf("ID: %s | Type: %s | Enabled: %v\n", t.ID[:8], t.Type, t.Enabled)
			}
		}
	}

	s += "\nPress 'r' to refresh, 'q' to quit.\n"
	return s
}
