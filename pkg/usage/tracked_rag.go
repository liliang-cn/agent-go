package usage

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// TrackedRAGProcessor wraps the RAG processor to track detailed query data
type TrackedRAGProcessor struct {
	processor    domain.RAGProcessor
	usageService *Service
	tokenCounter *TokenCounter
}

// NewTrackedRAGProcessor creates a new tracked RAG processor
func NewTrackedRAGProcessor(proc domain.RAGProcessor, usageService *Service) *TrackedRAGProcessor {
	return &TrackedRAGProcessor{
		processor:    proc,
		usageService: usageService,
		tokenCounter: NewTokenCounter(),
	}
}

// Query executes a RAG query with detailed tracking
func (t *TrackedRAGProcessor) Query(ctx context.Context, req domain.QueryRequest) (domain.QueryResponse, error) {
	return t.trackQuery(ctx, req, false)
}

// QueryWithTools executes a RAG query with tools and detailed tracking
func (t *TrackedRAGProcessor) QueryWithTools(ctx context.Context, req domain.QueryRequest) (domain.QueryResponse, error) {
	return t.trackQuery(ctx, req, true)
}

// StreamQuery executes a streaming RAG query with tracking
func (t *TrackedRAGProcessor) StreamQuery(ctx context.Context, req domain.QueryRequest, callback func(string)) error {
	// For streaming, we'll track what we can and handle the rest asynchronously
	return t.trackStreamQuery(ctx, req, false, callback)
}

// StreamQueryWithTools executes a streaming RAG query with tools and tracking
func (t *TrackedRAGProcessor) StreamQueryWithTools(ctx context.Context, req domain.QueryRequest, callback func(string)) error {
	return t.trackStreamQuery(ctx, req, true, callback)
}

// trackQuery is the main method that handles query tracking
func (t *TrackedRAGProcessor) trackQuery(ctx context.Context, req domain.QueryRequest, withTools bool) (domain.QueryResponse, error) {
	start := time.Now()

	// Get or create conversation
	conversationID, err := t.getOrCreateConversation(ctx)
	if err != nil {
		return domain.QueryResponse{}, fmt.Errorf("failed to get conversation: %w", err)
	}

	// Create message for the query
	messageID, err := t.createMessage(ctx, conversationID, req.Query, "user")
	if err != nil {
		return domain.QueryResponse{}, fmt.Errorf("failed to create message: %w", err)
	}

	// Create RAG query record
	ragQuery := NewRAGQueryRecord(conversationID, messageID, req)

	// Execute the actual query
	var response domain.QueryResponse
	var queryErr error

	retrievalStart := time.Now()
	if withTools {
		response, queryErr = t.processor.QueryWithTools(ctx, req)
	} else {
		response, queryErr = t.processor.Query(ctx, req)
	}
	retrievalTime := time.Since(retrievalStart)

	// Update timing metrics
	ragQuery.TotalLatency = time.Since(start).Milliseconds()
	ragQuery.RetrievalTime = retrievalTime.Milliseconds()
	ragQuery.GenerationTime = ragQuery.TotalLatency - ragQuery.RetrievalTime

	if queryErr != nil {
		// Record the error
		ragQuery.Success = false
		ragQuery.ErrorMessage = queryErr.Error()

		// Save the failed query record
		if err := t.usageService.repo.CreateRAGQuery(ctx, ragQuery); err != nil {
			log.Printf("Failed to save failed RAG query: %v", err)
		}

		return response, queryErr
	}

	// Update with successful results
	ragQuery.Success = true
	ragQuery.Answer = response.Answer
	ragQuery.ChunksFound = len(response.Sources)

	// Calculate token usage for tracking
	t.calculateTokenUsage(ragQuery, req, response)

	// Save the RAG query record
	if err := t.usageService.repo.CreateRAGQuery(ctx, ragQuery); err != nil {
		log.Printf("Failed to save RAG query: %v", err)
	}

	// Track chunk hits
	go t.trackChunkHits(ctx, ragQuery.ID, response.Sources)

	// Create assistant message for the response
	_, err = t.createMessage(ctx, conversationID, response.Answer, "assistant")
	if err != nil {
		log.Printf("Failed to create assistant message: %v", err)
	}

	return response, nil
}

// trackStreamQuery handles streaming query tracking
func (t *TrackedRAGProcessor) trackStreamQuery(ctx context.Context, req domain.QueryRequest, withTools bool, callback func(string)) error {
	start := time.Now()

	// Get or create conversation
	conversationID, err := t.getOrCreateConversation(ctx)
	if err != nil {
		return fmt.Errorf("failed to get conversation: %w", err)
	}

	// Create message for the query
	messageID, err := t.createMessage(ctx, conversationID, req.Query, "user")
	if err != nil {
		return fmt.Errorf("failed to create message: %w", err)
	}

	// Create RAG query record
	ragQuery := NewRAGQueryRecord(conversationID, messageID, req)

	// Collect the response for tracking
	var responseBuilder string
	wrappedCallback := func(chunk string) {
		responseBuilder += chunk
		callback(chunk)
	}

	// Execute streaming query
	retrievalStart := time.Now()
	var queryErr error
	if withTools {
		queryErr = t.processor.StreamQueryWithTools(ctx, req, wrappedCallback)
	} else {
		queryErr = t.processor.StreamQuery(ctx, req, wrappedCallback)
	}
	retrievalTime := time.Since(retrievalStart)

	// Update timing and results
	ragQuery.TotalLatency = time.Since(start).Milliseconds()
	ragQuery.RetrievalTime = retrievalTime.Milliseconds()
	ragQuery.GenerationTime = ragQuery.TotalLatency - ragQuery.RetrievalTime
	ragQuery.Answer = responseBuilder

	if queryErr != nil {
		ragQuery.Success = false
		ragQuery.ErrorMessage = queryErr.Error()
	} else {
		ragQuery.Success = true
		// Calculate token usage for successful streaming queries
		// Note: For streaming, we don't have access to the original sources
		// So we'll estimate based on the query and response only
		t.calculateTokenUsageStreaming(ragQuery, req)
	}

	// Save the RAG query record asynchronously
	go func() {
		if err := t.usageService.repo.CreateRAGQuery(ctx, ragQuery); err != nil {
			log.Printf("Failed to save streaming RAG query: %v", err)
		}

		// Create assistant message
		if ragQuery.Success {
			if _, err := t.createMessage(ctx, conversationID, responseBuilder, "assistant"); err != nil {
				log.Printf("Failed to create assistant message: %v", err)
			}
		}
	}()

	return queryErr
}

// trackChunkHits saves the chunk hits from the search results
func (t *TrackedRAGProcessor) trackChunkHits(ctx context.Context, ragQueryID string, chunks []domain.Chunk) {
	for i, chunk := range chunks {
		hit := NewRAGChunkHit(ragQueryID, chunk, i+1)

		// For now, mark all chunks as used in generation
		// In the future, we could implement more sophisticated tracking
		hit.UsedInGeneration = true

		if err := t.usageService.repo.CreateChunkHit(ctx, hit); err != nil {
			log.Printf("Failed to save chunk hit: %v", err)
		}
	}
}

// getOrCreateConversation gets or creates a conversation for tracking
func (t *TrackedRAGProcessor) getOrCreateConversation(ctx context.Context) (string, error) {
	// For now, create a new conversation for each query
	// In the future, we could implement session-based conversation tracking
	conversation := &Conversation{
		ID:        uuid.New().String(),
		Title:     "RAG Query Session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err := t.usageService.repo.CreateConversation(ctx, conversation)
	if err != nil {
		return "", err
	}

	return conversation.ID, nil
}

// createMessage creates a message record
func (t *TrackedRAGProcessor) createMessage(ctx context.Context, conversationID, content string, role string) (string, error) {
	message := &Message{
		ID:             uuid.New().String(),
		ConversationID: conversationID,
		Content:        content,
		Role:           role,
		CreatedAt:      time.Now(),
	}

	err := t.usageService.repo.CreateMessage(ctx, message)
	if err != nil {
		return "", err
	}

	return message.ID, nil
}

// Pass-through methods to the underlying processor

func (t *TrackedRAGProcessor) Ingest(ctx context.Context, req domain.IngestRequest) (domain.IngestResponse, error) {
	return t.processor.Ingest(ctx, req)
}

func (t *TrackedRAGProcessor) ListDocuments(ctx context.Context) ([]domain.Document, error) {
	return t.processor.ListDocuments(ctx)
}

func (t *TrackedRAGProcessor) DeleteDocument(ctx context.Context, documentID string) error {
	return t.processor.DeleteDocument(ctx, documentID)
}

func (t *TrackedRAGProcessor) Reset(ctx context.Context) error {
	return t.processor.Reset(ctx)
}

func (t *TrackedRAGProcessor) GetToolRegistry() interface{} {
	return t.processor.GetToolRegistry()
}

func (t *TrackedRAGProcessor) GetToolExecutor() interface{} {
	return t.processor.GetToolExecutor()
}

func (t *TrackedRAGProcessor) RegisterMCPTools(mcpService interface{}) error {
	// Use reflection or type assertion to handle the MCP service
	// For now, assume the method exists
	if method, ok := interface{}(t.processor).(interface{ RegisterMCPTools(interface{}) error }); ok {
		return method.RegisterMCPTools(mcpService)
	}
	return fmt.Errorf("RegisterMCPTools method not available")
}

// calculateTokenUsage calculates and updates token usage for the RAG query
func (t *TrackedRAGProcessor) calculateTokenUsage(ragQuery *RAGQueryRecord, req domain.QueryRequest, response domain.QueryResponse) {
	// Get model name from configuration or use default
	// TODO: This should come from the actual LLM service configuration
	model := "qwen3:4b" // Default from agentgo.toml

	// Build input context: query + retrieved chunks
	inputText := req.Query
	for _, source := range response.Sources {
		inputText += "\n" + source.Content
	}

	// Calculate token counts
	inputTokens := t.tokenCounter.EstimateTokens(inputText, model)
	outputTokens := t.tokenCounter.EstimateTokens(response.Answer, model)
	totalTokens := inputTokens + outputTokens

	// Calculate estimated cost
	estimatedCost := CalculateCost(model, inputTokens, outputTokens)

	// Update the query record
	ragQuery.InputTokens = inputTokens
	ragQuery.OutputTokens = outputTokens
	ragQuery.TotalTokens = totalTokens
	ragQuery.EstimatedCost = estimatedCost
	ragQuery.Model = model

	log.Printf("Token usage calculated - Input: %d, Output: %d, Total: %d, Cost: $%.6f",
		inputTokens, outputTokens, totalTokens, estimatedCost)
}

// calculateTokenUsageStreaming calculates token usage for streaming queries
func (t *TrackedRAGProcessor) calculateTokenUsageStreaming(ragQuery *RAGQueryRecord, req domain.QueryRequest) {
	// Get model name from configuration or use default
	model := "qwen3:4b" // Default from agentgo.toml

	// For streaming queries, we only have the query and response
	// We can't access the retrieved chunks, so we estimate based on typical RAG context
	inputTokens := t.tokenCounter.EstimateTokens(req.Query, model)
	outputTokens := t.tokenCounter.EstimateTokens(ragQuery.Answer, model)

	// Add estimated tokens for retrieved context (approximate)
	// Assume average chunk size and typical number of chunks
	estimatedContextTokens := t.tokenCounter.EstimateTokens("", model) // This will be 0, but we can add estimate
	if ragQuery.ChunksFound > 0 {
		// Estimate average chunk size (500 characters per chunk)
		avgChunkSize := 500 * ragQuery.ChunksFound
		estimatedContextTokens = t.tokenCounter.EstimateTokens(string(make([]byte, avgChunkSize)), model)
	}

	inputTokens += estimatedContextTokens
	totalTokens := inputTokens + outputTokens

	// Calculate estimated cost
	estimatedCost := CalculateCost(model, inputTokens, outputTokens)

	// Update the query record
	ragQuery.InputTokens = inputTokens
	ragQuery.OutputTokens = outputTokens
	ragQuery.TotalTokens = totalTokens
	ragQuery.EstimatedCost = estimatedCost
	ragQuery.Model = model

	log.Printf("Streaming token usage calculated - Input: %d, Output: %d, Total: %d, Cost: $%.6f",
		inputTokens, outputTokens, totalTokens, estimatedCost)
}
