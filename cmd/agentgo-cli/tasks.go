package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/liliang-cn/agent-go/pkg/scheduler"
	"github.com/spf13/cobra"
)

var tasksCmd = &cobra.Command{
	Use:   "task",
	Short: "Manage background tasks",
	Long:  `Manage and monitor background tasks (ingestion, mcp, etc).`,
}

var (
	textOutput bool
	jsonOutput bool
)

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks",
	Long:  `Manage and monitor background tasks (ingestion, mcp, etc).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Check for text/json output mode (non-interactive)
		if textOutput || jsonOutput || !isInteractive() {
			return listTasksSimple(textOutput, jsonOutput)
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
	taskListCmd.Flags().BoolVar(&textOutput, "text", false, "Output as plain text")
	taskListCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
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
func listTasksSimple(textOutput, jsonOutput bool) error {
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
