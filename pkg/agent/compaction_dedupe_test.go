package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Compaction and the duplicate-call record are each defensible and together
// they deadlock a run:
//
//	round N    read a file           → its content is in the history
//	compaction summarises it away    → the content leaves the context
//	round N+1  the model cannot see it, so it reads again
//	dedupe     "already called, its result is above" — it is not, compaction
//	           deleted it
//	round N+2  reads again … until the round budget is gone
//
// The record has to be cleared when compaction runs, for the same reason a
// write clears it: what the model can see has changed.
func TestCompactionClearsTheDuplicateRecord(t *testing.T) {
	svc, err := New("compaction-dedupe").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.AddToolWithMetadata("read_it", "Reads.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"}}},
		func(context.Context, map[string]interface{}) (interface{}, error) { return "content", nil },
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true})

	read := &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
		ID: "r1", Type: "function",
		Function: domain.FunctionCall{Name: "read_it", Arguments: map[string]interface{}{"path": "Makefile"}},
	}}}

	state := newQueryLoopState("goal", nil, 20)
	var msgs []domain.Message

	if kept, _, _ := svc.handleDuplicateToolCalls(msgs, cloneResult(read), state.PrevToolCalls); len(kept) != 1 {
		t.Fatal("the first read must execute")
	}
	// Still in context: a straight repeat is correctly refused.
	if kept, _, _ := svc.handleDuplicateToolCalls(msgs, cloneResult(read), state.PrevToolCalls); len(kept) != 0 {
		t.Fatal("a repeat with nothing changed should collapse")
	}

	// Compaction runs. The runtime clears the record; this test asserts the
	// consequence rather than reaching into the loop.
	clear(state.PrevToolCalls)

	kept, dups, _ := svc.handleDuplicateToolCalls(msgs, cloneResult(read), state.PrevToolCalls)
	if len(kept) != 1 {
		var hint string
		if len(dups) > 0 {
			hint, _ = dups[0].Result.(string)
		}
		t.Fatalf("after compaction the read must execute again; instead it was refused with %q — "+
			"pointing the model at content compaction had just deleted", hint)
	}
}

// The same thing end to end, through the loop: with the threshold set low
// enough that every round compacts, a model that keeps asking for the same
// read must keep getting it. Before the fix the first repeat was collapsed
// and every one after it, so the tool ran exactly once however long the run
// went on.
func TestCompactedRunKeepsAnsweringTheSameRead(t *testing.T) {
	llm := &repeatReadLLM{}
	svc, err := New("compaction-loop").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	var reads int32
	svc.AddToolWithMetadata("read_it", "Reads a file.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"}}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			atomic.AddInt32(&reads, 1)
			return "file content", nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true})

	events, err := svc.RunStreamWithOptions(context.Background(), "Keep reading it.",
		WithMaxTurns(4),
		// Threshold 1 token: every round compacts, which is the situation a
		// coding agent is permanently in at the 8000-token default once it
		// has read a few source files.
		WithAutoCompaction(1, 2),
	)
	if err != nil {
		t.Fatalf("RunStreamWithOptions: %v", err)
	}
	for range events {
	}

	if n := atomic.LoadInt32(&reads); n < 2 {
		t.Fatalf("the read executed %d time(s) over 4 compacted rounds; after compaction "+
			"removed the result, the repeat must be answered rather than refused", n)
	}
}

// repeatReadLLM asks for the same read every turn, which is what a model does
// when compaction keeps deleting the answer.
type repeatReadLLM struct{ turns int32 }

func (l *repeatReadLLM) reply(tools []domain.ToolDefinition) *domain.GenerationResult {
	if len(tools) == 0 {
		return &domain.GenerationResult{Content: "Done.", FinishReason: "stop"}
	}
	n := atomic.AddInt32(&l.turns, 1)
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID: fmt.Sprintf("call-%d", n), Type: "function",
			Function: domain.FunctionCall{
				Name:      "read_it",
				Arguments: map[string]interface{}{"path": "Makefile"},
			},
		}},
		FinishReason: "tool_calls",
	}
}

func (l *repeatReadLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *repeatReadLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *repeatReadLLM) GenerateWithTools(_ context.Context, _ []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(tools), nil
}

func (l *repeatReadLLM) StreamWithTools(_ context.Context, _ []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.reply(tools))
}

func (l *repeatReadLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *repeatReadLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}
