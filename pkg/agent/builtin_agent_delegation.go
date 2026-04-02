package agent

import (
	"context"
	"fmt"
	"strings"
)

var coreDelegableBuiltInAgentNames = []string{
	defaultAssistantAgentName,
	defaultOperatorAgentName,
	defaultStakeholderAgentName,
	defaultArchivistAgentName,
	defaultVerifierAgentName,
}

func (m *TeamManager) registerBuiltInAgentDelegationTools(svc *Service, model *AgentModel) {
	if svc == nil || model == nil {
		return
	}
	if isBuiltInAgentModel(model) && !canUseBuiltInDelegationTools(model) {
		return
	}
	allowedAgentNames := delegableBuiltInAgentNamesFor(model)
	allowedAgentLabel := strings.Join(allowedAgentNames, ", ")

	register := func(name, description string, parameters map[string]interface{}, metadata ToolMetadata, handler func(context.Context, map[string]interface{}) (interface{}, error)) {
		if svc.toolRegistry != nil && svc.toolRegistry.Has(name) {
			return
		}
		svc.AddToolWithMetadata(name, description, parameters, handler, metadata)
	}

	register("list_builtin_agents", "List delegable built-in standalone agents available to this agent.", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agents, err := m.listDelegableBuiltInAgentsFor(model)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]interface{}, 0, len(agents))
		for _, builtin := range agents {
			out = append(out, map[string]interface{}{
				"name":         builtin.Name,
				"description":  builtin.Description,
				"instructions": singleLinePromptText(builtin.Instructions),
				"model":        strings.TrimSpace(builtin.Model),
			})
		}
		return out, nil
	})

	register("delegate_builtin_agent", fmt.Sprintf("Synchronously delegate a focused task to one allowed built-in standalone agent (%s) and wait for the inline result.", allowedAgentLabel), map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_name": map[string]interface{}{
				"type":        "string",
				"description": "Built-in standalone agent name: " + allowedAgentLabel + ".",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Task prompt to run on the built-in agent.",
			},
		},
		"required": []string{"agent_name", "prompt"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agentName := getStringArg(args, "agent_name")
		prompt := getStringArg(args, "prompt")
		if prompt == "" {
			return nil, fmt.Errorf("prompt is required")
		}
		builtin, err := m.resolveDelegableBuiltInAgentFor(model, agentName)
		if err != nil {
			return nil, err
		}
		queryContext := svc.resolveMemoryQueryContextFromContext(ctx)
		runOptions := []RunOption{
			WithInheritedMemoryScope(queryContext.AgentID, queryContext.TeamID, queryContext.UserID),
		}
		result, err := m.dispatchTaskWithOptions(ctx, builtin.Name, prompt, "", runOptions)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"agent_name": builtin.Name,
			"result":     result,
		}, nil
	})

	register("submit_builtin_agent_task", fmt.Sprintf("Asynchronously submit work to one allowed built-in standalone agent (%s) and return immediately with a task id.", allowedAgentLabel), map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_name": map[string]interface{}{
				"type":        "string",
				"description": "Built-in standalone agent name: " + allowedAgentLabel + ".",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Task prompt to run asynchronously on the built-in agent.",
			},
		},
		"required": []string{"agent_name", "prompt"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agentName := getStringArg(args, "agent_name")
		prompt := getStringArg(args, "prompt")
		if prompt == "" {
			return nil, fmt.Errorf("prompt is required")
		}
		builtin, err := m.resolveDelegableBuiltInAgentFor(model, agentName)
		if err != nil {
			return nil, err
		}
		task, err := m.SubmitAgentTask(ctx, svc.CurrentSessionID(), builtin.Name, prompt)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"task_id":     task.ID,
			"agent_name":  task.AgentName,
			"ack_message": task.AckMessage,
			"status":      task.Status,
		}, nil
	})

	register("get_delegated_task_status", "Get the status of an async built-in-agent task previously created by submit_builtin_agent_task.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "Task id returned by submit_builtin_agent_task.",
			},
		},
		"required": []string{"task_id"},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		taskID := getStringArg(args, "task_id")
		if taskID == "" {
			return nil, fmt.Errorf("task_id is required")
		}
		return m.GetTask(taskID)
	})
}

func (m *TeamManager) buildDelegableBuiltInAgentsContext(model *AgentModel) string {
	if model == nil || isBuiltInAgentModel(model) {
		return ""
	}
	agents, err := m.listDelegableBuiltInAgentsFor(model)
	if err != nil || len(agents) == 0 {
		return ""
	}

	lines := []string{
		"Delegable system built-in agents you may use in addition to your own role and capabilities:",
		"- Prefer your own role first. Delegate only when the built-in agent is a better fit.",
		"- Use `delegate_builtin_agent` for a synchronous inline result.",
		"- Use `submit_builtin_agent_task` for background work you do not need to wait on immediately.",
	}
	for _, builtin := range agents {
		line := fmt.Sprintf("- %s: %s", builtin.Name, builtin.Description)
		if instr := strings.TrimSpace(builtin.Instructions); instr != "" {
			line += " Responsibilities: " + singleLinePromptText(instr)
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func canUseBuiltInDelegationTools(model *AgentModel) bool {
	if model == nil || !isBuiltInAgentModel(model) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(model.Name)) {
	case strings.ToLower(defaultIntentRouterAgentName):
		return true
	default:
		return false
	}
}

func delegableBuiltInAgentNamesFor(model *AgentModel) []string {
	return append([]string(nil), coreDelegableBuiltInAgentNames...)
}

func (m *TeamManager) listDelegableBuiltInAgentsFor(model *AgentModel) ([]*AgentModel, error) {
	names := delegableBuiltInAgentNamesFor(model)
	out := make([]*AgentModel, 0, len(names))
	for _, name := range names {
		model, err := m.store.GetAgentModelByName(name)
		if err != nil {
			return nil, err
		}
		if !isBuiltInAgentModel(model) {
			continue
		}
		out = append(out, model)
	}
	return out, nil
}

func (m *TeamManager) resolveDelegableBuiltInAgentFor(source *AgentModel, name string) (*AgentModel, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("agent_name is required")
	}
	for _, allowed := range delegableBuiltInAgentNamesFor(source) {
		if strings.EqualFold(allowed, name) {
			model, err := m.store.GetAgentModelByName(allowed)
			if err != nil {
				return nil, err
			}
			if !isBuiltInAgentModel(model) {
				return nil, fmt.Errorf("%s is not a delegable built-in agent", name)
			}
			return model, nil
		}
	}
	return nil, fmt.Errorf("%s is not a delegable built-in agent", name)
}
