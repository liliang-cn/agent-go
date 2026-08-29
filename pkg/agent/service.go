package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/mcp"
	memorypkg "github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
	"github.com/liliang-cn/agent-go/v3/pkg/prompt"
	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
	"github.com/liliang-cn/agent-go/v3/pkg/skills"
	taskpkg "github.com/liliang-cn/agent-go/v3/pkg/task"
)

// ProgressEvent is a progress notification emitted during a run.
type ProgressEvent struct {
	Type    string // "thinking", "tool_call", "tool_result", "done"
	Round   int
	Message string
	Tool    string
}

// ProgressCallback receives progress events.
type ProgressCallback func(ProgressEvent)

// Service is the main agent service that handles planning and execution
// This matches the interface expected by the CLI in cmd/agentgo-cli/agent/agent.go
type Service struct {
	// bgWork tracks goroutines a run leaves behind (memory extraction), so Close
	// can wait for them instead of letting them write after the caller has moved
	// on. See service_background.go.
	bgWork bgWorkGroup

	// closed records that Close has run, so a borrower still holding this
	// Service (a scheduled PromptExecutor, a Manager cache entry the host has
	// already replaced) is refused a new run instead of executing one against a
	// closed store. closeOnce keeps Close idempotent. See service_close.go.
	closed    atomic.Bool
	closeOnce sync.Once

	debug         bool
	llmService    domain.Generator
	mcpService    MCPToolExecutor
	ragProcessor  domain.Processor
	memoryService domain.MemoryService
	skillsService *skills.Service
	promptManager *prompt.Manager // Central prompt management
	store         *Store
	agent         *Agent
	registry      *Registry
	logger        *slog.Logger
	// cancelMu guards the in-flight run registry below. See run_cancel.go —
	// a service can be driving several runs at once (a chat turn, a scheduled
	// prompt, a sub-agent), so cancellation is a lookup in `runs`, not a
	// single stored CancelFunc.
	cancelMu              sync.RWMutex
	runs                  map[string]*runHandle
	runSeq                uint64
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
	modelName   string
	baseURL     string
	isFastModel bool

	// Hook system for lifecycle events
	hooks *HookRegistry

	// Async sub-agent coordinator

	// Stop-hook tracking: maps StopHookConfig registrations to their hook IDs
	// in the registry so UnregisterStopHooks can remove them.
	stopHookMu  sync.Mutex
	stopHookIDs []string

	// toolRegistry is the unified registry for custom, RAG, and Memory tools.
	// All modules register here so that LLM tool listing
	// dispatch go through a single source of truth.
	toolRegistry *ToolRegistry

	// outputLints is the registry of post-output lint rules consulted by the
	// runtime before emitting a final completion event. Lazily initialized via
	// OutputLints(); see pkg/agent/output_lint.go.
	outputLintsMu sync.RWMutex
	outputLints   *OutputLintRegistry

	// observers is the registry of passive observability aspects fanned out
	// at the model / tool / sub-agent / checkpoint seams. See observer.go.
	// Nil-safe: no observers means zero overhead.
	observersMu sync.RWMutex
	observers   []Observer

	// checkpointSink, when non-nil, is called by the runtime at every
	// round boundary so the message history can be persisted for
	// Tasks().Resume. Manager.buildServiceForModel wires this up;
	// services built directly via agent.New(...).Build() leave it nil
	// and skip persistence.
	checkpointSink CheckpointSink

	// thinkingOpts carries the run-scoped DeepSeek-style `thinking`
	// option set via WithThinking(). The runtime copies r.cfg.Thinking
	// onto the service at loop start and clears it on return so
	// toolGenerationOptions sees it on every per-round LLM call.
	thinkingMu   sync.RWMutex
	thinkingOpts *domain.ThinkingOptions

	// runMemory, when non-nil, is consulted at run start (recall) and run end
	// (capture). See RunMemory and Builder.WithRunMemory.
	runMemory RunMemory

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
	Skills  *skills.Service
	Prompts *prompt.Manager

	tokenCounter *pool.TokenCounter
	cfg          *config.Config

	// Optional execution sandbox handle, wired by WithSandbox. Caller owns
	// its lifecycle (Close); the service keeps the handle for the Sandbox()
	// accessor and for deliverable scanning. nil when not configured.
	execSandbox sandbox.Sandbox

	// defaultMaxTurns, when > 0, is the fallback tool-round budget used when a
	// run doesn't set RunConfig.MaxTurns. Set via WithAutonomy for long-horizon
	// tasks. lintRetryBudgetOverride likewise overrides defaultLintRetryBudget
	// on the per-run runtime when > 0.
	defaultMaxTurns         int
	lintRetryBudgetOverride int
}

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
		toolRegistry:          NewToolRegistry(),
		tokenCounter:          pool.NewTokenCounter(),
		inProgressTools:       make(map[string]int),
		sessionRelevantSkills: make(map[string][]string),
		taskSkillSatisfied:    make(map[string]bool),
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

	// 1.6. subagent_send_message
	sendMessageDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "subagent_send_message",
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
		return taskTerminalToolResult("task_blocked", args, defaultBlockedText), nil
	}, CategoryCustom, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel})
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

// DisableOutputLint removes one or more output lints by name from this
// service's registry. This is the opt-out for baseline lints that don't fit a
// product — e.g. a text-generating agent that should never be forced to call a
// file-write tool can drop the built-in "file_task_must_write". Safe to call
// after the service is built; no-op for names that aren't registered.
func (s *Service) DisableOutputLint(names ...string) {
	if s == nil {
		return
	}
	reg := s.OutputLints()
	for _, n := range names {
		reg.RemoveByName(n)
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
	_, events, err := s.startRun(ctx, goal, cfg)
	return events, err
}

// startRun is the single entry point into the agent loop. Both the streaming
// (RunStreamWithOptions) and the collecting (Run) surfaces go through it — v3
// has exactly one execution path, and Run is RunStream plus an event collector.
func (s *Service) startRun(ctx context.Context, goal string, cfg *RunConfig) (*Session, <-chan *Event, error) {
	if cfg == nil {
		cfg = DefaultRunConfig()
	}

	// A closed Service has no store to write to. Refusing here — the one entry
	// point into the loop — is what stops a borrower that outlived the owner
	// (a schedule still pointed at the service a host rebuilt) from running a
	// turn whose history nobody can save. See service_close.go.
	if s.Closed() {
		return nil, nil, ErrServiceClosed
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

	// Register the run so Cancel / CancelRun / CancelSession can reach it.
	// startRun is the single entry point into the loop, so registering here
	// covers Run, RunStream, Ask, Chat, structured output and the prompt
	// scheduler alike — the same reason constraints are resolved in the loop
	// and not in a per-entry-point helper.
	runCtx, releaseRun := s.registerRun(ctx, cfg.RunID, session.GetID(), taskID)

	runtime := NewRuntime(s, session, cfg)
	return session, s.observeRunStream(session, taskID, goal, startedAt, runtime.RunStream(runCtx, goal), releaseRun), nil
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

// runWithConfig runs the loop and collects its event stream into an
// ExecutionResult. There is no separate non-streaming implementation.
func (s *Service) runWithConfig(ctx context.Context, goal string, cfg *RunConfig) (*ExecutionResult, error) {
	startedAt := time.Now()
	s.resetRunMemorySaved()
	s.setRunning(true)
	defer s.setRunning(false)

	// Recall once per run, before the loop starts; every turn of this run
	// then carries the same recalled section in its system prompt.
	if cfg.recalledContext == "" {
		cfg.recalledContext = s.recallRunMemory(ctx, goal)
	}

	session, events, err := s.startRun(ctx, goal, cfg)
	if err != nil {
		return nil, err
	}

	result := &ExecutionResult{
		SessionID: session.GetID(),
		TaskID:    currentTaskID(session),
		StartedAt: &startedAt,
	}
	toolSeen := map[string]struct{}{}
	var lastError string

	for evt := range events {
		if evt == nil {
			continue
		}
		if evt.TokensUsed > 0 {
			result.EstimatedTokens += evt.TokensUsed
		}
		switch evt.Type {
		case EventTypeToolCall:
			if evt.ToolName != "" {
				result.ToolCalls++
				if _, seen := toolSeen[evt.ToolName]; !seen {
					toolSeen[evt.ToolName] = struct{}{}
					result.ToolsUsed = append(result.ToolsUsed, evt.ToolName)
				}
			}
		case EventTypeComplete:
			result.Success = true
			result.FinalResult = evt.Content
			result.Sources = evt.Sources
			result.StepsDone++
			result.StepsTotal++
		case EventTypeBlocked:
			result.Success = false
			result.Blocked = true
			result.FinalResult = evt.Content
			result.Error = evt.Content
			result.StepsFailed++
			result.StepsTotal++
		case EventTypeCancelled:
			// Deliberately leaves result.Error empty: a stop the caller asked
			// for is not something to report back to them as a failure.
			result.Success = false
			result.Cancelled = true
			result.StepsTotal++
		case EventTypeError:
			lastError = evt.Content
		}
	}

	if !result.Success && !result.Cancelled && result.Error == "" {
		result.Error = lastError
	}
	completedAt := time.Now()
	result.CompletedAt = &completedAt
	result.Duration = completedAt.Sub(startedAt).String()
	if result.EstimatedTokens == 0 {
		result.EstimatedTokens = s.estimateRunTokens(goal, result.FinalResult)
	}
	if result.Success {
		s.captureRunMemory(goal, result.Text())
	}
	return result, nil
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

func (s *Service) estimateRunTokens(goal string, finalResult interface{}) int {
	return s.estimateTextTokens(goal) + s.estimateTextTokens(formatResultForContent(finalResult))
}

func (s *Service) estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	if s.tokenCounter == nil {
		s.tokenCounter = pool.NewTokenCounter()
	}
	model := s.modelName
	if model == "" {
		model = "default"
	}
	return s.tokenCounter.EstimateTokens(text, model)
}
