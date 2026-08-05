package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/ptc"
	"github.com/liliang-cn/agent-go/v3/pkg/rag"
	"github.com/liliang-cn/agent-go/v3/pkg/skills"
	"github.com/spf13/cobra"
)

var (
	Cfg            *config.Config
	Verbose        bool
	Debug          bool // New debug flag
	EnablePTC      bool // Legacy compatibility flag; PTC is enabled by default.
	DisablePTC     bool // Disable Programmatic Tool Calling
	skillsService  *skills.Service
	skillsInitOnce sync.Once
	skillsInitErr  error

	// Structured-output flags for `agent run`. Load a JSON Schema from a
	// file and the runtime enforces it on the final answer (Tier A lint +
	// Tier B response_format).
	runSchemaFile   string
	runSchemaName   string
	runSchemaStrict bool
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

An agent can work independently, or it can join a team with a orchestrator or specialist role.`,
}

// runCmd runs an agent task
var runCmd = &cobra.Command{
	Use:   "run [goal]",
	Short: "Run an agent task",
	Long: `Run one agent task through the AgentGo runtime.

PTC (Programmatic Tool Calling) is enabled by default. Use --no-ptc only when
you need legacy direct function-calling behavior.

Pass --schema FILE to constrain the agent's final answer to a JSON Schema.
The runtime validates the response against the schema and re-prompts on
mismatch (works on every provider). Providers that support OpenAI
structured outputs additionally receive response_format for one-shot
compliant JSON.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if EnablePTC && DisablePTC {
			return fmt.Errorf("use either --ptc or --no-ptc, not both")
		}
		goal := args[0]
		ctx := context.Background()

		structSpec, err := loadStructuredOutputSpec(runSchemaFile, runSchemaName, runSchemaStrict)
		if err != nil {
			return err
		}

		// Use the new Event-Driven Stream Runner
		return runStream(ctx, goal, structSpec)
	},
}

// loadStructuredOutputSpec reads a JSON Schema from disk and packages it
// as a StructuredOutputSpec for WithStructuredOutput. Returns nil when no
// schema path is set so callers can pass it through unchecked.
func loadStructuredOutputSpec(path, name string, strict bool) (*agent.StructuredOutputSpec, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema file %q: %w", path, err)
	}
	// Validate the file parses as JSON so we fail at flag-parse time rather
	// than after the model has already started a turn.
	var probe interface{}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("schema file %q is not valid JSON: %w", path, err)
	}
	resolvedName := strings.TrimSpace(name)
	if resolvedName == "" {
		base := path
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		if idx := strings.LastIndex(base, "."); idx > 0 {
			base = base[:idx]
		}
		resolvedName = base
	}
	return &agent.StructuredOutputSpec{
		Name:   resolvedName,
		Schema: json.RawMessage(raw),
		Strict: strict,
	}, nil
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

// ptcChatCmd runs a PTC-enabled chat
var ptcChatCmd = &cobra.Command{
	Use:   "ptc-chat [message]",
	Short: "Chat with PTC (Programmatic Tool Calling) support",
	Long: `Chat with the agent using PTC mode. The LLM can generate JavaScript code
instead of JSON tool calls, which will be executed in a secure sandbox.

Example:
  agentgo agent ptc-chat "Write code to search for documents and process results"

Note: PTC is already enabled by default for agent run/chat; this command is a
compatibility shortcut that always forces PTC mode.`,
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
	runCmd.Flags().BoolVar(&EnablePTC, "ptc", false, "Force Programmatic Tool Calling on (default; compatibility flag)")
	runCmd.Flags().BoolVar(&DisablePTC, "no-ptc", false, "Disable Programmatic Tool Calling and use direct function calling")
	runCmd.Flags().StringVar(&runAgentName, "agent", "", "run a stored agent by name")
	runCmd.Flags().StringVar(&runSchemaFile, "schema", "", "constrain final answer to a JSON Schema (path to a .json file)")
	runCmd.Flags().StringVar(&runSchemaName, "schema-name", "", "schema name (defaults to the schema filename); used by OpenAI structured outputs")
	runCmd.Flags().BoolVar(&runSchemaStrict, "schema-strict", false, "enable strict mode: block the task when the model can't produce schema-compliant JSON")
	AgentCmd.AddCommand(runCmd)
	AgentCmd.AddCommand(agentListCmd)
	AgentCmd.AddCommand(agentShowCmd)
	AgentCmd.AddCommand(agentAddCmd)
	AgentCmd.AddCommand(agentUpdateCmd)
	AgentCmd.AddCommand(agentDeleteCmd)
	AgentCmd.AddCommand(sessionCmd)
	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionGetCmd)
	AgentCmd.AddCommand(ptcChatCmd)

	agentAddCmd.Flags().StringVar(&agentDescription, "description", "", "agent description")
	agentAddCmd.Flags().StringVar(&agentInstructions, "instructions", "", "agent system instructions")
	agentAddCmd.Flags().StringVar(&agentProvider, "provider", "", "preferred LLM provider")
	agentAddCmd.Flags().StringVar(&agentModel, "model", "", "preferred LLM model")
	agentAddCmd.Flags().StringVar(&agentMemoryType, "memory-type", "", "memory store type: file, cortex, memoryflow, graphflow")
	agentAddCmd.Flags().BoolVar(&agentA2AEnabled, "a2a", false, "enable A2A exposure for this standalone agent")

	agentUpdateCmd.Flags().StringVar(&agentUpdateName, "name", "", "rename the agent")
	agentUpdateCmd.Flags().StringVar(&agentDescription, "description", "", "new agent description")
	agentUpdateCmd.Flags().StringVar(&agentInstructions, "instructions", "", "new agent system instructions")
	agentUpdateCmd.Flags().StringVar(&agentProvider, "provider", "", "new preferred LLM provider")
	agentUpdateCmd.Flags().StringVar(&agentModel, "model", "", "new preferred LLM model")
	agentUpdateCmd.Flags().StringVar(&agentUpdateRole, "role", "", "set role to agent, orchestrator, or specialist")
	agentUpdateCmd.Flags().StringVar(&agentMemoryType, "memory-type", "", "memory store type: file, cortex, memoryflow, graphflow")
	agentUpdateCmd.Flags().BoolVar(&agentA2AEnabled, "a2a", false, "explicitly enable or disable A2A exposure for this standalone agent")

}

// initAgentServices initializes RAG client and agent service
func initAgentServices(ctx context.Context) (*rag.Client, *agent.Service, error) {
	// Initialize using agent Builder
	var agentService *agent.Service
	var buildErr error

	b := agent.New("AgentGo").
		WithSystemPrompt("You are a capable, direct assistant. Use the tools you have to finish the task, then answer.").
		WithRAG().
		WithMCP().
		WithMemory().
		WithSkills()

	switch {
	case EnablePTC && DisablePTC:
		return nil, nil, fmt.Errorf("use either --ptc or --no-ptc, not both")
	case DisablePTC:
		b = b.WithPTC(false)
	case EnablePTC:
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
			agentManager := agent.NewManager(agentStore)
			agentManager.SetConfig(Cfg)
			_ = agentManager.SeedDefaultAgent()
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
		// Terminal task tools map to final task events.
		if evt.ToolName == "task_complete" || evt.ToolName == "task_blocked" {
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
	case agent.EventTypeBlocked:
		if !state.hasPartialOutput && evt.Content != "" {
			fmt.Fprintf(w, "\n%s", evt.Content)
		}
		fmt.Fprint(w, "\n\n⛔ Task Blocked.\n")
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

// runStream runs the agent with Event Loop streaming output. When
// structSpec is non-nil the runtime constrains the final answer to the
// supplied JSON Schema (Tier A lint + Tier B response_format).
func runStream(ctx context.Context, goal string, structSpec *agent.StructuredOutputSpec) error {
	fmt.Printf("🎯 Agent Goal: %s\n\n", goal)
	if structSpec != nil {
		strict := ""
		if structSpec.Strict {
			strict = " (strict)"
		}
		fmt.Printf("📐 Structured output: schema=%s%s\n\n", structSpec.Name, strict)
	}

	ragClient, agentService, err := initRunnableAgentService(ctx, strings.TrimSpace(runAgentName))
	if err != nil {
		return err
	}
	if ragClient != nil {
		defer ragClient.Close()
	}
	defer agentService.Close()

	opts := []agent.RunOption{agent.WithDebug(Debug)}
	if structSpec != nil {
		opts = append(opts, agent.WithStructuredOutput(structSpec))
	}

	// Start streaming
	events, err := agentService.RunStreamWithOptions(ctx, goal, opts...)
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
	if EnablePTC && DisablePTC {
		return nil, nil, fmt.Errorf("use either --ptc or --no-ptc, not both")
	}
	manager, err := getManager()
	if err != nil {
		return nil, nil, err
	}
	svc, err := manager.Service(selectedAgentName)
	if err != nil {
		return nil, nil, err
	}
	svc.SetDebug(Debug)
	if DisablePTC {
		svc.SetPTC(nil)
	}
	return nil, svc, nil
}
