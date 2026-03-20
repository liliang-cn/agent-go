package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	"github.com/liliang-cn/agent-go/v2/pkg/rag"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
	"github.com/spf13/cobra"
)

var (
	Cfg            *config.Config
	Verbose        bool
	Debug          bool // New debug flag
	EnablePTC      bool // Enable Programmatic Tool Calling
	skillsService  *skills.Service
	skillsInitOnce sync.Once
	skillsInitErr  error
)

// SetSharedVariables sets the shared variables from the root command
func SetSharedVariables(c *config.Config, v bool) {
	Cfg = c
	Verbose = v
}

// AgentCmd is the main agent command
var AgentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Run agents and manage standalone agent profiles",
	Long: `Run agent tasks, planning, and execution, or manage standalone agents.

An agent can work independently, or it can join a squad with a captain or specialist role.`,
}

// runCmd runs an agent task
var runCmd = &cobra.Command{
	Use:   "run [goal]",
	Short: "Run an agent task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		goal := args[0]
		ctx := context.Background()

		// Use the new Event-Driven Stream Runner
		return runStream(ctx, goal)
	},
}

// executeCmd executes an existing plan by ID
var executeCmd = &cobra.Command{
	Use:   "execute [plan-id]",
	Short: "Execute an existing plan",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		planID := args[0]
		ctx := context.Background()

		_, agentService, err := initAgentServices(ctx)
		if err != nil {
			return err
		}
		defer agentService.Close()

		// Get the plan
		plan, err := agentService.GetPlan(planID)
		if err != nil {
			return fmt.Errorf("plan not found: %w", err)
		}

		fmt.Printf("🎯 Executing Plan: %s\n", plan.ID)
		fmt.Printf("📋 Goal: %s\n\n", plan.Goal)

		// Execute the plan
		result, err := agentService.ExecutePlan(ctx, plan)
		if err != nil {
			return fmt.Errorf("execution failed: %w", err)
		}

		// Print result
		fmt.Println("\n--- Results ---")
		if result.FinalResult != nil {
			fmt.Printf("\n--- Final Result ---\n%v\n", result.FinalResult)
		}
		if result.StartedAt != nil {
			fmt.Printf("Started: %s\n", result.StartedAt.Format("2006-01-02 15:04:05"))
		}
		if result.CompletedAt != nil {
			fmt.Printf("Completed: %s\n", result.CompletedAt.Format("2006-01-02 15:04:05"))
		}
		if result.EstimatedTokens > 0 {
			fmt.Printf("Estimated tokens: %d\n", result.EstimatedTokens)
		}
		if result.ToolCalls > 0 {
			fmt.Printf("Tool calls: %d\n", result.ToolCalls)
		}
		if result.Duration != "" {
			fmt.Printf("Duration: %s\n", result.Duration)
		}
		fmt.Printf("Steps: %d done, %d failed\n", result.StepsDone, result.StepsFailed)

		return nil
	},
}

// planCmd is the parent command for plan operations
var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Plan operations",
}

var planCreateCmd = &cobra.Command{
	Use:   "create [goal]",
	Short: "Create an agent plan (without execution)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		goal := args[0]
		ctx := context.Background()

		_, agentService, err := initAgentServices(ctx)
		if err != nil {
			return err
		}
		defer agentService.Close()

		// Use agent service's Plan method
		plan, err := agentService.Plan(ctx, goal)
		if err != nil {
			return fmt.Errorf("planning failed: %w", err)
		}

		// Print plan
		fmt.Printf("📋 Plan ID: %s\n", plan.ID)
		fmt.Printf("Goal: %s\n\n", plan.Goal)
		fmt.Println("Steps:")
		for _, step := range plan.Steps {
			fmt.Printf("  [%s] %s\n  └─ Tool: %s\n", step.ID, step.Description, step.Tool)
		}

		return nil
	},
}

var planListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agent plans",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		_, agentService, err := initAgentServices(ctx)
		if err != nil {
			return err
		}
		defer agentService.Close()

		plans, err := agentService.ListPlans("", 20)
		if err != nil {
			return fmt.Errorf("failed to list plans: %w", err)
		}

		if len(plans) == 0 {
			fmt.Println("No plans found")
			return nil
		}

		fmt.Println("Agent Plans:")
		for _, p := range plans {
			fmt.Printf("  [%s] %s\n", p.ID, p.Goal)
			fmt.Printf("     Status: %s | Steps: %d | Created: %s\n",
				p.Status, len(p.Steps), p.CreatedAt.Format("2006-01-02 15:04"))
		}

		return nil
	},
}

var planGetCmd = &cobra.Command{
	Use:   "get [plan-id]",
	Short: "Get plan details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		planID := args[0]
		ctx := context.Background()

		_, agentService, err := initAgentServices(ctx)
		if err != nil {
			return err
		}
		defer agentService.Close()

		plan, err := agentService.GetPlan(planID)
		if err != nil {
			return fmt.Errorf("plan not found: %w", err)
		}

		fmt.Printf("📋 Plan ID: %s\n", plan.ID)
		fmt.Printf("Goal: %s\n", plan.Goal)
		fmt.Printf("Status: %s\n", plan.Status)
		fmt.Printf("Created: %s\n", plan.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("\nSteps:\n")
		for i, step := range plan.Steps {
			status := "✓"
			if step.Status != "completed" {
				status = "✗"
			}
			fmt.Printf("  %d. [%s] %s\n", i+1, status, step.Description)
			fmt.Printf("     Tool: %s\n", step.Tool)
			if step.Error != "" {
				fmt.Printf("     Error: %s\n", step.Error)
			}
		}

		return nil
	},
}

// sessionCmd manages agent sessions
var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage agent sessions",
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agent sessions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		_, agentService, err := initAgentServices(ctx)
		if err != nil {
			return err
		}
		defer agentService.Close()

		sessions, err := agentService.ListSessions(20)
		if err != nil {
			return fmt.Errorf("failed to list sessions: %w", err)
		}

		if len(sessions) == 0 {
			fmt.Println("No sessions found")
			return nil
		}

		fmt.Println("Agent Sessions:")
		for _, s := range sessions {
			fmt.Printf("  [%s] %s - %d messages\n", s.ID, s.CreatedAt.Format("2006-01-02 15:04"), len(s.GetMessages()))
		}

		return nil
	},
}

var sessionGetCmd = &cobra.Command{
	Use:   "get [session-id]",
	Short: "Get session details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sessionID := args[0]

		ctx := context.Background()
		_, agentService, err := initAgentServices(ctx)
		if err != nil {
			return err
		}
		defer agentService.Close()

		session, err := agentService.GetSession(sessionID)
		if err != nil {
			return fmt.Errorf("failed to get session: %w", err)
		}

		fmt.Printf("Session ID: %s\n", session.GetID())
		fmt.Printf("Created: %s\n", session.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("Updated: %s\n", session.UpdatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("Messages: %d\n", len(session.GetMessages()))

		return nil
	},
}

// reviseCmd revises an existing plan
var reviseCmd = &cobra.Command{
	Use:   "revise [plan-id] [instruction]",
	Short: "Revise an existing plan with natural language",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		planID := args[0]
		instruction := args[1]
		ctx := context.Background()

		_, agentService, err := initAgentServices(ctx)
		if err != nil {
			return err
		}
		defer agentService.Close()

		// Get the original plan
		plan, err := agentService.GetPlan(planID)
		if err != nil {
			return fmt.Errorf("plan not found: %w", err)
		}

		fmt.Printf("📋 Original Plan: %s\n\n", plan.Goal)
		fmt.Println("Steps:")
		for i, step := range plan.Steps {
			fmt.Printf("  %d. [%s] %s\n", i+1, step.Tool, step.Description)
		}

		// Revise the plan
		fmt.Printf("\n✏️  Revising with: %s\n\n", instruction)
		revised, err := agentService.RevisePlan(ctx, plan, instruction)
		if err != nil {
			return fmt.Errorf("revision failed: %w", err)
		}

		fmt.Printf("📋 Revised Plan ID: %s\n", revised.ID)
		fmt.Printf("Goal: %s\n\n", revised.Goal)
		fmt.Println("Revised Steps:")
		for i, step := range revised.Steps {
			fmt.Printf("  %d. [%s] %s\n", i+1, step.Tool, step.Description)
		}

		return nil
	},
}

// ptcChatCmd runs a PTC-enabled chat
var ptcChatCmd = &cobra.Command{
	Use:   "ptc-chat [message]",
	Short: "Chat with PTC (Programmatic Tool Calling) support",
	Long: `Chat with the agent using PTC mode. The LLM can generate JavaScript code
instead of JSON tool calls, which will be executed in a secure sandbox.

Example:
  agentgo agent ptc-chat "Write code to search for documents and process results"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		message := args[0]
		ctx := context.Background()

		// Initialize agent services
		_, agentService, err := initAgentServices(ctx)
		if err != nil {
			return err
		}
		defer agentService.Close()

		// Create and configure PTC integration
		ptcConfig := agent.DefaultPTCConfig()
		ptcConfig.Enabled = true
		ptcConfig.MaxToolCalls = 20
		ptcConfig.Timeout = 30 * 1000000000 // 30 seconds in nanoseconds
		ptcConfig.Runtime = "goja"

		// Create PTC router with agent services
		router := ptc.NewAgentGoRouter(
			ptc.WithRAGProcessor(agentService.RAG),
			ptc.WithMCPService(agentService.MCP),
		)

		ptcIntegration, err := agent.NewPTCIntegration(ptcConfig, router)
		if err != nil {
			return fmt.Errorf("failed to create PTC integration: %w", err)
		}

		// Set PTC on agent service
		agentService.SetPTC(ptcIntegration)

		fmt.Printf("💬 PTC Chat: %s\n\n", message)

		// Run PTC chat
		result, err := agentService.ChatWithPTC(ctx, message)
		if err != nil {
			return fmt.Errorf("PTC chat failed: %w", err)
		}

		// Display results
		fmt.Println("--- Response ---")
		if result.PTCUsed && result.PTCResult != nil {
			fmt.Printf("PTC Mode: Enabled\n")
			fmt.Printf("Result Type: %s\n\n", result.PTCResult.Type)
			fmt.Println(result.PTCResult.FormatForLLM())
		} else {
			fmt.Println(result.LLMResponse)
		}

		fmt.Printf("\nSession ID: %s\n", result.SessionID)

		return nil
	},
}

func init() {
	runCmd.Flags().BoolVarP(&Debug, "debug", "D", false, "Enable verbose debugging output (show full prompts)")
	runCmd.Flags().BoolVar(&EnablePTC, "ptc", false, "Enable Programmatic Tool Calling (JS sandbox)")
	runCmd.Flags().StringVar(&runAgentName, "agent", "", "run a stored agent by name")
	executeCmd.Flags().BoolVar(&EnablePTC, "ptc", false, "Enable Programmatic Tool Calling (JS sandbox)")
	AgentCmd.AddCommand(runCmd)
	AgentCmd.AddCommand(agentListCmd)
	AgentCmd.AddCommand(agentShowCmd)
	AgentCmd.AddCommand(agentAddCmd)
	AgentCmd.AddCommand(agentUpdateCmd)
	AgentCmd.AddCommand(agentDeleteCmd)
	AgentCmd.AddCommand(agentJoinCmd)
	AgentCmd.AddCommand(agentLeaveCmd)
	AgentCmd.AddCommand(planCmd)
	planCmd.AddCommand(planCreateCmd)
	planCmd.AddCommand(planListCmd)
	planCmd.AddCommand(planGetCmd)
	AgentCmd.AddCommand(executeCmd)
	AgentCmd.AddCommand(reviseCmd)
	AgentCmd.AddCommand(sessionCmd)
	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionGetCmd)
	AgentCmd.AddCommand(ptcChatCmd)

	agentAddCmd.Flags().StringVar(&agentDescription, "description", "", "agent description")
	agentAddCmd.Flags().StringVar(&agentInstructions, "instructions", "", "agent system instructions")
	agentAddCmd.Flags().StringVar(&agentProvider, "provider", "", "preferred LLM provider")
	agentAddCmd.Flags().StringVar(&agentModel, "model", "", "preferred LLM model")

	agentUpdateCmd.Flags().StringVar(&agentUpdateName, "name", "", "rename the agent")
	agentUpdateCmd.Flags().StringVar(&agentDescription, "description", "", "new agent description")
	agentUpdateCmd.Flags().StringVar(&agentInstructions, "instructions", "", "new agent system instructions")
	agentUpdateCmd.Flags().StringVar(&agentProvider, "provider", "", "new preferred LLM provider")
	agentUpdateCmd.Flags().StringVar(&agentModel, "model", "", "new preferred LLM model")
	agentUpdateCmd.Flags().StringVar(&agentUpdateRole, "role", "", "set role to agent, captain, or specialist")

	agentJoinCmd.Flags().StringVar(&agentUpdateRole, "role", "specialist", "role inside the squad: specialist or captain")
	agentJoinCmd.Flags().StringVar(&agentUpdateSquadID, "squad-id", "", "target squad ID")
	agentJoinCmd.Flags().StringVar(&agentUpdateSquadName, "squad", "", "target squad name")
}

// initAgentServices initializes RAG client and agent service
func initAgentServices(ctx context.Context) (*rag.Client, *agent.Service, error) {
	// Initialize using agent Builder
	var agentService *agent.Service
	var buildErr error

	b := agent.New("AgentGo Frontdesk").
		WithSystemPrompt("You are the system Frontdesk and captain agent. You can interact with users, and delegate tasks to specialized agents using the tools provided.").
		WithRAG().
		WithMCP().
		WithMemory().
		WithRouter().
		WithSkills()

	if EnablePTC {
		b = b.WithPTC()
	}

	if Debug {
		b = b.WithDebug()
	}

	agentService, buildErr = b.Build()
	if buildErr != nil {
		return nil, nil, fmt.Errorf("failed to init agent: %w", buildErr)
	}

	// Initialize TeamManager
	if Cfg != nil {
		agentDBPath := Cfg.AgentDBPath()
		agentStore, storeErr := agent.NewStore(agentDBPath)
		if storeErr == nil {
			agentManager := agent.NewTeamManager(agentStore)
			agentManager.SetConfig(Cfg)
			_ = agentManager.SeedDefaultMembers()
			agentManager.RegisterCommanderTools(agentService)
		}
	}

	// For backward compatibility with existing code that needs ragClient
	var ragClient *rag.Client

	return ragClient, agentService, nil
}

type streamRenderState struct {
	currentRound     int
	hasPartialOutput bool
}

func renderStreamEvent(w io.Writer, evt *agent.Event, state *streamRenderState) {
	switch evt.Type {
	case agent.EventTypeStart:
		fmt.Fprintf(w, "🚀 %s\n", evt.Content)
	case agent.EventTypeThinking:
		state.currentRound++
		fmt.Fprintf(w, "\n🔄 [Round %d] Thinking...\n", state.currentRound)
		if evt.ToolResult != nil && evt.Content != "Thinking..." {
			fmt.Fprintf(w, "💭 %s\n", evt.Content)
		}
	case agent.EventTypeToolCall:
		fmt.Fprintf(w, "🛠️  Using Tool: %s (args: %v)\n", evt.ToolName, evt.ToolArgs)
	case agent.EventTypeToolResult:
		// task_complete already maps to the final assistant answer shown on complete.
		if evt.ToolName == "task_complete" {
			return
		}
		fmt.Fprintf(w, "✅ Tool Success: %s\n", evt.ToolName)
		if evt.ToolResult != nil {
			fmt.Fprintf(w, "📝 Result: %v\n", evt.ToolResult)
		}
	case agent.EventTypeHandoff:
		fmt.Fprintf(w, "🔀 Handoff: %s\n", evt.Content)
	case agent.EventTypePartial:
		fmt.Fprint(w, evt.Content)
		state.hasPartialOutput = true
	case agent.EventTypeComplete:
		if !state.hasPartialOutput && evt.Content != "" {
			fmt.Fprintf(w, "\n%s", evt.Content)
		}
		fmt.Fprint(w, "\n\n🏁 Task Completed!\n")
		if len(evt.Sources) > 0 {
			fmt.Fprint(w, "\n📚 Sources:\n")
			for i, src := range evt.Sources {
				preview := src.Content
				if len(preview) > 100 {
					preview = preview[:100] + "..."
				}
				fmt.Fprintf(w, "  %d. %s\n", i+1, preview)
			}
		}
	case agent.EventTypeDebug:
		label := strings.ToUpper(evt.DebugType)
		sep := strings.Repeat("─", 60)
		fmt.Fprintf(w, "\n\033[2m%s\n🐛 DEBUG [Round %d] %s\n%s\n%s\n%s\033[0m\n",
			sep, evt.Round, label, sep, evt.Content, sep)
	case agent.EventTypeError:
		fmt.Fprintf(w, "\n❌ Error: %s\n", evt.Content)
	}
}

// runStream runs the agent with Event Loop streaming output
func runStream(ctx context.Context, goal string) error {
	fmt.Printf("🎯 Agent Goal: %s\n\n", goal)

	ragClient, agentService, err := initRunnableAgentService(ctx, strings.TrimSpace(runAgentName))
	if err != nil {
		return err
	}
	if ragClient != nil {
		defer ragClient.Close()
	}
	defer agentService.Close()

	// Start streaming
	events, err := agentService.RunStreamWithOptions(ctx, goal, agent.WithDebug(Debug))
	if err != nil {
		return err
	}

	// Consume events
	out := io.Writer(os.Stdout)
	state := &streamRenderState{}
	for evt := range events {
		renderStreamEvent(out, evt, state)
	}

	return nil
}

func initRunnableAgentService(ctx context.Context, selectedAgentName string) (*rag.Client, *agent.Service, error) {
	if selectedAgentName == "" {
		return initAgentServices(ctx)
	}
	manager, err := getManager()
	if err != nil {
		return nil, nil, err
	}
	svc, err := manager.GetAgentService(selectedAgentName)
	if err != nil {
		return nil, nil, err
	}
	svc.SetDebug(Debug)
	return nil, svc, nil
}
