package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/cmd/agentgo-cli/internal/cliui"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

func runDelegatedTaskChainAsync(ctx context.Context, manager *agent.TeamManager, sessionID string, tasks []delegatedTask, follower *chatTaskFollower, background bool) error {
	if manager == nil {
		return fmt.Errorf("agent manager is not initialized")
	}
	if len(tasks) == 0 {
		return nil
	}

	if background {
		go func() {
			if err := executeDelegatedTaskChain(ctx, manager, sessionID, tasks, follower, false); err != nil {
				printChatTaskBlock(fmt.Sprintf("%s %v", cliui.Error, err))
			}
		}()
		return nil
	}

	return executeDelegatedTaskChain(ctx, manager, sessionID, tasks, nil, true)
}

func executeDelegatedTaskChain(ctx context.Context, manager *agent.TeamManager, sessionID string, tasks []delegatedTask, follower *chatTaskFollower, render bool) error {
	var previousResult string

	for idx, task := range tasks {
		instruction := buildDelegatedTaskInstruction(tasks, idx, previousResult)
		submitted, err := manager.Tasks().Submit(ctx, agent.TaskSubmitOptions{
			SessionID: sessionID,
			AgentName: task.AgentName,
			Input:     instruction,
		})
		if err != nil {
			return fmt.Errorf("failed to submit task for @%s: %w", task.AgentName, err)
		}

		if render {
			terminalTask, waitErr := waitForCanonicalTask(ctx, manager, submitted.ID, true)
			if waitErr != nil {
				return fmt.Errorf("task failed for @%s: %w", task.AgentName, waitErr)
			}
			if delegatedResultLooksFailed(terminalTask.Output) {
				return fmt.Errorf("task failed for @%s: %s", task.AgentName, strings.TrimSpace(terminalTask.Output))
			}
			previousResult = strings.TrimSpace(terminalTask.Output)
			continue
		}

		if follower != nil {
			follower.StartTask(ctx, submitted.ID)
		}

		terminalTask, waitErr := waitForCanonicalTask(ctx, manager, submitted.ID, false)
		if waitErr != nil {
			return fmt.Errorf("background task failed for @%s: %w", task.AgentName, waitErr)
		}
		if delegatedResultLooksFailed(terminalTask.Output) {
			return fmt.Errorf("background task failed for @%s: %s", task.AgentName, strings.TrimSpace(terminalTask.Output))
		}
		previousResult = strings.TrimSpace(terminalTask.Output)
	}

	if render {
		fmt.Println()
	}
	return nil
}

func waitForCanonicalTask(ctx context.Context, manager *agent.TeamManager, taskID string, render bool) (*taskpkg.Task, error) {
	events, unsubscribe, err := manager.SubscribeTask(taskID)
	if err != nil {
		return nil, err
	}
	defer unsubscribe()

	var renderer *chatTaskStreamRenderer
	if render {
		renderer = &chatTaskStreamRenderer{}
		defer renderer.Flush()
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case evt, ok := <-events:
			if !ok {
				if renderer != nil {
					renderer.Flush()
				}
				task, taskErr := manager.Tasks().Get(ctx, taskID)
				if taskErr != nil {
					return nil, taskErr
				}
				if task.Status == taskpkg.StatusFailed {
					return task, fmt.Errorf("%s", strings.TrimSpace(task.Error))
				}
				return task, nil
			}
			if renderer != nil {
				renderer.Handle(evt)
			}
			switch evt.Type {
			case agent.TaskEventTypeCompleted, agent.TaskEventTypeBlocked, agent.TaskEventTypeCancelled:
				task, taskErr := manager.Tasks().Get(ctx, taskID)
				if taskErr != nil {
					return nil, taskErr
				}
				if evt.Type == agent.TaskEventTypeBlocked {
					errText := strings.TrimSpace(task.Error)
					if errText == "" {
						errText = strings.TrimSpace(task.Output)
					}
					if errText == "" {
						errText = strings.TrimSpace(evt.Message)
					}
					if errText == "" {
						errText = "task blocked"
					}
					return task, fmt.Errorf("%s", errText)
				}
				return task, nil
			case agent.TaskEventTypeFailed:
				task, taskErr := manager.Tasks().Get(ctx, taskID)
				if taskErr != nil {
					return nil, taskErr
				}
				errText := strings.TrimSpace(task.Error)
				if errText == "" {
					errText = strings.TrimSpace(evt.Message)
				}
				if errText == "" {
					errText = "task failed"
				}
				return task, fmt.Errorf("%s", errText)
			}
		}
	}
}

func buildDelegatedTaskInstruction(tasks []delegatedTask, idx int, previousResult string) string {
	if idx <= 0 || strings.TrimSpace(previousResult) == "" {
		return tasks[idx].Instruction
	}
	return fmt.Sprintf(
		"Previous result from @%s:\n%s\n\nYour task:\n%s",
		tasks[idx-1].AgentName,
		previousResult,
		tasks[idx].Instruction,
	)
}
