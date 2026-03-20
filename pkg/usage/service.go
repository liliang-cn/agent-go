package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

// Service provides usage tracking functionality
type Service struct {
	repo         Repository
	tokenCounter *TokenCounter
	config       *config.Config
	mu           sync.RWMutex

	// Current conversation tracking
	currentConversation *Conversation
	currentMessages     []Message
}

// NewService creates a new usage tracking service
func NewService(cfg *config.Config) (*Service, error) {
	// Use data path from config
	return NewServiceWithDataDir(cfg, cfg.DataDir())
}

// NewServiceWithDataDir creates a new usage tracking service with custom data directory
func NewServiceWithDataDir(cfg *config.Config, dataDir string) (*Service, error) {
	// Initialize repository with database path
	dbPath := filepath.Join(dataDir, "usage.db")
	repo, err := NewSQLiteRepository(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	return &Service{
		repo:         repo,
		tokenCounter: NewTokenCounter(),
		config:       cfg,
	}, nil
}

// StartConversation starts a new conversation
func (s *Service) StartConversation(ctx context.Context, title string) (*Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	conversation := NewConversation(title)
	if err := s.repo.CreateConversation(ctx, conversation); err != nil {
		return nil, fmt.Errorf("failed to create conversation: %w", err)
	}

	s.currentConversation = conversation
	s.currentMessages = []Message{}

	return conversation, nil
}

// GetCurrentConversation returns the current conversation
func (s *Service) GetCurrentConversation() *Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentConversation
}

// SetCurrentConversation sets the current conversation
func (s *Service) SetCurrentConversation(ctx context.Context, conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conversation, err := s.repo.GetConversation(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("failed to get conversation: %w", err)
	}

	messages, err := s.repo.ListMessages(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("failed to list messages: %w", err)
	}

	s.currentConversation = conversation
	s.currentMessages = make([]Message, len(messages))
	for i, msg := range messages {
		s.currentMessages[i] = *msg
	}

	return nil
}

// AddMessage adds a message to the current conversation
func (s *Service) AddMessage(ctx context.Context, role, content string) (*Message, error) {
	return s.AddMessageWithModel(ctx, role, content, "default")
}

// AddMessageWithModel adds a message and counts tokens using the provided model.
func (s *Service) AddMessageWithModel(ctx context.Context, role, content, model string) (*Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentConversation == nil {
		// Auto-create a conversation if none exists
		conversation := NewConversation("New Conversation")
		if err := s.repo.CreateConversation(ctx, conversation); err != nil {
			return nil, fmt.Errorf("failed to create conversation: %w", err)
		}
		s.currentConversation = conversation
		s.currentMessages = []Message{}
	}

	if model == "" {
		model = "default"
	}

	tokenCount := s.tokenCounter.EstimateTokens(content, model)

	message := NewMessage(s.currentConversation.ID, role, content, tokenCount)
	if err := s.repo.CreateMessage(ctx, message); err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	s.currentMessages = append(s.currentMessages, *message)

	// Update conversation timestamp
	s.currentConversation.UpdatedAt = time.Now()
	if err := s.repo.UpdateConversation(ctx, s.currentConversation.ID, s.currentConversation.Title); err != nil {
		// Log error but don't fail
		fmt.Printf("Warning: failed to update conversation timestamp: %v\n", err)
	}

	return message, nil
}

// TrackLLMCall tracks an LLM API call
func (s *Service) TrackLLMCall(ctx context.Context, provider, model string, input, output string, startTime time.Time) (*UsageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create usage record
	record := NewUsageRecord("", "", CallTypeLLM)

	if s.currentConversation != nil {
		record.ConversationID = s.currentConversation.ID
	}

	record.Provider = provider
	record.Model = model
	record.Latency = time.Since(startTime).Milliseconds()

	// Estimate tokens
	record.InputTokens = s.tokenCounter.EstimateTokens(input, model)
	record.OutputTokens = s.tokenCounter.EstimateTokens(output, model)
	record.TotalTokens = record.InputTokens + record.OutputTokens

	// Calculate cost
	record.Cost = CalculateCost(model, record.InputTokens, record.OutputTokens)

	record.Success = true

	// Save to database
	if err := s.repo.CreateUsageRecord(ctx, record); err != nil {
		return nil, fmt.Errorf("failed to create usage record: %w", err)
	}

	return record, nil
}

// TrackLLMCallWithTokens tracks an LLM call with known token counts
func (s *Service) TrackLLMCallWithTokens(ctx context.Context, provider, model string, inputTokens, outputTokens int, startTime time.Time) (*UsageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record := NewUsageRecord("", "", CallTypeLLM)

	if s.currentConversation != nil {
		record.ConversationID = s.currentConversation.ID
	}

	record.Provider = provider
	record.Model = model
	record.InputTokens = inputTokens
	record.OutputTokens = outputTokens
	record.TotalTokens = inputTokens + outputTokens
	record.Latency = time.Since(startTime).Milliseconds()
	record.Cost = CalculateCost(model, inputTokens, outputTokens)
	record.Success = true

	if err := s.repo.CreateUsageRecord(ctx, record); err != nil {
		return nil, fmt.Errorf("failed to create usage record: %w", err)
	}

	return record, nil
}

// TrackMCPCall tracks an MCP tool call
func (s *Service) TrackMCPCall(ctx context.Context, toolName string, params interface{}, startTime time.Time) (*UsageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record := NewUsageRecord("", "", CallTypeMCP)

	if s.currentConversation != nil {
		record.ConversationID = s.currentConversation.ID
	}

	record.Provider = "mcp"
	record.Model = toolName
	record.Latency = time.Since(startTime).Milliseconds()
	record.Success = true

	// Store params as metadata
	if params != nil {
		paramsJSON, _ := json.Marshal(params)
		record.RequestMetadata = string(paramsJSON)
	}

	if err := s.repo.CreateUsageRecord(ctx, record); err != nil {
		return nil, fmt.Errorf("failed to create usage record: %w", err)
	}

	return record, nil
}

// TrackRAGCall tracks a RAG pipeline call
func (s *Service) TrackRAGCall(ctx context.Context, operation string, query string, resultCount int, startTime time.Time) (*UsageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record := NewUsageRecord("", "", CallTypeRAG)

	if s.currentConversation != nil {
		record.ConversationID = s.currentConversation.ID
	}

	record.Provider = "rag"
	record.Model = operation // e.g., "query", "ingest", "embed"
	record.Latency = time.Since(startTime).Milliseconds()
	record.Success = true

	// Estimate tokens for the query
	if query != "" {
		record.InputTokens = s.tokenCounter.EstimateTokens(query, "default")
	}

	// Store metadata
	metadata := map[string]interface{}{
		"query":        query,
		"result_count": resultCount,
	}
	metadataJSON, _ := json.Marshal(metadata)
	record.RequestMetadata = string(metadataJSON)

	if err := s.repo.CreateUsageRecord(ctx, record); err != nil {
		return nil, fmt.Errorf("failed to create usage record: %w", err)
	}

	return record, nil
}

// TrackError tracks an error in any type of call
func (s *Service) TrackError(ctx context.Context, callType CallType, provider, model string, errorMsg string, startTime time.Time) (*UsageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record := NewUsageRecord("", "", callType)

	if s.currentConversation != nil {
		record.ConversationID = s.currentConversation.ID
	}

	record.Provider = provider
	record.Model = model
	record.Latency = time.Since(startTime).Milliseconds()
	record.Success = false
	record.ErrorMessage = errorMsg

	if err := s.repo.CreateUsageRecord(ctx, record); err != nil {
		return nil, fmt.Errorf("failed to create usage record: %w", err)
	}

	return record, nil
}

// GetConversationHistory gets the conversation history with messages
func (s *Service) GetConversationHistory(ctx context.Context, conversationID string) (*ConversationWithMessages, error) {
	conversation, err := s.repo.GetConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}

	messages, err := s.repo.ListMessages(ctx, conversationID)
	if err != nil {
		return nil, err
	}

	totalTokens := 0
	msgList := make([]Message, len(messages))
	for i, msg := range messages {
		msgList[i] = *msg
		totalTokens += msg.TokenCount
	}

	return &ConversationWithMessages{
		Conversation: *conversation,
		Messages:     msgList,
		TokenUsage:   totalTokens,
	}, nil
}

// GetUsageStats gets usage statistics
func (s *Service) GetUsageStats(ctx context.Context, filter *UsageFilter) (*UsageStats, error) {
	return s.repo.GetUsageStats(ctx, filter)
}

// GetUsageStatsByType gets usage statistics grouped by call type
func (s *Service) GetUsageStatsByType(ctx context.Context, filter *UsageFilter) (UsageStatsByType, error) {
	return s.repo.GetUsageStatsByType(ctx, filter)
}

// GetUsageStatsByProvider gets usage statistics grouped by provider
func (s *Service) GetUsageStatsByProvider(ctx context.Context, filter *UsageFilter) (UsageStatsByProvider, error) {
	return s.repo.GetUsageStatsByProvider(ctx, filter)
}

// GetDailyUsage gets daily usage statistics
func (s *Service) GetDailyUsage(ctx context.Context, days int) (map[string]*UsageStats, error) {
	return s.repo.GetDailyUsage(ctx, days)
}

// GetTopModels gets the most used models
func (s *Service) GetTopModels(ctx context.Context, limit int) (map[string]int64, error) {
	return s.repo.GetTopModels(ctx, limit)
}

// ListConversations lists conversations
func (s *Service) ListConversations(ctx context.Context, limit, offset int) ([]*Conversation, error) {
	return s.repo.ListConversations(ctx, limit, offset)
}

// DeleteConversation deletes a conversation and all associated data
func (s *Service) DeleteConversation(ctx context.Context, conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If deleting current conversation, clear it
	if s.currentConversation != nil && s.currentConversation.ID == conversationID {
		s.currentConversation = nil
		s.currentMessages = []Message{}
	}

	return s.repo.DeleteConversation(ctx, conversationID)
}

// ExportConversation exports a conversation to JSON
func (s *Service) ExportConversation(ctx context.Context, conversationID string) ([]byte, error) {
	history, err := s.GetConversationHistory(ctx, conversationID)
	if err != nil {
		return nil, err
	}

	// Get usage records for this conversation
	records, err := s.repo.ListUsageRecords(ctx, &UsageFilter{
		ConversationID: conversationID,
	})
	if err != nil {
		return nil, err
	}

	export := map[string]interface{}{
		"conversation": history.Conversation,
		"messages":     history.Messages,
		"usage":        records,
		"token_usage":  history.TokenUsage,
		"exported_at":  time.Now(),
	}

	return json.MarshalIndent(export, "", "  ")
}

// RAG-specific methods

// ListRAGQueries lists RAG queries with filtering
func (s *Service) ListRAGQueries(ctx context.Context, filter *RAGSearchFilter) ([]*RAGQueryRecord, error) {
	return s.repo.ListRAGQueries(ctx, filter)
}

// GetRAGQuery gets a specific RAG query by ID
func (s *Service) GetRAGQuery(ctx context.Context, queryID string) (*RAGQueryRecord, error) {
	return s.repo.GetRAGQuery(ctx, queryID)
}

// GetRAGVisualization gets comprehensive visualization data for a RAG query
func (s *Service) GetRAGVisualization(ctx context.Context, queryID string) (*RAGQueryVisualization, error) {
	return s.repo.GetRAGVisualization(ctx, queryID)
}

// GetRAGAnalytics calculates comprehensive analytics for RAG queries
func (s *Service) GetRAGAnalytics(ctx context.Context, filter *RAGSearchFilter) (*RAGAnalytics, error) {
	return s.repo.GetRAGAnalytics(ctx, filter)
}

// GetRAGPerformanceReport generates a comprehensive performance analysis report
func (s *Service) GetRAGPerformanceReport(ctx context.Context, filter *RAGSearchFilter) (*RAGPerformanceReport, error) {
	analyzer := NewRAGPerformanceAnalyzer(s.repo)
	return analyzer.GeneratePerformanceReport(ctx, filter)
}

// GetRAGToolCalls gets all RAG tool calls with filtering
func (s *Service) GetRAGToolCalls(ctx context.Context, filter *RAGSearchFilter) ([]*RAGToolCall, error) {
	return s.repo.ListAllToolCalls(ctx, filter)
}

// Close closes the service and releases resources
func (s *Service) Close() error {
	return s.repo.Close()
}

// GenerateCallID generates a unique ID for tracking calls
func GenerateCallID() string {
	return uuid.New().String()
}
