package agent

import (
	"context"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type runtimeAsyncToolCollector struct {
	results chan ToolExecutionResult
	wg      sync.WaitGroup
}

func newRuntimeAsyncToolCollector() *runtimeAsyncToolCollector {
	return &runtimeAsyncToolCollector{
		results: make(chan ToolExecutionResult, 50),
	}
}

func (c *runtimeAsyncToolCollector) collect() []ToolExecutionResult {
	go func() {
		c.wg.Wait()
		close(c.results)
	}()

	var toolResults []ToolExecutionResult
	for tr := range c.results {
		toolResults = append(toolResults, tr)
	}
	return toolResults
}

func (r *Runtime) buildStreamingTurnCallbacks(ctx context.Context, taskCompleteResult *string, taskCompleteTriggered *bool, collector *runtimeAsyncToolCollector) StreamTurnCallbacks {
	toolCallDetected := false

	return StreamTurnCallbacks{
		OnToolCall: func(tc domain.ToolCall) error {
			if tc.Function.Name == "task_complete" {
				r.emitToolCall(tc.Function.Name, tc.Function.Arguments, "")
				if res, ok := tc.Function.Arguments["result"].(string); ok && res != "" {
					*taskCompleteResult = res
				}
				*taskCompleteTriggered = true
				return errTaskComplete
			}

			collector.wg.Add(1)
			go r.executeAsyncTool(ctx, tc, &collector.wg, collector.results)
			return nil
		},
		OnReasoning: func(text string) {
			r.emit(EventTypeThinking, text)
		},
		OnPartial: func(text string) {
			r.emit(EventTypePartial, text)
		},
		OnFirstToolCall: func() {
			if !toolCallDetected {
				r.emit(EventTypeThinking, "Planning tool usage...")
				toolCallDetected = true
			}
		},
	}
}

func (r *Runtime) buildStreamingToolExecutionCallbacks() ToolExecutionCallbacks {
	return ToolExecutionCallbacks{
		OnToolCall: func(name string, args map[string]interface{}, interruptBehavior string) {
			r.emitToolCall(name, args, interruptBehavior)
		},
		OnToolResult: func(name string, res interface{}, err error, interruptBehavior string) {
			r.emitToolResult(name, res, err, interruptBehavior)
		},
		OnToolState: func(name string, state string, interruptBehavior string) {
			r.emitToolState(name, state, interruptBehavior)
		},
		EventSink: r.forwardSubAgentEvent,
		Debug:     r.debugEnabled(),
	}
}
