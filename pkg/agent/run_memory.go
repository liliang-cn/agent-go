package agent

import (
	"context"
	"log/slog"
	"time"
)

// RunMemory hooks a run's start and end for an external long-term memory
// system (a knowledge graph, a vector store, a file). It is deliberately
// small: recall produces extra context for the run, capture receives what the
// run concluded. Everything else — extraction rules, storage, schemas — lives
// behind the implementation (see pkg/cortexbridge for the CortexDB one).
//
// Both calls are best-effort from the runtime's point of view: recall runs
// under a short timeout and a failure only logs (a run must never be blocked
// by its memory), and capture runs asynchronously after the run completes.
type RunMemory interface {
	// RecallForRun returns context relevant to the goal, to be injected into
	// the run's system prompt under a "Recalled context" heading. Return ""
	// for "nothing relevant" — the run proceeds without a memory section.
	RecallForRun(ctx context.Context, goal string) (string, error)

	// CaptureRun receives a successfully completed run's goal and final text,
	// so the implementation can persist whatever it considers durable (e.g.
	// decision lines, entities, a summary).
	CaptureRun(ctx context.Context, goal, finalText string) error
}

const (
	// runMemoryRecallTimeout bounds how long a run start may wait on recall.
	runMemoryRecallTimeout = 5 * time.Second
	// runMemoryCaptureTimeout bounds the post-run capture goroutine.
	runMemoryCaptureTimeout = 15 * time.Second
	// runMemoryMaxRecallChars caps the injected section so a runaway recall
	// cannot eat the whole context window (~2.5k tokens at 4 chars/token).
	runMemoryMaxRecallChars = 10_000
)

// recallRunMemory fetches recall context for a goal, bounded and log-only on
// failure. Returns "" when there is no run memory or nothing relevant.
func (s *Service) recallRunMemory(ctx context.Context, goal string) string {
	if s == nil || s.runMemory == nil {
		return ""
	}
	rctx, cancel := context.WithTimeout(ctx, runMemoryRecallTimeout)
	defer cancel()
	text, err := s.runMemory.RecallForRun(rctx, goal)
	if err != nil {
		slog.Warn("run-memory recall failed; continuing without it",
			"module", "agent.runmemory", "error", err)
		return ""
	}
	if len(text) > runMemoryMaxRecallChars {
		text = text[:runMemoryMaxRecallChars] + "\n[recalled context truncated]"
	}
	return text
}

// captureRunMemory persists a completed run into run memory, asynchronously —
// the caller gets their result back immediately, and a capture failure only
// logs.
func (s *Service) captureRunMemory(goal, finalText string) {
	if s == nil || s.runMemory == nil || finalText == "" {
		return
	}
	rm := s.runMemory
	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), runMemoryCaptureTimeout)
		defer cancel()
		if err := rm.CaptureRun(cctx, goal, finalText); err != nil {
			slog.Warn("run-memory capture failed",
				"module", "agent.runmemory", "error", err)
		}
	}()
}
