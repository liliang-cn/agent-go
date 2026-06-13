package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; no CGO required
)

// SQLiteRepository implements Repository using SQLite
type SQLiteRepository struct {
	db *sql.DB
}

// NewSQLiteRepository creates a new SQLite repository
func NewSQLiteRepository(dbPath string) (*SQLiteRepository, error) {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create database directory: %w", err)
		}
	}
	// modernc.org/sqlite registers as "sqlite"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	repo := &SQLiteRepository{db: db}
	if err := repo.Initialize(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	return repo, nil
}

// Initialize creates the necessary tables
func (r *SQLiteRepository) Initialize(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			metadata TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			token_count INTEGER NOT NULL,
			created_at TIMESTAMP NOT NULL,
			metadata TEXT,
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS usage_records (
			id TEXT PRIMARY KEY,
			conversation_id TEXT,
			message_id TEXT,
			call_type TEXT NOT NULL,
			provider TEXT,
			model TEXT,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0,
			cost REAL DEFAULT 0,
			latency INTEGER DEFAULT 0,
			success BOOLEAN DEFAULT 1,
			error_message TEXT,
			request_metadata TEXT,
			response_metadata TEXT,
			created_at TIMESTAMP NOT NULL,
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
			FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
		)`,
		// Create indexes for better query performance
		`CREATE INDEX IF NOT EXISTS idx_messages_conversation_id ON messages(conversation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_conversation_id ON usage_records(conversation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_call_type ON usage_records(call_type)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_provider ON usage_records(provider)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_model ON usage_records(model)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_created_at ON usage_records(created_at)`,
	}

	for _, query := range queries {
		if _, err := r.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("failed to execute query: %w", err)
		}
	}

	// Initialize RAG tables
	if err := r.InitializeRAGTables(ctx); err != nil {
		return fmt.Errorf("failed to initialize RAG tables: %w", err)
	}

	return nil
}

// CreateConversation creates a new conversation
func (r *SQLiteRepository) CreateConversation(ctx context.Context, conversation *Conversation) error {
	query := `INSERT INTO conversations (id, title, created_at, updated_at, metadata) 
			  VALUES (?, ?, ?, ?, ?)`

	_, err := r.db.ExecContext(ctx, query,
		conversation.ID,
		conversation.Title,
		conversation.CreatedAt,
		conversation.UpdatedAt,
		conversation.Metadata,
	)
	return err
}

// GetConversation retrieves a conversation by ID
func (r *SQLiteRepository) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	query := `SELECT id, title, created_at, updated_at, metadata 
			  FROM conversations WHERE id = ?`

	var conv Conversation
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&conv.ID,
		&conv.Title,
		&conv.CreatedAt,
		&conv.UpdatedAt,
		&conv.Metadata,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found")
	}
	return &conv, err
}

// ListConversations lists conversations
func (r *SQLiteRepository) ListConversations(ctx context.Context, limit, offset int) ([]*Conversation, error) {
	query := `SELECT id, title, created_at, updated_at, metadata 
			  FROM conversations 
			  ORDER BY updated_at DESC 
			  LIMIT ? OFFSET ?`

	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conversations []*Conversation
	for rows.Next() {
		var conv Conversation
		err := rows.Scan(
			&conv.ID,
			&conv.Title,
			&conv.CreatedAt,
			&conv.UpdatedAt,
			&conv.Metadata,
		)
		if err != nil {
			return nil, err
		}
		conversations = append(conversations, &conv)
	}

	return conversations, rows.Err()
}

// UpdateConversation updates a conversation's title
func (r *SQLiteRepository) UpdateConversation(ctx context.Context, id string, title string) error {
	query := `UPDATE conversations SET title = ?, updated_at = ? WHERE id = ?`
	_, err := r.db.ExecContext(ctx, query, title, time.Now(), id)
	return err
}

// DeleteConversation deletes a conversation
func (r *SQLiteRepository) DeleteConversation(ctx context.Context, id string) error {
	query := `DELETE FROM conversations WHERE id = ?`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

// CreateMessage creates a new message
func (r *SQLiteRepository) CreateMessage(ctx context.Context, message *Message) error {
	query := `INSERT INTO messages (id, conversation_id, role, content, token_count, created_at, metadata) 
			  VALUES (?, ?, ?, ?, ?, ?, ?)`

	_, err := r.db.ExecContext(ctx, query,
		message.ID,
		message.ConversationID,
		message.Role,
		message.Content,
		message.TokenCount,
		message.CreatedAt,
		message.Metadata,
	)
	return err
}

// GetMessage retrieves a message by ID
func (r *SQLiteRepository) GetMessage(ctx context.Context, id string) (*Message, error) {
	query := `SELECT id, conversation_id, role, content, token_count, created_at, metadata 
			  FROM messages WHERE id = ?`

	var msg Message
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&msg.ID,
		&msg.ConversationID,
		&msg.Role,
		&msg.Content,
		&msg.TokenCount,
		&msg.CreatedAt,
		&msg.Metadata,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("message not found")
	}
	return &msg, err
}

// ListMessages lists messages for a conversation
func (r *SQLiteRepository) ListMessages(ctx context.Context, conversationID string) ([]*Message, error) {
	query := `SELECT id, conversation_id, role, content, token_count, created_at, metadata 
			  FROM messages 
			  WHERE conversation_id = ? 
			  ORDER BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, query, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		var msg Message
		err := rows.Scan(
			&msg.ID,
			&msg.ConversationID,
			&msg.Role,
			&msg.Content,
			&msg.TokenCount,
			&msg.CreatedAt,
			&msg.Metadata,
		)
		if err != nil {
			return nil, err
		}
		messages = append(messages, &msg)
	}

	return messages, rows.Err()
}

// DeleteMessage deletes a message
func (r *SQLiteRepository) DeleteMessage(ctx context.Context, id string) error {
	query := `DELETE FROM messages WHERE id = ?`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

// CreateUsageRecord creates a new usage record
func (r *SQLiteRepository) CreateUsageRecord(ctx context.Context, record *UsageRecord) error {
	query := `INSERT INTO usage_records (
		id, conversation_id, message_id, call_type, provider, model,
		input_tokens, output_tokens, total_tokens, cost, latency,
		success, error_message, request_metadata, response_metadata, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := r.db.ExecContext(ctx, query,
		record.ID,
		record.ConversationID,
		record.MessageID,
		record.CallType,
		record.Provider,
		record.Model,
		record.InputTokens,
		record.OutputTokens,
		record.TotalTokens,
		record.Cost,
		record.Latency,
		record.Success,
		record.ErrorMessage,
		record.RequestMetadata,
		record.ResponseMetadata,
		record.CreatedAt,
	)
	return err
}

// GetUsageRecord retrieves a usage record by ID
func (r *SQLiteRepository) GetUsageRecord(ctx context.Context, id string) (*UsageRecord, error) {
	query := `SELECT id, conversation_id, message_id, call_type, provider, model,
			  input_tokens, output_tokens, total_tokens, cost, latency,
			  success, error_message, request_metadata, response_metadata, created_at
			  FROM usage_records WHERE id = ?`

	var record UsageRecord
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&record.ID,
		&record.ConversationID,
		&record.MessageID,
		&record.CallType,
		&record.Provider,
		&record.Model,
		&record.InputTokens,
		&record.OutputTokens,
		&record.TotalTokens,
		&record.Cost,
		&record.Latency,
		&record.Success,
		&record.ErrorMessage,
		&record.RequestMetadata,
		&record.ResponseMetadata,
		&record.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("usage record not found")
	}
	return &record, err
}

// ListUsageRecords lists usage records based on filter
func (r *SQLiteRepository) ListUsageRecords(ctx context.Context, filter *UsageFilter) ([]*UsageRecord, error) {
	query := `SELECT id, conversation_id, message_id, call_type, provider, model,
			  input_tokens, output_tokens, total_tokens, cost, latency,
			  success, error_message, request_metadata, response_metadata, created_at
			  FROM usage_records WHERE 1=1`

	args := []interface{}{}

	if filter.ConversationID != "" {
		query += " AND conversation_id = ?"
		args = append(args, filter.ConversationID)
	}
	if filter.CallType != "" {
		query += " AND call_type = ?"
		args = append(args, filter.CallType)
	}
	if filter.Provider != "" {
		query += " AND provider = ?"
		args = append(args, filter.Provider)
	}
	if filter.Model != "" {
		query += " AND model = ?"
		args = append(args, filter.Model)
	}
	if !filter.StartTime.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, filter.StartTime)
	}
	if !filter.EndTime.IsZero() {
		query += " AND created_at <= ?"
		args = append(args, filter.EndTime)
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*UsageRecord
	for rows.Next() {
		var record UsageRecord
		err := rows.Scan(
			&record.ID,
			&record.ConversationID,
			&record.MessageID,
			&record.CallType,
			&record.Provider,
			&record.Model,
			&record.InputTokens,
			&record.OutputTokens,
			&record.TotalTokens,
			&record.Cost,
			&record.Latency,
			&record.Success,
			&record.ErrorMessage,
			&record.RequestMetadata,
			&record.ResponseMetadata,
			&record.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		records = append(records, &record)
	}

	return records, rows.Err()
}

// GetUsageStats gets aggregated usage statistics
func (r *SQLiteRepository) GetUsageStats(ctx context.Context, filter *UsageFilter) (*UsageStats, error) {
	query := `SELECT 
		COUNT(*) as total_calls,
		COALESCE(SUM(input_tokens), 0) as total_input_tokens,
		COALESCE(SUM(output_tokens), 0) as total_output_tokens,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(cost), 0) as total_cost,
		COALESCE(AVG(latency), 0) as average_latency,
		COALESCE(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 0) as success_rate
		FROM usage_records WHERE 1=1`

	args := []interface{}{}

	if filter.ConversationID != "" {
		query += " AND conversation_id = ?"
		args = append(args, filter.ConversationID)
	}
	if filter.CallType != "" {
		query += " AND call_type = ?"
		args = append(args, filter.CallType)
	}
	if filter.Provider != "" {
		query += " AND provider = ?"
		args = append(args, filter.Provider)
	}
	if filter.Model != "" {
		query += " AND model = ?"
		args = append(args, filter.Model)
	}
	if !filter.StartTime.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, filter.StartTime)
	}
	if !filter.EndTime.IsZero() {
		query += " AND created_at <= ?"
		args = append(args, filter.EndTime)
	}

	var stats UsageStats
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&stats.TotalCalls,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalTokens,
		&stats.TotalCost,
		&stats.AverageLatency,
		&stats.SuccessRate,
	)

	return &stats, err
}

// GetUsageStatsByType gets usage statistics grouped by call type
func (r *SQLiteRepository) GetUsageStatsByType(ctx context.Context, filter *UsageFilter) (UsageStatsByType, error) {
	query := `SELECT 
		call_type,
		COUNT(*) as total_calls,
		COALESCE(SUM(input_tokens), 0) as total_input_tokens,
		COALESCE(SUM(output_tokens), 0) as total_output_tokens,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(cost), 0) as total_cost,
		COALESCE(AVG(latency), 0) as average_latency,
		COALESCE(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 0) as success_rate
		FROM usage_records WHERE 1=1`

	args := []interface{}{}

	if filter.ConversationID != "" {
		query += " AND conversation_id = ?"
		args = append(args, filter.ConversationID)
	}
	if !filter.StartTime.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, filter.StartTime)
	}
	if !filter.EndTime.IsZero() {
		query += " AND created_at <= ?"
		args = append(args, filter.EndTime)
	}

	query += " GROUP BY call_type"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(UsageStatsByType)
	for rows.Next() {
		var callType string
		var stats UsageStats
		err := rows.Scan(
			&callType,
			&stats.TotalCalls,
			&stats.TotalInputTokens,
			&stats.TotalOutputTokens,
			&stats.TotalTokens,
			&stats.TotalCost,
			&stats.AverageLatency,
			&stats.SuccessRate,
		)
		if err != nil {
			return nil, err
		}
		result[CallType(callType)] = &stats
	}

	return result, rows.Err()
}

// GetUsageStatsByProvider gets usage statistics grouped by provider
func (r *SQLiteRepository) GetUsageStatsByProvider(ctx context.Context, filter *UsageFilter) (UsageStatsByProvider, error) {
	query := `SELECT 
		provider,
		COUNT(*) as total_calls,
		COALESCE(SUM(input_tokens), 0) as total_input_tokens,
		COALESCE(SUM(output_tokens), 0) as total_output_tokens,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(cost), 0) as total_cost,
		COALESCE(AVG(latency), 0) as average_latency,
		COALESCE(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 0) as success_rate
		FROM usage_records WHERE provider IS NOT NULL`

	args := []interface{}{}

	if filter.ConversationID != "" {
		query += " AND conversation_id = ?"
		args = append(args, filter.ConversationID)
	}
	if !filter.StartTime.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, filter.StartTime)
	}
	if !filter.EndTime.IsZero() {
		query += " AND created_at <= ?"
		args = append(args, filter.EndTime)
	}

	query += " GROUP BY provider"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(UsageStatsByProvider)
	for rows.Next() {
		var provider string
		var stats UsageStats
		err := rows.Scan(
			&provider,
			&stats.TotalCalls,
			&stats.TotalInputTokens,
			&stats.TotalOutputTokens,
			&stats.TotalTokens,
			&stats.TotalCost,
			&stats.AverageLatency,
			&stats.SuccessRate,
		)
		if err != nil {
			return nil, err
		}
		result[provider] = &stats
	}

	return result, rows.Err()
}

// GetConversationStats gets usage statistics for a specific conversation
func (r *SQLiteRepository) GetConversationStats(ctx context.Context, conversationID string) (*UsageStats, error) {
	return r.GetUsageStats(ctx, &UsageFilter{ConversationID: conversationID})
}

// GetDailyUsage gets daily usage statistics
func (r *SQLiteRepository) GetDailyUsage(ctx context.Context, days int) (map[string]*UsageStats, error) {
	query := `SELECT 
		DATE(ur.created_at) as date,
		COUNT(*) as total_calls,
		COALESCE(SUM(ur.input_tokens), 0) as total_input_tokens,
		COALESCE(SUM(ur.output_tokens), 0) as total_output_tokens,
		COALESCE(SUM(ur.total_tokens), 0) as total_tokens,
		COALESCE(SUM(ur.cost), 0) as total_cost,
		COALESCE(AVG(ur.latency), 0) as average_latency,
		COALESCE(AVG(CASE WHEN ur.success THEN 1.0 ELSE 0.0 END), 0) as success_rate
		FROM usage_records ur
		WHERE ur.created_at >= DATE('now', '-' || ? || ' days')
		GROUP BY DATE(ur.created_at)
		ORDER BY date DESC`

	rows, err := r.db.QueryContext(ctx, query, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*UsageStats)
	for rows.Next() {
		var date string
		var stats UsageStats
		err := rows.Scan(
			&date,
			&stats.TotalCalls,
			&stats.TotalInputTokens,
			&stats.TotalOutputTokens,
			&stats.TotalTokens,
			&stats.TotalCost,
			&stats.AverageLatency,
			&stats.SuccessRate,
		)
		if err != nil {
			return nil, err
		}
		result[date] = &stats
	}

	return result, rows.Err()
}

// GetTopModels gets the most used models
func (r *SQLiteRepository) GetTopModels(ctx context.Context, limit int) (map[string]int64, error) {
	query := `SELECT ur.model, COUNT(*) as usage_count
		FROM usage_records ur
		WHERE ur.model IS NOT NULL
		GROUP BY ur.model
		ORDER BY usage_count DESC
		LIMIT ?`

	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var model string
		var count int64
		err := rows.Scan(&model, &count)
		if err != nil {
			return nil, err
		}
		result[model] = count
	}

	return result, rows.Err()
}

// GetCostByProvider gets total cost grouped by provider
func (r *SQLiteRepository) GetCostByProvider(ctx context.Context, startTime, endTime time.Time) (map[string]float64, error) {
	query := `SELECT ur.provider, COALESCE(SUM(ur.cost), 0) as total_cost
		FROM usage_records ur
		WHERE ur.provider IS NOT NULL`

	args := []interface{}{}

	if !startTime.IsZero() {
		query += " AND ur.created_at >= ?"
		args = append(args, startTime)
	}
	if !endTime.IsZero() {
		query += " AND ur.created_at <= ?"
		args = append(args, endTime)
	}

	query += " GROUP BY ur.provider"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var provider string
		var cost float64
		err := rows.Scan(&provider, &cost)
		if err != nil {
			return nil, err
		}
		result[provider] = cost
	}

	return result, rows.Err()
}

// Close closes the database connection
func (r *SQLiteRepository) Close() error {
	return r.db.Close()
}

// MarshalMetadata marshals metadata to JSON string
func MarshalMetadata(data interface{}) (string, error) {
	if data == nil {
		return "", nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalMetadata unmarshals metadata from JSON string
func UnmarshalMetadata(data string, v interface{}) error {
	if data == "" {
		return nil
	}
	return json.Unmarshal([]byte(data), v)
}
