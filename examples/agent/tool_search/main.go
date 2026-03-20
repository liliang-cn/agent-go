package main

import (
	"context"
	"fmt"
	"log"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

func main() {
	ctx := context.Background()

	// 1. Load configuration (from agentgo.toml or environment)
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 2. Build the Agent
	fmt.Println("=== Building Agent with Tool Search ===")
	builder := agent.New("ToolSearcher").
		WithConfig(cfg)

	// 3. Register Deferred Tools (Simulating a large catalog of tools)

	// We will register a few normal tools and several deferred tools.

	// Tool 1: Weather (Deferred)
	weatherTool := agent.BuildTool("check_climate_status").
		Description("Get the current weather for a specific location. Use this to check weather conditions.").
		Param("location", agent.TypeString, "City name, e.g., 'Tokyo'").
		Deferred(true). // <--- KEY: This marks it as not loaded initially
		Handler(func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			loc := args["location"].(string)
			return fmt.Sprintf("The climate in %s is sunny and 22°C.", loc), nil
		}).
		Build()

	// Tool 2: Flight Booking (Deferred)
	flightTool := agent.BuildTool("reserve_airline_ticket").
		Description("Book a flight to a destination. Use this for travel arrangements.").
		Param("destination", agent.TypeString, "City to fly to").
		Deferred(true).
		Handler(func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			dest := args["destination"].(string)
			return fmt.Sprintf("Ticket to %s reserved successfully.", dest), nil
		}).
		Build()

	// Tool 3: Expense Calculator (Deferred)
	expenseTool := agent.BuildTool("calculate_expenses").
		Description("Calculate business travel expenses. Use this for accounting.").
		Param("employee_id", agent.TypeString, "Employee ID").
		Deferred(true).
		Handler(func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			empID := args["employee_id"].(string)
			return fmt.Sprintf("Total expenses for employee %s: $1,250.00", empID), nil
		}).
		Build()

	// 4. Attach tools and build service
	agentSvc, err := builder.
		WithTools(weatherTool, flightTool, expenseTool).
		Build()

	if err != nil {
		log.Fatalf("Failed to build agent: %v", err)
	}

	// 5. Query the Agent
	fmt.Println("\n=== Starting Query ===")
	// The LLM will use the `search_available_tools` providing an instruction,
	// so the tool will directly execute it.

	query := "Can you calculate the expenses for employee 'EMP123'? Use search_available_tools with an instruction to do it in one step."

	fmt.Printf("User: %s\n\n", query)

	resp, err := agentSvc.Chat(ctx, query)
	if err != nil {
		log.Fatalf("Chat failed: %v", err)
	}

	fmt.Printf("\n=== Final Response ===\n%s\n", resp.Text())
}
