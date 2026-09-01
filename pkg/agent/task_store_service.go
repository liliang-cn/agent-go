// Where task memory meets the runtime.
//
// The store itself (task_store.go) is deliberately inert: tables and a
// contract. This file is the wiring that makes it matter — the read at run
// start that puts the hand-off in front of the model, and the writes at
// segment boundaries that give the next process something to read. Every
// call here is best-effort with a bounded timeout, for the same reason the
// plan store's are: a run that cannot reach its memory should still run,
// and a cancelled context must not be able to stop the record of the
// cancellation from being written.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
)

// taskStoreTimeout bounds a single task-memory read or write, mirroring
// planStoreTimeout: these are small rows, and a store that hangs longer than
// this is not going to answer.
const taskStoreTimeout = 5 * time.Second

// taskRunSummaryMaxChars caps a segment's write-time summary. The summary is
// the hand-off's bandwidth, not its archive — the full text lives in the
// segment's checkpoint.
const taskRunSummaryMaxChars = 400

// SetTaskStore attaches (or replaces) the service's task memory.
func (s *Service) SetTaskStore(ts TaskStore) {
	if s == nil {
		return
	}
	s.taskStoreMu.Lock()
	defer s.taskStoreMu.Unlock()
	s.taskStore = ts
}

// TaskStore returns the attached task memory, or nil.
func (s *Service) TaskStore() TaskStore {
	if s == nil {
		return nil
	}
	s.taskStoreMu.RLock()
	defer s.taskStoreMu.RUnlock()
	return s.taskStore
}

// taskResumeForRun renders what the store remembers about this task, or ""
// when there is no store, no task id, or nothing worth saying. Called once
// per run from startRun, like the plan summary and for the same
// cache-stability reason.
func (s *Service) taskResumeForRun(ctx context.Context, taskID string) string {
	ts := s.TaskStore()
	if ts == nil || taskID == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, taskStoreTimeout)
	defer cancel()
	return TaskResumeContext(ctx, ts, taskID)
}

// taskMemoryBeginTask records that the task is running, creating its row on
// first sight and — the part that matters — preserving the resume brief an
// earlier process left when the task is being picked back up.
func (s *Service) taskMemoryBeginTask(taskID, goal string) {
	ts := s.TaskStore()
	if ts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskStoreTimeout)
	defer cancel()

	brief := ""
	if prev, err := ts.LoadTask(ctx, taskID); err != nil {
		agentgolog.WithModule("agent.taskstore").Warn("begin task: load", "task", taskID, "error", err)
	} else if prev != nil {
		brief = prev.ResumeBrief
	}
	if err := ts.SaveTask(ctx, TaskState{
		ID: taskID, Goal: goal, Status: TaskStatusRunning, ResumeBrief: brief,
	}); err != nil {
		agentgolog.WithModule("agent.taskstore").Warn("begin task", "task", taskID, "error", err)
	}
	// A run still open now was abandoned by a dead process. Closing it here —
	// and only here — is what lets every later reader treat "open" as "in
	// flight" instead of guessing which open runs are ghosts.
	if err := ts.CloseOpenRuns(ctx, taskID, TaskRunOutcomeInterrupted); err != nil {
		agentgolog.WithModule("agent.taskstore").Warn("close open runs", "task", taskID, "error", err)
	}
}

// taskMemoryBeginRun opens an episode for one segment, returning "" when
// there is no store or the write failed — callers pass that back to
// taskMemoryEndRun, which treats it as "nothing to close".
func (s *Service) taskMemoryBeginRun(taskID string) string {
	ts := s.TaskStore()
	if ts == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskStoreTimeout)
	defer cancel()
	id, err := ts.BeginRun(ctx, TaskRun{TaskID: taskID})
	if err != nil {
		agentgolog.WithModule("agent.taskstore").Warn("begin run", "task", taskID, "error", err)
		return ""
	}
	return id
}

// taskMemoryEndRun closes a segment's episode with its outcome and write-time
// summary, and appends one journal entry so the task's history is greppable
// without the event stream.
func (s *Service) taskMemoryEndRun(runID, taskID string, seg SegmentOutcome, result *ExecutionResult) {
	ts := s.TaskStore()
	if ts == nil || runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskStoreTimeout)
	defer cancel()

	outcome := TaskRunOutcomeFailed
	cost := 0.0
	switch {
	case result != nil && result.Cancelled:
		outcome = TaskRunOutcomeCancelled
	case result != nil && result.Blocked:
		outcome = TaskRunOutcomeBlocked
	case result != nil && result.Success:
		outcome = TaskRunOutcomeSuccess
	}
	if result != nil {
		cost = result.EstimatedCostUSD
	}

	summary := seg.Text
	if seg.Error != "" {
		summary = "error: " + seg.Error
	}
	summary = truncateRunes(strings.TrimSpace(summary), taskRunSummaryMaxChars)

	if err := ts.EndRun(ctx, runID, outcome, summary, cost); err != nil {
		agentgolog.WithModule("agent.taskstore").Warn("end run", "run", runID, "error", err)
	}

	payload, err := json.Marshal(map[string]any{
		"segment":     seg.Index,
		"session_id":  seg.SessionID,
		"stop_reason": string(seg.StopReason),
		"rounds":      seg.Rounds,
		"productive":  seg.Productive,
		"duration_ms": seg.Duration.Milliseconds(),
	})
	if err != nil {
		return
	}
	if _, _, err := ts.AppendJournal(ctx, TaskJournalEntry{
		TaskID: taskID, RunID: runID, Kind: "segment", Payload: string(payload),
	}); err != nil {
		agentgolog.WithModule("agent.taskstore").Warn("journal segment", "run", runID, "error", err)
	}
}

// taskMemoryFinish writes the task's final status and its resume brief — the
// one deterministic paragraph the next process reads first. No model is
// consulted: the brief is assembled from what the supervisor already knows,
// because a hidden LLM call inside a bookkeeping write is a cost and a
// failure mode nobody asked for.
func (s *Service) taskMemoryFinish(taskID, goal, planKey string, out *LongRunResult) {
	ts := s.TaskStore()
	if ts == nil || out == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskStoreTimeout)
	defer cancel()

	var b strings.Builder
	if out.Done() {
		fmt.Fprintf(&b, "Finished after %d segment(s)", len(out.Segments))
	} else {
		fmt.Fprintf(&b, "Stopped (%s) after %d segment(s)", out.Stop, len(out.Segments))
	}
	if out.TotalCostUSD > 0 {
		fmt.Fprintf(&b, ", $%.2f spent", out.TotalCostUSD)
	}
	if done, total := s.planProgress(planKey); total > 0 {
		fmt.Fprintf(&b, ". Plan: %d of %d steps done", done, total)
	}
	if text := strings.TrimSpace(out.Text); text != "" {
		b.WriteString(". Last segment said: " + truncateRunes(text, taskRunSummaryMaxChars))
	}

	if err := ts.SaveTask(ctx, TaskState{
		ID: taskID, Goal: goal,
		Status:      taskStatusForStop(out.Stop),
		ResumeBrief: b.String(),
	}); err != nil {
		agentgolog.WithModule("agent.taskstore").Warn("finish task", "task", taskID, "error", err)
	}
}

// taskStatusForStop maps why the supervisor stopped onto the task's status.
// Budget and time exhaustion go back to pending on purpose: the work remains
// and the task is exactly as runnable as it was — that is what "resume" means.
func taskStatusForStop(stop LongRunStop) string {
	switch stop {
	case LongRunStopFinished:
		return TaskStatusCompleted
	case LongRunStopBlocked:
		return TaskStatusBlocked
	case LongRunStopCancelled:
		return TaskStatusCancelled
	case LongRunStopFailing:
		return TaskStatusFailed
	default:
		return TaskStatusPending
	}
}

// planProgress counts the stored plan's steps: how many are done, how many
// exist. (0, 0) when there is no plan.
func (s *Service) planProgress(key string) (done, total int) {
	if s == nil || key == "" {
		return 0, 0
	}
	for _, item := range s.scratchpadStore().get(key) {
		total++
		if item.Done {
			done++
		}
	}
	return done, total
}

// truncateRunes cuts s to at most max bytes without splitting a rune.
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
