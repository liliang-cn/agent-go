package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// TaskCheckpoint is a lightweight snapshot of an in-flight task taken at
// a deterministic boundary (end-of-round, after a tool call). Restoring a
// checkpoint reconstructs the message history needed to resume the run
// from that point instead of starting over.
//
// Checkpoints intentionally store only the message stream — they do NOT
// snapshot in-process state like discovered tools, MCP cache, or memory
// service connections. The runtime rebuilds those on resume the same way
// it does on a fresh run.
type TaskCheckpoint struct {
	ID        string           `json:"id"`
	TaskID    string           `json:"task_id"`
	Seq       int              `json:"seq"`        // monotonically increasing within a TaskID
	Round     int              `json:"round"`      // runtime loop round at snapshot time
	AfterTool string           `json:"after_tool"` // optional: name of the tool whose result triggered the snapshot
	SessionID string           `json:"session_id"`
	AgentName string           `json:"agent_name"`
	Messages  []domain.Message `json:"messages"`
	FinalText string           `json:"final_text,omitempty"` // populated on terminal-tool snapshots
	// Workspace is an optional gzip-tar archive of the sandbox workspace at
	// snapshot time. Populated only on terminal checkpoints when the service
	// has a sandbox, so a resumed run (or `task artifacts`) can recover the
	// files the agent produced. nil when no sandbox / not a terminal snapshot.
	Workspace []byte    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// CheckpointReason describes why a snapshot was written; used by the
// runtime to decide whether to elide redundant snapshots.
type CheckpointReason string

const (
	CheckpointReasonRoundEnd     CheckpointReason = "round_end"
	CheckpointReasonAfterTool    CheckpointReason = "after_tool"
	CheckpointReasonTaskComplete CheckpointReason = "task_complete"
	CheckpointReasonTaskBlocked  CheckpointReason = "task_blocked"
)

// MaxCheckpointsPerTask caps how many snapshots are kept per task. The
// writer prunes older entries when the count exceeds this cap. Picked
// generously enough that even longish tasks have meaningful resume
// granularity, but bounded so storage doesn't grow unbounded for chatty
// runs.
const MaxCheckpointsPerTask = 32

var errCheckpointMissing = errors.New("checkpoint not found")

// SaveTaskCheckpoint inserts a new checkpoint row. Callers should use
// (TeamManager).WriteCheckpoint instead, which assigns Seq and prunes.
func (s *Store) SaveTaskCheckpoint(cp *TaskCheckpoint) error {
	if s == nil || s.agentGoDB == nil || cp == nil {
		return nil
	}
	if strings.TrimSpace(cp.ID) == "" {
		cp.ID = uuid.NewString()
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	bytes, err := json.Marshal(cp.Messages)
	if err != nil {
		return fmt.Errorf("marshal checkpoint messages: %w", err)
	}
	db := s.agentGoDB.GetDB()
	_, err = db.Exec(`
		INSERT INTO task_checkpoints
		(id, task_id, seq, round, after_tool, session_id, agent_name, messages, final_text, workspace, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, cp.ID, cp.TaskID, cp.Seq, cp.Round, cp.AfterTool, cp.SessionID, cp.AgentName, string(bytes), cp.FinalText, cp.Workspace, cp.CreatedAt)
	return err
}

// LatestTaskCheckpoint returns the most recent checkpoint for a task, or
// errCheckpointMissing if none exist.
func (s *Store) LatestTaskCheckpoint(taskID string) (*TaskCheckpoint, error) {
	cps, err := s.ListTaskCheckpoints(taskID, 1)
	if err != nil {
		return nil, err
	}
	if len(cps) == 0 {
		return nil, errCheckpointMissing
	}
	// ListTaskCheckpoints omits the workspace blob to keep listing light;
	// re-load the full row so resume/artifacts get the snapshot.
	if full, err := s.GetTaskCheckpoint(cps[0].ID); err == nil {
		return full, nil
	}
	return cps[0], nil
}

// GetTaskCheckpoint loads a specific checkpoint by ID.
func (s *Store) GetTaskCheckpoint(id string) (*TaskCheckpoint, error) {
	if s == nil || s.agentGoDB == nil {
		return nil, errCheckpointMissing
	}
	db := s.agentGoDB.GetDB()
	row := db.QueryRow(`
		SELECT id, task_id, seq, round, after_tool, session_id, agent_name, messages, final_text, workspace, created_at
		FROM task_checkpoints WHERE id = ?
	`, id)
	cp := &TaskCheckpoint{}
	var messagesJSON string
	if err := row.Scan(&cp.ID, &cp.TaskID, &cp.Seq, &cp.Round, &cp.AfterTool, &cp.SessionID, &cp.AgentName, &messagesJSON, &cp.FinalText, &cp.Workspace, &cp.CreatedAt); err != nil {
		return nil, errCheckpointMissing
	}
	if err := json.Unmarshal([]byte(messagesJSON), &cp.Messages); err != nil {
		return nil, fmt.Errorf("unmarshal checkpoint messages: %w", err)
	}
	return cp, nil
}

// ListTaskCheckpoints returns checkpoints for a task in descending Seq
// order (newest first). Pass limit=0 for unlimited.
func (s *Store) ListTaskCheckpoints(taskID string, limit int) ([]*TaskCheckpoint, error) {
	if s == nil || s.agentGoDB == nil {
		return nil, nil
	}
	db := s.agentGoDB.GetDB()
	q := `SELECT id, task_id, seq, round, after_tool, session_id, agent_name, messages, final_text, created_at
		  FROM task_checkpoints WHERE task_id = ? ORDER BY seq DESC`
	args := []any{taskID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query task_checkpoints: %w", err)
	}
	defer rows.Close()
	var out []*TaskCheckpoint
	for rows.Next() {
		cp := &TaskCheckpoint{}
		var messagesJSON string
		if err := rows.Scan(&cp.ID, &cp.TaskID, &cp.Seq, &cp.Round, &cp.AfterTool, &cp.SessionID, &cp.AgentName, &messagesJSON, &cp.FinalText, &cp.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(messagesJSON), &cp.Messages); err != nil {
			return nil, fmt.Errorf("unmarshal checkpoint messages: %w", err)
		}
		out = append(out, cp)
	}
	return out, nil
}

// PruneTaskCheckpoints keeps only the most recent maxKeep checkpoints
// for the given task. Returns the number of rows deleted.
func (s *Store) PruneTaskCheckpoints(taskID string, maxKeep int) (int, error) {
	if s == nil || s.agentGoDB == nil || maxKeep <= 0 {
		return 0, nil
	}
	db := s.agentGoDB.GetDB()
	res, err := db.Exec(`
		DELETE FROM task_checkpoints
		WHERE task_id = ? AND id NOT IN (
			SELECT id FROM task_checkpoints WHERE task_id = ? ORDER BY seq DESC LIMIT ?
		)
	`, taskID, taskID, maxKeep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteTaskCheckpoints removes all checkpoints for a task. Useful when
// a task is deleted or fully completed and resume is no longer needed.
func (s *Store) DeleteTaskCheckpoints(taskID string) error {
	if s == nil || s.agentGoDB == nil {
		return nil
	}
	db := s.agentGoDB.GetDB()
	_, err := db.Exec(`DELETE FROM task_checkpoints WHERE task_id = ?`, taskID)
	return err
}

// --- Manager-level helpers ---------------------------------------------

// checkpointWriter serializes per-task seq assignment + write + prune so
// concurrent runtime callers don't race on Seq.
type checkpointWriter struct {
	mu      sync.Mutex
	store   *Store
	lastSeq map[string]int // taskID -> last assigned seq
}

func newCheckpointWriter(store *Store) *checkpointWriter {
	return &checkpointWriter{
		store:   store,
		lastSeq: make(map[string]int),
	}
}

// Write persists a checkpoint for the given task, assigning the next Seq
// and pruning older entries beyond MaxCheckpointsPerTask. Errors are
// logged by callers; the runtime never blocks on checkpoint failure.
func (w *checkpointWriter) Write(cp *TaskCheckpoint) error {
	if w == nil || w.store == nil || cp == nil {
		return nil
	}
	w.mu.Lock()
	w.lastSeq[cp.TaskID]++
	cp.Seq = w.lastSeq[cp.TaskID]
	w.mu.Unlock()

	if err := w.store.SaveTaskCheckpoint(cp); err != nil {
		return err
	}
	if _, err := w.store.PruneTaskCheckpoints(cp.TaskID, MaxCheckpointsPerTask); err != nil {
		return err
	}
	return nil
}

// SeedFromStore primes the in-memory seq counter for a task by querying
// the latest persisted seq. Used on TeamManager startup so resumed
// processes don't reset Seq numbering.
func (w *checkpointWriter) SeedFromStore(taskID string) {
	if w == nil || w.store == nil {
		return
	}
	cp, err := w.store.LatestTaskCheckpoint(taskID)
	if err != nil || cp == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if cp.Seq > w.lastSeq[taskID] {
		w.lastSeq[taskID] = cp.Seq
	}
}

// CheckpointSink is the runtime-facing surface for writing checkpoints.
// Implementations are typically backed by a TeamManager so seq
// assignment and pruning stay coherent across resumes. Services without
// a sink simply skip persistence.
type CheckpointSink interface {
	WriteCheckpoint(taskID string, reason CheckpointReason, round int, sessionID, agentName, finalText, afterTool string, messages []domain.Message, workspace []byte) error
}

// SetCheckpointSink wires a sink into the service. TeamManager calls
// this after constructing each per-agent service.
func (s *Service) SetCheckpointSink(sink CheckpointSink) {
	if s == nil {
		return
	}
	s.checkpointSink = sink
}

// CheckpointSink returns the configured sink (may be nil).
func (s *Service) CheckpointSink() CheckpointSink {
	if s == nil {
		return nil
	}
	return s.checkpointSink
}
