package agent

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
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

// AddHandoffMessage adds a handoff message to the session
// Converts HandoffMessage to domain.Message
func (s *Session) AddHandoffMessage(msg HandoffMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Convert tool calls
	toolCalls := make([]domain.ToolCall, len(msg.ToolCalls))
	for i, tc := range msg.ToolCalls {
		toolCalls[i] = domain.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: domain.FunctionCall{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			},
		}
	}

	s.Messages = append(s.Messages, domain.Message{
		Role:      msg.Role,
		Content:   msg.Content,
		ToolCalls: toolCalls,
	})
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

// ToMessages converts session messages to domain.Message format for LLM
func (s *Session) ToMessages() []domain.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.GetMessages()
}

// ToJSON serializes the session to JSON
func (s *Session) ToJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.Marshal(s)
}

// SessionFromJSON deserializes a session from JSON
func SessionFromJSON(data []byte) (*Session, error) {
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SessionManager manages multiple sessions
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewSessionManager creates a new session manager
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

// CreateSession creates a new session
func (sm *SessionManager) CreateSession(agentID string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	session := NewSession(agentID)
	sm.sessions[session.ID] = session
	return session
}

// GetSession retrieves a session by ID
func (sm *SessionManager) GetSession(id string) (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	session, ok := sm.sessions[id]
	return session, ok
}

// DeleteSession removes a session
func (sm *SessionManager) DeleteSession(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, id)
}

// ListSessions returns all session IDs
func (sm *SessionManager) ListSessions() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	ids := make([]string, 0, len(sm.sessions))
	for id := range sm.sessions {
		ids = append(ids, id)
	}
	return ids
}
