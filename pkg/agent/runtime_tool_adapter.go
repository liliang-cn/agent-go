package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

type runtimeAsyncToolCollector struct {
	results chan ToolExecutionResult
	wg      sync.WaitGroup

	// collapsed holds the answers for calls that were not executed because an
	// identical read-only call in the same turn already was. They are kept
	// beside the channel rather than pushed through it because nothing waits
	// on them: the channel is closed when the running tools finish, and a send
	// into a full buffer from the stream callback would block the stream.
	mu        sync.Mutex
	collapsed []ToolExecutionResult
}

func (c *runtimeAsyncToolCollector) addCollapsed(res ToolExecutionResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.collapsed = append(c.collapsed, res)
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
	c.mu.Lock()
	defer c.mu.Unlock()
	// Every collapsed call still gets a result under its own id. The provider
	// requires one tool message per tool call id, and a caller that skipped
	// them would send a malformed turn.
	return append(toolResults, c.collapsed...)
}

func (r *Runtime) buildStreamingTurnCallbacks(ctx context.Context, spanID string, taskTerminalName *string, taskTerminalResult *string, collector *runtimeAsyncToolCollector) StreamTurnCallbacks {
	toolCallDetected := false
	// The provider re-emits the FULL accumulated tool-call snapshot on every
	// streamed chunk (so overwrite-style consumers converge on the final
	// state). Execution must therefore dedupe per call: once a call's args
	// complete and it is dispatched, later snapshots re-deliver the same call
	// — with parallel tool calls, once per remaining fragment of the other
	// calls. Without this guard the same call runs dozens of times, which is
	// wasteful for read-only tools and dangerous for non-idempotent ones.
	dispatched := map[string]bool{}
	// And a second guard, on a different question. `dispatched` answers "have I
	// already started THIS call", which is about the provider re-sending one
	// call; this answers "have I already run a call with these exact arguments",
	// which is about the model emitting the same call several times in one
	// batch. They are different keys because a model emits duplicates under
	// DIFFERENT call ids, so an id-keyed map cannot see them.
	//
	// Measured on a graph-backed agent: one turn emitted ten tool calls inside
	// ten milliseconds, four of them identical, and across the run twenty-five
	// calls were thirteen distinct questions. handleDuplicateToolCalls has
	// collapsed exactly this since it was written, and on the streaming path it
	// runs after execution -- so the policy existed and the work was already
	// done by the time it applied.
	//
	// The policy is the same one, deliberately: only tools declared ReadOnly,
	// because a tool nobody described as safe to skip is not, and a repeated
	// write may be a read-modify-write loop rather than a mistake.
	seen := map[string]int{}

	return StreamTurnCallbacks{
		OnToolCall: func(tc domain.ToolCall) error {
			tc = normalizeStreamingToolCall(tc)
			if isTaskTerminalToolName(tc.Function.Name) {
				// Tool-call arguments stream in fragments: early chunks carry the
				// name but incomplete/unparseable JSON args, so the result is empty.
				// Aborting now (errTaskTerminal) would drop the answer. Wait until
				// the args have accumulated into a non-empty result before
				// terminating; if they never do, the stream ends naturally and the
				// post-turn terminal handler recovers from the full result.
				res := taskTerminalToolResult(tc.Function.Name, tc.Function.Arguments, "")
				if res == "" {
					// args not fully accumulated yet — keep streaming; the
					// post-turn handler recovers if they never complete.
					return nil
				}
				r.emitToolCall(tc.Function.Name, tc.Function.Arguments, "")
				*taskTerminalName = tc.Function.Name
				*taskTerminalResult = res
				return errTaskTerminal
			}
			if len(tc.Function.Arguments) == 0 {
				return nil
			}
			key := tc.ID
			if key == "" {
				key = tc.Function.Name + "|" + fmt.Sprint(tc.Function.Arguments)
			}
			if dispatched[key] {
				return nil
			}
			dispatched[key] = true

			sig := toolCallSignature(tc)
			seen[sig]++
			if seen[sig] > 1 && r.svc.toolCallIsReadOnly(tc.Function.Name) {
				collector.addCollapsed(ToolExecutionResult{
					ToolCallID: tc.ID,
					ToolName:   tc.Function.Name,
					ToolType:   "tool",
					Result:     duplicateReadOnlyHint(tc.Function.Name, seen[sig]),
				})
				return nil
			}

			collector.wg.Add(1)
			go r.executeAsyncTool(ctx, tc, &collector.wg, collector.results)
			return nil
		},
		OnReasoning: func(text string) {
			r.svc.emitObserver(func(o Observer) {
				o.OnModelDelta(ctx, ModelDelta{SpanID: spanID, Kind: "reasoning", Text: text})
			})
			r.emit(EventTypeThinking, text)
		},
		OnPartial: func(text string) {
			r.svc.emitObserver(func(o Observer) {
				o.OnModelDelta(ctx, ModelDelta{SpanID: spanID, Kind: "partial", Text: text})
			})
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

func normalizeStreamingToolCall(tc domain.ToolCall) domain.ToolCall {
	tc.ID = domain.NormalizeToolCallID(tc.ID)
	return tc
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
