package usage

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestService(t *testing.T) (*Service, func()) {
	// Create a temporary directory for test database
	tempDir, err := os.MkdirTemp("", "usage_test_*")
	require.NoError(t, err)

	cfg := &config.Config{
		// We'll use the default data dir since DataDir field doesn't exist yet
	}

	service, err := NewServiceWithDataDir(cfg, tempDir)
	require.NoError(t, err)

	cleanup := func() {
		service.Close()
		os.RemoveAll(tempDir)
	}

	return service, cleanup
}

func TestService_StartConversation(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	conversation, err := service.StartConversation(ctx, "Test Conversation")
	assert.NoError(t, err)
	assert.NotEmpty(t, conversation.ID)
	assert.Equal(t, "Test Conversation", conversation.Title)
	assert.False(t, conversation.CreatedAt.IsZero())
}

func TestService_AddMessage(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Start a conversation first
	conversation, err := service.StartConversation(ctx, "Test Conversation")
	require.NoError(t, err)

	// Add a user message
	userMessage, err := service.AddMessage(ctx, "user", "Hello, how are you?")
	assert.NoError(t, err)
	assert.NotEmpty(t, userMessage.ID)
	assert.Equal(t, conversation.ID, userMessage.ConversationID)
	assert.Equal(t, "user", userMessage.Role)
	assert.Equal(t, "Hello, how are you?", userMessage.Content)
	assert.Greater(t, userMessage.TokenCount, 0)

	// Add an assistant message
	assistantMessage, err := service.AddMessage(ctx, "assistant", "I'm doing well, thank you!")
	assert.NoError(t, err)
	assert.Equal(t, "assistant", assistantMessage.Role)
	assert.Equal(t, conversation.ID, assistantMessage.ConversationID)
}

func TestService_TrackLLMCall(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Start a conversation first
	_, err := service.StartConversation(ctx, "Test Conversation")
	require.NoError(t, err)

	startTime := time.Now().Add(-100 * time.Millisecond)

	record, err := service.TrackLLMCall(ctx, "openai", "gpt-4", "test input", "test output", startTime)
	assert.NoError(t, err)
	assert.NotEmpty(t, record.ID)
	assert.Equal(t, CallTypeLLM, record.CallType)
	assert.Equal(t, "openai", record.Provider)
	assert.Equal(t, "gpt-4", record.Model)
	assert.Greater(t, record.InputTokens, 0)
	assert.Greater(t, record.OutputTokens, 0)
	assert.Greater(t, record.TotalTokens, 0)
	assert.Greater(t, record.Latency, int64(0))
	assert.True(t, record.Success)
}

func TestService_TrackMCPCall(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	startTime := time.Now().Add(-50 * time.Millisecond)
	params := map[string]interface{}{
		"path": "/test/file.txt",
		"mode": "read",
	}

	record, err := service.TrackMCPCall(ctx, "filesystem", params, startTime)
	assert.NoError(t, err)
	assert.NotEmpty(t, record.ID)
	assert.Equal(t, CallTypeMCP, record.CallType)
	assert.Equal(t, "mcp", record.Provider)
	assert.Equal(t, "filesystem", record.Model)
	assert.Greater(t, record.Latency, int64(0))
	assert.True(t, record.Success)
	assert.NotEmpty(t, record.RequestMetadata)
}

func TestService_TrackRAGCall(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	startTime := time.Now().Add(-200 * time.Millisecond)

	record, err := service.TrackRAGCall(ctx, "query", "test query about documents", 5, startTime)
	assert.NoError(t, err)
	assert.NotEmpty(t, record.ID)
	assert.Equal(t, CallTypeRAG, record.CallType)
	assert.Equal(t, "rag", record.Provider)
	assert.Equal(t, "query", record.Model)
	assert.Greater(t, record.InputTokens, 0)
	assert.Greater(t, record.Latency, int64(0))
	assert.True(t, record.Success)
	assert.NotEmpty(t, record.RequestMetadata)
}

func TestService_TrackError(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	startTime := time.Now().Add(-100 * time.Millisecond)

	record, err := service.TrackError(ctx, CallTypeLLM, "openai", "gpt-4", "API rate limit exceeded", startTime)
	assert.NoError(t, err)
	assert.NotEmpty(t, record.ID)
	assert.Equal(t, CallTypeLLM, record.CallType)
	assert.Equal(t, "openai", record.Provider)
	assert.Equal(t, "gpt-4", record.Model)
	assert.False(t, record.Success)
	assert.Equal(t, "API rate limit exceeded", record.ErrorMessage)
	assert.Greater(t, record.Latency, int64(0))
}

func TestService_GetUsageStats(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create some test data
	startTime := time.Now().Add(-100 * time.Millisecond)

	// Track several calls
	_, err := service.TrackLLMCall(ctx, "openai", "gpt-4", "input1", "output1", startTime)
	require.NoError(t, err)

	_, err = service.TrackLLMCall(ctx, "openai", "gpt-3.5", "input2", "output2", startTime)
	require.NoError(t, err)

	_, err = service.TrackMCPCall(ctx, "filesystem", map[string]interface{}{"path": "/test"}, startTime)
	require.NoError(t, err)

	// Get overall stats
	stats, err := service.GetUsageStats(ctx, &UsageFilter{})
	assert.NoError(t, err)
	assert.Equal(t, int64(3), stats.TotalCalls)
	assert.Greater(t, stats.TotalTokens, int64(0))
	assert.Greater(t, stats.AverageLatency, 0.0)
	assert.Equal(t, 1.0, stats.SuccessRate)

	// Get stats by provider
	statsByProvider, err := service.GetUsageStatsByProvider(ctx, &UsageFilter{})
	assert.NoError(t, err)
	assert.Contains(t, statsByProvider, "openai")
	assert.Contains(t, statsByProvider, "mcp")
	assert.Equal(t, int64(2), statsByProvider["openai"].TotalCalls)
	assert.Equal(t, int64(1), statsByProvider["mcp"].TotalCalls)
}

func TestService_GetConversationHistory(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Start a conversation and add messages
	conversation, err := service.StartConversation(ctx, "Test Conversation")
	require.NoError(t, err)

	_, err = service.AddMessage(ctx, "user", "Hello!")
	require.NoError(t, err)

	_, err = service.AddMessage(ctx, "assistant", "Hi there!")
	require.NoError(t, err)

	// Get conversation history
	history, err := service.GetConversationHistory(ctx, conversation.ID)
	assert.NoError(t, err)
	assert.Equal(t, conversation.ID, history.Conversation.ID)
	assert.Len(t, history.Messages, 2)
	assert.Equal(t, "user", history.Messages[0].Role)
	assert.Equal(t, "assistant", history.Messages[1].Role)
	assert.Greater(t, history.TokenUsage, 0)
}

func TestService_ExportConversation(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Start a conversation and add messages
	conversation, err := service.StartConversation(ctx, "Test Conversation")
	require.NoError(t, err)

	_, err = service.AddMessage(ctx, "user", "Hello!")
	require.NoError(t, err)

	// Export the conversation
	data, err := service.ExportConversation(ctx, conversation.ID)
	assert.NoError(t, err)
	assert.NotEmpty(t, data)

	// Should be valid JSON
	var exported map[string]interface{}
	err = json.Unmarshal(data, &exported)
	assert.NoError(t, err)
	assert.Contains(t, exported, "conversation")
	assert.Contains(t, exported, "messages")
	assert.Contains(t, exported, "usage")
}

func TestService_DeleteConversation(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Start a conversation
	conversation, err := service.StartConversation(ctx, "Test Conversation")
	require.NoError(t, err)

	// Verify it exists
	_, err = service.GetConversationHistory(ctx, conversation.ID)
	assert.NoError(t, err)

	// Delete it
	err = service.DeleteConversation(ctx, conversation.ID)
	assert.NoError(t, err)

	// Verify it's gone
	_, err = service.GetConversationHistory(ctx, conversation.ID)
	assert.Error(t, err)
}

func TestTokenCounter_EstimateTokens(t *testing.T) {
	counter := NewTokenCounter()

	// Test basic estimation
	tokens := counter.EstimateTokens("Hello world", "gpt-4")
	assert.Greater(t, tokens, 0)
	assert.Less(t, tokens, 10) // Should be reasonable for a short phrase

	// Test empty string
	tokens = counter.EstimateTokens("", "gpt-4")
	assert.Equal(t, 0, tokens)

	// Test longer text
	longText := "This is a much longer piece of text that should result in more tokens being estimated."
	tokens = counter.EstimateTokens(longText, "gpt-4")
	assert.Greater(t, tokens, 10)

	// Test unknown model defaults to reasonable estimation
	tokens = counter.EstimateTokens("Hello world", "unknown-model")
	assert.Greater(t, tokens, 0)
}

func TestCalculateCost(t *testing.T) {
	// Test known models
	cost := CalculateCost("gpt-4", 1000, 500)
	assert.Greater(t, cost, 0.0)

	cost = CalculateCost("gpt-3.5-turbo", 1000, 500)
	assert.Greater(t, cost, 0.0)

	// Test unknown model (should return 0)
	cost = CalculateCost("unknown-model", 1000, 500)
	assert.Equal(t, 0.0, cost)

	// Test zero tokens
	cost = CalculateCost("gpt-4", 0, 0)
	assert.Equal(t, 0.0, cost)
}
