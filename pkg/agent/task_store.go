// Task memory: what a long task remembers about itself.
//
// A long task already leaves three kinds of state behind, and none of them
// answers "what happened":
//
//   - TaskQueue rows say what should run next and how each attempt ended — a
//     verdict, not an account.
//   - task_checkpoints hold raw message history — enough to replay a run,
//     far too much to hand the next one.
//   - plan_items carry the plan and its per-step notes — what remains to be
//     done, not what doing it taught.
//
// TaskStore is the episodic layer between them. One row per task holding a
// resume brief; one row per run holding a write-time summary; an append-only
// journal for audit and idempotency; and the lessons a run leaves behind.
//
// The design rule, learned from the plan store: durability is the cheap half,
// and the read path is the feature. A store full of history that nobody
// renders into the next run's context is a store that merely made the loss
// permanent. So the brief and the run summaries are written at write time,
// small on purpose, and TaskResumeContext renders them into a few hundred
// tokens a resumed run can actually be given — the raw journal is for grep
// and replay, not for the model.
//
// Like PlanStore and RunMemory, the interface is deliberately tiny and
// deliberately not CortexDB. There is no vector search here: "have I done
// something like this before" is RunMemory's question, and Learnings exist
// precisely to be promoted there by whoever owns that wiring.
package agent

import (
	"context"
	"strings"
	"time"

	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
)

// TaskStatusBlocked marks a task waiting on something only the outside world
// can provide. The other statuses live in task_queue.go and apply here too.
const TaskStatusBlocked = "blocked"

// TaskState is the mutable projection of a task: where it stands now.
// Everything else in the store is append-only history underneath it.
type TaskState struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	// Goal is the original ask, and is never rewritten — when a task drifts,
	// this is what it is measured against. SaveTask does not update it.
	Goal   string `json:"goal"`
	Status string `json:"status"` // TaskStatus* constants
	// ResumeBrief is the most important field in the store: a few hundred
	// tokens of "done, blocked-on, next" written by the run that knows,
	// for the run that will not. A task with an empty brief resumes the way
	// a plan nobody summarised resumes — by re-walking covered ground.
	ResumeBrief string    `json:"resume_brief,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TaskRun is one execution episode against a task. A long task is many runs;
// this is the row that survives each one.
type TaskRun struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	StartedAt time.Time `json:"started_at"`
	// EndedAt is zero while the run is open. A run found open by a later run
	// died with its process; mark it TaskRunOutcomeInterrupted rather than
	// leaving it to look forever in-flight.
	EndedAt time.Time `json:"ended_at,omitempty"`
	Outcome string    `json:"outcome,omitempty"` // TaskRunOutcome* constants
	// Summary is written when the run ends, by the run itself — what it did,
	// what it concluded, where it stopped. Like PlanItem.Note, it is the whole
	// bandwidth of the hand-off: a lazy summary is paid for by the next run.
	Summary string  `json:"summary,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// Run outcomes. "The run ended" and "the task finished" are different
// statements — a task's status lives on TaskState.
const (
	TaskRunOutcomeSuccess     = "success"
	TaskRunOutcomeFailed      = "failed"
	TaskRunOutcomeBlocked     = "blocked"
	TaskRunOutcomeCancelled   = "cancelled"
	TaskRunOutcomeInterrupted = "interrupted"
)

// TaskJournalEntry is one appended fact: a decision taken, a tool called, an
// observation made. The journal is the store's source of truth for what
// happened, and the only part meant to be read by tools (grep, replay,
// post-mortems) rather than models.
type TaskJournalEntry struct {
	Seq    int64  `json:"seq"`
	TaskID string `json:"task_id"`
	RunID  string `json:"run_id"`
	Kind   string `json:"kind"` // e.g. "decision", "tool_call", "observation", "error", "note"
	// Payload is JSON or plain text; the store does not look inside it.
	Payload string `json:"payload"`
	// IdemKey, when set, makes the append idempotent: a second append with
	// the same key is reported as a duplicate instead of inserted. This is
	// the crash-retry guard — record a side-effecting step under a stable
	// key before performing it, and a retried run discovers the step already
	// happened instead of doing it twice.
	IdemKey string    `json:"idem_key,omitempty"`
	At      time.Time `json:"at"`
}

// TaskLearning is a lesson a run distilled — an approach ruled out, a
// constraint discovered. Kept per task so a retry reads them first, and
// shaped to be promoted to RunMemory/CortexDB by whoever owns that wiring.
type TaskLearning struct {
	TaskID    string    `json:"task_id"`
	Lesson    string    `json:"lesson"`
	CreatedAt time.Time `json:"created_at"`
}

// TaskStore persists a task's episodic memory: state, runs, journal, lessons.
//
// Implementations should treat LoadTask on an unknown id as (nil, nil) — a
// task never seen is not an error condition — and must never rewrite
// TaskState.Goal on an existing row.
type TaskStore interface {
	// SaveTask inserts or updates the task's projection. On an existing row
	// everything but Goal and CreatedAt is replaced.
	SaveTask(ctx context.Context, t TaskState) error
	// LoadTask returns the task, or (nil, nil) when the id is unknown.
	LoadTask(ctx context.Context, id string) (*TaskState, error)
	// SaveResumeBrief replaces the task's hand-off brief. Unknown task is an
	// error: a brief with no task is a write to nowhere.
	SaveResumeBrief(ctx context.Context, taskID, brief string) error

	// BeginRun opens an episode. A blank run ID is filled in; the id actually
	// used is returned either way.
	BeginRun(ctx context.Context, run TaskRun) (string, error)
	// EndRun closes an episode with its outcome, write-time summary and cost.
	EndRun(ctx context.Context, runID, outcome, summary string, costUSD float64) error
	// RecentRuns returns the task's episodes, newest first, open runs
	// included (zero EndedAt).
	RecentRuns(ctx context.Context, taskID string, limit int) ([]TaskRun, error)
	// CloseOpenRuns closes every run of the task still open, with the given
	// outcome. Called when a task is picked up: a run left open by then was
	// abandoned by a dead process, and closing it there is what lets every
	// later reader treat "open" as "in flight" without guessing.
	CloseOpenRuns(ctx context.Context, taskID, outcome string) error

	// AppendJournal appends one entry and returns its sequence number. When
	// the entry carries an IdemKey already present, nothing is written and
	// the existing entry's seq is returned with duplicate=true.
	AppendJournal(ctx context.Context, e TaskJournalEntry) (seq int64, duplicate bool, err error)
	// Journal returns entries for a run with seq > afterSeq, oldest first.
	Journal(ctx context.Context, runID string, afterSeq int64, limit int) ([]TaskJournalEntry, error)

	// AddLearning records a lesson against the task.
	AddLearning(ctx context.Context, taskID, lesson string) error
	// Learnings returns the task's lessons, newest first.
	Learnings(ctx context.Context, taskID string, limit int) ([]TaskLearning, error)
}

const (
	// taskResumeMaxChars caps the rendered resume context (~2k tokens at 4
	// chars/token) so an over-written brief cannot eat the window it exists
	// to protect.
	taskResumeMaxChars = 8000
	// taskResumeRuns / taskResumeLearnings bound how much history the render
	// reaches for. The brief is the hand-off; these are corroboration.
	taskResumeRuns      = 3
	taskResumeLearnings = 5
)

// TaskResumeContext renders what a resumed run should be told about its task,
// or "" when there is nothing worth saying.
//
// This is the half that makes the store useful rather than merely durable —
// the same lesson PlanSummary carries. Callers inject the result (a system
// prompt section, a first message); where context belongs is the caller's
// decision, not this package's. Errors are logged and swallowed: a run that
// cannot read its history should still run.
func TaskResumeContext(ctx context.Context, s TaskStore, taskID string) string {
	if s == nil || taskID == "" {
		return ""
	}
	log := agentgolog.WithModule("agent.taskstore")

	t, err := s.LoadTask(ctx, taskID)
	if err != nil {
		log.Warn("resume context: load task", "task", taskID, "error", err)
		return ""
	}
	if t == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("Task in progress (status: " + t.Status + ").\n")
	b.WriteString("Goal: " + t.Goal + "\n")
	if t.ResumeBrief != "" {
		b.WriteString("Where it stands: " + t.ResumeBrief + "\n")
	}

	hasHistory := false
	if runs, err := s.RecentRuns(ctx, taskID, taskResumeRuns); err != nil {
		log.Warn("resume context: recent runs", "task", taskID, "error", err)
	} else if len(runs) > 0 {
		var lines []string
		for _, r := range runs {
			if r.EndedAt.IsZero() {
				// An open run is in flight — most often the very run this
				// context is being rendered for. Genuinely abandoned runs
				// were closed as interrupted when the task was picked up
				// (CloseOpenRuns), so skipping open ones here loses nothing.
				continue
			}
			line := "- [" + r.Outcome + "]"
			if r.Summary != "" {
				line += " " + r.Summary
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			hasHistory = true
			b.WriteString("Previous runs, newest first:\n" + strings.Join(lines, "\n") + "\n")
		}
	}

	if lessons, err := s.Learnings(ctx, taskID, taskResumeLearnings); err != nil {
		log.Warn("resume context: learnings", "task", taskID, "error", err)
	} else if len(lessons) > 0 {
		hasHistory = true
		b.WriteString("Lessons from earlier attempts:\n")
		for _, l := range lessons {
			b.WriteString("- " + l.Lesson + "\n")
		}
	}

	// A task with no brief and no history reduces to restating the goal,
	// which the caller already has. Only say something when there is
	// something to hand over.
	if t.ResumeBrief == "" && !hasHistory {
		return ""
	}

	b.WriteString("Carry on from where the brief and the latest run summary leave off. " +
		"Do not redo what previous runs concluded; treat their lessons as constraints.")

	return truncateRunes(b.String(), taskResumeMaxChars)
}
