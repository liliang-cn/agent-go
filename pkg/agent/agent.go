package agent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Agent represents an autonomous agent with instructions and tools
// Inspired by OpenAI Agents SDK: Agent = LLM + instructions + tools
type Agent struct {
	id           string
	name         string
	instructions string
	tools        []domain.ToolDefinition
	handoffs     []*Handoff
	mcpTools     []string // Specific MCP tools allowed for this agent (names). Use ["*"] for all.
	skills       []string // Specific Skills allowed (IDs). Use ["*"] for all.
	handlers     map[string]func(context.Context, map[string]interface{}) (interface{}, error)
	metadata     map[string]ToolMetadata
	model        string
	temperature  float64
}

// NewAgent creates a new Agent with default settings
func NewAgent(name string) *Agent {
	return &Agent{
		id:           uuid.New().String(),
		name:         name,
		instructions: "You are a helpful assistant.",
		tools:        []domain.ToolDefinition{},
		handoffs:     []*Handoff{},
		mcpTools:     []string{"*"}, // Default to all
		skills:       []string{"*"}, // Default to all
		handlers:     make(map[string]func(context.Context, map[string]interface{}) (interface{}, error)),
		metadata:     make(map[string]ToolMetadata),
		model:        "",
		temperature:  0.7,
	}
}

// AddTool adds a custom Go function tool to the agent
func (a *Agent) AddTool(name, description string, parameters map[string]interface{}, handler func(context.Context, map[string]interface{}) (interface{}, error)) {
	metadata, _ := inferGenericToolMetadata(name)
	a.AddToolWithMetadata(name, description, parameters, handler, metadata)
}

func (a *Agent) AddToolWithMetadata(name, description string, parameters map[string]interface{}, handler func(context.Context, map[string]interface{}) (interface{}, error), metadata ToolMetadata) {
	a.AddToolWithHandler(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}, handler, metadata)
}

// AddToolWithHandler adds a pre-built tool definition and its handler
func (a *Agent) AddToolWithHandler(def domain.ToolDefinition, handler func(context.Context, map[string]interface{}) (interface{}, error), metadata ...ToolMetadata) {
	if a.handlers == nil {
		a.handlers = make(map[string]func(context.Context, map[string]interface{}) (interface{}, error))
	}
	if a.metadata == nil {
		a.metadata = make(map[string]ToolMetadata)
	}
	a.handlers[def.Function.Name] = handler
	if len(metadata) > 0 {
		a.metadata[def.Function.Name] = metadata[0]
	}
	a.tools = append(a.tools, def)
}

// GetHandler returns the handler for a specific tool
func (a *Agent) GetHandler(name string) (func(context.Context, map[string]interface{}) (interface{}, error), bool) {
	if a.handlers == nil {
		return nil, false
	}
	handler, ok := a.handlers[name]
	return handler, ok
}

func (a *Agent) MetadataOf(name string) ToolMetadata {
	if a == nil || a.metadata == nil {
		return ToolMetadata{}
	}
	return a.metadata[name]
}

// SetAllowedMCPTools sets which MCP tools this agent can use
func (a *Agent) SetAllowedMCPTools(tools []string) {
	a.mcpTools = tools
}

// SetAllowedSkills sets which skills this agent can use
func (a *Agent) SetAllowedSkills(skills []string) {
	a.skills = skills
}

// AddAllowedMCPTool adds a single MCP tool to the allowed list
func (a *Agent) AddAllowedMCPTool(tool string) {
	if isAllAllowed(a.mcpTools) {
		a.mcpTools = []string{tool}
		return
	}
	a.mcpTools = append(a.mcpTools, tool)
}

// AddAllowedSkill adds a single skill to the allowed list
func (a *Agent) AddAllowedSkill(skill string) {
	if isAllAllowed(a.skills) {
		a.skills = []string{skill}
		return
	}
	a.skills = append(a.skills, skill)
}

// isAllAllowed checks if a list contains the wildcard "*"
func isAllAllowed(list []string) bool {
	for _, item := range list {
		if item == "*" {
			return true
		}
	}
	return false
}

// NewAgentWithConfig creates a new Agent with custom configuration
func NewAgentWithConfig(name, instructions string, tools []domain.ToolDefinition) *Agent {
	return &Agent{
		id:           uuid.New().String(),
		name:         name,
		instructions: instructions,
		tools:        tools,
		handoffs:     []*Handoff{},
		mcpTools:     []string{"*"}, // Default to all
		skills:       []string{"*"}, // Default to all
		handlers:     make(map[string]func(context.Context, map[string]interface{}) (interface{}, error)),
		metadata:     make(map[string]ToolMetadata),
		model:        "",
		temperature:  0.7,
	}
}

// ID returns the agent's unique ID
func (a *Agent) ID() string {
	return a.id
}

// Name returns the agent's name
func (a *Agent) Name() string {
	return a.name
}

// Instructions returns the agent's instructions
func (a *Agent) Instructions() string {
	return a.instructions
}

// Tools returns the agent's available tools
func (a *Agent) Tools() []domain.ToolDefinition {
	return a.tools
}

// Handoffs returns the agent's available handoffs
func (a *Agent) Handoffs() []*Handoff {
	return a.handoffs
}

// AddHandoff adds a handoff to the agent
func (a *Agent) AddHandoff(h *Handoff) {
	a.handoffs = append(a.handoffs, h)
}

// Model returns the model name
func (a *Agent) Model() string {
	return a.model
}

// Temperature returns the temperature setting
func (a *Agent) Temperature() float64 {
	return a.temperature
}

// SetInstructions sets the agent's instructions
func (a *Agent) SetInstructions(instructions string) {
	a.instructions = instructions
}

// SetName updates the agent's display name.
func (a *Agent) SetName(name string) {
	a.name = name
}

// SetTools sets the agent's available tools
func (a *Agent) SetTools(tools []domain.ToolDefinition) {
	a.tools = tools
}

// SetModel sets the model name
func (a *Agent) SetModel(model string) {
	a.model = model
}

// SetTemperature sets the temperature
func (a *Agent) SetTemperature(temp float64) {
	a.temperature = temp
}

// GetToolNames returns the names of available tools
func (a *Agent) GetToolNames() []string {
	names := make([]string, len(a.tools))
	for i, tool := range a.tools {
		names[i] = tool.Function.Name
	}
	return names
}

// HasTool checks if the agent has a specific tool
func (a *Agent) HasTool(toolName string) bool {
	for _, tool := range a.tools {
		if tool.Function.Name == toolName {
			return true
		}
	}
	return false
}

// AgentRunner runs an agent with a session
type AgentRunner struct {
	agent      *Agent
	planner    *Planner
	executor   *Executor
	sessionMgr *SessionManager
	store      *Store
}

// NewAgentRunner creates a new agent runner
func NewAgentRunner(
	agent *Agent,
	planner *Planner,
	executor *Executor,
	store *Store,
) *AgentRunner {
	return &AgentRunner{
		agent:      agent,
		planner:    planner,
		executor:   executor,
		sessionMgr: NewSessionManager(),
		store:      store,
	}
}

// Run runs the agent with a goal
func (r *AgentRunner) Run(ctx context.Context, goal string) (*ExecutionResult, error) {
	// Create or get session
	session := r.sessionMgr.CreateSession(r.agent.id)

	// Generate plan
	plan, err := r.planner.Plan(ctx, goal, session)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}

	// Save plan
	if err := r.store.SavePlan(plan); err != nil {
		return nil, fmt.Errorf("failed to save plan: %w", err)
	}

	// Execute plan
	result, err := r.executor.ExecutePlan(ctx, plan, session)
	if err != nil {
		return nil, fmt.Errorf("execution failed: %w", err)
	}

	// Save updated plan
	if err := r.store.SavePlan(plan); err != nil {
		return nil, fmt.Errorf("failed to save plan: %w", err)
	}

	// Save session
	if err := r.store.SaveSession(session); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	return result, nil
}

// RunWithSession runs the agent with a goal and existing session
func (r *AgentRunner) RunWithSession(ctx context.Context, goal string, sessionID string) (*ExecutionResult, error) {
	// Get or create session
	session, ok := r.sessionMgr.GetSession(sessionID)
	if !ok {
		// Try to load from store
		loadedSession, err := r.store.GetSession(sessionID)
		if err != nil {
			// Create new session
			session = NewSessionWithID(sessionID, r.agent.id)
		} else {
			session = loadedSession
		}
		r.sessionMgr.sessions[sessionID] = session
	}

	// Generate plan
	plan, err := r.planner.Plan(ctx, goal, session)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}

	// Save plan
	if err := r.store.SavePlan(plan); err != nil {
		return nil, fmt.Errorf("failed to save plan: %w", err)
	}

	// Execute plan
	result, err := r.executor.ExecutePlan(ctx, plan, session)
	if err != nil {
		return nil, fmt.Errorf("execution failed: %w", err)
	}

	// Save updated plan
	if err := r.store.SavePlan(plan); err != nil {
		return nil, fmt.Errorf("failed to save plan: %w", err)
	}

	// Save session
	if err := r.store.SaveSession(session); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	return result, nil
}
