package agent

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Session represents an agent conversation session with UUID v4
// Each conversation has its own unique UUID
type Session struct {
	mu        sync.RWMutex
	ID        string                 `json:"id"`
	AgentID   string                 `json:"agent_id"`
	Messages  []domain.Message       `json:"messages"`
	Summary   string                 `json:"summary,omitempty"` // Compacted key points
	Context   map[string]interface{} `json:"context,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`

	// loadedCount is how many messages this copy was loaded with, so a save can
	// merge only what this run appended. See store_session_merge.go.
	loadedCount int
}

// NewSession creates a new session with a UUID v4 ID
func NewSession(agentID string) *Session {
	now := time.Now()
	return &Session{
		ID:        uuid.New().String(), // UUID v4
		AgentID:   agentID,
		Messages:  []domain.Message{},
		Context:   make(map[string]interface{}),
		Metadata:  make(map[string]interface{}),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// NewSessionWithID creates a new session with a specific ID (for resuming)
func NewSessionWithID(id, agentID string) *Session {
	now := time.Now()
	return &Session{
		ID:        id,
		AgentID:   agentID,
		Messages:  []domain.Message{},
		Context:   make(map[string]interface{}),
		Metadata:  make(map[string]interface{}),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// GetID returns the session ID (UUID v4)
func (s *Session) GetID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ID
}

// AddMessage adds a message to the session
func (s *Session) AddMessage(msg domain.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
	s.UpdatedAt = time.Now()
}

// GetMessages returns all messages in the session
func (s *Session) GetMessages() []domain.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	messages := make([]domain.Message, len(s.Messages))
	copy(messages, s.Messages)
	return messages
}

// SetSummary sets the session summary
func (s *Session) SetSummary(summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Summary = summary
	s.UpdatedAt = time.Now()
}

// GetSummary returns the session summary
func (s *Session) GetSummary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Summary
}

// GetLastNMessages returns the last n messages
func (s *Session) GetLastNMessages(n int) []domain.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.Messages) <= n {
		messages := make([]domain.Message, len(s.Messages))
		copy(messages, s.Messages)
		return messages
	}
	messages := make([]domain.Message, n)
	copy(messages, s.Messages[len(s.Messages)-n:])
	return messages
}

// Clear clears all messages from the session
func (s *Session) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = []domain.Message{}
	s.UpdatedAt = time.Now()
}

// SetContext sets a context value
func (s *Session) SetContext(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Context == nil {
		s.Context = make(map[string]interface{})
	}
	s.Context[key] = value
	s.UpdatedAt = time.Now()
}

// GetContext gets a context value
func (s *Session) GetContext(key string) (interface{}, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Context == nil {
		return nil, false
	}
	val, ok := s.Context[key]
	return val, ok
}
