package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func (m *TeamManager) RegisterDispatcherTools(dispatcher *Service) {
	if dispatcher == nil {
		return
	}
	configureDispatcherService(dispatcher)
	m.configureDispatcherVerifierHook(dispatcher)

	register := func(name, description string, parameters map[string]interface{}, metadata ToolMetadata, handler func(context.Context, map[string]interface{}) (interface{}, error)) {
		dispatcher.AddToolWithMetadata(name, description, parameters, handler, metadata)
	}

	register("route_builtin_request", "Run PromptOptimizer and IntentRouter in parallel, then dispatch the optimized request to the best-fit built-in specialist and return the inline result.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The user's substantive request to route.",
			},
			"request": map[string]interface{}{
				"type":        "string",
				"description": "Alias for prompt; accepted for compatibility with model/tool-call variants.",
			},
		},
		"required": []string{"prompt"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		prompt := firstNonEmpty(getStringArg(args, "prompt"), getStringArg(args, "request"))
		if prompt == "" {
			return nil, fmt.Errorf("prompt is required")
		}
		queryContext := dispatcher.resolveMemoryQueryContextFromContext(ctx)
		routed, err := m.routeBuiltInRequest(ctx, prompt, queryContext)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"target_agent":        routed.TargetAgent,
			"intent_type":         routed.IntentType,
			"routing_reason":      routed.Reason,
			"optimized_prompt":    routed.OptimizedPrompt,
			"result":              routed.Result,
			"blocked":             routed.Blocked,
			"dispatch_task_id":    routed.DispatchTaskID,
			"verification_result": routed.VerificationResult,
		}, nil
	})

	register("list_teams", "List all teams with their current runtime status.", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		statuses, err := m.ListTeamStatuses()
		if err != nil {
			return nil, err
		}
		out := make([]map[string]interface{}, 0, len(statuses))
		for _, status := range statuses {
			out = append(out, map[string]interface{}{
				"team_id":         status.TeamID,
				"name":            status.Name,
				"description":     status.Description,
				"status":          status.Status,
				"agent_count":     status.AgentCount,
				"orchestrators":   append([]string(nil), status.OrchestratorNames...),
				"running_tasks":   status.RunningTasks,
				"queued_tasks":    status.QueuedTasks,
				"active_task_ids": append([]string(nil), status.ActiveTaskIDs...),
			})
		}
		return out, nil
	})

	register("get_team_status", "Get the runtime status of one team.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"team_id":   map[string]interface{}{"type": "string"},
			"team_name": map[string]interface{}{"type": "string"},
		},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		team, err := m.resolveTeamRef(getStringArg(args, "team_id"), getStringArg(args, "team_name"))
		if err != nil {
			return nil, err
		}
		return m.GetTeamStatus(team.ID)
	})

	register("list_agents", "List all known agents with their runtime status.", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		statuses, err := m.ListAgentStatuses()
		if err != nil {
			return nil, err
		}
		out := make([]map[string]interface{}, 0, len(statuses))
		for _, status := range statuses {
			out = append(out, map[string]interface{}{
				"agent_id":            status.AgentID,
				"name":                status.Name,
				"kind":                status.Kind,
				"status":              status.Status,
				"built_in":            status.BuiltIn,
				"preferred_provider":  status.PreferredProvider,
				"preferred_model":     status.PreferredModel,
				"configured_model":    status.ConfiguredModel,
				"runtime_mode":        status.RuntimeMode,
				"worker_count":        status.WorkerCount,
				"active_workers":      status.ActiveWorkers,
				"queue_depth":         status.QueueDepth,
				"processed_messages":  status.ProcessedMessages,
				"last_message_type":   status.LastMessageType,
				"last_correlation_id": status.LastCorrelationID,
				"last_error":          status.LastError,
				"last_active_at":      status.LastActiveAt,
				"running_tasks":       status.RunningTaskCount,
				"queued_tasks":        status.QueuedTaskCount,
				"teams":               append([]TeamMembership(nil), status.Teams...),
			})
		}
		return out, nil
	})

	register("get_agent_status", "Get the runtime status of one agent.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_name": map[string]interface{}{
				"type":        "string",
				"description": "The target agent name.",
			},
		},
		"required": []string{"agent_name"},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agentName := getStringArg(args, "agent_name")
		if agentName == "" {
			return nil, fmt.Errorf("agent_name is required")
		}
		return m.GetAgentStatus(agentName)
	})

	register("submit_agent_task", "Submit work to a standalone or team agent and return immediately with a task id.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_name": map[string]interface{}{
				"type":        "string",
				"description": "The target agent name.",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The task prompt to run in the background.",
			},
		},
		"required": []string{"agent_name", "prompt"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agentName := getStringArg(args, "agent_name")
		prompt := getStringArg(args, "prompt")
		if agentName == "" {
			return nil, fmt.Errorf("agent_name is required")
		}
		if prompt == "" {
			return nil, fmt.Errorf("prompt is required")
		}

		task, err := m.SubmitAgentTask(ctx, dispatcher.CurrentSessionID(), agentName, prompt)
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

	register("submit_team_task", "Queue a task for a team and return an immediate acknowledgement.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"team_id":   map[string]interface{}{"type": "string"},
			"team_name": map[string]interface{}{"type": "string"},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The task prompt to queue.",
			},
			"agent_names": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional target agent names. Defaults to the team orchestrator.",
			},
		},
		"required": []string{"prompt"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		team, err := m.resolveTeamRef(getStringArg(args, "team_id"), getStringArg(args, "team_name"))
		if err != nil {
			return nil, err
		}
		lead, err := m.GetLeadAgentForTeam(team.ID)
		if err != nil {
			return nil, err
		}

		prompt := getStringArg(args, "prompt")
		if prompt == "" {
			return nil, fmt.Errorf("prompt is required")
		}

		agentNames := getStringSliceArg(args, "agent_names")
		if len(agentNames) == 0 {
			agentNames = []string{lead.Name}
		}

		task, err := m.SubmitTeamTask(ctx, dispatcher.CurrentSessionID(), team.ID, prompt, agentNames)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"task_id":           task.ID,
			"team_id":           task.TeamID,
			"team_name":         team.Name,
			"orchestrator_name": task.OrchestratorName,
			"agent_names":       append([]string(nil), task.AgentNames...),
			"ack_message":       task.AckMessage,
			"status":            task.Status,
		}, nil
	})

	register("get_task_status", "Get one background task status and latest result.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "The task id returned by submit_agent_task or submit_team_task.",
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

	register("list_session_tasks", "List recent background tasks created in the current Dispatcher conversation.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Optional maximum number of tasks to return.",
			},
		},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		sessionID := strings.TrimSpace(dispatcher.CurrentSessionID())
		if sessionID == "" {
			return []map[string]interface{}{}, nil
		}
		limit := getIntArg(args, "limit", 10)
		tasks := m.ListSessionTasks(sessionID, limit)
		out := make([]map[string]interface{}, 0, len(tasks))
		for _, task := range tasks {
			out = append(out, map[string]interface{}{
				"task_id":     task.ID,
				"kind":        task.Kind,
				"status":      task.Status,
				"agent_name":  task.AgentName,
				"agent_names": append([]string(nil), task.AgentNames...),
				"team_id":     task.TeamID,
				"team_name":   task.TeamName,
				"result_text": task.ResultText,
				"error":       task.Error,
				"created_at":  task.CreatedAt,
				"started_at":  task.StartedAt,
				"finished_at": task.FinishedAt,
			})
		}
		return out, nil
	})

	register("list_team_tasks", "List recent tasks for a team.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"team_id":   map[string]interface{}{"type": "string"},
			"team_name": map[string]interface{}{"type": "string"},
			"limit":     map[string]interface{}{"type": "number"},
		},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		team, err := m.resolveTeamRef(getStringArg(args, "team_id"), getStringArg(args, "team_name"))
		if err != nil {
			return nil, err
		}
		limit := getIntArg(args, "limit", 10)
		tasks := m.ListSharedTasksForTeam(team.ID, time.Time{}, limit)
		out := make([]map[string]interface{}, 0, len(tasks))
		for _, task := range tasks {
			out = append(out, map[string]interface{}{
				"task_id":           task.ID,
				"orchestrator_name": task.OrchestratorName,
				"agent_names":       append([]string(nil), task.AgentNames...),
				"prompt":            task.Prompt,
				"status":            task.Status,
				"ack_message":       task.AckMessage,
				"result_text":       task.ResultText,
				"created_at":        task.CreatedAt,
				"started_at":        task.StartedAt,
				"finished_at":       task.FinishedAt,
			})
		}
		return out, nil
	})
}

var dispatcherBaseAllowedToolNames = map[string]struct{}{
	"task_complete":         {},
	"task_blocked":          {},
	"llm":                   {},
	"route_builtin_request": {},
	"send_agent_message":    {},
	"get_agent_messages":    {},
	"list_teams":            {},
	"get_team_status":       {},
	"list_agents":           {},
	"get_agent_status":      {},
	"submit_agent_task":     {},
	"submit_team_task":      {},
	"get_task_status":       {},
	"list_session_tasks":    {},
	"list_team_tasks":       {},
}

func configureDispatcherService(dispatcher *Service) {
	if dispatcher == nil {
		return
	}
	allowedToolNames := dispatcherAllowedToolNames(dispatcher)

	if dispatcher.toolRegistry != nil {
		for _, name := range []string{
			"delegate_to_subagent",
			"search_available_tools",
			"tool_search_tool_regex",
			"tool_search_tool_bm25",
			"memory_save",
			"memory_recall",
			"memory_update",
			"memory_delete",
		} {
			if _, ok := allowedToolNames[name]; !ok {
				dispatcher.toolRegistry.Unregister(name)
			}
		}
	}

	if dispatcher.agent != nil {
		filteredTools := make([]domain.ToolDefinition, 0, len(dispatcher.agent.tools))
		for _, tool := range dispatcher.agent.tools {
			if _, ok := allowedToolNames[tool.Function.Name]; ok {
				filteredTools = append(filteredTools, tool)
			}
		}
		dispatcher.agent.SetTools(filteredTools)

		for name := range dispatcher.agent.handlers {
			if _, ok := allowedToolNames[name]; !ok {
				delete(dispatcher.agent.handlers, name)
			}
		}
	}
}

func dispatcherAllowedToolNames(dispatcher *Service) map[string]struct{} {
	allowed := make(map[string]struct{}, len(dispatcherBaseAllowedToolNames)+4)
	for name := range dispatcherBaseAllowedToolNames {
		allowed[name] = struct{}{}
	}
	if dispatcher != nil && dispatcher.shouldExposeMemoryTools() {
		allowed["memory_save"] = struct{}{}
		allowed["memory_recall"] = struct{}{}
		allowed["memory_update"] = struct{}{}
		allowed["memory_delete"] = struct{}{}
	}
	return allowed
}

func configureOrchestratorService(orchestrator *Service) {
	if orchestrator == nil {
		return
	}

	if orchestrator.toolRegistry != nil {
		orchestrator.toolRegistry.Unregister("delegate_to_subagent")
	}

	if orchestrator.agent != nil {
		filteredTools := make([]domain.ToolDefinition, 0, len(orchestrator.agent.tools))
		for _, tool := range orchestrator.agent.tools {
			if tool.Function.Name == "delegate_to_subagent" {
				continue
			}
			filteredTools = append(filteredTools, tool)
		}
		orchestrator.agent.SetTools(filteredTools)
		delete(orchestrator.agent.handlers, "delegate_to_subagent")
	}
}

func (m *TeamManager) resolveTeamRef(teamID, teamName string) (*Team, error) {
	teamID = strings.TrimSpace(teamID)
	teamName = strings.TrimSpace(teamName)
	switch {
	case teamID != "":
		return m.store.GetTeam(teamID)
	case teamName != "":
		return m.store.GetTeamByName(teamName)
	default:
		return m.store.GetTeam(defaultTeamID)
	}
}

func getStringArg(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	if value, ok := args[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func getStringSliceArg(args map[string]interface{}, key string) []string {
	if args == nil {
		return nil
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				out = append(out, value)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if item, ok := value.(string); ok {
				item = strings.TrimSpace(item)
				if item != "" {
					out = append(out, item)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func getIntArg(args map[string]interface{}, key string, fallback int) int {
	if args == nil {
		return fallback
	}
	switch value := args[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return fallback
	}
}
