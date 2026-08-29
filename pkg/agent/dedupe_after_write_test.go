package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A read of a file that has since been written is a different read. Collapsing
// it hands the model the content from before its own edit, and tells it that
// content is current — which is the worst of both.
func TestReadAfterWriteIsNotCollapsed(t *testing.T) {
	svc, err := New("dedupe-after-write").
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
	svc.AddToolWithMetadata("write_it", "Writes.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"}}},
		func(context.Context, map[string]interface{}) (interface{}, error) { return "ok", nil },
		ToolMetadata{})

	read := &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
		ID: "r1", Type: "function",
		Function: domain.FunctionCall{Name: "read_it", Arguments: map[string]interface{}{"path": "Makefile"}},
	}}}
	write := &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
		ID: "w1", Type: "function",
		Function: domain.FunctionCall{Name: "write_it", Arguments: map[string]interface{}{"path": "Makefile"}},
	}}}

	seen := map[string]int{}
	var msgs []domain.Message

	// round 1: read
	if kept, _, _ := svc.handleDuplicateToolCalls(msgs, cloneResult(read), seen); len(kept) != 1 {
		t.Fatal("the first read must execute")
	}
	// round 2: write the same path
	if kept, _, _ := svc.handleDuplicateToolCalls(msgs, cloneResult(write), seen); len(kept) != 1 {
		t.Fatal("the write must execute")
	}
	// round 3: read it back — the file changed, so this is a new question
	kept, dups, _ := svc.handleDuplicateToolCalls(msgs, cloneResult(read), seen)
	if len(kept) != 1 {
		for _, d := range dups {
			if s, ok := d.Result.(string); ok && strings.Contains(s, "unchanged") {
				t.Fatalf("a read after a write was collapsed with %q — the model is handed "+
					"its own pre-edit content and told it is current", s)
			}
		}
		t.Fatal("a read after a write must execute again")
	}
}

func cloneResult(r *domain.GenerationResult) *domain.GenerationResult {
	c := *r
	c.ToolCalls = append([]domain.ToolCall(nil), r.ToolCalls...)
	return &c
}

// The collapse must still work when nothing has changed — that is what stops a
// model re-polling the same status forever.
func TestRepeatedReadWithNoWriteIsStillCollapsed(t *testing.T) {
	svc, err := New("dedupe-no-write").
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
	seen := map[string]int{}
	var msgs []domain.Message

	if kept, _, _ := svc.handleDuplicateToolCalls(msgs, cloneResult(read), seen); len(kept) != 1 {
		t.Fatal("the first read must execute")
	}
	for i := 2; i <= 4; i++ {
		kept, dups, _ := svc.handleDuplicateToolCalls(msgs, cloneResult(read), seen)
		if len(kept) != 0 || len(dups) != 1 {
			t.Fatalf("read %d executed again with nothing written in between", i)
		}
		// The hint has to name the count. "You already called this" is what the
		// model has been ignoring; "you have called this four times and it
		// cannot make progress" is a different statement.
		msg, _ := dups[0].Result.(string)
		if !strings.Contains(msg, itoa(i)) {
			t.Errorf("the hint should say how many times: %q", msg)
		}
		if !strings.Contains(msg, "take a different action") {
			t.Errorf("the hint should tell it what to do instead: %q", msg)
		}
	}
}

// A tool nobody declared is treated as state-changing, so it both re-executes
// and reopens the reads. Assuming an undescribed tool is safe to skip is the
// wrong way round.
func TestUnknownToolCountsAsAWrite(t *testing.T) {
	svc, err := New("dedupe-unknown-tool").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.AddToolWithMetadata("read_it", "Reads.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(context.Context, map[string]interface{}) (interface{}, error) { return "content", nil },
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true})

	read := &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
		ID: "r1", Type: "function",
		Function: domain.FunctionCall{Name: "read_it", Arguments: map[string]interface{}{}},
	}}}
	mystery := &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
		ID: "m1", Type: "function",
		Function: domain.FunctionCall{Name: "nobody_registered_me", Arguments: map[string]interface{}{}},
	}}}
	seen := map[string]int{}
	var msgs []domain.Message

	svc.handleDuplicateToolCalls(msgs, cloneResult(read), seen)
	svc.handleDuplicateToolCalls(msgs, cloneResult(mystery), seen)
	if kept, _, _ := svc.handleDuplicateToolCalls(msgs, cloneResult(read), seen); len(kept) != 1 {
		t.Error("a read after an undeclared tool ran must execute again")
	}
}
