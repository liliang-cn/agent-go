package agent

import (
	"context"
	"strings"
)

func (m *TeamManager) registerTaskPlanTools(service *Service, currentSessionID func() string) {
	if m == nil || service == nil {
		return
	}
	sessionID := func() string {
		if currentSessionID == nil {
			return ""
		}
		return strings.TrimSpace(currentSessionID())
	}
	register := func(name, description string, parameters map[string]interface{}, metadata ToolMetadata, handler func(context.Context, map[string]interface{}) (interface{}, error)) {
		if service.toolRegistry != nil && service.toolRegistry.Has(name) {
			return
		}
		service.AddToolWithMetadata(name, description, parameters, handler, metadata)
	}

	register("task_plan_create", "Create a lightweight work plan for a complex multi-step task. Use this before assigning planned work items to agents.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"goal": map[string]interface{}{
				"type":        "string",
				"description": "The overall user goal this plan supports.",
			},
			"items": map[string]interface{}{
				"type":        "array",
				"description": "Planned work items. Keep this focused; default to no more than five items.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":          map[string]interface{}{"type": "string", "description": "Optional stable item id."},
						"subject":     map[string]interface{}{"type": "string", "description": "Brief actionable title."},
						"description": map[string]interface{}{"type": "string", "description": "Detailed work instruction."},
						"active_form": map[string]interface{}{"type": "string", "description": "Present-progress label."},
						"owner_agent": map[string]interface{}{"type": "string", "description": "Preferred team agent owner."},
						"blocks":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
						"blocked_by":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					},
					"required": []string{"subject"},
				},
			},
		},
		"required": []string{"goal", "items"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		goal := getStringArg(args, "goal")
		items := taskPlanItemsFromToolArgs(args["items"])
		plan, err := m.Plans().Create(ctx, TaskPlanCreateOptions{
			SessionID: sessionID(),
			Goal:      goal,
			Items:     items,
		})
		if err != nil {
			return nil, err
		}
		return taskPlanToolView(plan), nil
	})

	register("task_plan_list", "List lightweight work plans for the current conversation.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Optional maximum number of plans to return.",
			},
		},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		plans, err := m.Plans().List(ctx)
		if err != nil {
			return nil, err
		}
		currentSessionID := sessionID()
		limit := getIntArg(args, "limit", 10)
		out := make([]map[string]interface{}, 0, len(plans))
		for _, plan := range plans {
			if currentSessionID != "" && strings.TrimSpace(plan.SessionID) != currentSessionID {
				continue
			}
			out = append(out, taskPlanToolView(plan))
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		return out, nil
	})

	register("task_plan_update", "Update a planned work item status, owner, dependencies, execution task id, or result.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"plan_id":           map[string]interface{}{"type": "string", "description": "Plan id returned by task_plan_create."},
			"item_id":           map[string]interface{}{"type": "string", "description": "Plan item id to update."},
			"subject":           map[string]interface{}{"type": "string"},
			"description":       map[string]interface{}{"type": "string"},
			"active_form":       map[string]interface{}{"type": "string"},
			"owner_agent":       map[string]interface{}{"type": "string"},
			"status":            map[string]interface{}{"type": "string", "enum": []string{"pending", "in_progress", "completed", "blocked", "failed"}},
			"add_blocks":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"add_blocked_by":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"execution_task_id": map[string]interface{}{"type": "string"},
			"result_text":       map[string]interface{}{"type": "string"},
			"error":             map[string]interface{}{"type": "string"},
		},
		"required": []string{"plan_id", "item_id"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		opts := taskPlanUpdateOptionsFromToolArgs(args)
		item, err := m.Plans().UpdateItem(ctx, getStringArg(args, "plan_id"), getStringArg(args, "item_id"), opts)
		if err != nil {
			return nil, err
		}
		return taskPlanItemToolView(*item), nil
	})

	register("task_plan_submit_item", "Start one ready planned work item as a real async agent task and link the execution task id back to the plan item.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"plan_id":    map[string]interface{}{"type": "string", "description": "Plan id returned by task_plan_create."},
			"item_id":    map[string]interface{}{"type": "string", "description": "Ready plan item id to execute."},
			"agent_name": map[string]interface{}{"type": "string", "description": "Optional override agent. Defaults to item owner_agent."},
			"input":      map[string]interface{}{"type": "string", "description": "Optional override prompt. Defaults to item description or subject."},
		},
		"required": []string{"plan_id", "item_id"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		task, err := m.Plans().SubmitItem(ctx, getStringArg(args, "plan_id"), getStringArg(args, "item_id"), TaskPlanSubmitItemOptions{
			SessionID: sessionID(),
			AgentName: getStringArg(args, "agent_name"),
			Input:     getStringArg(args, "input"),
		})
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"task_id":    task.ID,
			"status":     task.Status,
			"agent_name": task.AgentName,
			"input":      task.Input,
		}, nil
	})
}

func taskPlanItemsFromToolArgs(raw interface{}) []TaskPlanItem {
	rawItems, _ := raw.([]interface{})
	items := make([]TaskPlanItem, 0, len(rawItems))
	for _, rawItem := range rawItems {
		obj, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}
		item := TaskPlanItem{
			ID:          getStringArg(obj, "id"),
			Subject:     getStringArg(obj, "subject"),
			Description: getStringArg(obj, "description"),
			ActiveForm:  getStringArg(obj, "active_form"),
			OwnerAgent:  getStringArg(obj, "owner_agent"),
			Blocks:      getStringSliceArg(obj, "blocks"),
			BlockedBy:   getStringSliceArg(obj, "blocked_by"),
		}
		if strings.TrimSpace(item.Subject) != "" {
			items = append(items, item)
		}
	}
	return items
}

func taskPlanUpdateOptionsFromToolArgs(args map[string]interface{}) TaskPlanItemUpdateOptions {
	var opts TaskPlanItemUpdateOptions
	if value, ok := optionalStringArg(args, "subject"); ok {
		opts.Subject = &value
	}
	if value, ok := optionalStringArg(args, "description"); ok {
		opts.Description = &value
	}
	if value, ok := optionalStringArg(args, "active_form"); ok {
		opts.ActiveForm = &value
	}
	if value, ok := optionalStringArg(args, "owner_agent"); ok {
		opts.OwnerAgent = &value
	}
	if value, ok := optionalStringArg(args, "status"); ok {
		status := PlanItemStatus(strings.TrimSpace(value))
		opts.Status = &status
	}
	if value, ok := optionalStringArg(args, "execution_task_id"); ok {
		opts.ExecutionTaskID = &value
	}
	if value, ok := optionalStringArg(args, "result_text"); ok {
		opts.ResultText = &value
	}
	if value, ok := optionalStringArg(args, "error"); ok {
		opts.Error = &value
	}
	opts.AddBlocks = getStringSliceArg(args, "add_blocks")
	opts.AddBlockedBy = getStringSliceArg(args, "add_blocked_by")
	return opts
}

func optionalStringArg(args map[string]interface{}, key string) (string, bool) {
	if args == nil {
		return "", false
	}
	raw, ok := args[key]
	if !ok {
		return "", false
	}
	value, ok := raw.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func taskPlanToolView(plan *TaskPlan) map[string]interface{} {
	if plan == nil {
		return nil
	}
	items := make([]map[string]interface{}, 0, len(plan.Items))
	for _, item := range plan.Items {
		items = append(items, taskPlanItemToolView(item))
	}
	return map[string]interface{}{
		"plan_id":        plan.ID,
		"session_id":     plan.SessionID,
		"parent_task_id": plan.ParentTaskID,
		"goal":           plan.Goal,
		"items":          items,
		"created_at":     plan.CreatedAt,
		"updated_at":     plan.UpdatedAt,
	}
}

func taskPlanItemToolView(item TaskPlanItem) map[string]interface{} {
	return map[string]interface{}{
		"item_id":           item.ID,
		"subject":           item.Subject,
		"description":       item.Description,
		"active_form":       item.ActiveForm,
		"owner_agent":       item.OwnerAgent,
		"status":            item.Status,
		"blocks":            append([]string(nil), item.Blocks...),
		"blocked_by":        append([]string(nil), item.BlockedBy...),
		"execution_task_id": item.ExecutionTaskID,
		"result_text":       item.ResultText,
		"error":             item.Error,
		"created_at":        item.CreatedAt,
		"updated_at":        item.UpdatedAt,
	}
}
