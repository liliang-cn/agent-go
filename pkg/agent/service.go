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
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v2/pkg/log"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	memorypkg "github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	"github.com/liliang-cn/agent-go/v2/pkg/router"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
	"github.com/liliang-cn/agent-go/v2/pkg/usage"
	"golang.org/x/sync/errgroup"
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
	debug              bool
	llmService         domain.Generator
	mcpService         MCPToolExecutor
	ragProcessor       domain.Processor
	memoryService      domain.MemoryService
	skillsService      *skills.Service
	routerService      *router.Service // Semantic Router for fast intent recognition
	promptManager      *prompt.Manager // Central prompt management
	planner            *Planner
	executor           *Executor
	store              *Store
	agent              *Agent
	registry           *Registry
	logger             *slog.Logger
	cancelMu           sync.RWMutex
	cancelFunc         context.CancelFunc
	progressCb         ProgressCallback
	currentSessionID   string // Auto-generated UUID for Chat() method
	sessionMu          sync.RWMutex
	memoryStoreType    string
	memoryScopeAgentID string
	memoryScopeSquadID string
	memoryScopeUserID  string
	memorySaveMu       sync.RWMutex
	memorySavedInRun   bool
	ragSourcesMu       sync.RWMutex
	ragSources         []domain.Chunk // Collect RAG sources during execution
	isRunning          bool
	statusMu           sync.RWMutex
	permissionMu       sync.RWMutex
	permissionHandler  PermissionHandler
	permissionPolicy   PermissionPolicy

	// Model metadata for Info()
	modelName string
	baseURL   string

	// Hook system for lifecycle events
	hooks *HookRegistry

	// toolRegistry is the unified registry for custom, RAG, and Memory tools.
	// All modules register here so that both LLM listing and PTC callTool()
	// dispatch go through a single source of truth.
	toolRegistry *ToolRegistry

	// PTC (Programmatic Tool Calling) integration
	ptcIntegration *PTCIntegration

	// Execution history storage
	historyStore *HistoryStore

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
	instructions := "You are AgentGo, a helpful AI assistant. Use available tools to complete tasks efficiently. Call task_complete when done."

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
		llmService:         llmService,
		mcpService:         mcpService,
		ragProcessor:       ragProcessor,
		memoryService:      memoryService,
		promptManager:      promptMgr,
		store:              store,
		agent:              agent,
		registry:           registry,
		logger:             logger,
		memoryScopeAgentID: strings.TrimSpace(agent.Name()),
		hooks:              NewHookRegistry(),
		toolRegistry:       NewToolRegistry(),
		tokenCounter:       usage.NewTokenCounter(),
		// Public fields
		LLM:     llmService,
		RAG:     ragProcessor,
		Memory:  memoryService,
		Prompts: promptMgr,
	}

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
	s.toolRegistry.Register(delegateDef, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return s.executeSubAgentDelegation(ctx, s.agent, args)
	}, CategoryCustom)

	// 2. task_complete (optional registration if needed by some paths)
	completeDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "task_complete",
			Description: "Mark the current task as complete and provide the final result to the user. Call this when you have fully answered the question or finished all required steps.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"type":        "string",
						"description": "The final summary/result of the task",
					},
				},
				"required": []string{"result"},
			},
		},
	}
	s.toolRegistry.Register(completeDef, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		res, _ := args["result"].(string)
		return res, nil
	}, CategoryCustom)
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

	runtime := NewRuntime(s, session, cfg)
	return runtime.RunStream(ctx, goal), nil
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
func (s *Service) runWithConfig(ctx context.Context, goal string, cfg *RunConfig) (*ExecutionResult, error) {
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
	memoryQueryContext := s.resolveMemoryQueryContext(session)
	s.rememberMemoryQueryContext(session, memoryQueryContext)

	// Parallel Context Collection
	var (
		intent         *IntentRecognitionResult
		ragContext     string
		memoryContext  string
		memoryMemories []*domain.MemoryWithScore
		memoryLogic    string
	)

	g, groupCtx := errgroup.WithContext(runCtx)

	// 1. Intent Recognition
	g.Go(func() error {
		var err error
		intent, err = s.recognizeIntent(groupCtx, goal, session)
		return err
	})

	// 2. RAG Retrieval — skip when PTC is enabled (same reason as runtime.go:
	// the LLM must call rag_query explicitly via execute_javascript/callTool).
	if s.ragProcessor != nil && !s.isPTCEnabled() {
		g.Go(func() error {
			s.emitProgress("thinking", "🔍 Searching knowledge base...", 0, "")
			var err error
			ragContext, err = s.performRAGQuery(groupCtx, goal)
			if err == nil && ragContext != "" {
				s.emitProgress("tool_result", fmt.Sprintf("✓ Found %d relevant documents", countDocuments(ragContext)), 0, "")
			}
			return nil // Don't fail the whole run if RAG fails
		})
	}

	// 3. Memory Recall
	if s.memoryService != nil {
		g.Go(func() error {
			var err error
			memoryContext, memoryMemories, memoryLogic, err = s.memoryService.RetrieveAndInjectWithContextAndLogic(groupCtx, goal, memoryQueryContext)
			return err
		})
	}

	// Wait for all context collection to finish
	if err := g.Wait(); err != nil {
		s.logger.Warn("Context collection partial failure", slog.Any("error", err))
	}

	// Execute: PTC is just a transport mode — branch internally, same public API.
	var finalResult interface{}
	var ptcRes *PTCResult
	var execMetrics *executionMetrics

	if !s.isPTCEnabled() {
		if recalledAnswer, ok, err := s.answerExplicitMemoryRecall(runCtx, goal, intent, memoryContext, memoryMemories, cfg); err != nil {
			s.logger.Warn("Explicit memory recall shortcut failed", slog.Any("error", err))
		} else if ok {
			finalResult = recalledAnswer
			execMetrics = &executionMetrics{}
		}
	}

	if finalResult != nil {
		// Shortcut path already produced the answer.
	} else if s.isPTCEnabled() {
		var err error
		finalResult, ptcRes, err = s.runPTCExecution(runCtx, goal, session, cfg)
		if err != nil {
			return nil, err
		}
		// Use execution result if available
		if ptcRes != nil && ptcRes.ExecutionResult != nil && ptcRes.ExecutionResult.Output != "" {
			finalResult = ptcRes.ExecutionResult.Output
		}
	} else {
		var err error
		finalResult, execMetrics, err = s.executeWithLLM(runCtx, goal, intent, session, memoryContext, ragContext, cfg)
		if err != nil {
			return nil, err
		}
	}

	// Skip verification for faster response
	currentResult := finalResult

	// Add messages to session before saving
	session.AddMessage(domain.Message{
		Role:    "user",
		Content: goal,
	})
	if currentResult != nil {
		session.AddMessage(domain.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("%v", currentResult),
		})
	}

	// Create a simple plan to track this execution
	now := time.Now()
	plan := &Plan{
		ID:        uuid.New().String(),
		SessionID: session.GetID(),
		Goal:      goal,
		Status:    StatusCompleted,
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
		return nil, err
	}
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
	return result, nil
}

// Cancel forcefully stops the current agent execution
func (s *Service) Cancel() bool {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()

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
	s.toolRegistry.Register(def, handler, CategoryCustom)
}

func (s *Service) Info() AgentInfo {
	info := AgentInfo{
		ID:            s.agent.ID(),
		Name:          s.agent.Name(),
		Status:        s.Status(),
		Model:         s.modelName,
		BaseURL:       s.baseURL,
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
