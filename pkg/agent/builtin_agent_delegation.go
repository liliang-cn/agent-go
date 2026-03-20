package agent

import (
	"context"
	"fmt"
	"strings"
)

var delegableBuiltInAgentNames = []string{
	defaultAssistantAgentName,
	defaultOperatorAgentName,
	defaultStakeholderAgentName,
	defaultArchivistAgentName,
	defaultVerifierAgentName,
}

func (m *SquadManager) registerBuiltInAgentDelegationTools(svc *Service, model *AgentModel) {
	if svc == nil || model == nil {
		return
	}
	if isBuiltInAgentModel(model) && !strings.EqualFold(strings.TrimSpace(model.Name), defaultConciergeAgentName) {
		return
	}

	register := func(name, description string, parameters map[string]interface{}, handler func(context.Context, map[string]interface{}) (interface{}, error)) {
		if svc.toolRegistry != nil && svc.toolRegistry.Has(name) {
			return
		}
		svc.AddTool(name, description, parameters, handler)
	}

	register("list_builtin_agents", "List delegable built-in standalone agents that this custom agent may use when its own role is not the best fit.", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agents, err := m.listDelegableBuiltInAgents()
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

	register("delegate_builtin_agent", "Synchronously delegate a focused task to one built-in standalone agent such as Operator, Assistant, Stakeholder, Archivist, or Verifier and wait for the inline result.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_name": map[string]interface{}{
				"type":        "string",
				"description": "Built-in standalone agent name: Assistant, Operator, Stakeholder, Archivist, or Verifier.",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Task prompt to run on the built-in agent.",
			},
		},
		"required": []string{"agent_name", "prompt"},
	}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agentName := getStringArg(args, "agent_name")
		prompt := getStringArg(args, "prompt")
		if prompt == "" {
			return nil, fmt.Errorf("prompt is required")
		}
		builtin, err := m.resolveDelegableBuiltInAgent(agentName)
		if err != nil {
			return nil, err
		}
		result, err := m.DispatchTask(ctx, builtin.Name, prompt)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"agent_name": builtin.Name,
			"result":     result,
		}, nil
	})

	register("submit_builtin_agent_task", "Asynchronously submit work to one built-in standalone agent and return immediately with a task id.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_name": map[string]interface{}{
				"type":        "string",
				"description": "Built-in standalone agent name: Assistant, Operator, Stakeholder, Archivist, or Verifier.",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Task prompt to run asynchronously on the built-in agent.",
			},
		},
		"required": []string{"agent_name", "prompt"},
	}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agentName := getStringArg(args, "agent_name")
		prompt := getStringArg(args, "prompt")
		if prompt == "" {
			return nil, fmt.Errorf("prompt is required")
		}
		builtin, err := m.resolveDelegableBuiltInAgent(agentName)
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
	}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		taskID := getStringArg(args, "task_id")
		if taskID == "" {
			return nil, fmt.Errorf("task_id is required")
		}
		return m.GetTask(taskID)
	})
}

func (m *SquadManager) buildDelegableBuiltInAgentsContext(model *AgentModel) string {
	if model == nil || isBuiltInAgentModel(model) {
		return ""
	}
	agents, err := m.listDelegableBuiltInAgents()
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

func (m *SquadManager) listDelegableBuiltInAgents() ([]*AgentModel, error) {
	out := make([]*AgentModel, 0, len(delegableBuiltInAgentNames))
	for _, name := range delegableBuiltInAgentNames {
		model, err := m.store.GetAgentModelByName(name)
		if err != nil {
			return nil, err
		}
		if len(model.Squads) != 0 || !isBuiltInAgentModel(model) {
			continue
		}
		out = append(out, model)
	}
	return out, nil
}

func (m *SquadManager) resolveDelegableBuiltInAgent(name string) (*AgentModel, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("agent_name is required")
	}
	for _, allowed := range delegableBuiltInAgentNames {
		if strings.EqualFold(allowed, name) {
			model, err := m.store.GetAgentModelByName(allowed)
			if err != nil {
				return nil, err
			}
			if len(model.Squads) != 0 || !isBuiltInAgentModel(model) {
				return nil, fmt.Errorf("%s is not an eligible built-in standalone agent", name)
			}
			return model, nil
		}
	}
	return nil, fmt.Errorf("%s is not a delegable built-in agent", name)
}
