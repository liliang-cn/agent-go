package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v2/cmd/agentgo-cli/internal/cliui"
	"github.com/liliang-cn/agent-go/v2/cmd/agentgo-cli/internal/lineinput"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

type chatTaskFollower struct {
	manager *agent.TeamManager
	mu      sync.Mutex
	seen    map[string]struct{}
}

func newChatTaskFollower(manager *agent.TeamManager) *chatTaskFollower {
	if manager == nil {
		return nil
	}
	return &chatTaskFollower{
		manager: manager,
		seen:    make(map[string]struct{}),
	}
}

func (f *chatTaskFollower) StartSessionTasks(ctx context.Context, sessionID string) {
	f.StartSessionTasksSince(ctx, sessionID, time.Time{})
}

func (f *chatTaskFollower) StartSessionTasksSince(ctx context.Context, sessionID string, since time.Time) {
	if f == nil || f.manager == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}

	tasks, err := f.manager.Tasks().List(ctx, agent.TaskListOptions{Limit: 20})
	if err != nil {
		printChatTaskBlock(fmt.Sprintf("%s Task list failed: %v", cliui.Error, err))
		return
	}
	for _, task := range tasks {
		if task == nil || task.SessionID != sessionID {
			continue
		}
		if !since.IsZero() && task.CreatedAt.Before(since) {
			continue
		}
		f.mu.Lock()
		_, exists := f.seen[task.ID]
		if !exists {
			f.seen[task.ID] = struct{}{}
		}
		f.mu.Unlock()
		if exists {
			continue
		}

		if isTerminalTask(task.Status) {
			printChatTaskSnapshot(task)
			continue
		}
		go f.followTask(ctx, task.ID)
	}
}

func (f *chatTaskFollower) StartTask(ctx context.Context, taskID string) {
	if f == nil || f.manager == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}

	f.mu.Lock()
	_, exists := f.seen[taskID]
	if !exists {
		f.seen[taskID] = struct{}{}
	}
	f.mu.Unlock()
	if exists {
		return
	}

	go f.followTask(ctx, taskID)
}

func (f *chatTaskFollower) StartTaskIDs(ctx context.Context, taskIDs []string) {
	if f == nil {
		return
	}
	for _, taskID := range taskIDs {
		f.StartTask(ctx, taskID)
	}
}

func waitForChatSessionTasks(ctx context.Context, manager *agent.TeamManager, sessionID string, since time.Time) {
	if manager == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	tasks, err := manager.Tasks().List(ctx, agent.TaskListOptions{Limit: 50})
	if err != nil {
		printChatTaskBlock(fmt.Sprintf("%s Task list failed: %v", cliui.Error, err))
		return
	}
	for _, task := range tasks {
		if task == nil || task.SessionID != sessionID {
			continue
		}
		if !since.IsZero() && task.CreatedAt.Before(since) {
			continue
		}
		if isTerminalTask(task.Status) {
			printChatTaskSnapshot(task)
			continue
		}
		_, err := waitForCanonicalTask(ctx, manager, task.ID, true)
		if err != nil {
			printChatTaskBlock(fmt.Sprintf("%s Task wait failed for %s: %v", cliui.Error, shortTaskID(task.ID), err))
		}
	}
}

func (f *chatTaskFollower) followTask(ctx context.Context, taskID string) {
	events, unsubscribe, err := f.manager.SubscribeTask(taskID)
	if err != nil {
		printChatTaskBlock(fmt.Sprintf("%s Task follow failed for %s: %v", cliui.Error, taskID, err))
		return
	}
	defer unsubscribe()

	renderer := &chatTaskStreamRenderer{}
	defer renderer.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			renderer.Handle(evt)
			if evt.Type == agent.TaskEventTypeCompleted || evt.Type == agent.TaskEventTypeBlocked || evt.Type == agent.TaskEventTypeFailed || evt.Type == agent.TaskEventTypeCancelled {
				f.StartSessionTasks(ctx, evt.SessionID)
			}
		}
	}
}

// chatTaskStreamRenderer renders a TaskEvent stream so that an agent's partial
// (token) output is shown live, while preserving the existing block layout for
// every other event type. It is stateful per task-watch loop: callers create
// one renderer per Subscribe call and Flush it when the channel closes.
type chatTaskStreamRenderer struct {
	inPartial    bool
	activeAgent  string
	everStreamed bool
	// writeStream defaults to lineinput.WriteAsyncStream and is overridable in tests.
	writeStream func(string)
}

func (r *chatTaskStreamRenderer) write(chunk string) {
	if r.writeStream != nil {
		r.writeStream(chunk)
		return
	}
	lineinput.WriteAsyncStream(chunk)
}

func (r *chatTaskStreamRenderer) Handle(evt *agent.TaskEvent) {
	if evt == nil {
		return
	}

	if evt.Type == agent.TaskEventTypeRuntime && evt.Runtime != nil && evt.Runtime.Type == agent.EventTypePartial {
		chunk := evt.Runtime.Content
		if chunk == "" {
			return
		}
		agentName := strings.TrimSpace(evt.Runtime.AgentName)
		if agentName == "" {
			agentName = strings.TrimSpace(evt.AgentName)
		}
		if !r.inPartial || agentName != r.activeAgent {
			if r.inPartial {
				r.write("\n")
			}
			label := shortTaskID(evt.TaskID)
			prefix := fmt.Sprintf("%s [%s] @%s ▸ ", cliui.AgentReply, label, agentName)
			r.write(prefix)
			r.inPartial = true
			r.everStreamed = true
			r.activeAgent = agentName
		}
		r.write(chunk)
		return
	}

	// Any non-partial event ends an in-progress stream line.
	if r.inPartial {
		r.write("\n")
		r.inPartial = false
		r.activeAgent = ""
	}

	// If we already streamed the body, suppress the duplicate Completed body
	// block — keep just the short "completed" marker line.
	if evt.Type == agent.TaskEventTypeCompleted && r.everStreamed {
		if renderFollowUpSupplement(evt.AgentName, evt.Message) {
			return
		}
		line := fmt.Sprintf("%s [%s] Task completed", cliui.Success, shortTaskID(evt.TaskID))
		if evt.AgentName != "" {
			line += fmt.Sprintf(" by @%s", evt.AgentName)
		}
		printChatTaskBlock(line)
		return
	}

	renderChatTaskEvent(evt)
}

func (r *chatTaskStreamRenderer) Flush() {
	if r.inPartial {
		r.write("\n")
		r.inPartial = false
		r.activeAgent = ""
	}
}

func renderChatTaskEvent(evt *agent.TaskEvent) {
	if evt == nil {
		return
	}

	taskLabel := shortTaskID(evt.TaskID)
	switch evt.Type {
	case agent.TaskEventTypeCreated:
		if isFollowUpAgentEvent(evt.AgentName) && !shouldRenderChatTaskRuntimeEvent() {
			return
		}
		printChatTaskLine("%s [%s] %s", cliui.TaskCreated, taskLabel, firstNonEmpty(evt.Message, "Task created."))
	case agent.TaskEventTypeStarted:
		if isFollowUpAgentEvent(evt.AgentName) && !shouldRenderChatTaskRuntimeEvent() {
			return
		}
		printChatTaskLine("%s [%s] %s", cliui.TaskStarted, taskLabel, firstNonEmpty(evt.Message, "Task started."))
	case agent.TaskEventTypeRuntime:
		if shouldRenderChatTaskRuntimeEvent() {
			renderRuntimeTaskEvent(taskLabel, evt.Runtime)
		}
	case agent.TaskEventTypeCompleted:
		if renderFollowUpSupplement(evt.AgentName, evt.Message) {
			return
		}
		line := fmt.Sprintf("%s [%s] Task completed", cliui.Success, taskLabel)
		if evt.AgentName != "" {
			line += fmt.Sprintf(" by @%s", evt.AgentName)
		}
		if text := strings.TrimSpace(evt.Message); text != "" {
			printChatTaskBlock(line, text)
		} else {
			printChatTaskBlock(line)
		}
	case agent.TaskEventTypeBlocked:
		line := fmt.Sprintf("%s [%s] Task blocked", cliui.Error, taskLabel)
		if evt.AgentName != "" {
			line += fmt.Sprintf(" in @%s", evt.AgentName)
		}
		if text := strings.TrimSpace(evt.Message); text != "" {
			line += fmt.Sprintf(": %s", text)
		}
		printChatTaskBlock(line)
	case agent.TaskEventTypeFailed:
		line := fmt.Sprintf("%s [%s] Task failed", cliui.Error, taskLabel)
		if evt.AgentName != "" {
			line += fmt.Sprintf(" in @%s", evt.AgentName)
		}
		if text := strings.TrimSpace(evt.Message); text != "" {
			line += fmt.Sprintf(": %s", text)
		}
		printChatTaskBlock(line)
	}
}

func renderRuntimeTaskEvent(taskLabel string, evt *agent.Event) {
	if evt == nil {
		return
	}

	switch evt.Type {
	case agent.EventTypeStart, agent.EventTypeStateUpdate:
		if msg := summarizeChatTaskStatus(strings.TrimSpace(evt.Content)); msg != "" {
			printChatTaskLine("… [%s] @%s %s", taskLabel, evt.AgentName, msg)
		}
	case agent.EventTypeToolCall:
		printChatTaskLine("%s [%s] @%s %s", cliui.Tool, taskLabel, evt.AgentName, formatChatTaskToolCall(evt.ToolName))
	case agent.EventTypeToolResult:
		if strings.TrimSpace(evt.Content) != "" {
			printChatTaskLine("%s [%s] @%s %s: %s", cliui.Error, taskLabel, evt.AgentName, evt.ToolName, strings.TrimSpace(evt.Content))
		} else {
			printChatTaskLine("%s [%s] @%s %s done", cliui.ToolDone, taskLabel, evt.AgentName, evt.ToolName)
		}
	}
}

func shouldRenderChatTaskRuntimeEvent() bool {
	return debug || verbose
}

func printChatTaskSnapshot(task *taskpkg.Task) {
	if task == nil {
		return
	}

	taskLabel := shortTaskID(task.ID)
	switch task.Status {
	case taskpkg.StatusCompleted:
		if renderFollowUpSupplement(task.AgentName, task.Output) {
			return
		}
		line := fmt.Sprintf("%s [%s] Task completed", cliui.Success, taskLabel)
		if text := strings.TrimSpace(task.Output); text != "" {
			printChatTaskBlock(line, text)
		} else {
			printChatTaskBlock(line)
		}
	case taskpkg.StatusFailed:
		line := fmt.Sprintf("%s [%s] Task failed", cliui.Error, taskLabel)
		if text := strings.TrimSpace(task.Error); text != "" {
			line += fmt.Sprintf(": %s", text)
		}
		printChatTaskBlock(line)
	case taskpkg.StatusBlocked:
		line := fmt.Sprintf("%s [%s] Task blocked", cliui.Error, taskLabel)
		if text := strings.TrimSpace(firstNonEmpty(task.Error, task.Output)); text != "" {
			line += fmt.Sprintf(": %s", text)
		}
		printChatTaskBlock(line)
	}
}

func shortTaskID(taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if len(taskID) <= 8 {
		return taskID
	}
	return taskID[:8]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}

func executionResultAsyncTaskIDs(result *agent.ExecutionResult) []string {
	if result == nil || len(result.Metadata) == 0 {
		return nil
	}
	raw, ok := result.Metadata["async_task_ids"]
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func isTerminalTask(status taskpkg.Status) bool {
	switch status {
	case taskpkg.StatusCompleted, taskpkg.StatusBlocked, taskpkg.StatusFailed, taskpkg.StatusCancelled:
		return true
	default:
		return false
	}
}

func formatChatTaskToolCall(name string) string {
	if strings.TrimSpace(name) == "" {
		return "starting tool"
	}
	return fmt.Sprintf("using %s", name)
}

func renderFollowUpSupplement(agentName, text string) bool {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" || text == "NO_MEMORY_ACTION_NEEDED" {
		return true
	}
	if !strings.HasPrefix(text, "Supplement:") {
		if !strings.EqualFold(agentName, agent.ArchivistAgentName) {
			return false
		}
	}
	printChatTaskBlock(fmt.Sprintf("@%s: %s", agentName, text))
	return true
}

func isFollowUpAgentEvent(agentName string) bool {
	agentName = strings.TrimSpace(agentName)
	switch {
	case strings.EqualFold(agentName, agent.ArchivistAgentName):
		return true
	case strings.EqualFold(agentName, agent.VerifierAgentName):
		return true
	default:
		return false
	}
}

func printChatTaskLine(format string, args ...interface{}) {
	printChatTaskBlock(fmt.Sprintf(format, args...))
}

func printChatTaskBlock(lines ...string) {
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	if text == "" {
		return
	}
	if isInteractive() {
		lineinput.WriteAsyncLine(text)
		return
	}
	fmt.Println(text)
}

func summarizeChatTaskStatus(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	if strings.HasPrefix(msg, "Starting task:") {
		return "Starting task"
	}
	if strings.HasPrefix(msg, "Starting sub-agent goal:") {
		return "Starting delegated step"
	}
	if strings.Contains(msg, "Executing specific tool:") {
		return ""
	}
	if strings.EqualFold(msg, "Delegated step completed") {
		return ""
	}
	if idx := strings.Index(msg, "\n"); idx >= 0 {
		msg = msg[:idx]
	}
	if len(msg) > 96 {
		return msg[:96] + "..."
	}
	return msg
}
