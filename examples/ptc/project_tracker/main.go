// Package main demonstrates PTC with a project management / sprint tracker scenario.
//
// The LLM writes JavaScript that orchestrates calls to query developers, tasks,
// sprint progress, and code review stats — then synthesises a sprint health
// report in a single JS execution.
//
// Tools:
//   - list_developers:    list developers in a team, optionally by skill
//   - get_sprint_tasks:   tasks in a sprint, filterable by status/assignee
//   - get_developer_stats: workload & review stats for a developer
//   - get_sprint_summary:  high-level sprint metrics (velocity, burndown)
//
// Usage:
//
//	go run examples/ptc_project_tracker/main.go
//	go run examples/ptc_project_tracker/main.go "Who is overloaded this sprint and what tasks should be reassigned?"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

// ── Domain types ─────────────────────────────────────────────────────────────

// Developer represents a team member.
type Developer struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Role   string   `json:"role"`
	Skills []string `json:"skills"`
}

// Task represents a sprint task / user story.
type Task struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	AssigneeID  string `json:"assignee_id"`
	Status      string `json:"status"` // todo, in_progress, in_review, done
	Priority    string `json:"priority"`
	StoryPoints int    `json:"story_points"`
	Sprint      string `json:"sprint"`
	Labels      string `json:"labels"`
}

// DevStats holds workload and review metrics for a developer.
type DevStats struct {
	DeveloperID    string  `json:"developer_id"`
	Sprint         string  `json:"sprint"`
	PointsAssigned int     `json:"points_assigned"`
	PointsDone     int     `json:"points_done"`
	TasksOpen      int     `json:"tasks_open"`
	TasksDone      int     `json:"tasks_done"`
	PRsOpen        int     `json:"prs_open"`
	ReviewsPending int     `json:"reviews_pending"`
	AvgReviewHours float64 `json:"avg_review_hours"`
}

// SprintSummary holds aggregate sprint metrics.
type SprintSummary struct {
	Sprint          string  `json:"sprint"`
	TotalPoints     int     `json:"total_points"`
	CompletedPts    int     `json:"completed_points"`
	InProgressPts   int     `json:"in_progress_points"`
	TodoPts         int     `json:"todo_points"`
	VelocityTrend   string  `json:"velocity_trend"` // up, down, stable
	DaysRemaining   int     `json:"days_remaining"`
	BurndownOnTrack bool    `json:"burndown_on_track"`
	RiskLevel       string  `json:"risk_level"`
	CompletionPct   float64 `json:"completion_pct"`
}

// ── Mock data ────────────────────────────────────────────────────────────────

var developers = []Developer{
	{ID: "dev_01", Name: "Li Wei", Role: "senior_engineer", Skills: []string{"go", "kubernetes", "grpc"}},
	{ID: "dev_02", Name: "Zhang Min", Role: "engineer", Skills: []string{"go", "react", "typescript"}},
	{ID: "dev_03", Name: "Wang Fang", Role: "senior_engineer", Skills: []string{"python", "ml", "data"}},
	{ID: "dev_04", Name: "Chen Yu", Role: "junior_engineer", Skills: []string{"go", "javascript", "sql"}},
	{ID: "dev_05", Name: "Liu Na", Role: "tech_lead", Skills: []string{"go", "architecture", "kubernetes"}},
}

var tasks = []Task{
	// Li Wei — heavily loaded
	{ID: "TASK-101", Title: "Implement gRPC streaming endpoint", AssigneeID: "dev_01", Status: "in_progress", Priority: "high", StoryPoints: 8, Sprint: "S24", Labels: "backend"},
	{ID: "TASK-102", Title: "Add circuit breaker to payment service", AssigneeID: "dev_01", Status: "todo", Priority: "high", StoryPoints: 5, Sprint: "S24", Labels: "backend,reliability"},
	{ID: "TASK-103", Title: "Write integration tests for order API", AssigneeID: "dev_01", Status: "todo", Priority: "medium", StoryPoints: 3, Sprint: "S24", Labels: "testing"},
	{ID: "TASK-104", Title: "Fix race condition in cache layer", AssigneeID: "dev_01", Status: "in_progress", Priority: "critical", StoryPoints: 5, Sprint: "S24", Labels: "bug,backend"},

	// Zhang Min — moderate load
	{ID: "TASK-201", Title: "Build dashboard chart components", AssigneeID: "dev_02", Status: "in_progress", Priority: "medium", StoryPoints: 5, Sprint: "S24", Labels: "frontend"},
	{ID: "TASK-202", Title: "Migrate to React 19", AssigneeID: "dev_02", Status: "done", Priority: "medium", StoryPoints: 3, Sprint: "S24", Labels: "frontend,upgrade"},
	{ID: "TASK-203", Title: "Add API error handling in UI", AssigneeID: "dev_02", Status: "in_review", Priority: "high", StoryPoints: 3, Sprint: "S24", Labels: "frontend"},

	// Wang Fang — data/ML focus
	{ID: "TASK-301", Title: "Train recommendation model v2", AssigneeID: "dev_03", Status: "in_progress", Priority: "high", StoryPoints: 8, Sprint: "S24", Labels: "ml"},
	{ID: "TASK-302", Title: "Build data pipeline for user events", AssigneeID: "dev_03", Status: "done", Priority: "medium", StoryPoints: 5, Sprint: "S24", Labels: "data"},
	{ID: "TASK-303", Title: "A/B test framework integration", AssigneeID: "dev_03", Status: "todo", Priority: "medium", StoryPoints: 5, Sprint: "S24", Labels: "ml,testing"},

	// Chen Yu — junior, lighter tasks
	{ID: "TASK-401", Title: "Add pagination to user list API", AssigneeID: "dev_04", Status: "done", Priority: "low", StoryPoints: 2, Sprint: "S24", Labels: "backend"},
	{ID: "TASK-402", Title: "Write unit tests for auth middleware", AssigneeID: "dev_04", Status: "in_progress", Priority: "medium", StoryPoints: 3, Sprint: "S24", Labels: "testing"},
	{ID: "TASK-403", Title: "Document REST API endpoints", AssigneeID: "dev_04", Status: "todo", Priority: "low", StoryPoints: 2, Sprint: "S24", Labels: "docs"},

	// Liu Na — tech lead, fewer coding tasks
	{ID: "TASK-501", Title: "Design microservice decomposition plan", AssigneeID: "dev_05", Status: "done", Priority: "high", StoryPoints: 5, Sprint: "S24", Labels: "architecture"},
	{ID: "TASK-502", Title: "Review and approve deployment strategy", AssigneeID: "dev_05", Status: "in_review", Priority: "high", StoryPoints: 3, Sprint: "S24", Labels: "devops"},
	{ID: "TASK-503", Title: "Implement service mesh configuration", AssigneeID: "dev_05", Status: "todo", Priority: "medium", StoryPoints: 5, Sprint: "S24", Labels: "infrastructure"},
}

var devStatsData = map[string]DevStats{
	"dev_01": {DeveloperID: "dev_01", Sprint: "S24", PointsAssigned: 21, PointsDone: 0, TasksOpen: 4, TasksDone: 0, PRsOpen: 2, ReviewsPending: 3, AvgReviewHours: 18.5},
	"dev_02": {DeveloperID: "dev_02", Sprint: "S24", PointsAssigned: 11, PointsDone: 3, TasksOpen: 2, TasksDone: 1, PRsOpen: 1, ReviewsPending: 1, AvgReviewHours: 6.2},
	"dev_03": {DeveloperID: "dev_03", Sprint: "S24", PointsAssigned: 18, PointsDone: 5, TasksOpen: 2, TasksDone: 1, PRsOpen: 1, ReviewsPending: 0, AvgReviewHours: 4.0},
	"dev_04": {DeveloperID: "dev_04", Sprint: "S24", PointsAssigned: 7, PointsDone: 2, TasksOpen: 2, TasksDone: 1, PRsOpen: 0, ReviewsPending: 0, AvgReviewHours: 0},
	"dev_05": {DeveloperID: "dev_05", Sprint: "S24", PointsAssigned: 13, PointsDone: 5, TasksOpen: 2, TasksDone: 1, PRsOpen: 1, ReviewsPending: 5, AvgReviewHours: 3.1},
}

// ── Tool handlers ────────────────────────────────────────────────────────────

func handleListDevelopers(_ context.Context, args map[string]interface{}) (interface{}, error) {
	skill, _ := args["skill"].(string)

	var result []Developer
	for _, d := range developers {
		if skill != "" {
			found := false
			for _, s := range d.Skills {
				if strings.EqualFold(s, skill) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		result = append(result, d)
	}

	return map[string]interface{}{
		"developers": result,
		"count":      len(result),
	}, nil
}

func handleGetSprintTasks(_ context.Context, args map[string]interface{}) (interface{}, error) {
	sprint, _ := args["sprint"].(string)
	if sprint == "" {
		sprint = "S24" // default to current sprint
	}

	status, _ := args["status"].(string)
	assignee, _ := args["assignee_id"].(string)

	var result []Task
	for _, t := range tasks {
		if t.Sprint != sprint {
			continue
		}
		if status != "" && !strings.EqualFold(t.Status, status) {
			continue
		}
		if assignee != "" && t.AssigneeID != assignee {
			continue
		}
		result = append(result, t)
	}

	return map[string]interface{}{
		"sprint": sprint,
		"tasks":  result,
		"count":  len(result),
	}, nil
}

func handleGetDeveloperStats(_ context.Context, args map[string]interface{}) (interface{}, error) {
	devID, _ := args["developer_id"].(string)
	if devID == "" {
		return nil, fmt.Errorf("developer_id is required")
	}

	stats, ok := devStatsData[devID]
	if !ok {
		return nil, fmt.Errorf("no stats found for developer: %s", devID)
	}

	return stats, nil
}

func handleGetSprintSummary(_ context.Context, args map[string]interface{}) (interface{}, error) {
	sprint, _ := args["sprint"].(string)
	if sprint == "" {
		sprint = "S24"
	}

	var totalPts, donePts, inProgressPts, todoPts int
	for _, t := range tasks {
		if t.Sprint != sprint {
			continue
		}
		totalPts += t.StoryPoints
		switch t.Status {
		case "done":
			donePts += t.StoryPoints
		case "in_progress", "in_review":
			inProgressPts += t.StoryPoints
		case "todo":
			todoPts += t.StoryPoints
		}
	}

	completionPct := 0.0
	if totalPts > 0 {
		completionPct = float64(donePts) / float64(totalPts) * 100
	}

	riskLevel := "low"
	if completionPct < 25 {
		riskLevel = "high"
	} else if completionPct < 50 {
		riskLevel = "medium"
	}

	return SprintSummary{
		Sprint:          sprint,
		TotalPoints:     totalPts,
		CompletedPts:    donePts,
		InProgressPts:   inProgressPts,
		TodoPts:         todoPts,
		VelocityTrend:   "stable",
		DaysRemaining:   5,
		BurndownOnTrack: completionPct >= 40,
		RiskLevel:       riskLevel,
		CompletionPct:   completionPct,
	}, nil
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	question := "Give me a sprint health report for S24. " +
		"Identify who is overloaded, which high-priority tasks are at risk, " +
		"and suggest specific task reassignments to balance the workload. " +
		"Include code review bottleneck analysis."
	if len(os.Args) > 1 {
		question = strings.Join(os.Args[1:], " ")
	}

	fmt.Println("=== PTC Project Task Tracker ===")
	fmt.Printf("Question: %s\n\n", question)

	svc, err := createService()
	if err != nil {
		log.Fatalf("Failed to create agent service: %v", err)
	}
	defer svc.Close()

	// Show agent info
	info := svc.Info()
	fmt.Printf("--- Agent Configuration ---\n")
	fmt.Printf("  Model:    %s\n", info.Model)
	fmt.Printf("  Base URL: %s\n", info.BaseURL)
	fmt.Printf("  PTC:      %v\n", info.PTCEnabled)
	fmt.Printf("  MCP:      %v\n", info.MCPEnabled)
	fmt.Printf("  RAG:      %v\n", info.RAGEnabled)
	fmt.Printf("  Debug:    %v\n", info.Debug)
	fmt.Println("---------------------------")
	fmt.Println()

	registerTools(svc)

	if err := runWithStream(ctx, svc, question); err != nil {
		log.Fatalf("Execution failed: %v", err)
	}
}

func createService() (*agent.Service, error) {
	return agent.New("SprintAnalyst").
		WithPTC().
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
}

func registerTools(svc *agent.Service) {
	// Struct-based (typed params) — schema auto-generated from struct field tags.

	type listDevelopersParams struct {
		Skill string `json:"skill" desc:"Filter by skill (e.g. go, react, python, kubernetes). Optional."`
	}
	svc.Register(agent.NewTool(
		"list_developers",
		"List developers in the team, optionally filtered by skill. Returns { developers: [{ id, name, role, skills }], count }.",
		func(ctx context.Context, p *listDevelopersParams) (any, error) {
			return handleListDevelopers(ctx, map[string]interface{}{"skill": p.Skill})
		},
	))

	type getSprintTasksParams struct {
		Sprint     string `json:"sprint"      desc:"Sprint identifier (e.g. S24). Defaults to current sprint."`
		Status     string `json:"status"      desc:"Filter by status: todo, in_progress, in_review, done (optional)"`
		AssigneeID string `json:"assignee_id" desc:"Filter by developer ID (optional)"`
	}
	svc.Register(agent.NewTool(
		"get_sprint_tasks",
		"Get tasks in a sprint, optionally filtered by status or assignee. Returns { sprint, tasks: [{ id, title, assignee_id, status, priority, story_points, sprint, labels }], count }.",
		func(ctx context.Context, p *getSprintTasksParams) (any, error) {
			return handleGetSprintTasks(ctx, map[string]interface{}{
				"sprint":      p.Sprint,
				"status":      p.Status,
				"assignee_id": p.AssigneeID,
			})
		},
	))

	// Builder-based (fluent) for the remaining two tools.
	svc.Register(
		agent.BuildTool("get_developer_stats").
			Description("Get workload and code review stats for a developer in the current sprint. Returns { developer_id, sprint, points_assigned, points_done, tasks_open, tasks_done, prs_open, reviews_pending, avg_review_hours }.").
			Param("developer_id", agent.TypeString, "Developer ID (e.g. dev_01)", agent.Required()).
			Handler(handleGetDeveloperStats).
			Build(),
	)

	svc.Register(
		agent.BuildTool("get_sprint_summary").
			Description("Get high-level sprint metrics including velocity, burndown, and risk. Returns { sprint, total_points, completed_points, in_progress_points, todo_points, velocity_trend, days_remaining, burndown_on_track, risk_level, completion_pct }.").
			Param("sprint", agent.TypeString, "Sprint identifier. Defaults to current sprint.").
			Handler(handleGetSprintSummary).
			Build(),
	)
}

func runWithStream(ctx context.Context, svc *agent.Service, question string) error {
	events, err := svc.RunStream(ctx, question)
	if err != nil {
		return fmt.Errorf("RunStream: %w", err)
	}

	for evt := range events {
		switch evt.Type {
		case agent.EventTypeStart:
			fmt.Printf("[start] %s\n", evt.Content)
		case agent.EventTypeThinking:
			content := evt.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			fmt.Printf("[thinking] %s\n", content)
		case agent.EventTypeToolCall:
			argsJSON, _ := json.MarshalIndent(evt.ToolArgs, "  ", "  ")
			fmt.Printf("[tool_call] %s(%s)\n", evt.ToolName, string(argsJSON))
		case agent.EventTypeToolResult:
			result := fmt.Sprintf("%v", evt.ToolResult)
			if len(result) > 500 {
				result = result[:500] + "..."
			}
			fmt.Printf("[tool_result] %s -> %s\n", evt.ToolName, result)
		case agent.EventTypePartial:
			fmt.Print(evt.Content)
		case agent.EventTypeComplete:
			if evt.Content != "" {
				fmt.Printf("\n\n=== Final Answer ===\n%s\n", evt.Content)
			}
		case agent.EventTypeError:
			fmt.Printf("[error] %s\n", evt.Content)
			return fmt.Errorf("agent error: %s", evt.Content)
		case agent.EventTypeHandoff:
			fmt.Printf("[handoff] %s\n", evt.Content)
		}
	}

	return nil
}
