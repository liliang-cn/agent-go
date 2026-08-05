package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/resource"
	taskpkg "github.com/liliang-cn/agent-go/v3/pkg/task"
	_ "modernc.org/sqlite"
)

// AgentGoDB provides unified storage for application data
type AgentGoDB struct {
	mu     sync.RWMutex
	db     *sql.DB
	dbPath string
}

// NewAgentGoDB creates a new unified database for AgentGo
func NewAgentGoDB(dbPath string) (*AgentGoDB, error) {
	// Create the parent directory so callers can pass e.g. "./data/agentgo.db"
	// without pre-creating ./data (avoids a silent CANTOPEN at open time).
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create database directory %q: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := configureSQLiteDB(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to configure sqlite: %w", err)
	}

	store := &AgentGoDB{
		db:     db,
		dbPath: dbPath,
	}

	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// configureSQLiteDB sets up SQLite connection parameters
func configureSQLiteDB(db *sql.DB) error {
	_, err := db.Exec("PRAGMA busy_timeout=5000")
	if err != nil {
		return fmt.Errorf("failed to set busy timeout: %w", err)
	}
	if err := execSQLiteWithRetry(db, "PRAGMA journal_mode=WAL", 5, 100*time.Millisecond); err != nil {
		return fmt.Errorf("failed to set WAL mode: %w", err)
	}
	_, err = db.Exec("PRAGMA synchronous=NORMAL")
	if err != nil {
		return fmt.Errorf("failed to set synchronous: %w", err)
	}
	return nil
}

func execSQLiteWithRetry(db *sql.DB, query string, attempts int, delay time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if _, err := db.Exec(query); err == nil {
			return nil
		} else {
			lastErr = err
			if !isSQLiteLockedError(err) || i == attempts-1 {
				return err
			}
		}
		time.Sleep(delay)
	}
	return lastErr
}

func isSQLiteLockedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrConnDone) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "database table is locked")
}

func isSQLiteDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column name")
}

// initSchema creates all necessary tables
func (s *AgentGoDB) initSchema() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Config table for key-value configuration
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create config table: %w", err)
	}

	// Agents table for agent model configurations
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY,
			a2a_id TEXT UNIQUE,
			team_id TEXT,
			name TEXT UNIQUE NOT NULL,
			kind TEXT DEFAULT 'orchestrator',
			description TEXT NOT NULL,
			instructions TEXT NOT NULL,
			model TEXT,
			preferred_provider TEXT,
			preferred_model TEXT,
			required_llm_capability INTEGER DEFAULT 0,
			mcp_tools TEXT,
			skills TEXT,
			enable_rag BOOLEAN DEFAULT 0,
			enable_memory BOOLEAN DEFAULT 0,
			memory_store_type TEXT DEFAULT '',
			enable_ptc BOOLEAN DEFAULT 1,
			enable_mcp BOOLEAN DEFAULT 0,
			enable_a2a BOOLEAN DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create agents table: %w", err)
	}
	_, _ = s.db.Exec(`ALTER TABLE agents ADD COLUMN a2a_id TEXT`)
	_, _ = s.db.Exec(`ALTER TABLE agents ADD COLUMN enable_a2a BOOLEAN DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE agents ADD COLUMN memory_store_type TEXT DEFAULT ''`)

	if err := s.backfillA2AIDsLocked(); err != nil {
		return fmt.Errorf("failed to backfill a2a ids: %w", err)
	}

	// Chat sessions table - unified for all types (llm, rag, agent)
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_sessions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL DEFAULT 'llm',
			title TEXT,
			messages TEXT NOT NULL DEFAULT '[]',
			summary TEXT,
			context TEXT,
			metadata TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create chat_sessions table: %w", err)
	}

	// Migrate chat_sessions if needed (add summary and context columns)
	_, _ = s.db.Exec(`ALTER TABLE chat_sessions ADD COLUMN summary TEXT`)
	_, _ = s.db.Exec(`ALTER TABLE chat_sessions ADD COLUMN context TEXT`)

	// Create indexes for chat_sessions
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_chat_sessions_type ON chat_sessions(type)`)
	if err != nil {
		return fmt.Errorf("failed to create chat_sessions type index: %w", err)
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_chat_sessions_updated ON chat_sessions(updated_at DESC)`)
	if err != nil {
		return fmt.Errorf("failed to create chat_sessions updated index: %w", err)
	}

	// Agent plans table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_plans (
			id TEXT PRIMARY KEY,
			goal TEXT NOT NULL,
			session_id TEXT NOT NULL,
			steps TEXT NOT NULL,
			status TEXT NOT NULL,
			reasoning TEXT,
			error TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create agent_plans table: %w", err)
	}

	// Task checkpoints table stores periodic snapshots of an in-flight
	// task so a crashed/cancelled run can be resumed from the last
	// successful state instead of starting over. Each checkpoint captures
	// the task's message history at a deterministic boundary (round end,
	// after a successful tool call, after a terminal sentinel). Snapshots
	// are kept lightweight: messages are JSON-serialized; older
	// checkpoints for the same task are pruned by the writer.
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS task_checkpoints (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			round INTEGER NOT NULL,
			after_tool TEXT,
			session_id TEXT,
			agent_name TEXT,
			messages TEXT NOT NULL,
			final_text TEXT,
			workspace BLOB,
			created_at DATETIME NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create task_checkpoints table: %w", err)
	}
	// Migration for DBs created before the workspace-snapshot column existed.
	_, _ = s.db.Exec(`ALTER TABLE task_checkpoints ADD COLUMN workspace BLOB`)
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_checkpoints_task_seq ON task_checkpoints(task_id, seq)`); err != nil {
		return fmt.Errorf("failed to create task_checkpoints index: %w", err)
	}

	// Agent sessions table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_sessions (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			messages TEXT NOT NULL,
			summary TEXT,
			context TEXT,
			metadata TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create agent_sessions table: %w", err)
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			status TEXT NOT NULL,
			session_id TEXT,
			runtime_session_id TEXT,
			parent_task_id TEXT,
			continuation_id TEXT,
			queue_class TEXT,
			awaiting TEXT,
			team_id TEXT,
			team_name TEXT,
			agent_name TEXT,
			agent_names TEXT,
			input TEXT,
			output TEXT,
			error TEXT,
			frames TEXT,
			events TEXT,
			stats TEXT,
			source TEXT,
			source_id TEXT,
			created_at DATETIME NOT NULL,
			started_at DATETIME,
			finished_at DATETIME
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create tasks table: %w", err)
	}
	if err := s.ensureColumnExistsLocked("tasks", "stats", "TEXT"); err != nil {
		return fmt.Errorf("failed to add stats column to tasks: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_session_id ON tasks(session_id)`); err != nil {
		return fmt.Errorf("failed to create tasks session index: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)`); err != nil {
		return fmt.Errorf("failed to create tasks status index: %w", err)
	}
	if err := s.ensureColumnExistsLocked("tasks", "continuation_id", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumnExistsLocked("tasks", "queue_class", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumnExistsLocked("tasks", "awaiting", "TEXT"); err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS resources (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			name TEXT NOT NULL,
			provider TEXT,
			description TEXT,
			execution TEXT,
			metadata TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create resources table: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_resources_kind ON resources(kind)`); err != nil {
		return fmt.Errorf("failed to create resources kind index: %w", err)
	}

	// Chat session messages table (for granular message history)
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			metadata TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (session_id) REFERENCES chat_sessions(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create chat_messages table: %w", err)
	}

	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_chat_messages_session_id ON chat_messages(session_id)`)
	if err != nil {
		return fmt.Errorf("failed to create chat_messages session_id index: %w", err)
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS llm_providers (
			name TEXT PRIMARY KEY,
			base_url TEXT NOT NULL,
			key TEXT NOT NULL DEFAULT '',
			model_name TEXT NOT NULL,
			max_concurrency INTEGER NOT NULL DEFAULT 5,
			capability INTEGER NOT NULL DEFAULT 3,
			enabled BOOLEAN NOT NULL DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create llm_providers table: %w", err)
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS llm_provider_models (
			provider_name TEXT NOT NULL,
			model_name TEXT NOT NULL,
			is_default BOOLEAN NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (provider_name, model_name)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create llm_provider_models table: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT OR IGNORE INTO llm_provider_models (provider_name, model_name, is_default)
		SELECT name, model_name, 1
		FROM llm_providers
		WHERE TRIM(model_name) <> ''
	`)
	if err != nil {
		return fmt.Errorf("failed to backfill llm_provider_models table: %w", err)
	}

	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_llm_provider_models_provider_name ON llm_provider_models(provider_name)`)
	if err != nil {
		return fmt.Errorf("failed to create llm_provider_models provider_name index: %w", err)
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS embedding_providers (
			name TEXT PRIMARY KEY,
			base_url TEXT NOT NULL,
			key TEXT NOT NULL DEFAULT '',
			model_name TEXT NOT NULL,
			max_concurrency INTEGER NOT NULL DEFAULT 5,
			capability INTEGER NOT NULL DEFAULT 3,
			enabled BOOLEAN NOT NULL DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create embedding_providers table: %w", err)
	}

	return nil
}

func (s *AgentGoDB) tableColumnSetLocked(tableName string) (map[string]bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", tableName, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notNull    int
			defaultV   sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultV, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan %s column info: %w", tableName, err)
		}
		columns[strings.TrimSpace(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s column info: %w", tableName, err)
	}
	return columns, nil
}

func (s *AgentGoDB) ensureColumnExistsLocked(tableName, columnName, columnType string) error {
	columns, err := s.tableColumnSetLocked(tableName)
	if err != nil {
		return err
	}
	if columns[columnName] {
		return nil
	}
	if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, columnType)); err != nil && !isSQLiteDuplicateColumnError(err) {
		return fmt.Errorf("add %s to %s: %w", columnName, tableName, err)
	}
	return nil
}

// GetDB returns the underlying sql.DB.
// Use with caution for advanced operations.
func (s *AgentGoDB) GetDB() *sql.DB {
	return s.db
}

// Close closes the database connection
func (s *AgentGoDB) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// Plan represents an agent's execution plan
type Plan struct {
	ID        string    `json:"id"`
	Goal      string    `json:"goal"`
	SessionID string    `json:"session_id"`
	Steps     []byte    `json:"steps"` // JSON encoded steps
	Status    string    `json:"status"`
	Reasoning string    `json:"reasoning,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SavePlan saves or updates an agent plan
func (s *AgentGoDB) SavePlan(plan *Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		INSERT INTO agent_plans (id, goal, session_id, steps, status, reasoning, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			goal = excluded.goal,
			steps = excluded.steps,
			status = excluded.status,
			reasoning = excluded.reasoning,
			error = excluded.error,
			updated_at = excluded.updated_at
	`

	_, err := s.db.Exec(query, plan.ID, plan.Goal, plan.SessionID, plan.Steps, plan.Status, plan.Reasoning, plan.Error, plan.CreatedAt, plan.UpdatedAt)
	return err
}

// GetPlan retrieves a plan by ID
func (s *AgentGoDB) GetPlan(id string) (*Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var plan Plan
	query := `
		SELECT id, goal, session_id, steps, status, reasoning, error, created_at, updated_at
		FROM agent_plans
		WHERE id = ?
	`
	err := s.db.QueryRow(query, id).Scan(
		&plan.ID, &plan.Goal, &plan.SessionID, &plan.Steps,
		&plan.Status, &plan.Reasoning, &plan.Error, &plan.CreatedAt, &plan.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

// Chat session types
const (
	ChatTypeLLM   = "llm"
	ChatTypeRAG   = "rag"
	ChatTypeAgent = "agent"
	ChatTypeTeam  = "team"
)

// ChatMessage represents a single message in a chat session
type ChatMessage struct {
	Role             string            `json:"role"`
	Content          string            `json:"content"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []domain.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	TaskID           string            `json:"task_id,omitempty"`
	ResponseID       string            `json:"response_id,omitempty"`
}

// ChatSession represents a unified chat session
type ChatSession struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Title     string                 `json:"title"`
	Messages  []ChatMessage          `json:"messages"`
	Summary   string                 `json:"summary,omitempty"`
	Context   map[string]interface{} `json:"context,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// CreateSession creates a new chat session with UUID
func (s *AgentGoDB) CreateSession(sessionType string) *ChatSession {
	return &ChatSession{
		ID:        uuid.New().String(),
		Type:      sessionType,
		Title:     "",
		Messages:  []ChatMessage{},
		Metadata:  make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// SaveSession saves or updates a chat session
func (s *AgentGoDB) SaveSession(session *ChatSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session.UpdatedAt = time.Now()

	// Generate title from first message if not set
	if session.Title == "" && len(session.Messages) > 0 {
		for _, msg := range session.Messages {
			if msg.Role == "user" && msg.Content != "" {
				session.Title = truncateString(msg.Content, 100)
				break
			}
		}
	}

	messagesJSON, err := json.Marshal(session.Messages)
	if err != nil {
		return err
	}

	var contextJSON []byte
	if session.Context != nil {
		contextJSON, err = json.Marshal(session.Context)
		if err != nil {
			return err
		}
	}

	var metadataJSON []byte
	if session.Metadata != nil {
		metadataJSON, err = json.Marshal(session.Metadata)
		if err != nil {
			return err
		}
	}

	query := `
		INSERT INTO chat_sessions (id, type, title, messages, summary, context, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			type = excluded.type,
			title = excluded.title,
			messages = excluded.messages,
			summary = excluded.summary,
			context = excluded.context,
			metadata = excluded.metadata,
			updated_at = excluded.updated_at
	`

	_, err = s.db.Exec(query, session.ID, session.Type, session.Title, messagesJSON, session.Summary, contextJSON, metadataJSON, session.CreatedAt, session.UpdatedAt)
	return err
}

// GetSession retrieves a chat session by ID
func (s *AgentGoDB) GetSession(id string) (*ChatSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, type, title, messages, summary, context, metadata, created_at, updated_at
		FROM chat_sessions
		WHERE id = ?
	`

	var session ChatSession
	var messagesJSON, contextJSON, metadataJSON []byte
	var summary sql.NullString

	err := s.db.QueryRow(query, id).Scan(
		&session.ID,
		&session.Type,
		&session.Title,
		&messagesJSON,
		&summary,
		&contextJSON,
		&metadataJSON,
		&session.CreatedAt,
		&session.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	if summary.Valid {
		session.Summary = summary.String
	}

	if err := json.Unmarshal(messagesJSON, &session.Messages); err != nil {
		return nil, err
	}
	if len(session.Messages) == 0 {
		messages, err := s.getMessages(id, 0)
		if err == nil {
			session.Messages = messages
		}
	}

	if len(contextJSON) > 0 {
		if err := json.Unmarshal(contextJSON, &session.Context); err != nil {
			return nil, err
		}
	}

	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &session.Metadata); err != nil {
			return nil, err
		}
	}

	return &session, nil
}

func normalizeMessageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	return limit
}

func (s *AgentGoDB) getMessages(sessionID string, limit int) ([]ChatMessage, error) {
	query := `
		SELECT role, content FROM chat_messages
		WHERE session_id = ?
		ORDER BY created_at ASC
		LIMIT ?
	`
	rows, err := s.db.Query(query, sessionID, normalizeMessageLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []ChatMessage
	for rows.Next() {
		var msg ChatMessage
		if err := rows.Scan(&msg.Role, &msg.Content); err != nil {
			continue
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// ListSessions retrieves chat sessions with optional type filtering
func (s *AgentGoDB) ListSessions(sessionType string, limit int) ([]*ChatSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}

	var query string
	var rows *sql.Rows
	var err error

	if sessionType != "" {
		query = `
			SELECT id, type, title, messages, summary, context, metadata, created_at, updated_at
			FROM chat_sessions
			WHERE type = ?
			ORDER BY updated_at DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, sessionType, limit)
	} else {
		query = `
			SELECT id, type, title, messages, summary, context, metadata, created_at, updated_at
			FROM chat_sessions
			ORDER BY updated_at DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, limit)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*ChatSession
	for rows.Next() {
		var session ChatSession
		var messagesJSON, contextJSON, metadataJSON []byte
		var summary sql.NullString

		err := rows.Scan(
			&session.ID,
			&session.Type,
			&session.Title,
			&messagesJSON,
			&summary,
			&contextJSON,
			&metadataJSON,
			&session.CreatedAt,
			&session.UpdatedAt,
		)
		if err != nil {
			continue
		}

		if summary.Valid {
			session.Summary = summary.String
		}

		if err := json.Unmarshal(messagesJSON, &session.Messages); err != nil {
			continue
		}

		if len(contextJSON) > 0 {
			_ = json.Unmarshal(contextJSON, &session.Context)
		}

		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &session.Metadata)
		}

		sessions = append(sessions, &session)
	}

	return sessions, nil
}

// CountMessages returns the number of persisted messages for a session.
func (s *AgentGoDB) CountMessages(sessionID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM chat_messages
		WHERE session_id = ?
	`, sessionID).Scan(&count)
	return count, err
}

// ListPlans retrieves plans with optional limit and session filtering
func (s *AgentGoDB) ListPlans(sessionID string, limit int) ([]*Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}

	var query string
	var rows *sql.Rows
	var err error

	if sessionID != "" {
		query = `
			SELECT id, goal, session_id, steps, status, reasoning, error, created_at, updated_at
			FROM agent_plans WHERE session_id = ?
			ORDER BY created_at DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, sessionID, limit)
	} else {
		query = `
			SELECT id, goal, session_id, steps, status, reasoning, error, created_at, updated_at
			FROM agent_plans
			ORDER BY created_at DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, limit)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plans []*Plan
	for rows.Next() {
		var plan Plan
		err := rows.Scan(&plan.ID, &plan.Goal, &plan.SessionID, &plan.Steps,
			&plan.Status, &plan.Reasoning, &plan.Error, &plan.CreatedAt, &plan.UpdatedAt)
		if err != nil {
			continue
		}
		plans = append(plans, &plan)
	}

	return plans, nil
}

// DeleteSession deletes a chat session by ID
func (s *AgentGoDB) DeleteSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM chat_sessions WHERE id = ?", id)
	return err
}

// SaveConfig saves a config value
func (s *AgentGoDB) SaveConfig(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO config (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = CURRENT_TIMESTAMP
	`, key, value)
	return err
}

// GetConfig retrieves a config value
func (s *AgentGoDB) GetConfig(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var value string
	err := s.db.QueryRow("SELECT value FROM config WHERE key = ?", key).Scan(&value)
	if err != nil {
		return "", err
	}
	return value, nil
}

// ChatSession methods below

// AgentModel represents an agent model configuration
type AgentModel struct {
	ID                    string    `json:"id"`
	A2AID                 string    `json:"a2a_id"`
	TeamID                string    `json:"team_id"`
	Name                  string    `json:"name"`
	Kind                  string    `json:"kind"`
	Description           string    `json:"description"`
	Instructions          string    `json:"instructions"`
	Model                 string    `json:"model"`
	PreferredProvider     string    `json:"preferred_provider"`
	PreferredModel        string    `json:"preferred_model"`
	RequiredLLMCapability int       `json:"required_llm_capability"`
	MCPTools              []string  `json:"mcp_tools"`
	Skills                []string  `json:"skills"`
	EnableRAG             bool      `json:"enable_rag"`
	EnableMemory          bool      `json:"enable_memory"`
	MemoryStoreType       string    `json:"memory_store_type"`
	EnablePTC             bool      `json:"enable_ptc"`
	EnableMCP             bool      `json:"enable_mcp"`
	EnableA2A             bool      `json:"enable_a2a"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// SaveAgentModel saves or updates an agent model
func (s *AgentGoDB) SaveAgentModel(agent *AgentModel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(agent.A2AID) == "" {
		agent.A2AID = uuid.NewString()
	}

	mcpToolsJSON, _ := json.Marshal(agent.MCPTools)
	skillsJSON, _ := json.Marshal(agent.Skills)

	_, err := s.db.Exec(`
		INSERT INTO agents (id, a2a_id, team_id, name, kind, description, instructions, model, preferred_provider, preferred_model, required_llm_capability, mcp_tools, skills, enable_rag, enable_memory, memory_store_type, enable_ptc, enable_mcp, enable_a2a, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			a2a_id = excluded.a2a_id,
			team_id = excluded.team_id,
			name = excluded.name,
			kind = excluded.kind,
			description = excluded.description,
			instructions = excluded.instructions,
			model = excluded.model,
			preferred_provider = excluded.preferred_provider,
			preferred_model = excluded.preferred_model,
			required_llm_capability = excluded.required_llm_capability,
			mcp_tools = excluded.mcp_tools,
			skills = excluded.skills,
			enable_rag = excluded.enable_rag,
			enable_memory = excluded.enable_memory,
			memory_store_type = excluded.memory_store_type,
			enable_ptc = excluded.enable_ptc,
			enable_mcp = excluded.enable_mcp,
			enable_a2a = excluded.enable_a2a,
			updated_at = CURRENT_TIMESTAMP
	`, agent.ID, agent.A2AID, agent.TeamID, agent.Name, agent.Kind, agent.Description, agent.Instructions, agent.Model, agent.PreferredProvider, agent.PreferredModel, agent.RequiredLLMCapability, string(mcpToolsJSON), string(skillsJSON), agent.EnableRAG, agent.EnableMemory, agent.MemoryStoreType, agent.EnablePTC, agent.EnableMCP, agent.EnableA2A, agent.CreatedAt, agent.UpdatedAt)
	return err
}

// GetAgentModel retrieves an agent model by ID
func (s *AgentGoDB) GetAgentModel(id string) (*AgentModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agent := &AgentModel{}
	var mcpToolsJSON, skillsJSON string

	err := s.db.QueryRow(`
		SELECT id, a2a_id, team_id, name, kind, description, instructions, model, preferred_provider, preferred_model, required_llm_capability, mcp_tools, skills, enable_rag, enable_memory, memory_store_type, enable_ptc, enable_mcp, enable_a2a, created_at, updated_at
		FROM agents WHERE id = ?
	`, id).Scan(&agent.ID, &agent.A2AID, &agent.TeamID, &agent.Name, &agent.Kind, &agent.Description, &agent.Instructions, &agent.Model, &agent.PreferredProvider, &agent.PreferredModel, &agent.RequiredLLMCapability,
		&mcpToolsJSON, &skillsJSON, &agent.EnableRAG, &agent.EnableMemory, &agent.MemoryStoreType, &agent.EnablePTC, &agent.EnableMCP, &agent.EnableA2A, &agent.CreatedAt, &agent.UpdatedAt)

	if err != nil {
		return nil, err
	}

	_ = json.Unmarshal([]byte(mcpToolsJSON), &agent.MCPTools)
	_ = json.Unmarshal([]byte(skillsJSON), &agent.Skills)

	return agent, nil
}

// GetAgentModelByName retrieves an agent model by name
func (s *AgentGoDB) GetAgentModelByName(name string) (*AgentModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agent := &AgentModel{}
	var mcpToolsJSON, skillsJSON string

	err := s.db.QueryRow(`
		SELECT id, a2a_id, team_id, name, kind, description, instructions, model, preferred_provider, preferred_model, required_llm_capability, mcp_tools, skills, enable_rag, enable_memory, memory_store_type, enable_ptc, enable_mcp, enable_a2a, created_at, updated_at
		FROM agents WHERE name = ?
	`, name).Scan(&agent.ID, &agent.A2AID, &agent.TeamID, &agent.Name, &agent.Kind, &agent.Description, &agent.Instructions, &agent.Model, &agent.PreferredProvider, &agent.PreferredModel, &agent.RequiredLLMCapability,
		&mcpToolsJSON, &skillsJSON, &agent.EnableRAG, &agent.EnableMemory, &agent.MemoryStoreType, &agent.EnablePTC, &agent.EnableMCP, &agent.EnableA2A, &agent.CreatedAt, &agent.UpdatedAt)

	if err != nil {
		return nil, err
	}

	_ = json.Unmarshal([]byte(mcpToolsJSON), &agent.MCPTools)
	_ = json.Unmarshal([]byte(skillsJSON), &agent.Skills)

	return agent, nil
}

func (s *AgentGoDB) GetAgentModelByA2AID(a2aID string) (*AgentModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agent := &AgentModel{}
	var mcpToolsJSON, skillsJSON string

	err := s.db.QueryRow(`
		SELECT id, a2a_id, team_id, name, kind, description, instructions, model, preferred_provider, preferred_model, required_llm_capability, mcp_tools, skills, enable_rag, enable_memory, memory_store_type, enable_ptc, enable_mcp, enable_a2a, created_at, updated_at
		FROM agents WHERE a2a_id = ?
	`, strings.TrimSpace(a2aID)).Scan(&agent.ID, &agent.A2AID, &agent.TeamID, &agent.Name, &agent.Kind, &agent.Description, &agent.Instructions, &agent.Model, &agent.PreferredProvider, &agent.PreferredModel, &agent.RequiredLLMCapability,
		&mcpToolsJSON, &skillsJSON, &agent.EnableRAG, &agent.EnableMemory, &agent.MemoryStoreType, &agent.EnablePTC, &agent.EnableMCP, &agent.EnableA2A, &agent.CreatedAt, &agent.UpdatedAt)

	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(mcpToolsJSON), &agent.MCPTools)
	_ = json.Unmarshal([]byte(skillsJSON), &agent.Skills)
	return agent, nil
}

// ListAgentModels retrieves all agent models
func (s *AgentGoDB) ListAgentModels() ([]*AgentModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, a2a_id, team_id, name, kind, description, instructions, model, preferred_provider, preferred_model, required_llm_capability, mcp_tools, skills, enable_rag, enable_memory, memory_store_type, enable_ptc, enable_mcp, enable_a2a, created_at, updated_at
		FROM agents ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*AgentModel
	for rows.Next() {
		agent := &AgentModel{}
		var mcpToolsJSON, skillsJSON string

		err := rows.Scan(&agent.ID, &agent.A2AID, &agent.TeamID, &agent.Name, &agent.Kind, &agent.Description, &agent.Instructions, &agent.Model, &agent.PreferredProvider, &agent.PreferredModel, &agent.RequiredLLMCapability,
			&mcpToolsJSON, &skillsJSON, &agent.EnableRAG, &agent.EnableMemory, &agent.MemoryStoreType, &agent.EnablePTC, &agent.EnableMCP, &agent.EnableA2A, &agent.CreatedAt, &agent.UpdatedAt)
		if err != nil {
			continue
		}

		_ = json.Unmarshal([]byte(mcpToolsJSON), &agent.MCPTools)
		_ = json.Unmarshal([]byte(skillsJSON), &agent.Skills)
		agents = append(agents, agent)
	}

	return agents, nil
}

// DeleteAgentModel deletes an agent model by ID
func (s *AgentGoDB) DeleteAgentModel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(`DELETE FROM team_memberships WHERE agent_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	return err
}

func (s *AgentGoDB) backfillA2AIDsLocked() error {
	rows, err := s.db.Query(`SELECT id FROM agents WHERE a2a_id IS NULL OR trim(a2a_id) = ''`)
	if err != nil {
		return err
	}
	var agentIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		agentIDs = append(agentIDs, id)
	}
	rows.Close()
	for _, id := range agentIDs {
		if _, err := s.db.Exec(`UPDATE agents SET a2a_id = ? WHERE id = ?`, uuid.NewString(), id); err != nil {
			return err
		}
	}
	return nil
}

func (s *AgentGoDB) SaveTask(task *taskpkg.Task) error {
	if task == nil {
		return fmt.Errorf("task is required")
	}
	if strings.TrimSpace(task.ID) == "" {
		return fmt.Errorf("task id is required")
	}

	agentNamesJSON, _ := json.Marshal(task.AgentNames)
	framesJSON, _ := json.Marshal(task.Frames)
	eventsJSON, _ := json.Marshal(task.Events)
	awaitingJSON, _ := json.Marshal(task.Awaiting)
	statsJSON, _ := json.Marshal(task.Stats)

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO tasks (
			id, kind, status, session_id, runtime_session_id, parent_task_id,
			continuation_id, queue_class, awaiting, team_id, team_name, agent_name, agent_names, input, output, error,
			frames, events, stats, source, source_id, created_at, started_at, finished_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind,
			status = excluded.status,
			session_id = excluded.session_id,
			runtime_session_id = excluded.runtime_session_id,
			parent_task_id = excluded.parent_task_id,
			continuation_id = excluded.continuation_id,
			queue_class = excluded.queue_class,
			awaiting = excluded.awaiting,
			team_id = excluded.team_id,
			team_name = excluded.team_name,
			agent_name = excluded.agent_name,
			agent_names = excluded.agent_names,
			input = excluded.input,
			output = excluded.output,
			error = excluded.error,
			frames = excluded.frames,
			events = excluded.events,
			stats = excluded.stats,
			source = excluded.source,
			source_id = excluded.source_id,
			created_at = excluded.created_at,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at
	`, task.ID, task.Kind, task.Status, task.SessionID, task.RuntimeSessionID, task.ParentTaskID, task.ContinuationID, task.QueueClass, string(awaitingJSON),
		task.TeamID, task.TeamName, task.AgentName, string(agentNamesJSON), task.Input, task.Output, task.Error,
		string(framesJSON), string(eventsJSON), string(statsJSON), task.Source, task.SourceID, task.CreatedAt, task.StartedAt, task.FinishedAt)
	return err
}

func (s *AgentGoDB) GetTask(id string) (*taskpkg.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(`
		SELECT id, kind, status, session_id, runtime_session_id, parent_task_id,
		       continuation_id, queue_class, awaiting, team_id, team_name, agent_name, agent_names, input, output, error,
		       frames, events, stats, source, source_id, created_at, started_at, finished_at
		FROM tasks WHERE id = ?
	`, id)
	return scanTask(row)
}

func (s *AgentGoDB) ListTasks(limit int) ([]*taskpkg.Task, error) {
	tasks, _, err := s.ListTasksPaged(TaskListFilter{Limit: limit})
	return tasks, err
}

// TaskListFilter parameterizes a paginated, filtered task listing. Zero values
// mean "no constraint": Limit<=0 → 100, Offset<0 → 0, empty Status/Search → no
// filter. Search matches a substring of input, agent_name, or id.
type TaskListFilter struct {
	Limit  int
	Offset int
	Status string
	Search string
}

// whereClause builds the shared WHERE fragment (and its args) used by both the
// page query and the COUNT query so they always agree.
func (f TaskListFilter) whereClause() (string, []interface{}) {
	var conds []string
	var args []interface{}
	if status := strings.TrimSpace(f.Status); status != "" && !strings.EqualFold(status, "all") {
		conds = append(conds, "status = ?")
		args = append(args, status)
	}
	if search := strings.TrimSpace(f.Search); search != "" {
		like := "%" + search + "%"
		conds = append(conds, "(input LIKE ? OR agent_name LIKE ? OR id LIKE ?)")
		args = append(args, like, like, like)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListTasksPaged returns one page of tasks newest-first, plus the total number
// of tasks matching the filter (ignoring limit/offset) so callers can render
// pagination. SQL-level pagination — only Limit rows are scanned.
func (s *AgentGoDB) ListTasksPaged(f TaskListFilter) ([]*taskpkg.Task, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	where, args := f.whereClause()

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	pageArgs := append(append([]interface{}{}, args...), limit, offset)
	// frames/events are large per-task JSON blobs the list view never uses
	// (only the detail endpoint hydrates them). Select NULL for them so the
	// page query doesn't read megabytes off disk — scanTask leaves the fields
	// nil. This is the difference between a ~1s and a ~10ms /tasks page.
	rows, err := s.db.Query(`
		SELECT id, kind, status, session_id, runtime_session_id, parent_task_id,
		       continuation_id, queue_class, awaiting, team_id, team_name, agent_name, agent_names, input, output, error,
		       NULL AS frames, NULL AS events, stats, source, source_id, created_at, started_at, finished_at
		FROM tasks`+where+`
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var tasks []*taskpkg.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err == nil {
			tasks = append(tasks, task)
		}
	}
	return tasks, total, rows.Err()
}

type taskScanner interface {
	Scan(dest ...interface{}) error
}

func scanTask(scanner taskScanner) (*taskpkg.Task, error) {
	var task taskpkg.Task
	var agentNamesJSON, framesJSON, eventsJSON, awaitingJSON, statsJSON []byte
	var sessionID, runtimeSessionID, parentTaskID, continuationID, queueClass, teamID, teamName, agentName, input, output, errorText, source, sourceID sql.NullString
	var startedAt, finishedAt sql.NullTime
	if err := scanner.Scan(
		&task.ID, &task.Kind, &task.Status, &sessionID, &runtimeSessionID, &parentTaskID,
		&continuationID, &queueClass, &awaitingJSON, &teamID, &teamName, &agentName, &agentNamesJSON, &input, &output, &errorText,
		&framesJSON, &eventsJSON, &statsJSON, &source, &sourceID, &task.CreatedAt, &startedAt, &finishedAt,
	); err != nil {
		return nil, err
	}
	task.SessionID = sessionID.String
	task.RuntimeSessionID = runtimeSessionID.String
	task.ParentTaskID = parentTaskID.String
	task.ContinuationID = continuationID.String
	task.QueueClass = taskpkg.QueueClass(queueClass.String)
	task.TeamID = teamID.String
	task.TeamName = teamName.String
	task.AgentName = agentName.String
	task.Input = input.String
	task.Output = output.String
	task.Error = errorText.String
	task.Source = source.String
	task.SourceID = sourceID.String
	_ = json.Unmarshal(agentNamesJSON, &task.AgentNames)
	_ = json.Unmarshal(awaitingJSON, &task.Awaiting)
	_ = json.Unmarshal(framesJSON, &task.Frames)
	_ = json.Unmarshal(eventsJSON, &task.Events)
	if len(statsJSON) > 0 {
		task.Stats = &taskpkg.TaskStats{}
		_ = json.Unmarshal(statsJSON, task.Stats)
	}
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		task.FinishedAt = &finishedAt.Time
	}
	return &task, nil
}

func (s *AgentGoDB) SaveResource(res resource.Resource) error {
	if strings.TrimSpace(res.ID) == "" {
		return fmt.Errorf("resource id is required")
	}
	metadataJSON, _ := json.Marshal(res.Metadata)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO resources (id, kind, name, provider, description, execution, metadata, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind,
			name = excluded.name,
			provider = excluded.provider,
			description = excluded.description,
			execution = excluded.execution,
			metadata = excluded.metadata,
			updated_at = CURRENT_TIMESTAMP
	`, res.ID, res.Kind, res.Name, res.Provider, res.Description, res.Execution, string(metadataJSON))
	return err
}

func (s *AgentGoDB) ListResources() ([]resource.Resource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
		SELECT id, kind, name, provider, description, execution, metadata
		FROM resources
		ORDER BY kind, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []resource.Resource
	for rows.Next() {
		var res resource.Resource
		var metadataJSON []byte
		if err := rows.Scan(&res.ID, &res.Kind, &res.Name, &res.Provider, &res.Description, &res.Execution, &metadataJSON); err != nil {
			continue
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &res.Metadata)
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

// AddMessage adds a message to a session and ensures the session exists
func (s *AgentGoDB) AddMessage(sessionID string, role, content string, metadata map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Ensure session exists (INSERT OR IGNORE)
	// We use a generic type and title if it's new
	sessionType := "llm"
	if metadata != nil {
		if t, ok := metadata["type"].(string); ok {
			sessionType = t
		}
	}

	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO chat_sessions (id, type, title, messages, created_at, updated_at)
		VALUES (?, ?, ?, '[]', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, sessionID, sessionType, "New Session")
	if err != nil {
		return fmt.Errorf("failed to ensure session existence: %w", err)
	}

	// 2. Generate title from first user message if title is still default
	if role == "user" {
		var currentTitle string
		_ = s.db.QueryRow("SELECT title FROM chat_sessions WHERE id = ?", sessionID).Scan(&currentTitle)
		if currentTitle == "New Session" || currentTitle == "" {
			title := truncateString(content, 30)
			_, _ = s.db.Exec("UPDATE chat_sessions SET title = ? WHERE id = ?", title, sessionID)
		}
	}

	// 3. Insert the message
	var metadataJSON []byte
	if metadata != nil {
		metadataJSON, _ = json.Marshal(metadata)
	}

	query := `
		INSERT INTO chat_messages (id, session_id, role, content, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`
	_, err = s.db.Exec(query, uuid.New().String(), sessionID, role, content, metadataJSON)
	if err != nil {
		return err
	}

	// 4. Update session's updated_at
	_, err = s.db.Exec("UPDATE chat_sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?", sessionID)
	return err
}

// GetMessages retrieves messages for a session
func (s *AgentGoDB) GetMessages(sessionID string, limit int) ([]ChatMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getMessages(sessionID, limit)
}
