// Package main demonstrates Programmatic Tool Calling (PTC) with custom Go tools.
//
// This example mirrors Anthropic's PTC cookbook pattern: the LLM writes JavaScript
// that calls multiple tools via callTool(), orchestrating them in a single execution
// instead of making individual round-trips. Custom tools are registered via
// Service.AddTool() and are only accessible through callTool() inside the JS sandbox
// when PTC is enabled — they never appear as direct function-call tools.
//
// The mock API simulates a team expense tracking system with three endpoints:
//   - get_team_members: lists team members by department
//   - get_expenses: retrieves expense records for a team member
//   - get_budget: returns budget allocation for a department + quarter
//
// Usage:
//
//	go run examples/ptc_custom_tools/main.go
//	go run examples/ptc_custom_tools/main.go "What's the total travel spending for engineering in Q3?"
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

// ── Mock data ────────────────────────────────────────────────────────────────

// TeamMember represents an employee in a department.
type TeamMember struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Department string `json:"department"`
}

// Expense represents a single expense record.
type Expense struct {
	ID       string  `json:"id"`
	MemberID string  `json:"member_id"`
	Category string  `json:"category"`
	Amount   float64 `json:"amount"`
	Quarter  string  `json:"quarter"`
	Desc     string  `json:"description"`
}

// Budget represents a quarterly budget allocation.
type Budget struct {
	Department string  `json:"department"`
	Quarter    string  `json:"quarter"`
	Total      float64 `json:"total"`
	Travel     float64 `json:"travel"`
	Equipment  float64 `json:"equipment"`
	Training   float64 `json:"training"`
}

var teamMembers = []TeamMember{
	{ID: "emp_001", Name: "Alice Chen", Role: "Senior Engineer", Department: "engineering"},
	{ID: "emp_002", Name: "Bob Smith", Role: "Staff Engineer", Department: "engineering"},
	{ID: "emp_003", Name: "Carol Davis", Role: "Engineering Manager", Department: "engineering"},
	{ID: "emp_004", Name: "Dan Lee", Role: "Designer", Department: "design"},
	{ID: "emp_005", Name: "Eve Wilson", Role: "Product Manager", Department: "product"},
}

var expenses = []Expense{
	// Alice
	{ID: "exp_001", MemberID: "emp_001", Category: "travel", Amount: 1250.00, Quarter: "Q3", Desc: "Conference flight + hotel"},
	{ID: "exp_002", MemberID: "emp_001", Category: "equipment", Amount: 350.00, Quarter: "Q3", Desc: "Mechanical keyboard"},
	{ID: "exp_003", MemberID: "emp_001", Category: "travel", Amount: 800.00, Quarter: "Q3", Desc: "Client visit Seattle"},
	// Bob
	{ID: "exp_004", MemberID: "emp_002", Category: "travel", Amount: 2100.00, Quarter: "Q3", Desc: "Team offsite NYC"},
	{ID: "exp_005", MemberID: "emp_002", Category: "training", Amount: 500.00, Quarter: "Q3", Desc: "Go advanced course"},
	{ID: "exp_006", MemberID: "emp_002", Category: "travel", Amount: 450.00, Quarter: "Q3", Desc: "Customer demo Denver"},
	// Carol
	{ID: "exp_007", MemberID: "emp_003", Category: "travel", Amount: 3200.00, Quarter: "Q3", Desc: "Leadership summit + team visits"},
	{ID: "exp_008", MemberID: "emp_003", Category: "equipment", Amount: 1200.00, Quarter: "Q3", Desc: "Standing desk"},
	// Dan (design, not engineering)
	{ID: "exp_009", MemberID: "emp_004", Category: "travel", Amount: 900.00, Quarter: "Q3", Desc: "Design conference"},
	// Eve (product, not engineering)
	{ID: "exp_010", MemberID: "emp_005", Category: "travel", Amount: 1500.00, Quarter: "Q3", Desc: "Customer research trip"},
}

var budgets = []Budget{
	{Department: "engineering", Quarter: "Q3", Total: 15000.00, Travel: 8000.00, Equipment: 4000.00, Training: 3000.00},
	{Department: "design", Quarter: "Q3", Total: 5000.00, Travel: 2000.00, Equipment: 2000.00, Training: 1000.00},
	{Department: "product", Quarter: "Q3", Total: 8000.00, Travel: 4000.00, Equipment: 2000.00, Training: 2000.00},
}

// ── Tool handlers ────────────────────────────────────────────────────────────

func handleGetTeamMembers(_ context.Context, args map[string]interface{}) (interface{}, error) {
	dept, _ := args["department"].(string)
	if dept == "" {
		return nil, fmt.Errorf("department is required")
	}
	dept = strings.ToLower(dept)

	var result []TeamMember
	for _, m := range teamMembers {
		if m.Department == dept {
			result = append(result, m)
		}
	}
	return map[string]interface{}{
		"department": dept,
		"members":    result,
		"count":      len(result),
	}, nil
}

func handleGetExpenses(_ context.Context, args map[string]interface{}) (interface{}, error) {
	memberID, _ := args["member_id"].(string)
	quarter, _ := args["quarter"].(string)

	if memberID == "" {
		return nil, fmt.Errorf("member_id is required")
	}

	var result []Expense
	for _, e := range expenses {
		if e.MemberID == memberID {
			if quarter != "" && e.Quarter != quarter {
				continue
			}
			result = append(result, e)
		}
	}
	return map[string]interface{}{
		"member_id": memberID,
		"quarter":   quarter,
		"expenses":  result,
		"count":     len(result),
	}, nil
}

func handleGetBudget(_ context.Context, args map[string]interface{}) (interface{}, error) {
	dept, _ := args["department"].(string)
	quarter, _ := args["quarter"].(string)

	if dept == "" {
		return nil, fmt.Errorf("department is required")
	}
	dept = strings.ToLower(dept)

	for _, b := range budgets {
		if b.Department == dept && (quarter == "" || b.Quarter == quarter) {
			return b, nil
		}
	}
	return nil, fmt.Errorf("no budget found for department=%s quarter=%s", dept, quarter)
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Default question (or accept from CLI args)
	question := "Which engineering team members exceeded their Q3 travel budget allocation per person? Show the breakdown."
	if len(os.Args) > 1 {
		question = strings.Join(os.Args[1:], " ")
	}

	fmt.Println("=== PTC Custom Tools Example ===")
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
	fmt.Println("---------------------------")
	fmt.Println()

	registerTools(svc)

	if err := runWithStream(ctx, svc, question); err != nil {
		log.Fatalf("Execution failed: %v", err)
	}
}

// createService initialises the agent with PTC enabled and RAG/MCP/Skills disabled.
// We only want the LLM + PTC sandbox + our custom tools.
func createService() (*agent.Service, error) {
	return agent.New("ExpenseAnalyst").
		WithPTC().
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
}

// ── Typed param structs for the new API ──────────────────────────────────────

type getTeamMembersParams struct {
	Department string `json:"department" desc:"Department name (e.g. engineering, design, product)" required:"true"`
}

type getExpensesParams struct {
	MemberID string `json:"member_id" desc:"Employee ID (e.g. emp_001)" required:"true"`
	Quarter  string `json:"quarter"   desc:"Quarter to filter by (e.g. Q3). Optional."`
}

type getBudgetParams struct {
	Department string `json:"department" desc:"Department name" required:"true"`
	Quarter    string `json:"quarter"    desc:"Quarter (e.g. Q3)"`
}

// registerTools adds the three mock API tools to the service using the new
// ergonomic APIs: struct-based (NewTool) and builder-based (BuildTool).
// They are registered on both the agent (for tool definitions) and
// the PTC router (for callTool() inside the JS sandbox).
func registerTools(svc *agent.Service) {
	// Struct-based (typed) — schema is auto-generated from struct tags.
	svc.Register(agent.NewTool(
		"get_team_members",
		"Get a list of team members in a given department. Returns { department, members: [{ id, name, role, department }], count }.",
		func(ctx context.Context, p *getTeamMembersParams) (any, error) {
			return handleGetTeamMembers(ctx, map[string]interface{}{"department": p.Department})
		},
	))

	svc.Register(agent.NewTool(
		"get_expenses",
		"Get expense records for a team member, optionally filtered by quarter. Returns { member_id, quarter, expenses: [{ id, member_id, category, amount, quarter, description }], count }.",
		func(ctx context.Context, p *getExpensesParams) (any, error) {
			return handleGetExpenses(ctx, map[string]interface{}{"member_id": p.MemberID, "quarter": p.Quarter})
		},
	))

	// Builder-based (fluent) — equivalent schema, different construction style.
	svc.Register(
		agent.BuildTool("get_budget").
			Description("Get budget allocation for a department and quarter. Returns { department, quarter, total, travel, equipment, training } (all amounts in USD).").
			Param("department", agent.TypeString, "Department name", agent.Required()).
			Param("quarter", agent.TypeString, "Quarter (e.g. Q3)").
			Handler(handleGetBudget).
			Build(),
	)
}

// runWithStream executes the question via streaming and prints events in real time.
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
			// Truncate long thinking output
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
