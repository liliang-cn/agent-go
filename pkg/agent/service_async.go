package agent

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// executeDelegateAsync spawns a sub-agent asynchronously and returns immediately.
func (s *Service) executeDelegateAsync(ctx context.Context, currentAgent *Agent, args map[string]interface{}) (interface{}, error) {
	goal, _ := args["goal"].(string)
	name, _ := args["name"].(string)

	if goal == "" || name == "" {
		return nil, fmt.Errorf("delegate_async: 'goal' and 'name' arguments are required")
	}

	// For async, we don't block. We start a goroutine to run the subagent.
	// Context must be detached from the tool execution context so it doesn't get cancelled when the tool returns.
	bgCtx := context.Background()

	subAgent := s.CreateSubAgent(currentAgent, goal,
		WithSubAgentMode(SubAgentModeBackground),
		WithSubAgentMaxTurns(10), // Limit parallel agent turns
	)

	// Override the agent's name with the provided name
	subAgent.config.Agent = NewAgentWithConfig(name, currentAgent.Instructions(), currentAgent.tools)

	// Add to coordinator
	s.asyncTasks.Add(subAgent)

	session := getCurrentSession(ctx)
	if session == nil {
		s.logger.Warn("executeDelegateAsync: session not found in context, async notification may fail")
	}

	// Start goroutine to wait for it
	resultChan := s.asyncTasks.RunAsync(bgCtx, subAgent)

	go func() {
		res := <-resultChan

		// When finished, inject a task-notification user message into the parent session
		if session != nil {
			var output string
			if res.Error != nil {
				output = fmt.Sprintf("Error: %v", res.Error)
			} else if res.Result != nil {
				output = fmt.Sprintf("%v", res.Result)
			} else {
				output = "(no output)"
			}

			notification := fmt.Sprintf(`<task-notification>
<task-id>%s</task-id>
<status>%s</status>
<summary>Async SubAgent "%s" finished</summary>
<result>%s</result>
</task-notification>`, res.ID, string(res.State), name, output)

			session.AddMessage(domain.Message{
				Role:    "user",
				Content: notification,
			})
			s.store.SaveSession(session)
		}
	}()

	s.emitProgress("tool_call", fmt.Sprintf("→ Spawned Async SubAgent: %s", name), 0, "delegate_async")

	return map[string]interface{}{
		"status":  "spawned",
		"task_id": subAgent.ID(),
		"message": fmt.Sprintf("Sub-agent '%s' spawned successfully and is running in the background. Do not wait for it. You will receive a <task-notification> message when it finishes. You can continue with other work.", name),
	}, nil
}

// executeSendMessage handles sending a message to a running or paused async task
func (s *Service) executeSendMessage(ctx context.Context, currentAgent *Agent, args map[string]interface{}) (interface{}, error) {
	taskID, _ := args["to"].(string)
	message, _ := args["message"].(string)

	if taskID == "" || message == "" {
		return nil, fmt.Errorf("send_message: 'to' and 'message' arguments are required")
	}

	// Find the subagent
	s.asyncTasks.mu.RLock()
	sa, ok := s.asyncTasks.subagents[taskID]
	s.asyncTasks.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("send_message: no subagent found with ID '%s'", taskID)
	}

	state := sa.GetState()
	if state != SubAgentStatePaused && state != SubAgentStateCompleted && state != SubAgentStateFailed && state != SubAgentStateCancelled {
		return nil, fmt.Errorf("send_message: subagent '%s' is currently in state '%s', cannot send message right now", taskID, state)
	}

	s.emitProgress("tool_call", fmt.Sprintf("→ Sending message to SubAgent %s", taskID[:8]), 0, "send_message")

	session := getCurrentSession(ctx)

	go func() {
		bgCtx := context.Background()
		// Since Resume is synchronous, we run it in goroutine
		res, err := sa.Resume(bgCtx, message)

		newState := sa.GetState()
		if session != nil {
			var output string
			if err != nil {
				output = fmt.Sprintf("Error: %v", err)
			} else if res != nil {
				output = fmt.Sprintf("%v", res)
			} else {
				output = "(no output)"
			}

			notification := fmt.Sprintf(`<task-notification>
<task-id>%s</task-id>
<status>%s</status>
<summary>Async SubAgent response to your message</summary>
<result>%s</result>
</task-notification>`, sa.ID(), string(newState), output)

			session.AddMessage(domain.Message{
				Role:    "user",
				Content: notification,
			})
			s.store.SaveSession(session)
		}
	}()

	return map[string]interface{}{
		"status":  "message_sent",
		"task_id": sa.ID(),
		"message": "Message sent. The sub-agent has resumed working in the background. You will receive another <task-notification> when it finishes this new task.",
	}, nil
}
