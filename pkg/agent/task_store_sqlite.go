// TaskStore over SQLite: an append-only journal under a mutable projection.
//
// The shape follows the store's contract rather than the other way round.
// task_state is the one table that is updated in place — it is a projection,
// and losing an old value of "status" costs nothing because the history that
// produced it is in task_runs and task_journal, which are only ever appended
// to. The journal's idempotency key is a partial unique index: NULL keys
// never collide (most entries are plain observations), and a non-NULL key
// appended twice is answered with the original row's seq instead of a second
// insert — which is the entire crash-retry story, expressed as one index.
package agent

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SQLiteTaskStore persists task memory in the database a Service already
// owns. Safe for concurrent use; every write is a single statement or a
// single transaction.
type SQLiteTaskStore struct {
	db *sql.DB
}

var _ TaskStore = (*SQLiteTaskStore)(nil)

// NewSQLiteTaskStore creates the tables if needed and returns a store over db.
func NewSQLiteTaskStore(db *sql.DB) (*SQLiteTaskStore, error) {
	if db == nil {
		return nil, fmt.Errorf("agent: NewSQLiteTaskStore needs a database handle")
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS task_state (
			id           TEXT PRIMARY KEY,
			parent_id    TEXT NOT NULL DEFAULT '',
			goal         TEXT NOT NULL,
			status       TEXT NOT NULL DEFAULT 'pending',
			resume_brief TEXT NOT NULL DEFAULT '',
			created_at   DATETIME NOT NULL,
			updated_at   DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS task_runs (
			id         TEXT PRIMARY KEY,
			task_id    TEXT NOT NULL,
			started_at DATETIME NOT NULL,
			ended_at   DATETIME,
			outcome    TEXT NOT NULL DEFAULT '',
			summary    TEXT NOT NULL DEFAULT '',
			cost_usd   REAL NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs(task_id, started_at)`,
		`CREATE TABLE IF NOT EXISTS task_journal (
			seq      INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id  TEXT NOT NULL,
			run_id   TEXT NOT NULL,
			kind     TEXT NOT NULL,
			payload  TEXT NOT NULL,
			idem_key TEXT,
			at       DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_journal_run ON task_journal(run_id, seq)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_task_journal_idem
			ON task_journal(idem_key) WHERE idem_key IS NOT NULL`,
		`CREATE TABLE IF NOT EXISTS task_learnings (
			task_id    TEXT NOT NULL,
			lesson     TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_learnings_task ON task_learnings(task_id, created_at)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("agent: task store schema: %w", err)
		}
	}
	return &SQLiteTaskStore{db: db}, nil
}

// SaveTask inserts or updates the projection. Goal and created_at are
// write-once: the original ask is what a drifted task is measured against,
// so an update deliberately leaves both alone.
func (s *SQLiteTaskStore) SaveTask(ctx context.Context, t TaskState) error {
	if s == nil || s.db == nil {
		return nil
	}
	if t.ID == "" {
		return fmt.Errorf("agent: save task: empty id")
	}
	now := time.Now().UTC()
	created := t.CreatedAt
	if created.IsZero() {
		created = now
	}
	status := t.Status
	if status == "" {
		status = TaskStatusPending
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO task_state (id, parent_id, goal, status, resume_brief, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			parent_id    = excluded.parent_id,
			status       = excluded.status,
			resume_brief = excluded.resume_brief,
			updated_at   = excluded.updated_at`,
		t.ID, t.ParentID, t.Goal, status, t.ResumeBrief, created, now)
	if err != nil {
		return fmt.Errorf("agent: save task %q: %w", t.ID, err)
	}
	return nil
}

// LoadTask returns the task, or (nil, nil) when the id is unknown.
func (s *SQLiteTaskStore) LoadTask(ctx context.Context, id string) (*TaskState, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var t TaskState
	err := s.db.QueryRowContext(ctx, `
		SELECT id, parent_id, goal, status, resume_brief, created_at, updated_at
		FROM task_state WHERE id = ?`, id).
		Scan(&t.ID, &t.ParentID, &t.Goal, &t.Status, &t.ResumeBrief, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent: load task %q: %w", id, err)
	}
	return &t, nil
}

// SaveResumeBrief replaces the hand-off brief. An unknown task is an error —
// a brief with no task is a write to nowhere, and silently accepting it
// would hide the bug that produced it.
func (s *SQLiteTaskStore) SaveResumeBrief(ctx context.Context, taskID, brief string) error {
	if s == nil || s.db == nil {
		return nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE task_state SET resume_brief = ?, updated_at = ? WHERE id = ?`,
		brief, time.Now().UTC(), taskID)
	if err != nil {
		return fmt.Errorf("agent: save resume brief %q: %w", taskID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("agent: save resume brief: unknown task %q", taskID)
	}
	return nil
}

// BeginRun opens an episode, filling in a blank id.
func (s *SQLiteTaskStore) BeginRun(ctx context.Context, run TaskRun) (string, error) {
	if s == nil || s.db == nil {
		return run.ID, nil
	}
	if run.TaskID == "" {
		return "", fmt.Errorf("agent: begin run: empty task id")
	}
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	started := run.StartedAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO task_runs (id, task_id, started_at) VALUES (?, ?, ?)`,
		run.ID, run.TaskID, started); err != nil {
		return "", fmt.Errorf("agent: begin run for task %q: %w", run.TaskID, err)
	}
	return run.ID, nil
}

// EndRun closes an episode with its outcome, summary and cost.
func (s *SQLiteTaskStore) EndRun(ctx context.Context, runID, outcome, summary string, costUSD float64) error {
	if s == nil || s.db == nil {
		return nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE task_runs SET ended_at = ?, outcome = ?, summary = ?, cost_usd = ? WHERE id = ?`,
		time.Now().UTC(), outcome, summary, costUSD, runID)
	if err != nil {
		return fmt.Errorf("agent: end run %q: %w", runID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("agent: end run: unknown run %q", runID)
	}
	return nil
}

// RecentRuns returns the task's episodes, newest first, open runs included.
func (s *SQLiteTaskStore) RecentRuns(ctx context.Context, taskID string, limit int) ([]TaskRun, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = taskResumeRuns
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, started_at, ended_at, outcome, summary, cost_usd
		FROM task_runs WHERE task_id = ?
		ORDER BY started_at DESC, id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("agent: recent runs %q: %w", taskID, err)
	}
	defer rows.Close()

	var out []TaskRun
	for rows.Next() {
		var (
			r     TaskRun
			ended sql.NullTime
		)
		if err := rows.Scan(&r.ID, &r.TaskID, &r.StartedAt, &ended, &r.Outcome, &r.Summary, &r.CostUSD); err != nil {
			return nil, fmt.Errorf("agent: recent runs %q: %w", taskID, err)
		}
		if ended.Valid {
			r.EndedAt = ended.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CloseOpenRuns closes every still-open run of the task with the given
// outcome. Zero rows closed is the normal case, not an error.
func (s *SQLiteTaskStore) CloseOpenRuns(ctx context.Context, taskID, outcome string) error {
	if s == nil || s.db == nil {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE task_runs SET ended_at = ?, outcome = ? WHERE task_id = ? AND ended_at IS NULL`,
		time.Now().UTC(), outcome, taskID); err != nil {
		return fmt.Errorf("agent: close open runs %q: %w", taskID, err)
	}
	return nil
}

// AppendJournal appends one entry. A repeated IdemKey is not an error and not
// an insert: the original entry's seq comes back with duplicate=true, so a
// retried run can tell "already done" from "do it now" without a read first.
func (s *SQLiteTaskStore) AppendJournal(ctx context.Context, e TaskJournalEntry) (int64, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, nil
	}
	if e.RunID == "" || e.Kind == "" {
		return 0, false, fmt.Errorf("agent: append journal: run id and kind are required")
	}
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	// Empty keys become NULL so the partial unique index ignores them —
	// ordinary observations must never collide with each other.
	var idem interface{}
	if e.IdemKey != "" {
		idem = e.IdemKey
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO task_journal (task_id, run_id, kind, payload, idem_key, at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.TaskID, e.RunID, e.Kind, e.Payload, idem, at)
	if err != nil {
		return 0, false, fmt.Errorf("agent: append journal: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		var seq int64
		if err := s.db.QueryRowContext(ctx,
			`SELECT seq FROM task_journal WHERE idem_key = ?`, e.IdemKey).Scan(&seq); err != nil {
			return 0, true, fmt.Errorf("agent: append journal: read duplicate seq: %w", err)
		}
		return seq, true, nil
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("agent: append journal: %w", err)
	}
	return seq, false, nil
}

// Journal returns a run's entries with seq > afterSeq, oldest first.
func (s *SQLiteTaskStore) Journal(ctx context.Context, runID string, afterSeq int64, limit int) ([]TaskJournalEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, task_id, run_id, kind, payload, idem_key, at
		FROM task_journal WHERE run_id = ? AND seq > ?
		ORDER BY seq LIMIT ?`, runID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("agent: journal %q: %w", runID, err)
	}
	defer rows.Close()

	var out []TaskJournalEntry
	for rows.Next() {
		var (
			e    TaskJournalEntry
			idem sql.NullString
		)
		if err := rows.Scan(&e.Seq, &e.TaskID, &e.RunID, &e.Kind, &e.Payload, &idem, &e.At); err != nil {
			return nil, fmt.Errorf("agent: journal %q: %w", runID, err)
		}
		if idem.Valid {
			e.IdemKey = idem.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddLearning records a lesson against the task.
func (s *SQLiteTaskStore) AddLearning(ctx context.Context, taskID, lesson string) error {
	if s == nil || s.db == nil {
		return nil
	}
	if taskID == "" || lesson == "" {
		return fmt.Errorf("agent: add learning: task id and lesson are required")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO task_learnings (task_id, lesson, created_at) VALUES (?, ?, ?)`,
		taskID, lesson, time.Now().UTC()); err != nil {
		return fmt.Errorf("agent: add learning %q: %w", taskID, err)
	}
	return nil
}

// Learnings returns the task's lessons, newest first.
func (s *SQLiteTaskStore) Learnings(ctx context.Context, taskID string, limit int) ([]TaskLearning, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = taskResumeLearnings
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, lesson, created_at FROM task_learnings
		WHERE task_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("agent: learnings %q: %w", taskID, err)
	}
	defer rows.Close()

	var out []TaskLearning
	for rows.Next() {
		var l TaskLearning
		if err := rows.Scan(&l.TaskID, &l.Lesson, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("agent: learnings %q: %w", taskID, err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
