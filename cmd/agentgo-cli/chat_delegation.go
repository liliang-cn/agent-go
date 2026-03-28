package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/cmd/agentgo-cli/internal/cliui"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
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
		submitted, err := manager.SubmitAgentTask(ctx, sessionID, task.AgentName, instruction)
		if err != nil {
			return fmt.Errorf("failed to submit task for @%s: %w", task.AgentName, err)
		}

		if render {
			terminalTask, waitErr := waitForAsyncTask(ctx, manager, submitted.ID, true)
			if waitErr != nil {
				return fmt.Errorf("task failed for @%s: %w", task.AgentName, waitErr)
			}
			if delegatedResultLooksFailed(terminalTask.ResultText) {
				return fmt.Errorf("task failed for @%s: %s", task.AgentName, strings.TrimSpace(terminalTask.ResultText))
			}
			previousResult = strings.TrimSpace(terminalTask.ResultText)
			continue
		}

		if follower != nil {
			follower.StartTask(ctx, submitted.ID)
		}

		terminalTask, waitErr := waitForAsyncTask(ctx, manager, submitted.ID, false)
		if waitErr != nil {
			return fmt.Errorf("background task failed for @%s: %w", task.AgentName, waitErr)
		}
		if delegatedResultLooksFailed(terminalTask.ResultText) {
			return fmt.Errorf("background task failed for @%s: %s", task.AgentName, strings.TrimSpace(terminalTask.ResultText))
		}
		previousResult = strings.TrimSpace(terminalTask.ResultText)
	}

	if render {
		fmt.Println()
	}
	return nil
}

func waitForAsyncTask(ctx context.Context, manager *agent.TeamManager, taskID string, render bool) (*agent.AsyncTask, error) {
	events, unsubscribe, err := manager.SubscribeTask(taskID)
	if err != nil {
		return nil, err
	}
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case evt, ok := <-events:
			if !ok {
				task, taskErr := manager.GetTask(taskID)
				if taskErr != nil {
					return nil, taskErr
				}
				if task.Status == agent.AsyncTaskStatusFailed {
					return task, fmt.Errorf("%s", strings.TrimSpace(task.Error))
				}
				return task, nil
			}
			if render {
				renderChatTaskEvent(evt)
			}
			switch evt.Type {
			case agent.TaskEventTypeCompleted, agent.TaskEventTypeCancelled:
				task, taskErr := manager.GetTask(taskID)
				if taskErr != nil {
					return nil, taskErr
				}
				return task, nil
			case agent.TaskEventTypeFailed:
				task, taskErr := manager.GetTask(taskID)
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
