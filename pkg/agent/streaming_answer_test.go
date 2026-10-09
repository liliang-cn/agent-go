package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

const streamedAnswer = "## 巡检\n\norange2 的数据盘已满（\"scsi1\" 100%），建议扩容。😀"

// chunkedAnswerLLM answers the way a streaming provider delivers a tool call:
// snapshots of task_complete whose arguments text grows a few bytes at a time
// and only parses on the last one.
type chunkedAnswerLLM struct{ terminalLLM }

func (chunkedAnswerLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	raw, _ := json.Marshal(map[string]string{"result": streamedAnswer})
	for end := 5; ; end += 7 {
		if end > len(raw) {
			end = len(raw)
		}
		var args map[string]interface{}
		_ = json.Unmarshal(raw[:end], &args)
		err := cb(&domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID: "call-1", Type: "function",
			Function: domain.FunctionCall{Name: "task_complete", Arguments: args, RawArguments: string(raw[:end])},
		}}})
		if err != nil || end == len(raw) {
			return err
		}
	}
}

// The answer a model writes into task_complete reaches the stream as it is
// written, in pieces that join up to exactly the answer, instead of arriving
// whole when the model is done.
func TestTaskCompleteAnswerStreamsAsItIsWritten(t *testing.T) {
	svc, err := New("streamed-answer").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(chunkedAnswerLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Report the disks.")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var parts []string
	final := ""
	for ev := range events {
		switch ev.Type {
		case EventTypePartial:
			parts = append(parts, ev.Content)
		case EventTypeComplete:
			final = ev.Content
		}
	}
	if len(parts) < 3 {
		t.Fatalf("the answer came in %d pieces, want it streamed: %q", len(parts), parts)
	}
	if got := strings.Join(parts, ""); got != streamedAnswer {
		t.Fatalf("streamed pieces join to %q, want %q", got, streamedAnswer)
	}
	if final != streamedAnswer {
		t.Fatalf("final answer = %q, want %q", final, streamedAnswer)
	}
}
