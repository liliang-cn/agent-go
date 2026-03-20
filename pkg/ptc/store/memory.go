package store

import (
	"context"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

// MemoryStore is an in-memory execution history store
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]*ptc.ExecutionHistory
	order   []string // maintains insertion order
	maxSize int
}

// NewMemoryStore creates a new in-memory store
func NewMemoryStore(maxSize int) *MemoryStore {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &MemoryStore{
		entries: make(map[string]*ptc.ExecutionHistory),
		order:   make([]string, 0),
		maxSize: maxSize,
	}
}

// Save saves an execution history entry
func (s *MemoryStore) Save(ctx context.Context, history *ptc.ExecutionHistory) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove oldest if at capacity
	if len(s.order) >= s.maxSize {
		oldest := s.order[0]
		delete(s.entries, oldest)
		s.order = s.order[1:]
	}

	s.entries[history.ID] = history
	s.order = append(s.order, history.ID)
	return nil
}

// Get retrieves an execution history by ID
func (s *MemoryStore) Get(ctx context.Context, id string) (*ptc.ExecutionHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history, ok := s.entries[id]
	if !ok {
		return nil, nil
	}
	return history, nil
}

// List lists execution history entries
func (s *MemoryStore) List(ctx context.Context, limit int) ([]*ptc.ExecutionHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.order) {
		limit = len(s.order)
	}

	// Return most recent entries
	result := make([]*ptc.ExecutionHistory, 0, limit)
	start := len(s.order) - limit
	if start < 0 {
		start = 0
	}

	for i := len(s.order) - 1; i >= start && len(result) < limit; i-- {
		if entry, ok := s.entries[s.order[i]]; ok {
			result = append(result, entry)
		}
	}

	return result, nil
}

// Delete removes entries older than the specified time
func (s *MemoryStore) Delete(ctx context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	newOrder := make([]string, 0)
	for _, id := range s.order {
		if entry, ok := s.entries[id]; ok {
			if entry.ExecutedAt.Before(before) {
				delete(s.entries, id)
			} else {
				newOrder = append(newOrder, id)
			}
		}
	}
	s.order = newOrder
	return nil
}

// Clear clears all entries
func (s *MemoryStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = make(map[string]*ptc.ExecutionHistory)
	s.order = make([]string, 0)
	return nil
}

// Size returns the number of entries
func (s *MemoryStore) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}
