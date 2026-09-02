package agent

import (
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Swapping the memory backend on a live service.
//
// Which backend a service uses was decided once, at construction, and there
// was no way to change it afterwards: the field was written by the builder
// and read from nineteen places, with no setter and no lock. A host that
// wanted to move a user from file memory to a shared CortexDB had to build a
// second Service and throw the first away, taking the conversation, the
// scratchpad and the in-flight runs with it.
//
// This is the same shape as SetPlanStore, with the one difference that
// matters: a memory service owns a goroutine. Its durable writer is holding
// a queue of extractions that have not been written yet, so dropping the
// pointer does not free it — it strands them, silently, which is the failure
// this repository keeps finding and keeps refusing to ship.

// memory returns the current memory service, or nil when the service has
// none. Every read goes through here because the backend can be replaced
// while runs are in flight.
func (s *Service) memory() domain.MemoryService {
	if s == nil {
		return nil
	}
	s.memoryMu.RLock()
	defer s.memoryMu.RUnlock()
	return s.memoryService
}

// SetMemoryService replaces the memory backend and returns the one it
// replaced, already drained and closed.
//
// The outgoing service is closed on purpose, and it is the part to
// understand before calling this. A memory service holds a background writer
// and a queue of extractions still to be persisted; closing it drains that
// queue, so the memories a run just decided to keep are written before the
// backend goes away. Not closing it would leak the goroutine and lose those
// writes without a word — and a silent loss is worse than a loud failure,
// which is what a caller still using the old handle elsewhere will now get.
//
// Pass nil to turn memory off. Retrieval and auto-store both become no-ops;
// the run works, it simply stops remembering.
//
// Swap when the service is idle. A run already in flight reads the backend
// at each point it needs one, so one that is mid-turn can retrieve from the
// old backend and store into the new. That is rarely what anyone wants:
// check ActiveRuns() first, or swap between turns.
func (s *Service) SetMemoryService(next domain.MemoryService) domain.MemoryService {
	if s == nil {
		return nil
	}
	s.memoryMu.Lock()
	previous := s.memoryService
	s.memoryService = next
	s.memoryMu.Unlock()

	if previous == nil || previous == next {
		return previous
	}
	// Drain before letting go. The writer is a goroutine holding work.
	if closer, ok := previous.(interface{ Close() error }); ok && closer != nil {
		_ = closer.Close()
	}
	return previous
}

// MemoryEnabled reports whether this service currently has a memory backend.
func (s *Service) MemoryEnabled() bool { return s.memory() != nil }
