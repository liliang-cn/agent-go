package agent

import (
	"context"
	"testing"
)

func TestToolUseSink_RecordsInnerNames(t *testing.T) {
	seen := map[string]bool{}
	ctx := withToolUseSink(context.Background(), func(name string) { seen[name] = true })

	recordToolUse(ctx, "fs_write")
	recordToolUse(ctx, "web_search")
	recordToolUse(ctx, "") // ignored

	if !seen["fs_write"] || !seen["web_search"] {
		t.Fatalf("sink did not record inner tool names: %v", seen)
	}
	if seen[""] {
		t.Fatalf("empty tool name should be ignored")
	}
}

func TestRecordToolUse_NoSinkIsNoop(t *testing.T) {
	// Must not panic when no sink is installed.
	recordToolUse(context.Background(), "fs_write")
}

func TestRuntimeTrackToolName_ShowsInSnapshot(t *testing.T) {
	r := &Runtime{}
	r.trackToolName("fs_write")
	r.trackToolName("  ") // ignored
	r.trackToolName("scratchpad_set")

	got := map[string]bool{}
	for _, n := range r.toolNamesUsedSnapshot() {
		got[n] = true
	}
	if !got["fs_write"] || !got["scratchpad_set"] {
		t.Fatalf("trackToolName not reflected in snapshot: %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 tracked names, got %v", got)
	}
}
