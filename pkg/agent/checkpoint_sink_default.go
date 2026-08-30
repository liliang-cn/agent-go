// A run that cannot be resumed is a run that has to be redone.
//
// Every terminal state and every Nth round asks the Service for a checkpoint
// sink, and SetCheckpointSink was only ever called by Manager. A Service built
// the ordinary way — agent.New(...).Build(), which is what RunSegments is
// driven with — had no sink, so persistRoundCheckpoint returned at `sink ==
// nil` and wrote nothing. Silently.
//
// Measured on a successful one-hour, eleven-segment run: zero rows in
// task_checkpoints. AutonomyProfile.CheckpointEveryRounds was set to 1 the
// whole time and did nothing. Had that run died at minute fifty-five there
// would have been nothing to resume from.
//
// The Service already owns the database the checkpoints belong in. It should
// use it.
package agent

import (
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// serviceCheckpointSink writes checkpoints into the Service's own store.
//
// It seeds each task's sequence counter from disk the first time it writes for
// that task, so a process that picks a task back up continues its numbering
// instead of restarting at 1 and interleaving with what is already there.
type serviceCheckpointSink struct {
	writer *checkpointWriter

	mu     sync.Mutex
	seeded map[string]bool
}

func newServiceCheckpointSink(store *Store) *serviceCheckpointSink {
	if store == nil {
		return nil
	}
	return &serviceCheckpointSink{
		writer: newCheckpointWriter(store),
		seeded: make(map[string]bool),
	}
}

func (s *serviceCheckpointSink) WriteCheckpoint(taskID string, _ CheckpointReason, round int, sessionID, agentName, finalText, afterTool string, messages []domain.Message, workspace []byte) error {
	if s == nil || s.writer == nil || taskID == "" {
		return nil
	}
	s.mu.Lock()
	first := !s.seeded[taskID]
	s.seeded[taskID] = true
	s.mu.Unlock()
	if first {
		s.writer.SeedFromStore(taskID)
	}

	return s.writer.Write(&TaskCheckpoint{
		TaskID:    taskID,
		Round:     round,
		AfterTool: afterTool,
		SessionID: sessionID,
		AgentName: agentName,
		Messages:  cloneMessagesForCheckpoint(messages),
		FinalText: finalText,
		Workspace: workspace,
	})
}
