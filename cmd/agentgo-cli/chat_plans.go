package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/cmd/agentgo-cli/internal/cliui"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func handleChatPlanCommand(ctx context.Context, manager *agent.TeamManager, sessionID, input string, follower *chatTaskFollower) (bool, error) {
	if !strings.HasPrefix(strings.TrimSpace(input), "/plan") && strings.TrimSpace(input) != "/plans" {
		return false, nil
	}
	if manager == nil {
		return true, fmt.Errorf("task plan commands require an agent store")
	}

	fields := strings.Fields(input)
	if len(fields) == 0 {
		return false, nil
	}
	if fields[0] == "/plans" || (fields[0] == "/plan" && (len(fields) == 1 || fields[1] == "list")) {
		printChatPlanList(ctx, manager, sessionID, 10)
		return true, nil
	}
	if fields[0] != "/plan" {
		return false, nil
	}
	if len(fields) >= 2 && fields[1] == "ready" {
		planID := ""
		if len(fields) >= 3 {
			planID = fields[2]
		}
		printChatPlanReadyItems(ctx, manager, sessionID, planID)
		return true, nil
	}
	if len(fields) >= 4 && fields[1] == "submit" {
		agentName := ""
		if len(fields) >= 5 {
			agentName = fields[4]
		}
		task, err := manager.Plans().SubmitItem(ctx, fields[2], fields[3], agent.TaskPlanSubmitItemOptions{
			SessionID: sessionID,
			AgentName: agentName,
		})
		if err != nil {
			return true, err
		}
		printChatTaskBlock(fmt.Sprintf("%s Plan item submitted: %s -> task %s (@%s)", cliui.TaskStarted, fields[3], shortTaskID(task.ID), task.AgentName))
		if follower != nil {
			follower.StartTask(ctx, task.ID)
		}
		return true, nil
	}

	printChatTaskBlock(
		fmt.Sprintf("%s Plan commands:", cliui.Tip),
		"  /plans",
		"  /plan ready [plan_id]",
		"  /plan submit <plan_id> <item_id> [agent_name]",
	)
	return true, nil
}

func printChatPlanList(ctx context.Context, manager *agent.TeamManager, sessionID string, limit int) {
	if manager == nil {
		return
	}
	plans, err := sessionTaskPlans(ctx, manager, sessionID, limit)
	if err != nil {
		printChatTaskBlock(fmt.Sprintf("%s Plan list failed: %v", cliui.Error, err))
		return
	}
	if len(plans) == 0 {
		return
	}
	for _, plan := range plans {
		printChatPlanSnapshot(plan)
	}
}

func printChatPlanReadyItems(ctx context.Context, manager *agent.TeamManager, sessionID, planID string) {
	plans, err := sessionTaskPlans(ctx, manager, sessionID, 20)
	if err != nil {
		printChatTaskBlock(fmt.Sprintf("%s Plan list failed: %v", cliui.Error, err))
		return
	}
	for _, plan := range plans {
		if strings.TrimSpace(planID) != "" && plan.ID != planID {
			continue
		}
		items, err := manager.Plans().ReadyItems(ctx, plan.ID)
		if err != nil {
			printChatTaskBlock(fmt.Sprintf("%s Plan ready failed for %s: %v", cliui.Error, shortPlanID(plan.ID), err))
			continue
		}
		lines := []string{fmt.Sprintf("%s Plan %s ready items", cliui.TaskStarted, shortPlanID(plan.ID))}
		for _, item := range items {
			lines = append(lines, formatChatPlanItem(item))
		}
		if len(items) == 0 {
			lines = append(lines, "  (none)")
		}
		printChatTaskBlock(lines...)
	}
}

func printChatPlanSnapshot(plan *agent.TaskPlan) {
	if plan == nil {
		return
	}
	counts := map[agent.PlanItemStatus]int{}
	for _, item := range plan.Items {
		counts[item.Status]++
	}
	lines := []string{
		fmt.Sprintf("%s Plan %s: %s", cliui.TaskCreated, shortPlanID(plan.ID), plan.Goal),
		fmt.Sprintf("  pending:%d in_progress:%d completed:%d blocked:%d failed:%d",
			counts[agent.PlanItemStatusPending],
			counts[agent.PlanItemStatusInProgress],
			counts[agent.PlanItemStatusCompleted],
			counts[agent.PlanItemStatusBlocked],
			counts[agent.PlanItemStatusFailed],
		),
	}
	for _, item := range plan.Items {
		lines = append(lines, formatChatPlanItem(item))
	}
	printChatTaskBlock(lines...)
}

func sessionTaskPlans(ctx context.Context, manager *agent.TeamManager, sessionID string, limit int) ([]*agent.TaskPlan, error) {
	plans, err := manager.Plans().List(ctx)
	if err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	out := make([]*agent.TaskPlan, 0, len(plans))
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		if sessionID != "" && strings.TrimSpace(plan.SessionID) != sessionID {
			continue
		}
		out = append(out, plan)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func formatChatPlanItem(item agent.TaskPlanItem) string {
	owner := ""
	if strings.TrimSpace(item.OwnerAgent) != "" {
		owner = " @" + strings.TrimSpace(item.OwnerAgent)
	}
	taskID := ""
	if strings.TrimSpace(item.ExecutionTaskID) != "" {
		taskID = " task:" + shortTaskID(item.ExecutionTaskID)
	}
	blockedBy := ""
	if len(item.BlockedBy) > 0 {
		blockedBy = " blocked_by:" + strings.Join(item.BlockedBy, ",")
	}
	return fmt.Sprintf("  - [%s] %s %s%s%s%s", item.Status, item.ID, item.Subject, owner, taskID, blockedBy)
}

func shortPlanID(planID string) string {
	planID = strings.TrimSpace(planID)
	if len(planID) <= 8 {
		return planID
	}
	return planID[:8]
}
