package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

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
	_, err := db.Exec("PRAGMA journal_mode=WAL")
	if err != nil {
		return fmt.Errorf("failed to set WAL mode: %w", err)
	}
	_, err = db.Exec("PRAGMA busy_timeout=5000")
	if err != nil {
		return fmt.Errorf("failed to set busy timeout: %w", err)
	}
	_, err = db.Exec("PRAGMA synchronous=NORMAL")
	if err != nil {
		return fmt.Errorf("failed to set synchronous: %w", err)
	}
	return nil
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
			team_id TEXT,
			name TEXT UNIQUE NOT NULL,
			kind TEXT DEFAULT 'captain',
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
			enable_ptc BOOLEAN DEFAULT 0,
			enable_mcp BOOLEAN DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create agents table: %w", err)
	}

	// Teams/Squads table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS squads (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			description TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create squads table: %w", err)
	}

	// Squad memberships
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS squad_memberships (
			agent_id TEXT NOT NULL,
			squad_id TEXT NOT NULL,
			role TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (agent_id, squad_id),
			FOREIGN KEY (squad_id) REFERENCES squads(id) ON DELETE CASCADE,
			FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create squad_memberships table: %w", err)
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

	// Shared tasks table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS shared_tasks (
			id TEXT PRIMARY KEY,
			session_id TEXT,
			squad_id TEXT NOT NULL,
			squad_name TEXT,
			captain_name TEXT NOT NULL,
			agent_names TEXT NOT NULL,
			prompt TEXT NOT NULL,
			ack_message TEXT,
			status TEXT NOT NULL,
			queued_ahead INTEGER DEFAULT 0,
			result_text TEXT,
			results TEXT,
			created_at DATETIME NOT NULL,
			started_at DATETIME,
			finished_at DATETIME
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create shared_tasks table: %w", err)
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
	ChatTypeSquad = "squad"
)

// ChatMessage represents a single message in a chat session
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

// ListConfig retrieves all config key-value pairs
func (s *AgentGoDB) ListConfig() (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT key, value FROM config")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	config := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		config[key] = value
	}
	return config, nil
}

// ChatSession methods below

// Squad represents a team/squad definition
type Squad struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SaveSquad saves or updates a squad
func (s *AgentGoDB) SaveSquad(squad *Squad) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO squads (id, name, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			updated_at = CURRENT_TIMESTAMP
	`, squad.ID, squad.Name, squad.Description, squad.CreatedAt, squad.UpdatedAt)
	return err
}

// GetSquad retrieves a squad by ID
func (s *AgentGoDB) GetSquad(id string) (*Squad, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	squad := &Squad{}
	err := s.db.QueryRow(`
		SELECT id, name, description, created_at, updated_at
		FROM squads WHERE id = ?
	`, id).Scan(&squad.ID, &squad.Name, &squad.Description, &squad.CreatedAt, &squad.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return squad, nil
}

// GetSquadByName retrieves a squad by name
func (s *AgentGoDB) GetSquadByName(name string) (*Squad, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	squad := &Squad{}
	err := s.db.QueryRow(`
		SELECT id, name, description, created_at, updated_at
		FROM squads WHERE lower(name) = lower(?)
	`, name).Scan(&squad.ID, &squad.Name, &squad.Description, &squad.CreatedAt, &squad.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return squad, nil
}

// ListSquads retrieves all squads
func (s *AgentGoDB) ListSquads() ([]*Squad, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, name, description, created_at, updated_at
		FROM squads ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var squads []*Squad
	for rows.Next() {
		squad := &Squad{}
		if err := rows.Scan(&squad.ID, &squad.Name, &squad.Description, &squad.CreatedAt, &squad.UpdatedAt); err != nil {
			continue
		}
		squads = append(squads, squad)
	}
	return squads, nil
}

// DeleteSquad deletes a squad by ID
func (s *AgentGoDB) DeleteSquad(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM squads WHERE id = ?`, id)
	return err
}

// AgentModel represents an agent model configuration
type AgentModel struct {
	ID                    string    `json:"id"`
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
	EnablePTC             bool      `json:"enable_ptc"`
	EnableMCP             bool      `json:"enable_mcp"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// SaveAgentModel saves or updates an agent model
func (s *AgentGoDB) SaveAgentModel(agent *AgentModel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mcpToolsJSON, _ := json.Marshal(agent.MCPTools)
	skillsJSON, _ := json.Marshal(agent.Skills)

	_, err := s.db.Exec(`
		INSERT INTO agents (id, team_id, name, kind, description, instructions, model, preferred_provider, preferred_model, required_llm_capability, mcp_tools, skills, enable_rag, enable_memory, enable_ptc, enable_mcp, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
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
			enable_ptc = excluded.enable_ptc,
			enable_mcp = excluded.enable_mcp,
			updated_at = CURRENT_TIMESTAMP
	`, agent.ID, agent.TeamID, agent.Name, agent.Kind, agent.Description, agent.Instructions, agent.Model, agent.PreferredProvider, agent.PreferredModel, agent.RequiredLLMCapability, string(mcpToolsJSON), string(skillsJSON), agent.EnableRAG, agent.EnableMemory, agent.EnablePTC, agent.EnableMCP, agent.CreatedAt, agent.UpdatedAt)
	return err
}

// GetAgentModel retrieves an agent model by ID
func (s *AgentGoDB) GetAgentModel(id string) (*AgentModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agent := &AgentModel{}
	var mcpToolsJSON, skillsJSON string

	err := s.db.QueryRow(`
		SELECT id, team_id, name, kind, description, instructions, model, preferred_provider, preferred_model, required_llm_capability, mcp_tools, skills, enable_rag, enable_memory, enable_ptc, enable_mcp, created_at, updated_at
		FROM agents WHERE id = ?
	`, id).Scan(&agent.ID, &agent.TeamID, &agent.Name, &agent.Kind, &agent.Description, &agent.Instructions, &agent.Model, &agent.PreferredProvider, &agent.PreferredModel, &agent.RequiredLLMCapability,
		&mcpToolsJSON, &skillsJSON, &agent.EnableRAG, &agent.EnableMemory, &agent.EnablePTC, &agent.EnableMCP, &agent.CreatedAt, &agent.UpdatedAt)

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
		SELECT id, team_id, name, kind, description, instructions, model, preferred_provider, preferred_model, required_llm_capability, mcp_tools, skills, enable_rag, enable_memory, enable_ptc, enable_mcp, created_at, updated_at
		FROM agents WHERE name = ?
	`, name).Scan(&agent.ID, &agent.TeamID, &agent.Name, &agent.Kind, &agent.Description, &agent.Instructions, &agent.Model, &agent.PreferredProvider, &agent.PreferredModel, &agent.RequiredLLMCapability,
		&mcpToolsJSON, &skillsJSON, &agent.EnableRAG, &agent.EnableMemory, &agent.EnablePTC, &agent.EnableMCP, &agent.CreatedAt, &agent.UpdatedAt)

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
		SELECT id, team_id, name, kind, description, instructions, model, preferred_provider, preferred_model, required_llm_capability, mcp_tools, skills, enable_rag, enable_memory, enable_ptc, enable_mcp, created_at, updated_at
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

		err := rows.Scan(&agent.ID, &agent.TeamID, &agent.Name, &agent.Kind, &agent.Description, &agent.Instructions, &agent.Model, &agent.PreferredProvider, &agent.PreferredModel, &agent.RequiredLLMCapability,
			&mcpToolsJSON, &skillsJSON, &agent.EnableRAG, &agent.EnableMemory, &agent.EnablePTC, &agent.EnableMCP, &agent.CreatedAt, &agent.UpdatedAt)
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

	if _, err := s.db.Exec(`DELETE FROM squad_memberships WHERE agent_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	return err
}

// SquadMembership represents an agent's membership in a squad
type SquadMembership struct {
	AgentID   string    `json:"agent_id"`
	SquadID   string    `json:"squad_id"`
	SquadName string    `json:"squad_name,omitempty"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SaveSquadMembership saves or updates a squad membership
func (s *AgentGoDB) SaveSquadMembership(m *SquadMembership) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		INSERT INTO squad_memberships (agent_id, squad_id, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, squad_id) DO UPDATE SET
			role = excluded.role,
			updated_at = excluded.updated_at
	`
	_, err := s.db.Exec(query, m.AgentID, m.SquadID, m.Role, m.CreatedAt, m.UpdatedAt)
	return err
}

// ListSquadMemberships retrieves memberships with optional filtering
func (s *AgentGoDB) ListSquadMemberships(agentID, squadID string) ([]*SquadMembership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT m.agent_id, m.squad_id, q.name as squad_name, m.role, m.created_at, m.updated_at
		FROM squad_memberships m
		JOIN squads q ON m.squad_id = q.id
		WHERE 1=1
	`
	args := []interface{}{}
	if agentID != "" {
		query += " AND m.agent_id = ?"
		args = append(args, agentID)
	}
	if squadID != "" {
		query += " AND m.squad_id = ?"
		args = append(args, squadID)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*SquadMembership
	for rows.Next() {
		var m SquadMembership
		if err := rows.Scan(&m.AgentID, &m.SquadID, &m.SquadName, &m.Role, &m.CreatedAt, &m.UpdatedAt); err != nil {
			continue
		}
		result = append(result, &m)
	}
	return result, nil
}

// DeleteSquadMembership deletes a specific membership
func (s *AgentGoDB) DeleteSquadMembership(agentID, squadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM squad_memberships WHERE agent_id = ? AND squad_id = ?", agentID, squadID)
	return err
}

// DeleteMembershipsBySquad deletes all memberships for a squad
func (s *AgentGoDB) DeleteMembershipsBySquad(squadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM squad_memberships WHERE squad_id = ?", squadID)
	return err
}

// SharedTask represents a multi-agent task
type SharedTask struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"session_id"`
	SquadID     string     `json:"squad_id"`
	SquadName   string     `json:"squad_name"`
	CaptainName string     `json:"captain_name"`
	AgentNames  []string   `json:"agent_names"`
	Prompt      string     `json:"prompt"`
	AckMessage  string     `json:"ack_message"`
	Status      string     `json:"status"`
	QueuedAhead int        `json:"queued_ahead"`
	ResultText  string     `json:"result_text"`
	Results     []byte     `json:"results"` // JSON encoded
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// SaveSharedTask saves or updates a shared task
func (s *AgentGoDB) SaveSharedTask(task *SharedTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	agentNamesJSON, _ := json.Marshal(task.AgentNames)

	query := `
		INSERT INTO shared_tasks (
			id, session_id, squad_id, squad_name, captain_name, agent_names, prompt, ack_message,
			status, queued_ahead, result_text, results, created_at, started_at, finished_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			squad_id = excluded.squad_id,
			squad_name = excluded.squad_name,
			captain_name = excluded.captain_name,
			agent_names = excluded.agent_names,
			prompt = excluded.prompt,
			ack_message = excluded.ack_message,
			status = excluded.status,
			queued_ahead = excluded.queued_ahead,
			result_text = excluded.result_text,
			results = excluded.results,
			created_at = excluded.created_at,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at
	`
	_, err := s.db.Exec(query,
		task.ID, task.SessionID, task.SquadID, task.SquadName, task.CaptainName,
		string(agentNamesJSON), task.Prompt, task.AckMessage, task.Status,
		task.QueuedAhead, task.ResultText, task.Results,
		task.CreatedAt, task.StartedAt, task.FinishedAt,
	)
	return err
}

// ListSharedTasks retrieves all shared tasks
func (s *AgentGoDB) ListSharedTasks() ([]*SharedTask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, session_id, squad_id, squad_name, captain_name, agent_names, prompt, ack_message,
		       status, queued_ahead, result_text, results, created_at, started_at, finished_at
		FROM shared_tasks
		ORDER BY created_at ASC
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*SharedTask
	for rows.Next() {
		var task SharedTask
		var agentNamesJSON []byte
		if err := rows.Scan(
			&task.ID, &task.SessionID, &task.SquadID, &task.SquadName, &task.CaptainName,
			&agentNamesJSON, &task.Prompt, &task.AckMessage, &task.Status,
			&task.QueuedAhead, &task.ResultText, &task.Results,
			&task.CreatedAt, &task.StartedAt, &task.FinishedAt,
		); err != nil {
			continue
		}
		_ = json.Unmarshal(agentNamesJSON, &task.AgentNames)
		result = append(result, &task)
	}
	return result, nil
}

// DeleteMembershipsByAgent deletes all memberships for an agent
func (s *AgentGoDB) DeleteMembershipsByAgent(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM squad_memberships WHERE agent_id = ?", agentID)
	return err
}

// MigrateAgentStore migrates data from the old agent store to AgentGoDB
func (s *AgentGoDB) MigrateAgentStore(oldStore interface {
	ListAgentModels() ([]*AgentModel, error)
	ListSquads() ([]*Squad, error)
}) error {
	// Migrate squads
	squads, err := oldStore.ListSquads()
	if err != nil {
		return fmt.Errorf("failed to list squads from old store: %w", err)
	}
	for _, squad := range squads {
		if err := s.SaveSquad(squad); err != nil {
			return fmt.Errorf("failed to migrate squad %s: %w", squad.ID, err)
		}
	}

	// Migrate agents
	agents, err := oldStore.ListAgentModels()
	if err != nil {
		return fmt.Errorf("failed to list agents from old store: %w", err)
	}
	for _, agent := range agents {
		if err := s.SaveAgentModel(agent); err != nil {
			return fmt.Errorf("failed to migrate agent %s: %w", agent.ID, err)
		}
	}

	return nil
}

// AddAgentToSquad adds an agent to a squad
func (s *AgentGoDB) AddAgentToSquad(squadID, agentID, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO squad_memberships (squad_id, agent_id, role, created_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(squad_id, agent_id) DO UPDATE SET
			role = excluded.role,
			updated_at = CURRENT_TIMESTAMP
	`, squadID, agentID, role)
	return err
}

// RemoveAgentFromSquad removes an agent from a squad
func (s *AgentGoDB) RemoveAgentFromSquad(squadID, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM squad_memberships WHERE squad_id = ? AND agent_id = ?`, squadID, agentID)
	return err
}

// GetSquadAgents retrieves all agents in a squad
func (s *AgentGoDB) GetSquadAgents(squadID string) ([]*AgentModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT a.id, a.team_id, a.name, a.kind, a.description, a.instructions, a.model, a.preferred_provider, a.preferred_model, a.required_llm_capability, a.mcp_tools, a.skills, a.enable_rag, a.enable_memory, a.enable_ptc, a.enable_mcp, a.created_at, a.updated_at
		FROM agents a
		JOIN squad_memberships sm ON a.id = sm.agent_id
		WHERE sm.squad_id = ?
	`, squadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*AgentModel
	for rows.Next() {
		agent := &AgentModel{}
		var mcpToolsJSON, skillsJSON string

		err := rows.Scan(&agent.ID, &agent.TeamID, &agent.Name, &agent.Kind, &agent.Description, &agent.Instructions, &agent.Model, &agent.PreferredProvider, &agent.PreferredModel, &agent.RequiredLLMCapability,
			&mcpToolsJSON, &skillsJSON, &agent.EnableRAG, &agent.EnableMemory, &agent.EnablePTC, &agent.EnableMCP, &agent.CreatedAt, &agent.UpdatedAt)
		if err != nil {
			continue
		}

		_ = json.Unmarshal([]byte(mcpToolsJSON), &agent.MCPTools)
		_ = json.Unmarshal([]byte(skillsJSON), &agent.Skills)
		agents = append(agents, agent)
	}

	return agents, nil
}

// NormalizeAgentKind normalizes the agent kind string
func NormalizeAgentKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "captain"
	}
	switch kind {
	case "captain", "specialist", "agent", "leader", "lead", "lead-agent", "commander":
		return "captain"
	default:
		return "captain"
	}
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
