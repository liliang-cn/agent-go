package domain

import "context"

// MemoryReconcileOutcome is what happened to one explicitly saved memory.
type MemoryReconcileOutcome struct {
	// Op is add, update (stored, and TargetID retired) or noop (nothing
	// stored; TargetID already says it).
	Op       string `json:"op"`
	TargetID string `json:"target_id,omitempty"`
	// ID is the stored memory's id; empty for a noop.
	ID string `json:"id,omitempty"`
}

// MemoryReconcilingAdder is an optional MemoryService capability: an explicit
// save that is reconciled against what is already remembered, the way the
// automatic writer's extraction is. Without it an explicit "I moved to
// Beijing" was added beside "I live in Chengdu", and both stayed current.
type MemoryReconcilingAdder interface {
	AddReconciled(ctx context.Context, memory *Memory) (MemoryReconcileOutcome, error)
}
