package agent

import (
	"strings"
	"sync"

	taskpkg "github.com/liliang-cn/agent-go/v3/pkg/task"
)

// Concurrent writes to one task row.
//
// A single run has at least two goroutines writing the same task record:
//
//	runtime  — completeRun/blockRun -> persistMessages/persistFinalAnswer
//	           -> SaveSession -> SaveTaskFramesFromSession   (writes Frames)
//	observer — observeRunStream, on every event
//	           -> persistRunTaskEvent / persistRunTaskState   (writes Events, Status, Output)
//
// Each did GetTask -> mutate -> SaveTask with no serialisation, and SaveTask
// replaces the whole row. So two interleaved read-modify-write cycles lose one
// side's field entirely: the observer reads the row before the runtime has
// written Frames, then writes its own copy back on top, and the frames are gone
// for good. It reproduced roughly twice in forty runs, and the loss is
// permanent — a 15s poll never sees them, because nothing writes them again.
//
// Sessions already had this fixed (sessionSaveLocks); tasks did not. Every
// read-modify-write on a task now goes through updateTask, which holds a
// per-task-id lock for the whole cycle.
var taskSaveLocks sync.Map // task id -> *sync.Mutex

func lockTaskSave(id string) func() {
	value, _ := taskSaveLocks.LoadOrStore(id, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// updateTask runs one atomic read-modify-write against a task row.
//
// mutate receives the stored task, or nil when the row does not exist yet; it
// returns the task to persist, or nil to skip the write. Holding the lock
// across the read and the write is the whole point — callers must not do their
// own GetTask/SaveTask pair.
func (s *Store) updateTask(taskID string, mutate func(existing *UnifiedTask) *UnifiedTask) error {
	if s == nil || s.agentGoDB == nil {
		return nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}

	unlock := lockTaskSave(taskID)
	defer unlock()

	existing, err := s.agentGoDB.GetTask(taskID)
	if err != nil {
		existing = nil
	}
	updated := mutate(existing)
	if updated == nil {
		return nil
	}
	return s.agentGoDB.SaveTask(updated)
}

// UpdateTask is the exported form, for callers outside this package that need
// to amend a task without racing the runtime's own writes.
func (s *Store) UpdateTask(taskID string, mutate func(existing *taskpkg.Task) *taskpkg.Task) error {
	return s.updateTask(taskID, mutate)
}
