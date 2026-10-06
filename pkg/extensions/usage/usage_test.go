package usage

import (
	"bytes"
	"context"
	"math"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func TestLedgerSumsTokensByModel(t *testing.T) {
	e := New()
	ctx := context.Background()
	e.OnModelEnd(ctx, agent.ModelInfo{Model: "usage-test-model"},
		&agent.ModelResult{PromptTokens: 1_000_000, CachedTokens: 500_000, CompletionTokens: 100_000}, nil)
	e.OnModelEnd(ctx, agent.ModelInfo{Model: "usage-test-model"},
		&agent.ModelResult{PromptTokens: 10, CompletionTokens: 5}, nil)
	e.OnModelEnd(ctx, agent.ModelInfo{Model: "other-model-zz"},
		&agent.ModelResult{PromptTokens: 7, CompletionTokens: 3}, nil)
	e.OnModelEnd(ctx, agent.ModelInfo{}, nil, nil) // a failed turn carries no result
	e.OnModelRetry(ctx, agent.ModelRetryInfo{})
	e.OnCompaction(ctx, agent.CompactionInfo{})
	e.OnSegment(ctx, agent.SegmentInfo{})
	e.OnSegment(ctx, agent.SegmentInfo{Ending: true})

	s := e.Snapshot()
	if s.Total.Calls != 3 || s.Total.PromptTokens != 1_000_017 || s.Total.CompletionTokens != 100_008 {
		t.Fatalf("total = %+v", s.Total)
	}
	if m := s.ByModel["usage-test-model"]; m.Calls != 2 || m.PromptTokens != 1_000_010 || m.CachedTokens != 500_000 || m.CompletionTokens != 100_005 {
		t.Fatalf("usage-test-model = %+v", m)
	}
	if m := s.ByModel["other-model-zz"]; m.Calls != 1 || m.PromptTokens != 7 || m.CompletionTokens != 3 {
		t.Fatalf("other-model-zz = %+v", m)
	}
	if s.Retries != 1 || s.Compactions != 1 || s.Segments != 1 {
		t.Fatalf("counters = %+v", s)
	}
	if r := s.ByModel["usage-test-model"].CacheHitRate(); math.Abs(r-500_000.0/1_000_010.0) > 1e-9 {
		t.Fatalf("cache hit rate = %v", r)
	}

	var buf bytes.Buffer
	e.Report(&buf)
	out := buf.String()
	for _, want := range []string{"usage-test-model", "other-model-zz", "total", "retries 1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}

	e.Reset()
	if s := e.Snapshot(); s.Total.Calls != 0 || len(s.ByModel) != 0 {
		t.Fatalf("reset left %+v", s)
	}
}
