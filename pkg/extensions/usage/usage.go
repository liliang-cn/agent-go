// Package usage meters what a service spends: tokens by model with the
// prompt-cache split, priced where a price is known, plus the retries,
// compactions and segments a long run goes through.
//
// It is an Observer with a ledger. ExecutionResult already carries usage for
// one run; this is the same accounting across every run the service makes,
// which is the number a host wants on a dashboard or a daily cap.
package usage

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// Totals is the accounting for one model, or for everything.
type Totals struct {
	Calls            int
	PromptTokens     int
	CachedTokens     int
	CompletionTokens int
	// CostUSD sums the calls that could be priced.
	CostUSD float64
	// Unpriced counts calls whose model had no price; their cost is unknown,
	// not zero, and CostUSD does not include them.
	Unpriced int
}

// Snapshot is a point-in-time copy of the ledger.
type Snapshot struct {
	Since       time.Time
	Total       Totals
	ByModel     map[string]Totals
	Retries     int
	Compactions int
	Segments    int
}

// CacheHitRate is the share of prompt tokens served from cache.
func (t Totals) CacheHitRate() float64 {
	if t.PromptTokens == 0 {
		return 0
	}
	return float64(t.CachedTokens) / float64(t.PromptTokens)
}

// Extension implements agent.Extension and agent.Observer.
type Extension struct {
	agent.BaseObserver

	mu          sync.Mutex
	since       time.Time
	total       Totals
	byModel     map[string]*Totals
	retries     int
	compactions int
	segments    int
}

// New returns an empty ledger.
func New() *Extension {
	return &Extension{since: time.Now(), byModel: map[string]*Totals{}}
}

// Name implements agent.Extension.
func (e *Extension) Name() string { return "usage" }

// OnModelEnd implements agent.Observer.
func (e *Extension) OnModelEnd(_ context.Context, info agent.ModelInfo, res *agent.ModelResult, _ error) {
	if res == nil {
		return
	}
	model := info.Model
	if model == "" {
		model = "(unknown)"
	}
	cost, priced := pool.CalculateCostDetailed(model, res.PromptTokens, res.CachedTokens, res.CompletionTokens)

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, t := range []*Totals{&e.total, e.bucket(model)} {
		t.Calls++
		t.PromptTokens += res.PromptTokens
		t.CachedTokens += res.CachedTokens
		t.CompletionTokens += res.CompletionTokens
		if priced {
			t.CostUSD += cost
		} else {
			t.Unpriced++
		}
	}
}

// OnModelRetry implements agent.Observer.
func (e *Extension) OnModelRetry(_ context.Context, _ agent.ModelRetryInfo) {
	e.mu.Lock()
	e.retries++
	e.mu.Unlock()
}

// OnCompaction implements agent.Observer.
func (e *Extension) OnCompaction(_ context.Context, _ agent.CompactionInfo) {
	e.mu.Lock()
	e.compactions++
	e.mu.Unlock()
}

// OnSegment implements agent.Observer; counts segment starts.
func (e *Extension) OnSegment(_ context.Context, info agent.SegmentInfo) {
	if info.Ending {
		return
	}
	e.mu.Lock()
	e.segments++
	e.mu.Unlock()
}

func (e *Extension) bucket(model string) *Totals {
	t := e.byModel[model]
	if t == nil {
		t = &Totals{}
		e.byModel[model] = t
	}
	return t
}

// Snapshot copies the ledger.
func (e *Extension) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Snapshot{
		Since: e.since, Total: e.total, ByModel: make(map[string]Totals, len(e.byModel)),
		Retries: e.retries, Compactions: e.compactions, Segments: e.segments,
	}
	for m, t := range e.byModel {
		s.ByModel[m] = *t
	}
	return s
}

// Reset clears the ledger, for a host that reports per day.
func (e *Extension) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.since = time.Now()
	e.total = Totals{}
	e.byModel = map[string]*Totals{}
	e.retries, e.compactions, e.segments = 0, 0, 0
}

// Report writes the ledger as a table.
func (e *Extension) Report(w io.Writer) {
	s := e.Snapshot()
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "model\tcalls\tprompt\tcached\tcompletion\tcache hit\tcost")
	models := make([]string, 0, len(s.ByModel))
	for m := range s.ByModel {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		writeRow(tw, m, s.ByModel[m])
	}
	writeRow(tw, "total", s.Total)
	fmt.Fprintf(tw, "retries %d\tcompactions %d\tsegments %d\tsince %s\n",
		s.Retries, s.Compactions, s.Segments, s.Since.Format(time.RFC3339))
	tw.Flush()
}

func writeRow(w io.Writer, name string, t Totals) {
	cost := fmt.Sprintf("$%.4f", t.CostUSD)
	if t.Unpriced > 0 {
		cost += fmt.Sprintf(" (+%d unpriced)", t.Unpriced)
	}
	fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%.0f%%\t%s\n",
		name, t.Calls, t.PromptTokens, t.CachedTokens, t.CompletionTokens, 100*t.CacheHitRate(), cost)
}
