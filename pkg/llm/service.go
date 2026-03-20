package llm

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
)

// Service wraps an LLM with conversation support
type Service struct {
	generator     domain.Generator
	promptManager *prompt.Manager
	chats         map[string]*Conversation
	chatMu        sync.RWMutex
}

// Conversation represents a chat conversation with history
type Conversation struct {
	ID           string
	Messages     []domain.Message
	systemPrompt string
}

// NewService creates a new LLM service with a generator
func NewService(generator domain.Generator) *Service {
	return &Service{
		generator:     generator,
		promptManager: prompt.NewManager(),
		chats:         make(map[string]*Conversation),
	}
}

// SetPromptManager sets a custom prompt manager
func (s *Service) SetPromptManager(m *prompt.Manager) {
	s.promptManager = m
}

// Generate generates text using the configured generator
func (s *Service) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return s.generator.Generate(ctx, prompt, opts)
}

// Stream generates text with streaming using the configured generator
func (s *Service) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return s.generator.Stream(ctx, prompt, opts, callback)
}

// GenerateWithTools generates text with tool calling support
func (s *Service) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	if gen, ok := s.generator.(interface {
		GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error)
	}); ok {
		return gen.GenerateWithTools(ctx, messages, tools, opts)
	}
	// Fallback: simple generate
	fullPrompt := s.buildPromptFromMessages(messages)
	content, err := s.generator.Generate(ctx, fullPrompt, opts)
	return &domain.GenerationResult{Content: content}, err
}

// StreamWithTools generates text with tool calling support in streaming mode
func (s *Service) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	if gen, ok := s.generator.(interface {
		StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error
	}); ok {
		return gen.StreamWithTools(ctx, messages, tools, opts, callback)
	}
	// Fallback: simple stream
	fullPrompt := s.buildPromptFromMessages(messages)
	return s.generator.Stream(ctx, fullPrompt, opts, func(chunk string) {
		_ = callback(&domain.GenerationResult{Content: chunk})
	})
}

// GenerateStructured generates structured JSON output using the configured generator
func (s *Service) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if gen, ok := s.generator.(interface {
		GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error)
	}); ok {
		return gen.GenerateStructured(ctx, prompt, schema, opts)
	}
	return nil, fmt.Errorf("operation GenerateStructured not supported")
}

// buildPromptFromMessages builds a prompt from message list
func (s *Service) buildPromptFromMessages(messages []domain.Message) string {
	var prompt string
	for _, msg := range messages {
		prompt += msg.Role + ": " + msg.Content + "\n"
	}
	return prompt
}

// Health checks the health of the underlying generator
func (s *Service) Health(ctx context.Context) error {
	if gen, ok := s.generator.(interface {
		Health(ctx context.Context) error
	}); ok {
		return gen.Health(ctx)
	}
	return nil // No health check available
}

// ProviderType returns the provider type (if available)
func (s *Service) ProviderType() domain.ProviderType {
	if gen, ok := s.generator.(interface{ ProviderType() domain.ProviderType }); ok {
		return gen.ProviderType()
	}
	return "" // Unknown
}

// RecognizeIntent analyzes a user request (if supported)
func (s *Service) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	if gen, ok := s.generator.(interface {
		RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error)
	}); ok {
		return gen.RecognizeIntent(ctx, request)
	}
	return nil, fmt.Errorf("RecognizeIntent not supported")
}

// Compact summarizes a list of messages into key points
func (s *Service) Compact(ctx context.Context, messages []domain.Message) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}

	systemPrompt, err := s.promptManager.Render(prompt.LLMCompact, nil)
	if err != nil {
		systemPrompt = "You are a helpful assistant that summarizes long conversations."
	}

	// Build the messages for compaction
	compactionMessages := []domain.Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Please summarize the following conversation into key points:\n\n%s", s.buildPromptFromMessages(messages)),
		},
	}

	// Generate the summary
	result, err := s.GenerateWithTools(ctx, compactionMessages, nil, &domain.GenerationOptions{
		Temperature: 0.3, // Lower temperature for more focused summary
		MaxTokens:   1000,
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate summary for compaction: %w", err)
	}

	return result.Content, nil
}

// ============================================
// Chat API - Simple conversation interface
// ============================================

// Chat sends a message and returns response with conversation history
// Creates a new conversation with UUID if no conversation exists
func (s *Service) Chat(ctx context.Context, message string) (string, error) {
	s.chatMu.Lock()
	if len(s.chats) == 0 {
		// Create default conversation
		conv := &Conversation{
			ID:       uuid.New().String(),
			Messages: []domain.Message{},
		}
		s.chats[conv.ID] = conv
	}
	// Get first conversation
	var convID string
	for id := range s.chats {
		convID = id
		break
	}
	s.chatMu.Unlock()

	return s.ChatWithID(ctx, convID, message)
}

// ChatWithID sends a message to a specific conversation
func (s *Service) ChatWithID(ctx context.Context, convID, message string) (string, error) {
	s.chatMu.Lock()
	conv, exists := s.chats[convID]
	if !exists {
		conv = &Conversation{
			ID:       convID,
			Messages: []domain.Message{},
		}
		s.chats[convID] = conv
	}
	s.chatMu.Unlock()

	// Add user message
	conv.Messages = append(conv.Messages, domain.Message{
		Role:    "user",
		Content: message,
	})

	// Build messages for API
	messages := make([]domain.Message, len(conv.Messages))
	copy(messages, conv.Messages)

	// Generate response
	result, err := s.GenerateWithTools(ctx, messages, nil, &domain.GenerationOptions{
		Temperature: 0.7,
		MaxTokens:   2000,
	})
	if err != nil {
		return "", err
	}

	// Add assistant response
	response := result.Content
	conv.Messages = append(conv.Messages, domain.Message{
		Role:    "assistant",
		Content: response,
	})

	return response, nil
}

// CurrentConversationID returns the current conversation ID
func (s *Service) CurrentConversationID() string {
	s.chatMu.RLock()
	defer s.chatMu.RUnlock()
	for id := range s.chats {
		return id
	}
	return ""
}

// SetConversation sets a specific conversation as current
func (s *Service) SetConversation(convID string) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if _, exists := s.chats[convID]; !exists {
		s.chats[convID] = &Conversation{
			ID:       convID,
			Messages: []domain.Message{},
		}
	}
}

// ResetConversation clears the current conversation and starts a new one
func (s *Service) ResetConversation() string {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	// Clear all conversations
	s.chats = make(map[string]*Conversation)
	// Create new default conversation
	convID := uuid.New().String()
	s.chats[convID] = &Conversation{
		ID:       convID,
		Messages: []domain.Message{},
	}
	return convID
}

// GetMessages returns all messages in a conversation
func (s *Service) GetMessages(convID string) []domain.Message {
	s.chatMu.RLock()
	defer s.chatMu.RUnlock()
	if conv, exists := s.chats[convID]; exists {
		messages := make([]domain.Message, len(conv.Messages))
		copy(messages, conv.Messages)
		return messages
	}
	return nil
}

// SetSystemPrompt sets the system prompt for a conversation
func (s *Service) SetSystemPrompt(convID, prompt string) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if conv, exists := s.chats[convID]; exists {
		conv.systemPrompt = prompt
		// Prepend system message if not exists
		if len(conv.Messages) == 0 || conv.Messages[0].Role != "system" {
			conv.Messages = append([]domain.Message{{Role: "system", Content: prompt}}, conv.Messages...)
		} else {
			conv.Messages[0] = domain.Message{Role: "system", Content: prompt}
		}
	}
}
