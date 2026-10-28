package trace

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A call whose answer came whole has no time to first output: its first byte
// is its last, and the old record called a 34-second wait "ttft 34.35s".
func TestTTFTOnlyForStreamedCalls(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	ctx := WithRun(context.Background(), "r")

	whole := Model(ctx, "constraints")
	whole.Wire().Read(10) // first (and only) bytes of a JSON answer
	whole.End(&domain.TokenUsage{PromptTokens: 5, CompletionTokens: 1}, nil)

	streamed := Model(ctx, "turn")
	streamed.Wire().Streamed()
	time.Sleep(2 * time.Millisecond)
	streamed.Output()
	time.Sleep(2 * time.Millisecond)
	streamed.End(&domain.TokenUsage{PromptTokens: 10, CompletionTokens: 2, CachedPromptTokens: 4, CacheWriteTokens: 3}, nil)

	recs, sum, _ := Timeline(ctx)
	if recs[0].TTFTMs != 0 || recs[0].Streamed {
		t.Fatalf("a whole answer reported ttft: %+v", recs[0])
	}
	if !recs[1].Streamed || recs[1].TTFTMs <= 0 || recs[1].CacheWrite != 3 {
		t.Fatalf("streamed call: %+v", recs[1])
	}
	if sum.Tokens == nil || *sum.Tokens != (Tokens{Prompt: 15, Completion: 3, Cached: 4, CacheWrite: 3}) {
		t.Fatalf("tokens: %+v", sum.Tokens)
	}
	if sum.ByPurpose["constraints"].Calls != 1 || sum.ByPurpose["turn"].Tokens.CacheWrite != 3 {
		t.Fatalf("by purpose: %+v", sum.ByPurpose)
	}
}

func TestTallyAddsToolsLintsAndCompactions(t *testing.T) {
	start := time.Now().Add(-time.Second)
	tl := NewTally(start)
	tl.Model("turn", start.Add(100*time.Millisecond), 300*time.Millisecond, 50*time.Millisecond, Tokens{Prompt: 10, Completion: 1})
	tl.Tool("bash", 20*time.Millisecond, false)
	tl.Tool("bash", 30*time.Millisecond, true)
	tl.Tool("fs_read", 5*time.Millisecond, false)
	tl.Lint(true)
	tl.Lint(false)
	tl.Compaction()
	s := tl.SummaryAt(start.Add(time.Second))
	if s.TotalMs != 1000 || s.FirstModelCallMs != 100 || s.FirstAnswerTokenMs != 150 {
		t.Fatalf("times: %+v", s)
	}
	if s.ToolCalls != 3 || s.ToolFailures != 1 || s.Tools["bash"] != (ToolSummary{Calls: 2, Failures: 1, Ms: 50}) {
		t.Fatalf("tools: %+v", s)
	}
	if s.LintRetries != 1 || s.LintBlocks != 1 || s.Compactions != 1 || s.ModelMs != 300 {
		t.Fatalf("counts: %+v", s)
	}
}
