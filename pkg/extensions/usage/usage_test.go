package usage

import (
	"bytes"
	"context"
	"math"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

func TestLedgerSumsByModelAndPricesWhatItCan(t *testing.T) {
	pool.RegisterModelPricing("usage-test-model", pool.ModelPricing{
		InputPer1K: 0.001, CachedInputPer1K: 0.0001, OutputPer1K: 0.002, // $1 / $0.1 / $2 per million
	})
	e := New()
	ctx := context.Background()
	e.OnModelEnd(ctx, agent.ModelInfo{Model: "usage-test-model"},
		&agent.ModelResult{PromptTokens: 1_000_000, CachedTokens: 500_000, CompletionTokens: 100_000}, nil)
	e.OnModelEnd(ctx, agent.ModelInfo{Model: "usage-test-model"},
		&agent.ModelResult{PromptTokens: 10, CompletionTokens: 5}, nil)
	e.OnModelEnd(ctx, agent.ModelInfo{Model: "nobody-prices-this-model-zz"},
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
	// 500k fresh at $1/M + 500k cached at $0.1/M + 100k out at $2/M = 0.5 + 0.05 + 0.2.
	want := 0.75 + (10*1.0+5*2.0)/1e6
	if math.Abs(s.ByModel["usage-test-model"].CostUSD-want) > 1e-9 {
		t.Fatalf("cost = %v want %v", s.ByModel["usage-test-model"].CostUSD, want)
	}
	if s.ByModel["nobody-prices-this-model-zz"].Unpriced != 1 || s.Total.Unpriced != 1 {
		t.Fatalf("unpriced not counted: %+v", s)
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
	for _, want := range []string{"usage-test-model", "nobody-prices-this-model-zz", "+1 unpriced", "total", "retries 1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}

	e.Reset()
	if s := e.Snapshot(); s.Total.Calls != 0 || len(s.ByModel) != 0 {
		t.Fatalf("reset left %+v", s)
	}
}
