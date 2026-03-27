package squad

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func runSquadLiveDispatch(ctx context.Context, manager *agent.SquadManager, conversationKey, agentName, instruction string, debug bool) (string, error) {

	var (
		events <-chan *agent.Event
		err    error
	)
	if strings.TrimSpace(conversationKey) != "" {
		if debug {
			events, err = manager.ChatWithMemberStreamWithOptions(ctx, conversationKey, agentName, instruction, agent.WithDebug(true))
		} else {
			events, err = manager.ChatWithMemberStream(ctx, conversationKey, agentName, instruction)
		}
	} else {
		if debug {
			events, err = manager.DispatchTaskStreamWithOptions(ctx, agentName, instruction, agent.WithDebug(true))
		} else {
			events, err = manager.DispatchTaskStream(ctx, agentName, instruction)
		}
	}
	if err != nil {
		return "", err
	}

	if debug {
		return renderSquadDebugEvents(events)
	}
	return renderSquadLiveEvents(events)
}

func renderSquadLiveEvents(events <-chan *agent.Event) (string, error) {
	var (
		partial            strings.Builder
		final              string
		printedResponseHdr bool
		lastStatus         string
		pending            pendingLiveLine
	)

	for evt := range events {
		switch evt.Type {
		case agent.EventTypeStart, agent.EventTypeStateUpdate:
			if msg := summarizeLiveStatus(strings.TrimSpace(evt.Content)); msg != "" && msg != lastStatus {
				emitLiveLine(&pending, fmt.Sprintf("… @%s %s", evt.AgentName, msg))
				lastStatus = msg
			}
		case agent.EventTypeThinking:
			continue
		case agent.EventTypeToolCall:
			emitLiveLine(&pending, fmt.Sprintf("🛠 @%s %s", evt.AgentName, formatLiveToolCall(evt.ToolName)))
		case agent.EventTypeToolResult:
			if strings.TrimSpace(evt.Content) != "" {
				emitLiveLine(&pending, fmt.Sprintf("❌ @%s %s: %s", evt.AgentName, evt.ToolName, strings.TrimSpace(evt.Content)))
			} else {
				emitLiveLine(&pending, fmt.Sprintf("📦 @%s %s done", evt.AgentName, evt.ToolName))
			}
		case agent.EventTypePartial:
			flushLiveLine(&pending)
			if !printedResponseHdr {
				fmt.Printf("\n💬 @%s:\n", evt.AgentName)
				printedResponseHdr = true
			}
			fmt.Print(evt.Content)
			partial.WriteString(evt.Content)
		case agent.EventTypeComplete:
			final = strings.TrimSpace(evt.Content)
			flushLiveLine(&pending)
			if printedResponseHdr {
				if !strings.HasSuffix(partial.String(), "\n") {
					fmt.Println()
				}
			} else if final != "" {
				fmt.Printf("\n💬 @%s:\n%s\n", evt.AgentName, final)
				printedResponseHdr = true
			}
		case agent.EventTypeError:
			flushLiveLine(&pending)
			msg := strings.TrimSpace(evt.Content)
			if msg == "" {
				msg = "agent execution failed"
			}
			return "", errors.New(msg)
		}
	}
	flushLiveLine(&pending)

	if final != "" {
		return final, nil
	}
	return strings.TrimSpace(partial.String()), nil
}

type pendingLiveLine struct {
	line  string
	count int
}

func emitLiveLine(pending *pendingLiveLine, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if pending.line == line {
		pending.count++
		return
	}
	flushLiveLine(pending)
	pending.line = line
	pending.count = 1
}

func flushLiveLine(pending *pendingLiveLine) {
	if pending == nil || pending.line == "" {
		return
	}
	if pending.count > 1 {
		fmt.Printf("%s x%d\n", pending.line, pending.count)
	} else {
		fmt.Println(pending.line)
	}
	pending.line = ""
	pending.count = 0
}

func formatLiveToolCall(name string) string {
	if strings.TrimSpace(name) == "" {
		return "starting tool"
	}
	return fmt.Sprintf("using %s", name)
}

func summarizeLiveStatus(msg string) string {
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
