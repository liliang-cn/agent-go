package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	_ "github.com/mattn/go-sqlite3"
)

// HistoryStore manages execution history persistence
type HistoryStore struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
}

// HistoryRecord represents a single execution history record
type HistoryRecord struct {
	ID         string                 `json:"id"`
	SessionID  string                 `json:"session_id"`
	AgentID    string                 `json:"agent_id"`
	Goal       string                 `json:"goal"`
	Role       string                 `json:"role"` // "user", "assistant", "tool"
	Content    string                 `json:"content"`
	ToolName   string                 `json:"tool_name"` // For tool messages
	ToolCallID string                 `json:"tool_call_id"`
	ToolArgs   map[string]interface{} `json:"tool_args"`
	ToolResult interface{}            `json:"tool_result"`
	Round      int                    `json:"round"`
	CreatedAt  time.Time              `json:"created_at"`
	DurationMs int64                  `json:"duration_ms"`
	Success    bool                   `json:"success"`
	Error      string                 `json:"error,omitempty"`
}

// HistorySummary provides a summary of an execution session
type HistorySummary struct {
	SessionID   string    `json:"session_id"`
	AgentID     string    `json:"agent_id"`
	Goal        string    `json:"goal"`
	Turns       int       `json:"turns"`
	ToolCalls   int       `json:"tool_calls"`
	Success     bool      `json:"success"`
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at"`
	DurationMs  int64     `json:"duration_ms"`
}

// NewHistoryStore creates a new history store
func NewHistoryStore(dbPath string) (*HistoryStore, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create history directory: %w", err)
	}

	// Open database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open history database: %w", err)
	}

	store := &HistoryStore{
		db:   db,
		path: dbPath,
	}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize history schema: %w", err)
	}

	return store, nil
}

// initSchema creates the necessary tables
func (s *HistoryStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS execution_history (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		agent_id TEXT,
		goal TEXT,
		role TEXT NOT NULL,
		content TEXT,
		tool_name TEXT,
		tool_call_id TEXT,
		tool_args TEXT,
		tool_result TEXT,
		round INTEGER DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		duration_ms INTEGER DEFAULT 0,
		success INTEGER DEFAULT 1,
		error TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_history_session ON execution_history(session_id);
	CREATE INDEX IF NOT EXISTS idx_history_created ON execution_history(created_at);
	CREATE INDEX IF NOT EXISTS idx_history_agent ON execution_history(agent_id);

	CREATE TABLE IF NOT EXISTS execution_sessions (
		session_id TEXT PRIMARY KEY,
		agent_id TEXT,
		goal TEXT,
		turns INTEGER DEFAULT 0,
		tool_calls INTEGER DEFAULT 0,
		success INTEGER DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		completed_at TIMESTAMP,
		duration_ms INTEGER DEFAULT 0
	);
	`

	_, err := s.db.Exec(schema)
	return err
}

// Record adds a new history record
func (s *HistoryStore) Record(ctx context.Context, record *HistoryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var toolArgs, toolResult []byte
	var err error

	if record.ToolArgs != nil {
		toolArgs, err = json.Marshal(record.ToolArgs)
		if err != nil {
			toolArgs = []byte("{}")
		}
	}
	if record.ToolResult != nil {
		toolResult, err = json.Marshal(record.ToolResult)
		if err != nil {
			toolResult = []byte("null")
		}
	}

	success := 1
	if !record.Success {
		success = 0
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO execution_history
		(id, session_id, agent_id, goal, role, content, tool_name, tool_call_id, tool_args, tool_result, round, created_at, duration_ms, success, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.ID, record.SessionID, record.AgentID, record.Goal, record.Role, record.Content,
		record.ToolName, record.ToolCallID, string(toolArgs), string(toolResult),
		record.Round, record.CreatedAt, record.DurationMs, success, record.Error)

	return err
}

// RecordMessage records a conversation message
func (s *HistoryStore) RecordMessage(ctx context.Context, sessionID, agentID, goal string, msg domain.Message, round int) error {
	record := &HistoryRecord{
		ID:        generateID(),
		SessionID: sessionID,
		AgentID:   agentID,
		Goal:      goal,
		Role:      msg.Role,
		Content:   fmt.Sprintf("%v", msg.Content),
		Round:     round,
		CreatedAt: time.Now(),
		Success:   true,
	}

	// Handle tool calls
	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			record.ToolName = tc.Function.Name
			record.ToolCallID = tc.ID
			record.ToolArgs = tc.Function.Arguments
		}
	}

	return s.Record(ctx, record)
}

// RecordToolResult records a tool execution result
func (s *HistoryStore) RecordToolResult(ctx context.Context, sessionID, agentID, goal, toolName, toolCallID string, args map[string]interface{}, result interface{}, success bool, errMsg string, round int) error {
	record := &HistoryRecord{
		ID:         generateID(),
		SessionID:  sessionID,
		AgentID:    agentID,
		Goal:       goal,
		Role:       "tool_result",
		ToolName:   toolName,
		ToolCallID: toolCallID,
		ToolArgs:   args,
		ToolResult: result,
		Round:      round,
		CreatedAt:  time.Now(),
		Success:    success,
		Error:      errMsg,
	}

	return s.Record(ctx, record)
}

// CompleteSession marks a session as completed
func (s *HistoryStore) CompleteSession(ctx context.Context, sessionID, agentID, goal string, turns, toolCalls int, success bool, durationMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	successInt := 0
	if success {
		successInt = 1
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO execution_sessions
		(session_id, agent_id, goal, turns, tool_calls, success, created_at, completed_at, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)
	`, sessionID, agentID, goal, turns, toolCalls, successInt, durationMs)

	return err
}

// GetSessionHistory retrieves all records for a session
func (s *HistoryStore) GetSessionHistory(ctx context.Context, sessionID string) ([]*HistoryRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, agent_id, goal, role, content, tool_name, tool_call_id, tool_args, tool_result, round, created_at, duration_ms, success, error
		FROM execution_history
		WHERE session_id = ?
		ORDER BY created_at ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*HistoryRecord
	for rows.Next() {
		record := &HistoryRecord{}
		var toolArgs, toolResult sql.NullString
		var success int

		err := rows.Scan(
			&record.ID, &record.SessionID, &record.AgentID, &record.Goal,
			&record.Role, &record.Content, &record.ToolName, &record.ToolCallID,
			&toolArgs, &toolResult, &record.Round, &record.CreatedAt,
			&record.DurationMs, &success, &record.Error,
		)
		if err != nil {
			return nil, err
		}

		record.Success = success == 1

		if toolArgs.Valid && toolArgs.String != "" {
			json.Unmarshal([]byte(toolArgs.String), &record.ToolArgs)
		}
		if toolResult.Valid && toolResult.String != "" {
			json.Unmarshal([]byte(toolResult.String), &record.ToolResult)
		}

		records = append(records, record)
	}

	return records, nil
}

// ListSessions lists recent sessions
func (s *HistoryStore) ListSessions(ctx context.Context, limit int) ([]*HistorySummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, agent_id, goal, turns, tool_calls, success, created_at, completed_at, duration_ms
		FROM execution_sessions
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []*HistorySummary
	for rows.Next() {
		summary := &HistorySummary{}
		var success int

		err := rows.Scan(
			&summary.SessionID, &summary.AgentID, &summary.Goal,
			&summary.Turns, &summary.ToolCalls, &success,
			&summary.CreatedAt, &summary.CompletedAt, &summary.DurationMs,
		)
		if err != nil {
			return nil, err
		}

		summary.Success = success == 1
		summaries = append(summaries, summary)
	}

	return summaries, nil
}

// Close closes the database connection
func (s *HistoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// generateID generates a unique ID for history records
func generateID() string {
	return fmt.Sprintf("hist_%d", time.Now().UnixNano())
}
