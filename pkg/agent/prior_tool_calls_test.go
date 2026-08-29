package agent

import (
	"context"
	"strings"
	"testing"
)

// A contract lint asks whether the user's request was carried out. A segment
// is not the task: a plan created in segment zero is still there in segment
// five, and telling that segment "you never called scratchpad_set" is asking
// the wrong run. Measured on a live segmented run, blocked on exactly that
// while its plan sat on disk with two steps already ticked.
func TestPriorSegmentsCountTowardsTheContract(t *testing.T) {
	t.Parallel()
	lint := RequestedActionContract()

	asked := LintContext{
		Goal:           "keep a scratchpad plan",
		AvailableTools: []string{"scratchpad_set"},
		RequestedActions: []RequestedAction{{
			Kind:          "note",
			Description:   "Keep a scratchpad plan with one step per milestone.",
			SatisfiedBy:   "scratchpad_set",
			Unconditional: true,
		}},
	}

	// This segment never touched it, and nothing says an earlier one did.
	if ok, _ := lint.Check("Done.", asked); ok {
		t.Fatal("with no evidence the action was ever carried out, the contract should hold")
	}

	// The same segment, told that an earlier one did it.
	withPrior := asked
	withPrior.ToolCalls = []string{"scratchpad_set"}
	if ok, reason := lint.Check("Done.", withPrior); !ok {
		t.Errorf("the action was carried out earlier in the task: %s", reason)
	}
}

// The merge is what carries it: the runtime hands the lint this run's tools
// plus the ones earlier segments reported.
func TestRuntimeMergesPriorToolCalls(t *testing.T) {
	t.Parallel()
	r := &Runtime{cfg: &RunConfig{PriorToolCalls: []string{"scratchpad_set", "fs_write"}}}
	r.toolNamesUsed = map[string]bool{"fs_write": true, "bash": true}

	got := strings.Join(sortedCopy(r.toolNamesUsedForTask()), ",")
	want := "bash,fs_write,scratchpad_set"
	if got != want {
		t.Errorf("merged tools = %q, want %q (deduplicated, this run plus earlier segments)", got, want)
	}
}

// A single run has no earlier segments and must be unchanged.
func TestSingleRunSeesOnlyItsOwnTools(t *testing.T) {
	t.Parallel()
	r := &Runtime{cfg: &RunConfig{}}
	r.toolNamesUsed = map[string]bool{"fs_write": true}
	if got := r.toolNamesUsedForTask(); len(got) != 1 || got[0] != "fs_write" {
		t.Errorf("got %v, want just this run's tools", got)
	}
}

// RunSegments has to accumulate them, or the merge has nothing to work with.
func TestRunSegmentsAccumulatesToolsAcrossSegments(t *testing.T) {
	llm := &scriptedLLM{finishAt: 3}
	svc := buildSegmentedService(t, "segments-accumulate", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work.", LongRunConfig{
		MaxSegments:      4,
		RoundsPerSegment: 2,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if len(res.Segments) < 2 {
		t.Fatalf("need several segments, got %d", len(res.Segments))
	}
	// Every segment ran the same tool, so the task's tool set is stable — what
	// matters is that later segments were given it rather than starting blank.
	// The observable proof is that none of them was blocked by a contract lint
	// for never having called it.
	for _, seg := range res.Segments {
		if strings.Contains(seg.Error, "never called it") {
			t.Errorf("segment %d was told it never called a tool an earlier segment used: %s",
				seg.Index, seg.Error)
		}
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
