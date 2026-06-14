package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

// WriteCheckpoint persists a snapshot of the given task and prunes older
// entries beyond the per-task cap. Errors are returned but never block
// the runtime; callers typically log and continue.
func (m *TeamManager) WriteCheckpoint(taskID string, reason CheckpointReason, round int, sessionID, agentName, finalText, afterTool string, messages []domain.Message, workspace []byte) error {
	if m == nil || m.checkpointWriter == nil {
		return nil
	}
	if strings.TrimSpace(taskID) == "" {
		return errors.New("WriteCheckpoint: taskID required")
	}
	cp := &TaskCheckpoint{
		TaskID:    taskID,
		Round:     round,
		AfterTool: afterTool,
		SessionID: sessionID,
		AgentName: agentName,
		Messages:  cloneMessagesForCheckpoint(messages),
		FinalText: finalText,
		Workspace: workspace,
		CreatedAt: time.Now(),
	}
	return m.checkpointWriter.Write(cp)
}

// ListCheckpoints returns up to limit checkpoints for taskID, newest
// first. Pass limit=0 for unlimited.
func (m *TeamManager) ListCheckpoints(taskID string, limit int) ([]*TaskCheckpoint, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	return m.store.ListTaskCheckpoints(taskID, limit)
}

// LatestCheckpoint returns the most recent checkpoint for a task, or
// errCheckpointMissing if none exist.
func (m *TeamManager) LatestCheckpoint(taskID string) (*TaskCheckpoint, error) {
	if m == nil || m.store == nil {
		return nil, errCheckpointMissing
	}
	return m.store.LatestTaskCheckpoint(taskID)
}

// TaskArtifacts returns the files captured in a task's most recent workspace
// snapshot, plus the raw gzip-tar archive (for extraction). Returns an empty
// list (no error) when the task has no checkpoint or its checkpoints carry no
// workspace snapshot (e.g. the run had no sandbox configured).
func (m *TeamManager) TaskArtifacts(taskID string) ([]ArchiveEntry, []byte, error) {
	if m == nil || m.store == nil {
		return nil, nil, nil
	}
	cp, err := m.store.LatestTaskCheckpoint(taskID)
	if err != nil {
		if errors.Is(err, errCheckpointMissing) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if cp == nil || len(cp.Workspace) == 0 {
		return nil, nil, nil
	}
	entries, err := ListArchive(cp.Workspace)
	if err != nil {
		return nil, nil, err
	}
	return entries, cp.Workspace, nil
}

// CheckpointResumeOptions configures Tasks().ResumeFromCheckpoint.
type CheckpointResumeOptions struct {
	// CheckpointID, if set, resumes from that specific checkpoint
	// instead of the latest one.
	CheckpointID string
	// FollowUp is an optional user instruction appended to the resumed
	// history. Useful for "and now also do X" style continuation.
	FollowUp string
	// RestoreWorkspace, when non-nil, is a sandbox the checkpoint's workspace
	// snapshot is restored into before the resumed run starts — so the agent
	// picks up with the files it had produced. Left nil by the default team
	// resume path (which has no sandbox); library users / the claw CLI pass
	// the sandbox they rebuilt for the resumed run.
	RestoreWorkspace sandbox.Sandbox
}

// ResumeFromCheckpoint rebuilds an in-flight task from its latest (or
// named) checkpoint and re-runs it on the configured agent. Distinct
// from the existing Resume, which resumes a yielded task with new input
// — this picks up after a crash/cancellation by replaying the saved
// message history through the runtime.
//
// MVP semantics:
//   - the agent's session history is replaced with the checkpoint's
//     messages plus the optional FollowUp
//   - the task event subscription stays attached to the original taskID,
//     so callers can reuse SubscribeTask
//   - new events are emitted with the same TaskID
//   - the task is marked completed/blocked/failed by the resumed run
//     just like a fresh submit
//
// Resume is intentionally narrow in this first iteration: it does NOT
// resurrect discovered tools, in-flight memory writes, or PTC sandbox
// state. The runtime rebuilds those from scratch on resume.
func (s *TaskService) ResumeFromCheckpoint(ctx context.Context, taskID string, opts CheckpointResumeOptions) (*taskpkg.Task, error) {
	m := s.manager
	if m == nil {
		return nil, errors.New("Resume: nil team manager")
	}

	var (
		cp  *TaskCheckpoint
		err error
	)
	if strings.TrimSpace(opts.CheckpointID) != "" {
		cp, err = m.store.GetTaskCheckpoint(opts.CheckpointID)
	} else {
		cp, err = m.store.LatestTaskCheckpoint(taskID)
	}
	if err != nil {
		return nil, fmt.Errorf("load checkpoint for task %s: %w", taskID, err)
	}
	if cp == nil {
		return nil, errCheckpointMissing
	}

	// Restore the workspace snapshot into the caller-provided sandbox so the
	// resumed run sees the files the task had produced. Best-effort.
	if opts.RestoreWorkspace != nil && len(cp.Workspace) > 0 {
		if err := restoreWorkspaceBytes(ctx, opts.RestoreWorkspace, cp.Workspace); err != nil {
			return nil, fmt.Errorf("restore workspace for task %s: %w", taskID, err)
		}
	}

	// Look up the original task to recover agent + session metadata.
	// First try the in-memory async map (process-local fast path), then
	// fall back to the persisted UnifiedTask. CLI invocations of
	// `task replay` will always go through the store path because the
	// asyncTasks map is rebuilt per process.
	agentName := strings.TrimSpace(cp.AgentName)
	sessionID := strings.TrimSpace(cp.SessionID)
	if agentName == "" || sessionID == "" {
		if original, err := m.GetTask(taskID); err == nil && original != nil {
			if agentName == "" {
				agentName = original.AgentName
			}
			if sessionID == "" {
				sessionID = original.SessionID
			}
		} else if unified, err := m.store.GetTask(taskID); err == nil && unified != nil {
			if agentName == "" {
				agentName = unified.AgentName
			}
			if sessionID == "" {
				sessionID = unified.SessionID
			}
			// Hydrate an in-memory async record so the runtime path
			// can update status / emit events / etc. without crashing
			// on a missing map entry.
			m.upsertAsyncTask(&AsyncTask{
				ID:        taskID,
				TaskID:    taskID,
				SessionID: sessionID,
				Kind:      AsyncTaskKindAgent,
				Status:    AsyncTaskStatusRunning,
				AgentName: agentName,
				Prompt:    unified.Input,
				CreatedAt: unified.CreatedAt,
			})
		} else {
			return nil, fmt.Errorf("resume: original task %s not found in memory or store", taskID)
		}
	}
	if agentName == "" {
		return nil, fmt.Errorf("resume: cannot determine agent for task %s", taskID)
	}

	// Reset task state to running so subscribers see a fresh start.
	now := time.Now()
	m.updateAsyncTask(taskID, func(existing *AsyncTask) {
		existing.Status = AsyncTaskStatusRunning
		existing.Error = ""
		existing.FinishedAt = nil
		existing.StartedAt = &now
	})
	m.emitTaskEvent(taskID, &TaskEvent{
		TaskID:    taskID,
		SessionID: sessionID,
		AgentName: agentName,
		Type:      TaskEventTypeStarted,
		Message:   fmt.Sprintf("%s resumed from checkpoint seq %d.", agentName, cp.Seq),
		Timestamp: now,
	}, false)

	// Stitch in any follow-up the caller wants appended.
	resumeMessages := cloneMessagesForCheckpoint(cp.Messages)
	if strings.TrimSpace(opts.FollowUp) != "" {
		resumeMessages = append(resumeMessages, domain.Message{
			Role:    "user",
			Content: opts.FollowUp,
			TaskID:  taskID,
		})
	}

	// Write a fresh "resume" checkpoint so the new run's lineage is
	// preserved even if it crashes again.
	_ = m.WriteCheckpoint(taskID, CheckpointReasonRoundEnd, cp.Round, sessionID, agentName, "", "resume", resumeMessages, nil)

	// Run the resumed task. We reuse the legacy async-task path so the
	// existing subscriber/event-forwarding plumbing keeps working.
	go func() {
		runCtx := context.WithoutCancel(ctx)
		events, runErr := m.ChatWithMemberStreamWithOptions(runCtx, sessionID, agentName, "", WithTaskID(taskID), WithResumeMessages(resumeMessages))
		if runErr != nil {
			m.failAsyncTask(taskID, agentName, runErr)
			return
		}
		finalText, blocked, runErr := m.forwardRuntimeEvents(taskID, events)
		if runErr != nil {
			m.failAsyncTask(taskID, agentName, runErr)
			return
		}
		if blocked {
			m.blockAsyncTask(taskID, finalText, agentName)
			return
		}
		m.completeAsyncTask(taskID, finalText, agentName)
	}()

	return m.GetUnifiedTask(taskID)
}

func cloneMessagesForCheckpoint(in []domain.Message) []domain.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.Message, len(in))
	copy(out, in)
	return out
}
