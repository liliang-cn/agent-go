package agent

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	"github.com/liliang-cn/agent-go/v2/pkg/router"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

// SetMCPService sets the MCP service for full public access
func (s *Service) SetMCPService(mcpSvc *mcp.Service) {
	s.MCP = mcpSvc
	// Register MCP tools in registry for tool search
	if mcpSvc != nil && s.toolRegistry != nil {
		go s.registerMCPToolsInRegistry(mcpSvc)
	}
}

// registerMCPToolsInRegistry registers MCP tools in the registry for tool search
func (s *Service) registerMCPToolsInRegistry(mcpSvc *mcp.Service) {
	tools := mcpSvc.GetAvailableTools(context.Background())
	for _, t := range tools {
		params := t.InputSchema
		if params == nil {
			params = map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"args": map[string]interface{}{
						"description": "arguments",
						"type":        "object",
					},
				},
			}
		}
		// Register as deferred tool for search
		toolName := t.Name
		s.toolRegistry.RegisterWithMetadata(domain.ToolDefinition{
			Type:         "function",
			DeferLoading: true, // MCP tools are always deferred for search
			Function: domain.ToolFunction{
				Name:        toolName,
				Description: t.Description,
				Parameters:  params,
			},
		}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return mcpSvc.CallTool(ctx, toolName, args)
		}, CategoryMCP, s.lookupToolMetadata(toolName))
	}
}

// registerSkillsInRegistry registers skills in the registry for tool search
func (s *Service) registerSkillsInRegistry(skillsService *skills.Service) {
	skillsList, err := skillsService.ListSkills(context.Background(), skills.SkillFilter{})
	if err != nil {
		return
	}
	for _, sk := range skillsList {
		if !sk.Enabled || sk.DisableModelInvocation {
			continue
		}
		// Build variable schema from skill definition
		properties := make(map[string]interface{})
		required := make([]string, 0)
		for _, v := range sk.Variables {
			prop := map[string]interface{}{
				"type":        getSkillVarTypeString(v.Type),
				"description": v.Description,
			}
			if v.Default != nil {
				prop["default"] = v.Default
			}
			properties[v.Name] = prop
			if v.Required {
				required = append(required, v.Name)
			}
		}
		desc := sk.Description
		if desc == "" {
			desc = sk.Name
		}
		desc = "Skill workflow: " + desc + ". Call this tool to receive step-by-step instructions for this task; you MUST then follow those instructions to complete the work."
		toolName := "skill_" + sk.ID
		// Register as deferred tool for search
		skillID := sk.ID
		s.toolRegistry.RegisterWithMetadata(domain.ToolDefinition{
			Type:         "function",
			DeferLoading: true, // Skills are always deferred for search
			Function: domain.ToolFunction{
				Name:        toolName,
				Description: desc,
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": properties,
					"required":   required,
				},
			},
		}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			res, err := skillsService.Execute(ctx, &skills.ExecutionRequest{
				SkillID:   skillID,
				Variables: args,
			})
			if err != nil {
				return nil, err
			}
			return res.Output, nil
		}, CategorySkill, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock})
	}
}

// SetRouter sets the semantic router for improved intent recognition
func (s *Service) SetRouter(routerService *router.Service) {
	s.routerService = routerService
	s.Router = routerService
	s.planner.SetRouter(routerService)
}

// SetPTC sets the PTC integration for programmatic tool calling
func (s *Service) SetPTC(ptcIntegration *PTCIntegration) {
	s.ptcIntegration = ptcIntegration
	s.PTC = ptcIntegration
	if ptcIntegration != nil {
		ptcIntegration.SetSearchProvider(s)
	}
}

// SetModelInfo sets the model metadata for Info()
func (s *Service) SetModelInfo(modelName, baseURL string) {
	s.modelName = modelName
	s.baseURL = baseURL
}

// SetHistoryStore sets the history store for execution recording
func (s *Service) SetHistoryStore(historyStore *HistoryStore) {
	s.historyStore = historyStore
}

// GetHistoryStore returns the current history store
func (s *Service) GetHistoryStore() *HistoryStore {
	return s.historyStore
}

// MemoryService returns the agent's DB-backed memory service (may be nil).
// Use this to integrate external components with the same memory store.
func (s *Service) MemoryService() domain.MemoryService {
	return s.memoryService
}

// isPTCEnabled reports whether PTC mode is active.
// When PTC is enabled the agent is expected to call tools explicitly via
// execute_javascript / callTool, so automatic RAG pre-injection must be
// suppressed to avoid spoiling the answer before the LLM can act.
func (s *Service) isPTCEnabled() bool {
	return s.ptcIntegration != nil && s.ptcIntegration.config != nil && s.ptcIntegration.config.Enabled
}

// RegisterAgent registers a new agent with the service
func (s *Service) RegisterAgent(agent *Agent) {
	if s.registry != nil {
		s.registry.Register(agent)
	}
}

// AddTool registers a custom Go function tool on the default agent.
// The tool becomes available to the LLM via function calling and, when PTC is
// enabled, also via callTool() inside the JavaScript sandbox.
func (s *Service) AddTool(name, description string, parameters map[string]interface{},
	handler func(context.Context, map[string]interface{}) (interface{}, error)) {
	metadata, _ := inferGenericToolMetadata(name)
	s.AddToolWithMetadata(name, description, parameters, handler, metadata)
}

func (s *Service) AddToolWithMetadata(name, description string, parameters map[string]interface{},
	handler func(context.Context, map[string]interface{}) (interface{}, error), metadata ToolMetadata) {
	def := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}

	// Single registration point: the ToolRegistry.
	// collectAllAvailableTools() reads from here; PTC's callTool() routes through
	// the registry too (via SyncToPTCRouter called at build time, and for tools
	// added after build we also sync to the ptcRouter directly below).
	s.toolRegistry.RegisterWithMetadata(def, handler, CategoryCustom, metadata)

	// Also keep a handler on the default agent so currentAgent.GetHandler() still
	// works for multi-agent priority dispatch in executeToolCalls().
	if s.agent != nil {
		s.agent.AddToolWithMetadata(name, description, parameters, handler, metadata)
	}

	// If PTC is already configured, register directly on the router so that
	// tools added after Build() are immediately accessible via callTool().
	if s.ptcIntegration != nil && s.ptcIntegration.router != nil {
		_ = s.ptcIntegration.router.RegisterTool(name, &ptc.ToolInfo{
			Name:        name,
			Description: description,
			Parameters:  parameters,
			Category:    CategoryCustom,
		}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return handler(ctx, args)
		})
	}
}

// Register registers a *Tool (created via NewTool[T] or BuildTool) on the default
// agent and on the PTC router. It is the preferred alternative to AddTool when
// using the typed or fluent builder APIs.
func (s *Service) Register(tool *Tool) {
	def := tool.toToolDefinition()
	s.toolRegistry.RegisterWithMetadata(def, tool.handler, CategoryCustom, ToolMetadata{
		ReadOnly:          tool.ReadOnly(),
		ConcurrencySafe:   tool.ConcurrencySafe(),
		Destructive:       tool.Destructive(),
		InterruptBehavior: tool.InterruptBehavior(),
	})

	if s.agent != nil {
		s.agent.AddToolWithHandler(def, tool.handler, ToolMetadata{
			ReadOnly:          tool.ReadOnly(),
			ConcurrencySafe:   tool.ConcurrencySafe(),
			Destructive:       tool.Destructive(),
			InterruptBehavior: tool.InterruptBehavior(),
		})
	}

	if s.ptcIntegration != nil && s.ptcIntegration.config.Enabled {
		info := &ptc.ToolInfo{
			Name:        def.Function.Name,
			Description: def.Function.Description,
			Parameters:  def.Function.Parameters,
			Category:    CategoryCustom,
		}
		_ = s.ptcIntegration.router.RegisterTool(def.Function.Name, info, tool.handler)
	}
}

// SetSkillsService sets the skills service for agent integration
func (s *Service) SetSkillsService(skillsService *skills.Service) {
	s.skillsService = skillsService
	s.Skills = skillsService

	// Register skills in registry for tool search
	if skillsService != nil && s.toolRegistry != nil {
		go s.registerSkillsInRegistry(skillsService)
	}

	// Re-create agent with updated tools
	if skillsService != nil {
		tools := collectAvailableTools(s.mcpService, s.ragProcessor, skillsService)
		s.agent = NewAgentWithConfig(
			s.agent.Name(),
			s.agent.Instructions(),
			tools,
		)
		s.planner = NewPlanner(s, s.llmService, tools)
		// Restore router if it was set
		if s.routerService != nil {
			s.planner.SetRouter(s.routerService)
		}
		// Set skills service on executor for tool execution
		s.executor.SetSkillsService(skillsService)
	}
}

// SetProgressCallback sets the progress callback for execution events
func (s *Service) SetProgressCallback(cb ProgressCallback) {
	s.progressCb = cb
}

// SetDebug sets debug mode
func (s *Service) SetDebug(debug bool) {
	s.debug = debug
}

// SetAgentInstructions sets the instructions for the default agent
func (s *Service) SetAgentInstructions(instructions string) {
	if s.agent != nil {
		s.agent.SetInstructions(instructions)
	}
}

// RegisterHook registers a hook for lifecycle events
// Returns the hook ID for later unregistration
func (s *Service) RegisterHook(event HookEvent, handler HookHandler, opts ...HookOption) string {
	return s.hooks.Register(event, handler, opts...)
}

// UnregisterHook removes a hook by ID
func (s *Service) UnregisterHook(hookID string) bool {
	return s.hooks.Unregister(hookID)
}

// GetHooks returns the hook registry for advanced usage
func (s *Service) GetHooks() *HookRegistry {
	return s.hooks
}

// CreateSubAgent creates a sub-agent wrapper for isolated execution
func (s *Service) CreateSubAgent(agent *Agent, goal string, opts ...SubAgentOption) *SubAgent {
	cfg := SubAgentConfig{
		Agent:   agent,
		Goal:    goal,
		Service: s,
	}
	return NewSubAgent(cfg, opts...)
}

// emitProgress emits a progress event if callback is set
func (s *Service) emitProgress(eventType, message string, round int, tool string) {
	if s.progressCb != nil {
		s.progressCb(ProgressEvent{
			Type:    eventType,
			Round:   round,
			Message: message,
			Tool:    tool,
		})
	}
}

// AddFunctionSkill adds a function-based skill dynamically
func (s *Service) AddFunctionSkill(id, name, description string, fn func(ctx context.Context, vars map[string]interface{}) (string, error)) error {
	if s.skillsService == nil {
		return fmt.Errorf("skills service not initialized")
	}
	s.skillsService.RegisterFunction(id, name, description, fn)
	return nil
}
