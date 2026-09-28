package agent

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Per-tool reliability: how often each tool has been called, how often it
// failed, how often the model called it wrongly, and how long it takes.
//
// executionMetrics counts tool calls per run and forgets them when the run
// ends. A tool's track record is a property of the tool, not of a run: an MCP
// server that fails one call in three, a fetch that takes nine seconds, is as
// true tomorrow as today. So the counts are kept per Service and written
// through to the Service's own database when it has one — the same database
// the plan and task memory already live in — and survive the process.
//
// What is NOT done with them: they are not rendered into the tool index or
// the schema. Both sit in the prompt prefix, which must be byte-stable across
// a run for the prompt cache to hold (see prompt_cache.go and the tool-order
// fix). A failure rate changes with every call; putting it there would rewrite
// the prefix every round, which is exactly the bug that took cache hits from
// 83% to 9%. A host that wants the model to see them can surface them in a
// tool result, which sits at the tail of the conversation.

// ToolOutcomeKind is what happened to one call.
type ToolOutcomeKind string

const (
	// ToolOutcomeSuccess: the tool ran and returned no error.
	ToolOutcomeSuccess ToolOutcomeKind = "success"
	// ToolOutcomeError: the tool ran and returned an error.
	ToolOutcomeError ToolOutcomeKind = "error"
	// ToolOutcomeInvalidArgs: the call failed its declared schema and did not
	// run. Counted apart: it says something about the model, not the tool.
	ToolOutcomeInvalidArgs ToolOutcomeKind = "invalid_args"
)

// ToolOutcome is one call's result, as recorded.
type ToolOutcome struct {
	Tool      string          `json:"tool"`
	Kind      ToolOutcomeKind `json:"kind"`
	Latency   time.Duration   `json:"latency_ns"`
	Error     string          `json:"error,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	At        time.Time       `json:"at"`
}

// ToolReliability is one tool's accumulated record.
type ToolReliability struct {
	Tool        string `json:"tool"`
	Successes   int64  `json:"successes"`
	Errors      int64  `json:"errors"`
	InvalidArgs int64  `json:"invalid_args"`
	// TotalLatency and MaxLatency cover executed calls only; a refused call
	// took no time in the tool.
	TotalLatency time.Duration `json:"total_latency_ns"`
	MaxLatency   time.Duration `json:"max_latency_ns"`
	LastError    string        `json:"last_error,omitempty"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

// Executions is how many times the tool actually ran.
func (r ToolReliability) Executions() int64 { return r.Successes + r.Errors }

// ErrorRate is errors over executions, 0 when it has never run.
func (r ToolReliability) ErrorRate() float64 {
	if n := r.Executions(); n > 0 {
		return float64(r.Errors) / float64(n)
	}
	return 0
}

// MeanLatency is the average time one execution took.
func (r ToolReliability) MeanLatency() time.Duration {
	if n := r.Executions(); n > 0 {
		return r.TotalLatency / time.Duration(n)
	}
	return 0
}

func (r *ToolReliability) apply(o ToolOutcome) {
	switch o.Kind {
	case ToolOutcomeSuccess:
		r.Successes++
	case ToolOutcomeError:
		r.Errors++
		r.LastError = truncateReliabilityError(o.Error)
	case ToolOutcomeInvalidArgs:
		r.InvalidArgs++
	}
	if o.Kind != ToolOutcomeInvalidArgs {
		r.TotalLatency += o.Latency
		if o.Latency > r.MaxLatency {
			r.MaxLatency = o.Latency
		}
	}
	r.UpdatedAt = o.At
}

func truncateReliabilityError(s string) string {
	const max = 300
	if len(s) > max {
		return s[:max]
	}
	return s
}

// ToolReliabilityStore persists outcomes. RecordToolOutcome must add to what
// is stored, not replace it, so two Services on one store both count.
type ToolReliabilityStore interface {
	RecordToolOutcome(ctx context.Context, outcome ToolOutcome) error
	LoadToolReliability(ctx context.Context) ([]ToolReliability, error)
}

// ToolReliabilityObserver is told about every recorded outcome, with the
// tool's record after it. Optional, like ResourceObserver: adding a method to
// Observer would break every host implementing it without BaseObserver.
type ToolReliabilityObserver interface {
	OnToolOutcome(ctx context.Context, outcome ToolOutcome, record ToolReliability)
}

// toolReliabilityTracker is the in-process half. With a store it holds this
// process's view on top of what was stored when each tool was first touched;
// reads through the accessor go to the store, which also sees other
// processes.
type toolReliabilityTracker struct {
	mu      sync.Mutex
	records map[string]*ToolReliability
	seeded  map[string]bool
	store   ToolReliabilityStore
}

func newToolReliabilityTracker() *toolReliabilityTracker {
	return &toolReliabilityTracker{records: map[string]*ToolReliability{}, seeded: map[string]bool{}}
}

// SetToolReliabilityStore sets where tool outcomes are persisted; nil keeps
// them in memory only. Counts already recorded in memory are kept.
func (s *Service) SetToolReliabilityStore(rs ToolReliabilityStore) {
	t := s.reliability()
	t.mu.Lock()
	t.store = rs
	t.seeded = map[string]bool{}
	t.mu.Unlock()
}

func (s *Service) reliability() *toolReliabilityTracker {
	s.toolReliabilityOnce.Do(func() {
		if s.toolReliability == nil {
			s.toolReliability = newToolReliabilityTracker()
		}
	})
	return s.toolReliability
}

// recordToolOutcome counts one call and tells the observers.
func (s *Service) recordToolOutcome(ctx context.Context, o ToolOutcome) {
	if s == nil || o.Tool == "" {
		return
	}
	if o.At.IsZero() {
		o.At = time.Now()
	}
	t := s.reliability()

	t.mu.Lock()
	store := t.store
	rec := t.records[o.Tool]
	if rec == nil {
		rec = &ToolReliability{Tool: o.Tool}
		t.records[o.Tool] = rec
	}
	needSeed := store != nil && !t.seeded[o.Tool]
	t.seeded[o.Tool] = true
	t.mu.Unlock()

	if needSeed {
		// Start this process's view from what is already stored, so the
		// observer sees the tool's whole record and not only this process's.
		if stored, err := store.LoadToolReliability(ctx); err == nil {
			for _, r := range stored {
				if r.Tool == o.Tool {
					t.mu.Lock()
					base := r
					base.Successes += rec.Successes
					base.Errors += rec.Errors
					base.InvalidArgs += rec.InvalidArgs
					base.TotalLatency += rec.TotalLatency
					if rec.MaxLatency > base.MaxLatency {
						base.MaxLatency = rec.MaxLatency
					}
					*rec = base
					t.mu.Unlock()
					break
				}
			}
		}
	}

	t.mu.Lock()
	rec.apply(o)
	snapshot := *rec
	t.mu.Unlock()

	if store != nil {
		if err := store.RecordToolOutcome(context.WithoutCancel(ctx), o); err != nil && s.logger != nil {
			s.logger.Debug("tool reliability write failed", "tool", o.Tool, "error", err.Error())
		}
	}

	s.observersMu.RLock()
	var watchers []Observer
	for _, obs := range s.observers {
		if _, ok := obs.(ToolReliabilityObserver); ok {
			watchers = append(watchers, obs)
		}
	}
	s.observersMu.RUnlock()
	for _, obs := range watchers {
		ro := obs.(ToolReliabilityObserver)
		s.invokeObserver(obs, func(Observer) { ro.OnToolOutcome(ctx, o, snapshot) })
	}
}

// ToolReliability returns every tool's record, sorted by name. With a store it
// reads the store, so it includes calls made by other Services sharing it and
// by earlier processes.
func (s *Service) ToolReliability() []ToolReliability {
	if s == nil {
		return nil
	}
	t := s.reliability()
	t.mu.Lock()
	store := t.store
	t.mu.Unlock()
	if store != nil {
		if out, err := store.LoadToolReliability(context.Background()); err == nil {
			sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
			return out
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ToolReliability, 0, len(t.records))
	for _, r := range t.records {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

// ToolReliabilityOf returns one tool's record.
func (s *Service) ToolReliabilityOf(tool string) (ToolReliability, bool) {
	for _, r := range s.ToolReliability() {
		if r.Tool == tool {
			return r, true
		}
	}
	return ToolReliability{}, false
}

// SQLiteToolReliabilityStore keeps the counts in the Service's database, one
// row per tool, incremented in SQL so concurrent writers never lose a count.
type SQLiteToolReliabilityStore struct {
	db *sql.DB
}

// NewSQLiteToolReliabilityStore creates the table if needed.
func NewSQLiteToolReliabilityStore(db *sql.DB) (*SQLiteToolReliabilityStore, error) {
	if db == nil {
		return nil, fmt.Errorf("agent: NewSQLiteToolReliabilityStore needs a database handle")
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS tool_reliability (
			tool             TEXT    PRIMARY KEY,
			successes        INTEGER NOT NULL DEFAULT 0,
			errors           INTEGER NOT NULL DEFAULT 0,
			invalid_args     INTEGER NOT NULL DEFAULT 0,
			total_latency_us INTEGER NOT NULL DEFAULT 0,
			max_latency_us   INTEGER NOT NULL DEFAULT 0,
			last_error       TEXT    NOT NULL DEFAULT '',
			updated_at       INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		return nil, fmt.Errorf("agent: create tool_reliability: %w", err)
	}
	return &SQLiteToolReliabilityStore{db: db}, nil
}

// RecordToolOutcome adds one outcome to the tool's row.
func (st *SQLiteToolReliabilityStore) RecordToolOutcome(ctx context.Context, o ToolOutcome) error {
	if st == nil || st.db == nil {
		return nil
	}
	var succ, errs, invalid, lat int64
	lastErr := ""
	switch o.Kind {
	case ToolOutcomeSuccess:
		succ = 1
	case ToolOutcomeError:
		errs = 1
		lastErr = truncateReliabilityError(o.Error)
	case ToolOutcomeInvalidArgs:
		invalid = 1
	}
	if o.Kind != ToolOutcomeInvalidArgs {
		lat = o.Latency.Microseconds()
	}
	at := o.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO tool_reliability
			(tool, successes, errors, invalid_args, total_latency_us, max_latency_us, last_error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tool) DO UPDATE SET
			successes        = successes + excluded.successes,
			errors           = errors + excluded.errors,
			invalid_args     = invalid_args + excluded.invalid_args,
			total_latency_us = total_latency_us + excluded.total_latency_us,
			max_latency_us   = MAX(max_latency_us, excluded.max_latency_us),
			last_error       = CASE WHEN excluded.last_error != '' THEN excluded.last_error ELSE last_error END,
			updated_at       = excluded.updated_at`,
		o.Tool, succ, errs, invalid, lat, lat, lastErr, at.UnixNano())
	if err != nil {
		return fmt.Errorf("agent: record tool outcome %q: %w", o.Tool, err)
	}
	return nil
}

// LoadToolReliability returns every stored row.
func (st *SQLiteToolReliabilityStore) LoadToolReliability(ctx context.Context) ([]ToolReliability, error) {
	if st == nil || st.db == nil {
		return nil, nil
	}
	rows, err := st.db.QueryContext(ctx, `
		SELECT tool, successes, errors, invalid_args, total_latency_us, max_latency_us, last_error, updated_at
		FROM tool_reliability ORDER BY tool`)
	if err != nil {
		return nil, fmt.Errorf("agent: load tool reliability: %w", err)
	}
	defer rows.Close()
	var out []ToolReliability
	for rows.Next() {
		var (
			r            ToolReliability
			total, maxUS int64
			updated      int64
		)
		if err := rows.Scan(&r.Tool, &r.Successes, &r.Errors, &r.InvalidArgs, &total, &maxUS, &r.LastError, &updated); err != nil {
			return nil, fmt.Errorf("agent: scan tool reliability: %w", err)
		}
		r.TotalLatency = time.Duration(total) * time.Microsecond
		r.MaxLatency = time.Duration(maxUS) * time.Microsecond
		if updated > 0 {
			r.UpdatedAt = time.Unix(0, updated)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
