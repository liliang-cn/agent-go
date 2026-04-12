package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestStreamingTurnCallbacksSkipsEmptyArgumentToolDelta(t *testing.T) {
	runtime := &Runtime{}
	var terminalName, terminalResult string
	collector := newRuntimeAsyncToolCollector()

	callbacks := runtime.buildStreamingTurnCallbacks(context.Background(), &terminalName, &terminalResult, collector)
	if err := callbacks.OnToolCall(domain.ToolCall{
		Function: domain.FunctionCall{Name: "mcp_filesystem_write_file", Arguments: map[string]interface{}{}},
	}); err != nil {
		t.Fatalf("OnToolCall() error = %v", err)
	}

	if terminalName != "" || terminalResult != "" {
		t.Fatalf("empty non-terminal delta should not terminalize, got name=%q result=%q", terminalName, terminalResult)
	}
	if len(collector.results) != 0 {
		t.Fatalf("empty non-terminal delta should not execute a tool")
	}
}
