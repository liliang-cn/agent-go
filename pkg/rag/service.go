package rag

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Service provides RAG (Retrieval-Augmented Generation) functionality
type Service struct {
	store              domain.VectorStore
	embedder           domain.EmbedderProvider
	llm                domain.Generator // LLM for generating responses
	relevanceThreshold float64
	conversations      map[string]*Conversation
	convMu             sync.RWMutex
}

// Conversation represents a RAG chat conversation
type Conversation struct {
	ID       string
	Messages []domain.Message
}

// NewService creates a new RAG service
func NewService(store domain.VectorStore, embedder domain.EmbedderProvider) *Service {
	return &Service{
		store:              store,
		embedder:           embedder,
		relevanceThreshold: 0.7,
		conversations:      make(map[string]*Conversation),
	}
}

// NewServiceWithLLM creates a new RAG service with LLM for chat
func NewServiceWithLLM(store domain.VectorStore, embedder domain.EmbedderProvider, llm domain.Generator) *Service {
	return &Service{
		store:              store,
		embedder:           embedder,
		llm:                llm,
		relevanceThreshold: 0.7,
		conversations:      make(map[string]*Conversation),
	}
}

// NewServiceWithThreshold creates a new RAG service with custom relevance threshold
func NewServiceWithThreshold(store domain.VectorStore, embedder domain.EmbedderProvider, threshold float64) *Service {
	return &Service{
		store:              store,
		embedder:           embedder,
		relevanceThreshold: threshold,
		conversations:      make(map[string]*Conversation),
	}
}

// SetLLM sets the LLM for generating responses
func (s *Service) SetLLM(llm domain.Generator) {
	s.llm = llm
}

// SearchResult represents a search result with relevance information
type SearchResult struct {
	Chunk      *domain.Chunk
	Score      float64 // Similarity score (0-1, higher is better)
	IsRelevant bool    // Whether this result meets the relevance threshold
}

// SearchOptions configures the search behavior
type SearchOptions struct {
	MaxResults        int     // Maximum number of results to return
	MinRelevanceScore float64 // Minimum score for a result to be considered relevant
	IncludeIrrelevant bool    // If true, return all results even if below threshold
}

// DefaultSearchOptions returns sensible defaults
func DefaultSearchOptions() SearchOptions {
	return SearchOptions{
		MaxResults:        5,
		MinRelevanceScore: 0.7,
		IncludeIrrelevant: false,
	}
}

// SearchWithRelevance performs semantic search and filters by relevance
func (s *Service) SearchWithRelevance(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if s.store == nil || s.embedder == nil {
		return nil, fmt.Errorf("RAG service not properly initialized")
	}

	// Generate embeddings for the query
	embeddings, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate embeddings: %w", err)
	}

	// Search for chunks
	chunks, err := s.store.Search(ctx, embeddings, opts.MaxResults*2) // Get more to filter
	if err != nil {
		return nil, fmt.Errorf("failed to search chunks: %w", err)
	}

	// Calculate similarity scores and filter by relevance
	var results []SearchResult
	for _, chunk := range chunks {
		// Calculate cosine similarity if we have chunk embeddings
		// For now, we'll use a simple heuristic based on content matching
		score := s.calculateRelevanceScore(query, &chunk)

		isRelevant := score >= opts.MinRelevanceScore

		if isRelevant || opts.IncludeIrrelevant {
			results = append(results, SearchResult{
				Chunk:      &chunk,
				Score:      score,
				IsRelevant: isRelevant,
			})
		}
	}

	// Sort by score (highest first)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Limit to MaxResults
	if len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}

	return results, nil
}

// GetRelevantContext retrieves relevant context for a query
func (s *Service) GetRelevantContext(ctx context.Context, query string, maxDocs int) (string, int, error) {
	opts := SearchOptions{
		MaxResults:        maxDocs,
		MinRelevanceScore: s.relevanceThreshold,
		IncludeIrrelevant: false,
	}

	results, err := s.SearchWithRelevance(ctx, query, opts)
	if err != nil {
		return "", 0, err
	}

	// Only include relevant results
	var relevantDocs []SearchResult
	for _, r := range results {
		if r.IsRelevant {
			relevantDocs = append(relevantDocs, r)
		}
	}

	if len(relevantDocs) == 0 {
		return "", 0, nil // No relevant context found
	}

	// Build context string
	var contexts []string
	for _, result := range relevantDocs {
		source := "unknown"
		if result.Chunk.Metadata != nil && result.Chunk.Metadata["source"] != nil {
			source = fmt.Sprintf("%v", result.Chunk.Metadata["source"])
		}

		// Include score in context for transparency
		contexts = append(contexts, fmt.Sprintf(
			"[Source: %s | Relevance: %.2f]\n%s",
			source,
			result.Score,
			result.Chunk.Content,
		))
	}

	contextStr := strings.Join(contexts, "\n\n---\n\n")
	return contextStr, len(relevantDocs), nil
}

// calculateRelevanceScore calculates a simple relevance score between query and chunk
// This is a placeholder - in production, you'd use proper vector similarity
func (s *Service) calculateRelevanceScore(query string, chunk *domain.Chunk) float64 {
	// Convert to lowercase for comparison
	queryLower := strings.ToLower(query)
	contentLower := strings.ToLower(chunk.Content)

	// Split query into words
	queryWords := strings.Fields(queryLower)
	if len(queryWords) == 0 {
		return 0.0
	}

	// Count matching words
	matchCount := 0
	for _, word := range queryWords {
		if len(word) > 2 && strings.Contains(contentLower, word) {
			matchCount++
		}
	}

	// Calculate score based on match ratio
	score := float64(matchCount) / float64(len(queryWords))

	// Boost score if query appears as exact substring
	if strings.Contains(contentLower, queryLower) {
		score = math.Min(score*1.5, 1.0)
	}

	// Add small random component to simulate vector similarity variance
	// In production, this would be actual cosine similarity from embeddings
	score = score*0.9 + 0.1*0.5 // Adding 0.05 base score

	return math.Min(score, 1.0)
}

// SetRelevanceThreshold updates the relevance threshold
func (s *Service) SetRelevanceThreshold(threshold float64) {
	s.relevanceThreshold = math.Max(0.0, math.Min(1.0, threshold))
}

// IsAvailable checks if RAG service is available
func (s *Service) IsAvailable() bool {
	return s.store != nil && s.embedder != nil
}

// ============================================
// Chat API with Memory
// ============================================

// Chat sends a message with RAG context and conversation history
func (s *Service) Chat(ctx context.Context, message string) (string, error) {
	s.convMu.Lock()
	if len(s.conversations) == 0 {
		conv := &Conversation{
			ID:       uuid.New().String(),
			Messages: []domain.Message{},
		}
		s.conversations[conv.ID] = conv
	}
	var convID string
	for id := range s.conversations {
		convID = id
		break
	}
	s.convMu.Unlock()

	return s.ChatWithID(ctx, convID, message)
}

// ChatWithID sends a message to a specific conversation with RAG context
func (s *Service) ChatWithID(ctx context.Context, convID, message string) (string, error) {
	s.convMu.Lock()
	conv, exists := s.conversations[convID]
	if !exists {
		conv = &Conversation{
			ID:       convID,
			Messages: []domain.Message{},
		}
		s.conversations[convID] = conv
	}
	s.convMu.Unlock()

	// Add user message
	conv.Messages = append(conv.Messages, domain.Message{
		Role:    "user",
		Content: message,
	})

	// Get relevant context
	context, docsFound, err := s.GetRelevantContext(ctx, message, 3)
	if err != nil {
		return "", fmt.Errorf("failed to get context: %w", err)
	}

	// Build prompt with context and history
	var prompt strings.Builder
	if docsFound > 0 {
		prompt.WriteString("Relevant context:\n")
		prompt.WriteString(context)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("Conversation:\n")
	for _, msg := range conv.Messages {
		prompt.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, msg.Content))
	}

	// Generate response
	var response string
	if s.llm != nil {
		response, err = s.llm.Generate(ctx, prompt.String(), &domain.GenerationOptions{
			Temperature: 0.7,
			MaxTokens:   1000,
		})
		if err != nil {
			return "", fmt.Errorf("failed to generate: %w", err)
		}
	} else {
		// No LLM, return context only
		if docsFound > 0 {
			response = fmt.Sprintf("Found %d relevant documents:\n%s", docsFound, context)
		} else {
			response = "No relevant information found."
		}
	}

	// Add assistant response
	conv.Messages = append(conv.Messages, domain.Message{
		Role:    "assistant",
		Content: response,
	})

	return response, nil
}

// CurrentConversationID returns the current conversation ID
func (s *Service) CurrentConversationID() string {
	s.convMu.RLock()
	defer s.convMu.RUnlock()
	for id := range s.conversations {
		return id
	}
	return ""
}

// ResetConversation clears current conversation and starts a new one
func (s *Service) ResetConversation() string {
	s.convMu.Lock()
	defer s.convMu.Unlock()
	s.conversations = make(map[string]*Conversation)
	convID := uuid.New().String()
	s.conversations[convID] = &Conversation{
		ID:       convID,
		Messages: []domain.Message{},
	}
	return convID
}

// GetConversationMessages returns all messages in a conversation
func (s *Service) GetConversationMessages(convID string) []domain.Message {
	s.convMu.RLock()
	defer s.convMu.RUnlock()
	if conv, exists := s.conversations[convID]; exists {
		messages := make([]domain.Message, len(conv.Messages))
		copy(messages, conv.Messages)
		return messages
	}
	return nil
}
