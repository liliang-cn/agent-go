package agent

import (
	"context"
	"log/slog"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Snapshots taken while the run is still going.
//
// CheckpointReasonRoundEnd and CheckpointReasonAfterTool were declared from
// the start and never written by a live run: the only three call sites were
// complete, blocked and cancelled. So a task in flight did not exist on disk.
// For a run measured in minutes that is merely a missed feature. For one
// measured in hours it is the whole problem — the thing worth resuming is
// precisely the run that has not finished yet, and it was the one thing never
// written down.
//
// A round-end snapshot is cheap in the way the terminal one is not: it skips
// the workspace archive. Tarring the sandbox on every round of a long run
// would cost more than the loop itself, and a resumed run can re-derive files
// from the plan and the history far more easily than it can re-derive the
// conversation.

// defaultCheckpointEveryRounds writes a snapshot at the end of every round.
// The writer prunes to MaxCheckpointsPerTask, so the cost is bounded storage
// and one small write per round — against losing an hour of work to a crash.
const defaultCheckpointEveryRounds = 1

// resolveCheckpointEveryRounds reports how often to snapshot. Non-positive on
// the service means "not set", the same convention as the round budget.
func (r *Runtime) resolveCheckpointEveryRounds() int {
	if r != nil && r.svc != nil && r.svc.checkpointEveryRounds > 0 {
		return r.svc.checkpointEveryRounds
	}
	return defaultCheckpointEveryRounds
}

// persistRoundCheckpoint snapshots the history at a round boundary when the
// interval says to. Best-effort throughout: a snapshot is insurance, and
// insurance that can fail the run it insures is worse than none.
func (r *Runtime) persistRoundCheckpoint(round int, messages []domain.Message) {
	if r == nil || r.svc == nil || round <= 0 {
		return
	}
	every := r.resolveCheckpointEveryRounds()
	if every <= 0 || round%every != 0 {
		return
	}
	taskID := currentTaskID(r.session)
	if taskID == "" {
		return
	}
	sink := r.svc.CheckpointSink()
	if sink == nil {
		return
	}
	// Observer seam, so a round-end snapshot is as visible to tracing as a
	// terminal one. Fired before the write for the same reason: what the
	// runtime decided is observable even where nothing persists it.
	sessionID := r.sessionID()
	agentName := r.currentAgentName()
	r.svc.emitObserver(func(o Observer) {
		o.OnCheckpoint(context.Background(), CheckpointInfo{
			TaskID:    taskID,
			RunID:     r.runID(),
			SessionID: sessionID,
			AgentName: agentName,
			Reason:    string(CheckpointReasonRoundEnd),
			Round:     round,
			Messages:  len(messages),
		})
	})
	if err := sink.WriteCheckpoint(taskID, CheckpointReasonRoundEnd, round,
		sessionID, agentName, "", string(CheckpointReasonRoundEnd), messages, nil); err != nil {
		r.log().Debug("failed to write round checkpoint",
			slog.String("task_id", taskID), slog.String("error", err.Error()))
	}
}
