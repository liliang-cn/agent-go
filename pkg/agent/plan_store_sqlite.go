// A durable plan, in rows rather than a blob.
//
// PlanStore's contract is whole-list: SavePlan replaces everything under a
// key. That is right for a plan — it is a dozen steps, and one writer owns it
// — and keeping the interface at two methods is why anyone can implement it in
// twenty lines.
//
// What is not right is implementing that contract as delete-everything-then-
// insert. The model's own vocabulary is per-item — scratchpad_check step 3,
// scratchpad_note step 5 — and a rewrite that discards rows discards the one
// thing a long run most wants to know afterwards: when each step was reached,
// how long it took, and which steps were checked, unchecked and checked again.
// On a run measured in hours, "it kept going back to step 7" is the diagnosis,
// and a blob cannot answer it.
//
// So SavePlan is a diff inside one transaction: rows that did not change are
// left alone, and their timestamps survive.
package agent

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SQLitePlanStore persists plans as rows in the database a Service already
// owns. Safe for concurrent use; every write is one transaction.
type SQLitePlanStore struct {
	db *sql.DB
}

// NewSQLitePlanStore creates the table if needed and returns a store over db.
func NewSQLitePlanStore(db *sql.DB) (*SQLitePlanStore, error) {
	if db == nil {
		return nil, fmt.Errorf("agent: NewSQLitePlanStore needs a database handle")
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS plan_items (
			plan_key   TEXT    NOT NULL,
			idx        INTEGER NOT NULL,
			text       TEXT    NOT NULL,
			done       INTEGER NOT NULL DEFAULT 0,
			note       TEXT    NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			done_at    DATETIME,
			PRIMARY KEY (plan_key, idx)
		)`); err != nil {
		return nil, fmt.Errorf("agent: create plan_items: %w", err)
	}
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_plan_items_key ON plan_items(plan_key, idx)`); err != nil {
		return nil, fmt.Errorf("agent: index plan_items: %w", err)
	}
	return &SQLitePlanStore{db: db}, nil
}

// LoadPlan returns the stored plan for a key, in step order. An unknown key is
// an empty plan and no error.
func (s *SQLitePlanStore) LoadPlan(ctx context.Context, key string) ([]PlanItem, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT text, done, note FROM plan_items WHERE plan_key = ? ORDER BY idx`, key)
	if err != nil {
		return nil, fmt.Errorf("agent: load plan %q: %w", key, err)
	}
	defer rows.Close()

	var out []PlanItem
	for rows.Next() {
		var (
			item PlanItem
			done int
		)
		if err := rows.Scan(&item.Text, &done, &item.Note); err != nil {
			return nil, fmt.Errorf("agent: scan plan %q: %w", key, err)
		}
		item.Done = done != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

// SavePlan replaces the plan under key, as a diff.
//
// Steps whose text, done flag and note are all unchanged are not written at
// all, so created_at and done_at keep saying when that step first appeared and
// when it was finished. A step that flips to done gains a done_at; one that is
// unchecked loses it, which is itself worth being able to see.
func (s *SQLitePlanStore) SavePlan(ctx context.Context, key string, items []PlanItem) error {
	if s == nil || s.db == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("agent: save plan %q: %w", key, err)
	}
	defer func() { _ = tx.Rollback() }()

	existing := map[int]PlanItem{}
	rows, err := tx.QueryContext(ctx,
		`SELECT idx, text, done, note FROM plan_items WHERE plan_key = ?`, key)
	if err != nil {
		return fmt.Errorf("agent: save plan %q: %w", key, err)
	}
	for rows.Next() {
		var (
			idx  int
			it   PlanItem
			done int
		)
		if err := rows.Scan(&idx, &it.Text, &done, &it.Note); err != nil {
			rows.Close()
			return fmt.Errorf("agent: save plan %q: %w", key, err)
		}
		it.Done = done != 0
		existing[idx] = it
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("agent: save plan %q: %w", key, err)
	}

	now := time.Now().UTC()
	for i, item := range items {
		prev, had := existing[i]
		if had && prev == item {
			continue // untouched: leave its timestamps alone
		}
		done := 0
		if item.Done {
			done = 1
		}
		// done_at is set on the transition into done and cleared on the way
		// out, so a step checked, unchecked and checked again reads as the
		// last time it was actually finished.
		var doneAt interface{}
		if item.Done {
			if had && prev.Done {
				doneAt = nil // preserved below by COALESCE
			} else {
				doneAt = now
			}
		}
		if had {
			if item.Done && doneAt == nil {
				_, err = tx.ExecContext(ctx,
					`UPDATE plan_items SET text=?, done=?, note=?, updated_at=? WHERE plan_key=? AND idx=?`,
					item.Text, done, item.Note, now, key, i)
			} else {
				_, err = tx.ExecContext(ctx,
					`UPDATE plan_items SET text=?, done=?, note=?, updated_at=?, done_at=? WHERE plan_key=? AND idx=?`,
					item.Text, done, item.Note, now, doneAt, key, i)
			}
		} else {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO plan_items (plan_key, idx, text, done, note, created_at, updated_at, done_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				key, i, item.Text, done, item.Note, now, now, doneAt)
		}
		if err != nil {
			return fmt.Errorf("agent: save plan %q step %d: %w", key, i, err)
		}
	}

	// A shortened plan drops its tail.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM plan_items WHERE plan_key = ? AND idx >= ?`, key, len(items)); err != nil {
		return fmt.Errorf("agent: save plan %q: %w", key, err)
	}
	return tx.Commit()
}

// PlanStepTiming is one step's history: what it says, whether it is finished,
// and when each of those became true.
type PlanStepTiming struct {
	Index     int
	Text      string
	Note      string
	Done      bool
	CreatedAt time.Time
	UpdatedAt time.Time
	// DoneAt is zero while the step is unfinished.
	DoneAt time.Time
}

// Duration is how long the step took: from first appearing to being checked
// off. Zero while it is still open.
func (t PlanStepTiming) Duration() time.Duration {
	if t.DoneAt.IsZero() {
		return 0
	}
	return t.DoneAt.Sub(t.CreatedAt)
}

// Timeline returns each step with its timestamps.
//
// This is the question a long run leaves behind — which step took four hours,
// which one was checked and then reopened — and it is the reason the plan is
// rows and not a blob.
func (s *SQLitePlanStore) Timeline(ctx context.Context, key string) ([]PlanStepTiming, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT idx, text, done, note, created_at, updated_at, done_at
		 FROM plan_items WHERE plan_key = ? ORDER BY idx`, key)
	if err != nil {
		return nil, fmt.Errorf("agent: plan timeline %q: %w", key, err)
	}
	defer rows.Close()

	var out []PlanStepTiming
	for rows.Next() {
		var (
			t      PlanStepTiming
			done   int
			doneAt sql.NullTime
		)
		if err := rows.Scan(&t.Index, &t.Text, &done, &t.Note, &t.CreatedAt, &t.UpdatedAt, &doneAt); err != nil {
			return nil, fmt.Errorf("agent: plan timeline %q: %w", key, err)
		}
		t.Done = done != 0
		if doneAt.Valid {
			t.DoneAt = doneAt.Time
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
