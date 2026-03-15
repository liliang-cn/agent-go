package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Common errors
var (
	ErrConversationNotFound = errors.New("conversation not found")
)

// ConversationMessage represents a single message in a conversation
type ConversationMessage struct {
	Role      string      `json:"role"`
	Content   string      `json:"content"`
	Sources   []RAGSource `json:"sources,omitempty"`
	Thinking  string      `json:"thinking,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

// RAGSource represents a source document for RAG
type RAGSource struct {
	ID      string  `json:"id"`
	Source  string  `json:"source"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// Conversation represents a complete conversation
type Conversation struct {
	ID        string                 `json:"id"`
	Title     string                 `json:"title"`
	Messages  []ConversationMessage  `json:"messages"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt int64                  `json:"created_at"`
	UpdatedAt int64                  `json:"updated_at"`
}

// ConversationStore handles conversation persistence
type ConversationStore struct {
	db *sql.DB
}

// NewConversationStore creates a new conversation store
func NewConversationStore(db *sql.DB) (*ConversationStore, error) {
	store := &ConversationStore{db: db}
	if err := store.createTables(); err != nil {
		return nil, err
	}
	return store, nil
}

// createTables creates necessary tables for conversations
func (s *ConversationStore) createTables() error {
	query := `
	CREATE TABLE IF NOT EXISTS conversations (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		messages TEXT NOT NULL,
		metadata TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	
	CREATE INDEX IF NOT EXISTS idx_conversations_created_at ON conversations(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON conversations(updated_at DESC);
	`
	_, err := s.db.Exec(query)
	return err
}

// SaveConversation saves or updates a conversation
func (s *ConversationStore) SaveConversation(conv *Conversation) error {
	if conv.ID == "" {
		conv.ID = uuid.New().String()
	}

	now := time.Now().Unix()
	if conv.CreatedAt == 0 {
		conv.CreatedAt = now
	}
	conv.UpdatedAt = now

	// Generate title from first message if not set
	if conv.Title == "" && len(conv.Messages) > 0 {
		conv.Title = truncateString(conv.Messages[0].Content, 100)
	}

	messagesJSON, err := json.Marshal(conv.Messages)
	if err != nil {
		return err
	}

	var metadataJSON []byte
	if conv.Metadata != nil {
		metadataJSON, err = json.Marshal(conv.Metadata)
		if err != nil {
			return err
		}
	}

	query := `
	INSERT INTO conversations (id, title, messages, metadata, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		title = excluded.title,
		messages = excluded.messages,
		metadata = excluded.metadata,
		updated_at = excluded.updated_at
	`

	_, err = s.db.Exec(query, conv.ID, conv.Title, messagesJSON, metadataJSON, conv.CreatedAt, conv.UpdatedAt)
	return err
}

// GetConversation retrieves a conversation by ID
func (s *ConversationStore) GetConversation(id string) (*Conversation, error) {
	query := `
	SELECT id, title, messages, metadata, created_at, updated_at
	FROM conversations
	WHERE id = ?
	`

	var conv Conversation
	var messagesJSON, metadataJSON sql.NullString

	err := s.db.QueryRow(query, id).Scan(
		&conv.ID,
		&conv.Title,
		&messagesJSON,
		&metadataJSON,
		&conv.CreatedAt,
		&conv.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	if messagesJSON.Valid {
		if err := json.Unmarshal([]byte(messagesJSON.String), &conv.Messages); err != nil {
			return nil, err
		}
	}

	if metadataJSON.Valid {
		if err := json.Unmarshal([]byte(metadataJSON.String), &conv.Metadata); err != nil {
			return nil, err
		}
	}

	return &conv, nil
}

// ListConversations retrieves conversations with pagination
func (s *ConversationStore) ListConversations(limit, offset int) ([]*Conversation, int, error) {
	// Get total count
	var total int
	err := s.db.QueryRow("SELECT COUNT(*) FROM conversations").Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Get conversations
	query := `
	SELECT id, title, messages, metadata, created_at, updated_at
	FROM conversations
	ORDER BY updated_at DESC
	LIMIT ? OFFSET ?
	`

	rows, err := s.db.Query(query, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var conversations []*Conversation
	for rows.Next() {
		var conv Conversation
		var messagesJSON, metadataJSON sql.NullString

		err := rows.Scan(
			&conv.ID,
			&conv.Title,
			&messagesJSON,
			&metadataJSON,
			&conv.CreatedAt,
			&conv.UpdatedAt,
		)
		if err != nil {
			return nil, 0, err
		}

		if messagesJSON.Valid {
			if err := json.Unmarshal([]byte(messagesJSON.String), &conv.Messages); err != nil {
				return nil, 0, err
			}
		}

		if metadataJSON.Valid {
			if err := json.Unmarshal([]byte(metadataJSON.String), &conv.Metadata); err != nil {
				return nil, 0, err
			}
		}

		conversations = append(conversations, &conv)
	}

	return conversations, total, nil
}

// DeleteConversation deletes a conversation by ID
func (s *ConversationStore) DeleteConversation(id string) error {
	_, err := s.db.Exec("DELETE FROM conversations WHERE id = ?", id)
	return err
}

// SearchConversations searches conversations by content
func (s *ConversationStore) SearchConversations(query string, limit, offset int) ([]*Conversation, int, error) {
	// Get total count
	var total int
	searchQuery := "%" + query + "%"
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM conversations WHERE title LIKE ? OR messages LIKE ?",
		searchQuery, searchQuery,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Get conversations
	sqlQuery := `
	SELECT id, title, messages, metadata, created_at, updated_at
	FROM conversations
	WHERE title LIKE ? OR messages LIKE ?
	ORDER BY updated_at DESC
	LIMIT ? OFFSET ?
	`

	rows, err := s.db.Query(sqlQuery, searchQuery, searchQuery, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var conversations []*Conversation
	for rows.Next() {
		var conv Conversation
		var messagesJSON, metadataJSON sql.NullString

		err := rows.Scan(
			&conv.ID,
			&conv.Title,
			&messagesJSON,
			&metadataJSON,
			&conv.CreatedAt,
			&conv.UpdatedAt,
		)
		if err != nil {
			return nil, 0, err
		}

		if messagesJSON.Valid {
			if err := json.Unmarshal([]byte(messagesJSON.String), &conv.Messages); err != nil {
				return nil, 0, err
			}
		}

		if metadataJSON.Valid {
			if err := json.Unmarshal([]byte(metadataJSON.String), &conv.Metadata); err != nil {
				return nil, 0, err
			}
		}

		conversations = append(conversations, &conv)
	}

	return conversations, total, nil
}
