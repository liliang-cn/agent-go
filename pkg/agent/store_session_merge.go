package agent

import (
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Concurrent runs on one session.
//
// GetSession builds a fresh Session from the database on every call, so two runs
// on the same id hold two independent copies. Each appends its own turns and
// calls SaveSession, which writes its whole message list — so the second save
// overwrites the first, and the first run's question and both answers vanish.
// Measured before this existed: two overlapping asks left a history of
// "user: B, user: B" and no assistant turns at all.
//
// The fix is to make saving additive. A session remembers how many messages it
// was loaded with; on save, only the turns appended since then are merged into
// whatever the database holds now. Saves for one id are serialised, so two runs
// finishing together cannot interleave a read-modify-write.
//
// This keeps the copy-per-run model (no cache to invalidate, no lifetime to
// manage) and makes the durable history the union of both runs, which is what a
// conversation that took two questions at once should look like.

// sessionSaveLocks serialises saves per session id.
var sessionSaveLocks sync.Map // session id -> *sync.Mutex

func lockSessionSave(id string) func() {
	value, _ := sessionSaveLocks.LoadOrStore(id, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// baseline returns how many messages the session was loaded with.
func (s *Session) baseline() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadedCount
}

// setBaseline records the loaded message count, so a later save knows which
// turns this run added.
func (s *Session) setBaseline(n int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.loadedCount = n
	s.mu.Unlock()
}

// appendedSince returns the messages added after the baseline.
func (s *Session) appendedSince() []domain.Message {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loadedCount >= len(s.Messages) {
		return nil
	}
	out := make([]domain.Message, len(s.Messages)-s.loadedCount)
	copy(out, s.Messages[s.loadedCount:])
	return out
}

// mergeMessagesForSave computes what should be persisted: the stored history,
// plus this run's new turns, minus any that are already there.
//
// Dedup is by (role, content, tool_call_id) because a retried save must not
// double-write, while two different runs legitimately produce distinct turns.
func mergeMessagesForSave(stored, appended []domain.Message) []domain.Message {
	if len(appended) == 0 {
		return stored
	}
	seen := make(map[string]bool, len(stored))
	key := func(m domain.Message) string {
		return m.Role + "\x00" + m.Content + "\x00" + m.ToolCallID
	}
	for _, m := range stored {
		seen[key(m)] = true
	}
	out := make([]domain.Message, 0, len(stored)+len(appended))
	out = append(out, stored...)
	for _, m := range appended {
		k := key(m)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, m)
	}
	return out
}
