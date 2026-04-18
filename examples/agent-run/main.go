// Package main demonstrates running an AgentGo agent with PTC-enabled custom tools.
//
// This example is intentionally simple:
//   - build an agent from pkg/agent
//   - enable PTC on the agent
//   - register a few custom Go tools
//   - run a question that requires multiple tool calls
//
// Usage:
//
//	go run ./examples/agent-run
//	go run ./examples/agent-run "Calculate the actual engineering travel spend in Q3 from member expenses, then compare it with the department travel budget."
//	DEBUG=1 go run ./examples/agent-run
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

type teamMember struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Department string `json:"department"`
}

type expense struct {
	ID       string  `json:"id"`
	MemberID string  `json:"member_id"`
	Category string  `json:"category"`
	Amount   float64 `json:"amount"`
	Quarter  string  `json:"quarter"`
	Desc     string  `json:"description"`
}

type budget struct {
	Department string  `json:"department"`
	Quarter    string  `json:"quarter"`
	Total      float64 `json:"total"`
	Travel     float64 `json:"travel"`
	Equipment  float64 `json:"equipment"`
	Training   float64 `json:"training"`
}

var teamMembers = []teamMember{
	{ID: "emp_001", Name: "Alice Chen", Role: "Senior Engineer", Department: "engineering"},
	{ID: "emp_002", Name: "Bob Smith", Role: "Staff Engineer", Department: "engineering"},
	{ID: "emp_003", Name: "Carol Davis", Role: "Engineering Manager", Department: "engineering"},
	{ID: "emp_004", Name: "Dan Lee", Role: "Designer", Department: "design"},
	{ID: "emp_005", Name: "Eve Wilson", Role: "Product Manager", Department: "product"},
}

var expenses = []expense{
	{ID: "exp_001", MemberID: "emp_001", Category: "travel", Amount: 1250.00, Quarter: "Q3", Desc: "Conference flight + hotel"},
	{ID: "exp_002", MemberID: "emp_001", Category: "equipment", Amount: 350.00, Quarter: "Q3", Desc: "Mechanical keyboard"},
	{ID: "exp_003", MemberID: "emp_001", Category: "travel", Amount: 800.00, Quarter: "Q3", Desc: "Client visit Seattle"},
	{ID: "exp_004", MemberID: "emp_002", Category: "travel", Amount: 2100.00, Quarter: "Q3", Desc: "Team offsite NYC"},
	{ID: "exp_005", MemberID: "emp_002", Category: "training", Amount: 500.00, Quarter: "Q3", Desc: "Go advanced course"},
	{ID: "exp_006", MemberID: "emp_002", Category: "travel", Amount: 450.00, Quarter: "Q3", Desc: "Customer demo Denver"},
	{ID: "exp_007", MemberID: "emp_003", Category: "travel", Amount: 3200.00, Quarter: "Q3", Desc: "Leadership summit + team visits"},
	{ID: "exp_008", MemberID: "emp_003", Category: "equipment", Amount: 1200.00, Quarter: "Q3", Desc: "Standing desk"},
	{ID: "exp_009", MemberID: "emp_004", Category: "travel", Amount: 900.00, Quarter: "Q3", Desc: "Design conference"},
	{ID: "exp_010", MemberID: "emp_005", Category: "travel", Amount: 1500.00, Quarter: "Q3", Desc: "Customer research trip"},
}

var budgets = []budget{
	{Department: "engineering", Quarter: "Q3", Total: 15000.00, Travel: 8000.00, Equipment: 4000.00, Training: 3000.00},
	{Department: "design", Quarter: "Q3", Total: 5000.00, Travel: 2000.00, Equipment: 2000.00, Training: 1000.00},
	{Department: "product", Quarter: "Q3", Total: 8000.00, Travel: 4000.00, Equipment: 2000.00, Training: 2000.00},
}

type getTeamMembersParams struct {
	Department string `json:"department" desc:"Department name (for example engineering, design, product)." required:"true"`
}

type getExpensesParams struct {
	MemberID string `json:"member_id" desc:"Employee ID (for example emp_001)." required:"true"`
	Quarter  string `json:"quarter" desc:"Quarter to filter by (for example Q3)."`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	question := "Calculate the actual engineering travel spend in Q3 from member expenses, then compare it with the department travel budget. Show the per-person travel breakdown and answer in plain English."
	if len(os.Args) > 1 {
		question = strings.Join(os.Args[1:], " ")
	}

	fmt.Println("=== Agent Run with PTC Tools ===")
	fmt.Printf("Question: %s\n\n", question)

	svc, err := agent.New("agent-run-demo").
		WithPTC().
		WithSystemPrompt("You are a finance operations agent. Use the available tools to compute answers from data instead of guessing. When comparing spend and budget, calculate the actual spend from expense records first, then compare against the budget tool result. If you use PTC code, return a readable plain-English answer with the key totals and per-person breakdown instead of a raw object.").
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
	if err != nil {
		log.Fatalf("failed to build agent service: %v", err)
	}
	defer svc.Close()

	registerTools(svc)

	info := svc.Info()
	fmt.Println("--- Agent Configuration ---")
	fmt.Printf("  Model:    %s\n", info.Model)
	fmt.Printf("  Base URL: %s\n", info.BaseURL)
	fmt.Printf("  PTC:      %v\n", info.PTCEnabled)
	fmt.Println("---------------------------")
	fmt.Println()

	events, err := svc.RunStream(ctx, question)
	if err != nil {
		log.Fatalf("RunStream failed: %v", err)
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
			if evt.ToolName == "task_complete" || evt.ToolName == "task_blocked" {
				fmt.Printf("[tool_result] %s -> %v\n", evt.ToolName, evt.ToolResult)
				continue
			}
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
			log.Fatalf("agent error: %s", evt.Content)
		case agent.EventTypeHandoff:
			fmt.Printf("[handoff] %s\n", evt.Content)
		}
	}
}

func registerTools(svc *agent.Service) {
	svc.Register(agent.NewTool(
		"get_team_members",
		"Get a list of team members in a department. Returns { department, members: [{ id, name, role, department }], count }.",
		func(_ context.Context, p *getTeamMembersParams) (any, error) {
			if strings.TrimSpace(p.Department) == "" {
				return nil, fmt.Errorf("department is required")
			}
			dept := strings.ToLower(strings.TrimSpace(p.Department))

			result := make([]teamMember, 0)
			for _, member := range teamMembers {
				if member.Department == dept {
					result = append(result, member)
				}
			}

			return map[string]any{
				"department": dept,
				"members":    result,
				"count":      len(result),
			}, nil
		},
	))

	svc.Register(agent.NewTool(
		"get_expenses",
		"Get expense records for a team member, optionally filtered by quarter. Returns { member_id, quarter, expenses: [{ id, member_id, category, amount, quarter, description }], count }.",
		func(_ context.Context, p *getExpensesParams) (any, error) {
			memberID := strings.TrimSpace(p.MemberID)
			if memberID == "" {
				return nil, fmt.Errorf("member_id is required")
			}
			quarter := strings.ToUpper(strings.TrimSpace(p.Quarter))

			result := make([]expense, 0)
			for _, item := range expenses {
				if item.MemberID != memberID {
					continue
				}
				if quarter != "" && item.Quarter != quarter {
					continue
				}
				result = append(result, item)
			}

			return map[string]any{
				"member_id": memberID,
				"quarter":   quarter,
				"expenses":  result,
				"count":     len(result),
			}, nil
		},
	))

	svc.Register(
		agent.BuildTool("get_budget").
			Description("Get budget allocation for a department and quarter. Returns { department, quarter, total, travel, equipment, training } in USD.").
			Param("department", agent.TypeString, "Department name", agent.Required()).
			Param("quarter", agent.TypeString, "Quarter (for example Q3)").
			Handler(func(_ context.Context, args map[string]any) (any, error) {
				dept, _ := args["department"].(string)
				quarter, _ := args["quarter"].(string)
				dept = strings.ToLower(strings.TrimSpace(dept))
				quarter = strings.ToUpper(strings.TrimSpace(quarter))
				if dept == "" {
					return nil, fmt.Errorf("department is required")
				}

				for _, item := range budgets {
					if item.Department == dept && (quarter == "" || item.Quarter == quarter) {
						return item, nil
					}
				}

				return nil, fmt.Errorf("no budget found for department=%s quarter=%s", dept, quarter)
			}).
			Build(),
	)
}
