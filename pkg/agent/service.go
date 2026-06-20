package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/browser"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v2/pkg/log"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	memorypkg "github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	"github.com/liliang-cn/agent-go/v2/pkg/router"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
	"github.com/liliang-cn/agent-go/v2/pkg/usage"
)

// ProgressEvent 进度事件
type ProgressEvent struct {
	Type    string // "thinking", "tool_call", "tool_result", "done"
	Round   int
	Message string
	Tool    string
}

// ProgressCallback 进度回调函数
type ProgressCallback func(ProgressEvent)

// Service is the main agent service that handles planning and execution
// This matches the interface expected by the CLI in cmd/agentgo-cli/agent/agent.go
type Service struct {
	debug                 bool
	llmService            domain.Generator
	mcpService            MCPToolExecutor
	ragProcessor          domain.Processor
	memoryService         domain.MemoryService
	skillsService         *skills.Service
	routerService         *router.Service // Semantic Router for fast intent recognition
	promptManager         *prompt.Manager // Central prompt management
	planner               *Planner
	executor              *Executor
	store                 *Store
	agent                 *Agent
	registry              *Registry
	logger                *slog.Logger
	cancelMu              sync.RWMutex
	cancelFunc            context.CancelFunc
	progressCb            ProgressCallback
	currentSessionID      string // Auto-generated UUID for Chat() method
	sessionMu             sync.RWMutex
	memoryStoreType       string
	memoryScopeAgentID    string
	memoryScopeTeamID     string
	memoryScopeUserID     string
	memorySaveMu          sync.RWMutex
	memorySavedInRun      bool
	ragSourcesMu          sync.RWMutex
	ragSources            []domain.Chunk // Collect RAG sources during execution
	isRunning             bool
	statusMu              sync.RWMutex
	permissionMu          sync.RWMutex
	permissionHandler     PermissionHandler
	permissionPolicy      PermissionPolicy
	inProgressToolsMu     sync.RWMutex
	inProgressTools       map[string]int
	relevantSkillsMu      sync.RWMutex
	sessionRelevantSkills map[string][]string
	skillPolicyMu         sync.RWMutex
	taskSkillSatisfied    map[string]bool

	// Model metadata for Info()
	modelName     string
	baseURL       string
	isFastModel   bool
	contextWindow int // Optional: context window size (0 = unknown, use default)

	// Hook system for lifecycle events
	hooks *HookRegistry

	// Subconscious background worker pool
	subconscious *SubconsciousWorkerPool

	// Async sub-agent coordinator
	asyncTasks *SubAgentCoordinator

	// Stop-hook tracking: maps StopHookConfig registrations to their hook IDs
	// in the registry so UnregisterStopHooks can remove them.
	stopHookMu  sync.Mutex
	stopHookIDs []string

	// toolRegistry is the unified registry for custom, RAG, and Memory tools.
	// All modules register here so that both LLM listing and PTC callTool()
	// dispatch go through a single source of truth.
	toolRegistry *ToolRegistry

	// PTC (Programmatic Tool Calling) integration
	ptcIntegration *PTCIntegration

	// Execution history storage
	historyStore *HistoryStore

	// outputLints is the registry of post-output lint rules consulted by the
	// runtime before emitting a final completion event. Lazily initialized via
	// OutputLints(); see pkg/agent/output_lint.go.
	outputLintsMu sync.RWMutex
	outputLints   *OutputLintRegistry

	// checkpointSink, when non-nil, is called by the runtime at every
	// round boundary so the message history can be persisted for
	// Tasks().Resume. TeamManager.buildServiceForModel wires this up;
	// services built directly via agent.New(...).Build() leave it nil
	// and skip persistence.
	checkpointSink CheckpointSink

	// thinkingOpts carries the run-scoped DeepSeek-style `thinking`
	// option set via WithThinking(). The runtime copies r.cfg.Thinking
	// onto the service at loop start and clears it on return so
	// toolGenerationOptions sees it on every per-round LLM call.
	thinkingMu   sync.RWMutex
	thinkingOpts *domain.ThinkingOptions

	// responseFormat carries the run-scoped structured-output spec set
	// by the runtime when RunConfig.StructuredOutput is non-nil. Cleared
	// at run end so a later run on the same Service can't inherit it.
	responseFormatMu sync.RWMutex
	responseFormat   *domain.ResponseFormat

	// Public access to underlying services
	LLM     domain.Generator
	MCP     *mcp.Service // Full access to MCP service (Chat, StartServers, etc.)
	RAG     domain.Processor
	Memory  domain.MemoryService
	Router  *router.Service
	Skills  *skills.Service
	Prompts *prompt.Manager
	PTC     *PTCIntegration

	tokenCounter *usage.TokenCounter
	cfg          *config.Config

	// Optional execution sandbox + browser handles, wired by
	// WithSandbox/WithBrowser. Caller owns their lifecycle (Close); the
	// service keeps the handles for accessors (Sandbox/Browser) and for
	// deliverable scanning. nil when not configured.
	execSandbox   sandbox.Sandbox
	execBrowser   browser.Browser
	visionEnabled bool

	// defaultMaxTurns, when > 0, is the fallback tool-round budget used when a
	// run doesn't set RunConfig.MaxTurns. Set via WithAutonomy for long-horizon
	// tasks. lintRetryBudgetOverride likewise overrides defaultLintRetryBudget
	// on the per-run runtime when > 0.
	defaultMaxTurns         int
	lintRetryBudgetOverride int
}

// Ensure Service implements ptc.SearchProvider
var _ ptc.SearchProvider = (*Service)(nil)

// NewService creates a new agent service with the given dependencies.
//
// Deprecated: Prefer agent.New("name").WithRAG().WithMemory().Build() for
// a more ergonomic and composable construction. NewService is kept for
// internal use by the CLI and advanced callers that need fine-grained control.
func NewService(
	llmService domain.Generator,
	mcpService MCPToolExecutor,
	ragProcessor domain.Processor,
	agentDBPath string,
	memoryService domain.MemoryService,
) (*Service, error) {
	// Initialize store
	store, err := NewStore(agentDBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent store: %w", err)
	}

	// Initialize prompt manager
	promptMgr := prompt.NewManager()

	// Collect available tools
	tools := collectAvailableTools(mcpService, ragProcessor, nil)

	// Concise agent instructions — key behaviors only
	instructions := "You are AgentGo, a helpful AI assistant. Use available tools to complete tasks efficiently. Finish the task or explicitly block it; call task_complete when done and task_blocked when a concrete blocker prevents completion."

	// Create default agent
	agent := NewAgentWithConfig(
		"AgentGo Agent",
		instructions,
		tools,
	)

	// Initialize registry and register default agent
	registry := NewRegistry()
	registry.Register(agent)

	// Initialize logger
	logger := agentgolog.WithModule("agent.service")

	// Create service first (so we can pass it to planner/executor)
	s := &Service{
		llmService:            llmService,
		mcpService:            mcpService,
		ragProcessor:          ragProcessor,
		memoryService:         memoryService,
		promptManager:         promptMgr,
		store:                 store,
		agent:                 agent,
		registry:              registry,
		logger:                logger,
		memoryScopeAgentID:    strings.TrimSpace(agent.Name()),
		hooks:                 NewHookRegistry(),
		asyncTasks:            NewSubAgentCoordinator(),
		toolRegistry:          NewToolRegistry(),
		tokenCounter:          usage.NewTokenCounter(),
		inProgressTools:       make(map[string]int),
		sessionRelevantSkills: make(map[string][]string),
		taskSkillSatisfied:    make(map[string]bool),
		// Public fields
		LLM:     llmService,
		RAG:     ragProcessor,
		Memory:  memoryService,
		Prompts: promptMgr,
	}

	// Initialize and start subconscious pool
	s.subconscious = NewSubconsciousWorkerPool(s)
	s.subconscious.Start(1) // 1 worker is enough for background tasks

	// Inject prompt manager into memory service if it supports it
	if memoryService != nil {
		if m, ok := memoryService.(interface{ SetPromptManager(*prompt.Manager) }); ok {
			m.SetPromptManager(promptMgr)
		}
	}

	// Create planner with service reference
	s.planner = NewPlanner(s, llmService, tools)
	s.planner.SetPromptManager(promptMgr)

	// Create executor with service reference
	s.executor = NewExecutor(s, llmService, nil, mcpService, ragProcessor, memoryService)

	// Register built-in tools in registry
	s.registerBuiltInTools()

	return s, nil
}

// registerBuiltInTools registers core tools that are always available
func (s *Service) registerBuiltInTools() {
	// 1. delegate_to_subagent
	delegateDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "delegate_to_subagent",
			Description: "Delegate a specific task to a sub-agent. The sub-agent will execute the task with a subset of available tools and return the result. Use this for focused, isolated tasks.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"goal": map[string]interface{}{
						"type":        "string",
						"description": "The specific task/goal for the sub-agent to accomplish",
					},
					"tools_allowlist": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Optional list of tool names the sub-agent is allowed to use.",
					},
					"tools_denylist": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Optional list of tool names the sub-agent is NOT allowed to use.",
					},
				},
				"required": []string{"goal"},
			},
		},
	}
	s.toolRegistry.RegisterWithMetadata(delegateDef, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return s.executeSubAgentDelegation(ctx, s.agent, args)
	}, CategoryCustom, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock})

	// 1.5. delegate_async
	delegateAsyncDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "delegate_async",
			Description: "Spawn a sub-agent in the background to execute a task asynchronously. Returns immediately with a task ID. The sub-agent will run in isolation and notify you via a <task-notification> user message when it finishes or fails. Use this for parallel research or long-running independent tasks.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"goal": map[string]interface{}{
						"type":        "string",
						"description": "The specific task/goal for the sub-agent to accomplish",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "A short, descriptive name for the sub-agent (e.g., 'auth-researcher')",
					},
				},
				"required": []string{"goal", "name"},
			},
		},
	}
	s.toolRegistry.RegisterWithMetadata(delegateAsyncDef, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return s.executeDelegateAsync(ctx, s.agent, args)
	}, CategoryCustom, ToolMetadata{InterruptBehavior: InterruptBehaviorCancel})

	// 1.6. send_message
	sendMessageDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "send_message",
			Description: "Send a message to an already running or paused sub-agent using its task ID. This is the only way to follow up on a completed async task or interact with an active sub-agent. Do not fabricate their responses.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"to": map[string]interface{}{
						"type":        "string",
						"description": "The task ID (agent ID) of the target sub-agent",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The instruction, question, or follow-up task to send",
					},
				},
				"required": []string{"to", "message"},
			},
		},
	}
	s.toolRegistry.RegisterWithMetadata(sendMessageDef, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return s.executeSendMessage(ctx, s.agent, args)
	}, CategoryCustom, ToolMetadata{InterruptBehavior: InterruptBehaviorCancel})

	// 2. task_complete (optional registration if needed by some paths)
	completeDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "task_complete",
			Description: "Mark the current task as complete. The 'result' you pass is shown to the user verbatim as the final answer, so it must BE the answer itself — not a description of what you did.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"type":        "string",
						"description": "The complete, final answer written directly to the user (second person). Include the full content — explanation, steps, commands, code — exactly as the user should read it. Do NOT write a third-person meta-summary of your work (e.g. \"Provided a step-by-step guide…\"); write the guide itself.",
					},
				},
				"required": []string{"result"},
			},
		},
	}
	s.toolRegistry.RegisterWithMetadata(completeDef, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		res, _ := args["result"].(string)
		return res, nil
	}, CategoryCustom, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel})

	blockedDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "task_blocked",
			Description: "Mark the current task as blocked. Call this only when you cannot complete the task now because of a concrete external blocker, missing permission, missing input, unavailable resource, or unsafe action.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"blocker": map[string]interface{}{
						"type":        "string",
						"description": "The concrete blocker and what was attempted before blocking.",
					},
				},
				"required": []string{"blocker"},
			},
		},
	}
	s.toolRegistry.RegisterWithMetadata(blockedDef, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return taskTerminalToolResult("task_blocked", args, "Task blocked."), nil
	}, CategoryCustom, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel})
}

// Plan generates an execution plan for the given goal
// This matches the CLI expectation: agentService.Plan(ctx, goal)
func (s *Service) Plan(ctx context.Context, goal string) (*Plan, error) {
	session := NewSession(s.agent.ID())
	plan, err := s.planner.PlanWithFallback(ctx, goal, session)
	if err != nil {
		return nil, err
	}
	// Save plan to database
	if err := s.store.SavePlan(plan); err != nil {
		return nil, fmt.Errorf("failed to save plan: %w", err)
	}
	return plan, nil
}

// RevisePlan revises an existing plan based on user feedback.
// The user can modify the plan through natural language chat
func (s *Service) RevisePlan(ctx context.Context, plan *Plan, instruction string) (*Plan, error) {
	data := map[string]interface{}{
		"Goal":        plan.Goal,
		"Status":      plan.Status,
		"Steps":       plan.Steps,
		"Instruction": instruction,
	}

	rendered, err := s.promptManager.Render(prompt.AgentRevisePlan, data)
	if err != nil {
		return nil, fmt.Errorf("failed to render revision prompt: %w", err)
	}

	// Call LLM to get revised plan
	response, err := s.llmService.Generate(ctx, rendered, &domain.GenerationOptions{
		Temperature: 0.3,
		MaxTokens:   2000,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	// Parse the response
	var revisedPlan struct {
		Reasoning string `json:"reasoning"`
		Steps     []struct {
			Tool        string                 `json:"tool"`
			Description string                 `json:"description"`
			Arguments   map[string]interface{} `json:"arguments"`
		} `json:"steps"`
	}

	// Extract JSON from response
	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")
	if jsonStart == -1 || jsonEnd == -1 {
		return nil, fmt.Errorf("no valid JSON in LLM response")
	}
	jsonStr := response[jsonStart : jsonEnd+1]

	if err := json.Unmarshal([]byte(jsonStr), &revisedPlan); err != nil {
		return nil, fmt.Errorf("failed to parse revised plan: %w", err)
	}

	// Create new plan with revisions
	newPlan := &Plan{
		ID:        uuid.New().String(),
		SessionID: plan.SessionID,
		Goal:      plan.Goal,
		Status:    PlanStatusPending,
		Reasoning: revisedPlan.Reasoning,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Convert steps
	for i, step := range revisedPlan.Steps {
		newPlan.Steps = append(newPlan.Steps, Step{
			ID:          fmt.Sprintf("step-%d", i+1),
			Tool:        step.Tool,
			Description: step.Description,
			Arguments:   step.Arguments,
			Status:      StepStatusPending,
		})
	}

	// Save revised plan
	if err := s.store.SavePlan(newPlan); err != nil {
		return nil, fmt.Errorf("failed to save revised plan: %w", err)
	}

	return newPlan, nil
}

// ExecutePlan executes the given plan
// This matches the CLI expectation: agentService.ExecutePlan(ctx, plan)
func (s *Service) ExecutePlan(ctx context.Context, plan *Plan) (*ExecutionResult, error) {
	result, err := s.executor.ExecutePlan(ctx, plan, nil)
	if err != nil {
		return nil, fmt.Errorf("execution failed: %w", err)
	}

	// Save the plan state
	if err := s.store.SavePlan(plan); err != nil {
		return nil, fmt.Errorf("failed to save plan: %w", err)
	}

	if !result.Success {
		return result, fmt.Errorf("plan execution completed with errors: %s", result.Error)
	}

	return result, nil
}

// OutputLints returns the post-output lint registry for this service. The
// registry is lazily created on first access. Lints registered here are
// consulted by the runtime before emitting a final completion event.
func (s *Service) OutputLints() *OutputLintRegistry {
	if s == nil {
		return nil
	}
	s.outputLintsMu.RLock()
	reg := s.outputLints
	s.outputLintsMu.RUnlock()
	if reg != nil {
		return reg
	}
	s.outputLintsMu.Lock()
	defer s.outputLintsMu.Unlock()
	if s.outputLints == nil {
		s.outputLints = NewOutputLintRegistry()
	}
	return s.outputLints
}

// RegisterOutputLint adds a lint to the service's registry. If agents is
// empty the lint runs for every agent; otherwise it runs only for agents
// whose name matches one of the provided values (case-insensitive).
func (s *Service) RegisterOutputLint(lint OutputLint, agents ...string) {
	if s == nil || lint == nil {
		return
	}
	reg := s.OutputLints()
	if len(agents) == 0 {
		reg.RegisterGlobal(lint)
		return
	}
	for _, name := range agents {
		reg.RegisterForAgent(name, lint)
	}
}

// RunStream executes a goal and returns a stream of events
// This is the preferred method for reactive applications.
func (s *Service) RunStream(ctx context.Context, goal string) (<-chan *Event, error) {
	return s.RunStreamWithOptions(ctx, goal)
}

// RunStreamWithOptions executes a goal and returns a stream of events using the provided run options.
func (s *Service) RunStreamWithOptions(ctx context.Context, goal string, opts ...RunOption) (<-chan *Event, error) {
	cfg := DefaultRunConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	sessionID := strings.TrimSpace(cfg.SessionID)
	if sessionID == "" {
		sessionID = s.CurrentSessionID()
		if sessionID == "" {
			s.ResetSession()
			sessionID = s.CurrentSessionID()
		}
	} else {
		s.SetSessionID(sessionID)
	}

	session, err := s.store.GetSession(sessionID)
	if err != nil {
		session = NewSessionWithID(sessionID, s.agent.ID())
	}
	ensureTaskID(session, cfg)
	if inherited := strings.TrimSpace(cfg.InheritedMemoryAgentID); inherited != "" {
		session.SetContext(sessionContextMemoryAgentScope, inherited)
	}
	if inherited := strings.TrimSpace(cfg.InheritedMemoryTeamID); inherited != "" {
		session.SetContext(sessionContextMemoryTeamScope, inherited)
	}
	if inherited := strings.TrimSpace(cfg.InheritedMemoryUserID); inherited != "" {
		session.SetContext(sessionContextMemoryUserScope, inherited)
	}
	s.rememberMemoryQueryContext(session, s.resolveMemoryQueryContext(session))
	taskID := ensureTaskID(session, cfg)
	startedAt := time.Now()
	s.persistRunTaskState(session, taskID, taskRunStateOptions{
		status:    taskpkg.StatusRunning,
		input:     goal,
		createdAt: startedAt,
	})

	if routedEvents, ok, err := s.streamDirectDispatcherRoute(ctx, session, goal); ok {
		return s.observeRunStream(session, taskID, goal, startedAt, routedEvents), err
	}

	runtime := NewRuntime(s, session, cfg)
	return s.observeRunStream(session, taskID, goal, startedAt, runtime.RunStream(ctx, goal)), nil
}

// Run executes a goal with optional configuration.
// Usage:
//
// // Simple
// result, err := svc.Run(ctx, "goal")
//
// // With options
// result, err := svc.Run(ctx, "goal",
//
//	agent.WithMaxTurns(10),
//	agent.WithSessionID("session-123"),
//	agent.WithStoreHistory(true),
//
// )
func (s *Service) Run(ctx context.Context, goal string, opts ...RunOption) (*ExecutionResult, error) {
	cfg := DefaultRunConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return s.runWithConfig(ctx, goal, cfg)
}

// runWithConfig is the internal implementation
func (s *Service) runWithConfig(ctx context.Context, goal string, cfg *RunConfig) (_ *ExecutionResult, runErr error) {
	if cfg == nil {
		cfg = DefaultRunConfig()
	}
	startTime := time.Now()
	s.resetRunMemorySaved()
	s.setRunning(true)
	defer s.setRunning(false)

	// Create cancellable context for this run
	runCtx, cancel := context.WithCancel(ctx)

	// Store cancel function for external cancellation
	s.cancelMu.Lock()
	s.cancelFunc = cancel
	s.cancelMu.Unlock()

	defer func() {
		s.cancelMu.Lock()
		s.cancelFunc = nil
		s.cancelMu.Unlock()
	}()

	// Load or create session based on SessionID
	var session *Session
	if cfg.SessionID != "" {
		var err error
		session, err = s.store.GetSession(cfg.SessionID)
		if err != nil {
			session = NewSessionWithID(cfg.SessionID, s.agent.ID())
		}
	} else {
		session = NewSession(s.agent.ID())
	}
	taskID := ensureTaskID(session, cfg)
	s.persistRunTaskState(session, taskID, taskRunStateOptions{
		status:    taskpkg.StatusRunning,
		input:     goal,
		createdAt: startTime,
	})
	s.persistRunTaskEvent(session, taskID, &Event{
		Type:      EventTypeStart,
		AgentName: s.agent.Name(),
		Content:   goal,
		Timestamp: startTime,
	})
	defer func() {
		if runErr == nil {
			return
		}
		s.persistRunTaskState(session, taskID, taskRunStateOptions{
			status:      taskpkg.StatusFailed,
			input:       goal,
			output:      "",
			errorText:   runErr.Error(),
			createdAt:   startTime,
			finishedAt:  time.Now(),
			appendError: true,
		})
	}()
	if inherited := strings.TrimSpace(cfg.InheritedMemoryAgentID); inherited != "" {
		session.SetContext(sessionContextMemoryAgentScope, inherited)
	}
	if inherited := strings.TrimSpace(cfg.InheritedMemoryTeamID); inherited != "" {
		session.SetContext(sessionContextMemoryTeamScope, inherited)
	}
	if inherited := strings.TrimSpace(cfg.InheritedMemoryUserID); inherited != "" {
		session.SetContext(sessionContextMemoryUserScope, inherited)
	}
	s.rememberMemoryQueryContext(session, s.resolveMemoryQueryContext(session))

	if routedResult, ok, err := s.executeDirectDispatcherRoute(runCtx, session, goal); ok {
		if err != nil {
			runErr = err
			return nil, runErr
		}
		completedAt := time.Now()
		s.persistRunTaskEvent(session, taskID, &Event{
			Type:      EventTypeToolCall,
			AgentName: s.agent.Name(),
			ToolName:  "route_builtin_request",
			ToolArgs:  map[string]interface{}{"prompt": normalizeTaskPrompt(goal)},
			Timestamp: time.Now(),
		})
		s.persistRunTaskEvent(session, taskID, &Event{
			Type:       EventTypeToolResult,
			AgentName:  s.agent.Name(),
			ToolName:   "route_builtin_request",
			ToolResult: routedResult.Metadata["route_builtin_result"],
			Content:    fmt.Sprintf("%v", routedResult.FinalResult),
			Timestamp:  completedAt,
		})
		routedResult.StartedAt = &startTime
		routedResult.CompletedAt = &completedAt
		routedResult.EstimatedTokens = s.estimateRunTokens(goal, routedResult.FinalResult)
		session.AddMessage(withTaskID(domain.Message{
			Role:    "user",
			Content: goal,
		}, taskID))
		session.AddMessage(withTaskID(domain.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("%v", routedResult.FinalResult),
		}, taskID))
		_ = s.store.SaveSession(session)
		routedResult.TaskID = taskID
		status := taskpkg.StatusCompleted
		if blocked, ok := routedResult.Metadata["dispatch_blocked"].(bool); ok && blocked {
			status = taskpkg.StatusBlocked
		}
		s.persistRunTaskState(session, taskID, taskRunStateOptions{
			status:     status,
			input:      goal,
			output:     fmt.Sprintf("%v", routedResult.FinalResult),
			createdAt:  startTime,
			finishedAt: completedAt,
		})
		return routedResult, nil
	}
	prepared := s.prepareConversationContext(runCtx, goal, session, prepareConversationOptions{
		includeIntent: true,
		emitProgress:  true,
	})
	intent := prepared.intent
	ragContext := prepared.ragContext
	memoryContext := prepared.memoryContext
	memoryMemories := prepared.memoryMemories
	memoryLogic := prepared.memoryLogic

	// Execute: PTC is just a transport mode — branch internally, same public API.
	var finalResult interface{}
	var ptcRes *PTCResult
	var execMetrics *executionMetrics

	if cfg.DisableMemoryRecallShortcut {
		// Action-taking agent opted out: skip the recall short-circuit so
		// tool turns aren't hijacked by an answer-from-memory response.
	} else if recalledAnswer, ok, err := s.answerExplicitMemoryRecall(runCtx, goal, intent, memoryContext, memoryMemories, cfg); err != nil {
		s.logger.Warn("Explicit memory recall shortcut failed", slog.Any("error", err))
	} else if ok {
		finalResult = recalledAnswer
		execMetrics = &executionMetrics{}
	}

	if finalResult != nil {
		// Shortcut path already produced the answer.
	} else if s.isPTCEnabled() && !cfg.DisablePTC && !isMemoryToolIntent(intent) && len(memoryMemories) == 0 {
		var err error
		finalResult, ptcRes, err = s.runPTCExecution(runCtx, goal, session, cfg)
		if err != nil {
			runErr = err
			return nil, runErr
		}
		// Use execution result if available
		if ptcRes != nil && ptcRes.Output != "" {
			finalResult = ptcRes.Output
		}
	} else {
		var err error
		finalResult, execMetrics, err = s.executeWithLLM(runCtx, goal, intent, session, memoryContext, ragContext, cfg)
		if err != nil {
			runErr = err
			return nil, runErr
		}
	}

	// Skip verification for faster response
	currentResult := finalResult
	blockedResult := false
	if terminal, ok := finalResult.(terminalRunResult); ok {
		currentResult = terminal.Text
		blockedResult = terminal.Blocked
	}

	// Add messages to session before saving
	session.AddMessage(withTaskID(domain.Message{
		Role:    "user",
		Content: goal,
	}, taskID))
	if currentResult != nil {
		session.AddMessage(withTaskID(domain.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("%v", currentResult),
		}, taskID))
	}

	// Create a simple plan to track this execution
	now := time.Now()
	planStatus := StatusCompleted
	if blockedResult {
		planStatus = StatusFailed
	}
	plan := &Plan{
		ID:        uuid.New().String(),
		SessionID: session.GetID(),
		Goal:      goal,
		Status:    planStatus,
		CreatedAt: now,
		UpdatedAt: now,
		Steps: []Step{
			{
				ID:          uuid.New().String(),
				Description: goal,
				Tool:        "llm",
				Status:      StepCompleted,
				Result:      currentResult,
			},
		},
	}
	if err := s.store.SavePlan(plan); err != nil {
		log.Printf("[Agent] Failed to save plan: %v", err)
	}

	// Persist session history
	if err := s.store.SaveSession(session); err != nil {
		log.Printf("[Agent] Failed to save session: %v", err)
	}

	result, err := s.finalizeExecution(runCtx, session, goal, intent, memoryMemories, memoryLogic, "", currentResult)
	if err != nil {
		runErr = err
		return nil, runErr
	}
	result.TaskID = taskID
	completedAt := time.Now()
	result.StartedAt = &startTime
	result.CompletedAt = &completedAt
	result.EstimatedTokens = s.estimateRunTokens(goal, currentResult)
	if execMetrics != nil {
		result.ToolCalls = execMetrics.toolCalls
		result.ToolsUsed = uniqueStrings(execMetrics.toolsUsed)
		result.EstimatedTokens += execMetrics.estimatedTokens
	}
	if ptcRes != nil {
		result.PTCResult = ptcRes
		if ptcRes.ExecutionResult != nil {
			result.ToolCalls = len(ptcRes.ExecutionResult.ToolCalls)
		}
		result.ToolsUsed = uniqueStrings(toolNamesFromPTC(ptcRes))
		result.EstimatedTokens = s.estimateRunTokens(goal, currentResult) + s.estimatePTCTokens(ptcRes)
	}
	finalStatus := taskpkg.StatusCompleted
	if blockedResult {
		finalStatus = taskpkg.StatusBlocked
	}
	s.persistRunTaskState(session, taskID, taskRunStateOptions{
		status:     finalStatus,
		input:      goal,
		output:     result.Text(),
		createdAt:  startTime,
		finishedAt: completedAt,
	})
	// Synthesize metrics from PTC path when execMetrics is nil.
	if execMetrics == nil {
		execMetrics = &executionMetrics{
			estimatedTokens: result.EstimatedTokens,
			toolCalls:       result.ToolCalls,
			toolsUsed:       result.ToolsUsed,
			totalDurationMs: completedAt.Sub(startTime).Milliseconds(),
		}
	}
	s.persistRunTaskStats(session, taskID, execMetrics)
	return result, nil
}

func isMemoryToolIntent(intent *IntentRecognitionResult) bool {
	if intent == nil {
		return false
	}
	switch strings.TrimSpace(intent.IntentType) {
	case "memory_save", "memory_recall", "memory_update", "memory_delete":
		return true
	default:
		return false
	}
}

// Cancel forcefully stops the current agent execution
func (s *Service) Cancel() bool {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()

	if s.hasBlockingToolInProgress() {
		log.Printf("[Agent] Cancellation deferred: blocking tool still in progress")
		return false
	}

	if s.cancelFunc != nil {
		log.Printf("[Agent] Cancelling current execution...")
		s.cancelFunc()
		return true
	}
	return false
}

// ─── Cognitive Memory APIs ───────────────────────────────────────────────────

// TriggerReflection manually triggers memory consolidation for a session.
// The LLM analyses accumulated facts and generates higher-level observations.
// Returns a summary of what was consolidated, or an error.
func (s *Service) TriggerReflection(ctx context.Context, sessionID string) (string, error) {
	if s.memoryService == nil {
		return "", fmt.Errorf("memory service not configured")
	}
	return s.memoryService.Reflect(ctx, sessionID)
}

// ExplainMemory returns the full evolution graph for a memory, tracing how
// raw facts were consolidated into observations. Requires a file-based memory
// service (FileMemoryStore path).
func (s *Service) ExplainMemory(ctx context.Context, memoryID string) (*memorypkg.MemoryEvolutionNode, error) {
	svc, ok := s.memoryService.(*memorypkg.Service)
	if !ok {
		return nil, fmt.Errorf("ExplainMemory requires a *memory.Service (file-based store)")
	}
	return svc.GetEvolution(ctx, memoryID)
}

// SetAgentDirective stores a mission statement and hard directives as high-priority
// preference memories. These are injected into every prompt with the highest priority,
// overriding any conflicting context.
func (s *Service) SetAgentDirective(ctx context.Context, sessionID string, mission string, directives []string) error {
	if s.memoryService == nil {
		return fmt.Errorf("memory service not configured")
	}
	now := time.Now()
	if mission != "" {
		if err := s.memoryService.Add(ctx, &domain.Memory{
			Type:       domain.MemoryTypePreference,
			Content:    "Agent mission: " + mission,
			Importance: 1.0,
			SourceType: domain.MemorySourceUserInput,
			SessionID:  sessionID,
			CreatedAt:  now,
		}); err != nil {
			return fmt.Errorf("storing mission: %w", err)
		}
	}
	for _, d := range directives {
		if err := s.memoryService.Add(ctx, &domain.Memory{
			Type:       domain.MemoryTypePreference,
			Content:    "Directive: " + d,
			Importance: 1.0,
			SourceType: domain.MemorySourceUserInput,
			SessionID:  sessionID,
			CreatedAt:  now,
		}); err != nil {
			return fmt.Errorf("storing directive %q: %w", d, err)
		}
	}
	return nil
}

// Info returns structured information about the agent's status and configuration.
// GetToolRegistry returns the tool registry for direct access
func (s *Service) GetToolRegistry() *ToolRegistry {
	return s.toolRegistry
}

// RegisterTool registers a custom tool in the tool registry
func (s *Service) RegisterTool(def domain.ToolDefinition, handler ToolHandler) {
	metadata, _ := inferGenericToolMetadata(def.Function.Name)
	s.RegisterToolWithMetadata(def, handler, metadata)
}

func (s *Service) RegisterToolWithMetadata(def domain.ToolDefinition, handler ToolHandler, metadata ToolMetadata) {
	s.toolRegistry.RegisterWithMetadata(def, handler, CategoryCustom, metadata)
}

func (s *Service) Info() AgentInfo {
	info := AgentInfo{
		ID:            s.agent.ID(),
		Name:          s.agent.Name(),
		Status:        s.Status(),
		Model:         s.modelName,
		BaseURL:       s.baseURL,
		FastModel:     s.isFastModel,
		RAGEnabled:    s.ragProcessor != nil,
		PTCEnabled:    s.isPTCEnabled(),
		MemoryEnabled: s.memoryService != nil,
		MCPEnabled:    s.mcpService != nil,
		SkillsEnabled: s.skillsService != nil,
	}

	if s.agent != nil {
		info.Tools = s.agent.GetToolNames()
	}

	return info
}

// Status returns the current status of the agent ("running" or "idle").
func (s *Service) Status() string {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	if s.isRunning {
		return "running"
	}
	return "idle"
}

// IsRunning returns true if the agent is currently executing a task.
func (s *Service) IsRunning() bool {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.isRunning
}

func (s *Service) setRunning(running bool) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.isRunning = running
}

func (s *Service) estimateGenerationTokens(messages []domain.Message, result *domain.GenerationResult) int {
	total := s.estimateDomainMessagesTokens(messages)
	if result == nil {
		return total
	}
	total += s.estimateTextTokens(result.Content)
	total += s.estimateTextTokens(result.ReasoningContent)
	for _, tc := range result.ToolCalls {
		total += s.estimateTextTokens(tc.Function.Name)
		if b, err := json.Marshal(tc.Function.Arguments); err == nil {
			total += s.estimateTextTokens(string(b))
		}
	}
	return total
}

func (s *Service) estimateRunTokens(goal string, finalResult interface{}) int {
	return s.estimateTextTokens(goal) + s.estimateTextTokens(formatResultForContent(finalResult))
}

func (s *Service) estimatePTCTokens(res *PTCResult) int {
	if res == nil || res.ExecutionResult == nil {
		return 0
	}

	total := s.estimateTextTokens(formatResultForContent(res.ExecutionResult.Output))
	total += s.estimateTextTokens(formatResultForContent(res.ExecutionResult.ReturnValue))
	for _, logLine := range res.ExecutionResult.Logs {
		total += s.estimateTextTokens(logLine)
	}
	for _, tc := range res.ExecutionResult.ToolCalls {
		total += s.estimateTextTokens(tc.ToolName)
		if b, err := json.Marshal(tc.Arguments); err == nil {
			total += s.estimateTextTokens(string(b))
		}
		total += s.estimateTextTokens(formatResultForContent(tc.Result))
		total += s.estimateTextTokens(tc.Error)
	}
	return total
}

func (s *Service) estimateDomainMessagesTokens(messages []domain.Message) int {
	total := 0
	for _, message := range messages {
		total += 4
		total += s.estimateTextTokens(message.Role)
		total += s.estimateTextTokens(message.Content)
		total += s.estimateTextTokens(message.ReasoningContent)
		for _, tc := range message.ToolCalls {
			total += s.estimateTextTokens(tc.Function.Name)
			if b, err := json.Marshal(tc.Function.Arguments); err == nil {
				total += s.estimateTextTokens(string(b))
			}
		}
	}
	return total
}

func (s *Service) estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	if s.tokenCounter == nil {
		s.tokenCounter = usage.NewTokenCounter()
	}
	model := s.modelName
	if model == "" {
		model = "default"
	}
	return s.tokenCounter.EstimateTokens(text, model)
}

func toolNamesFromPTC(res *PTCResult) []string {
	if res == nil || res.ExecutionResult == nil {
		return nil
	}
	names := make([]string, 0, len(res.ExecutionResult.ToolCalls))
	for _, tc := range res.ExecutionResult.ToolCalls {
		if tc.ToolName != "" {
			names = append(names, tc.ToolName)
		}
	}
	return names
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
