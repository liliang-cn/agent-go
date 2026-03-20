package memory

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockMemoryStore is a mock implementation of domain.MemoryStore
type MockMemoryStore struct {
	mock.Mock
}

func (m *MockMemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	args := m.Called(ctx, memory)
	return args.Error(0)
}

func (m *MockMemoryStore) Search(ctx context.Context, vector []float64, topK int, minScore float64) ([]*domain.MemoryWithScore, error) {
	args := m.Called(ctx, vector, topK, minScore)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.MemoryWithScore), args.Error(1)
}

func (m *MockMemoryStore) SearchBySession(ctx context.Context, sessionID string, vector []float64, topK int) ([]*domain.MemoryWithScore, error) {
	args := m.Called(ctx, sessionID, vector, topK)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.MemoryWithScore), args.Error(1)
}

func (m *MockMemoryStore) SearchByScope(ctx context.Context, vector []float64, scopes []domain.MemoryScope, topK int) ([]*domain.MemoryWithScore, error) {
	args := m.Called(ctx, vector, scopes, topK)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.MemoryWithScore), args.Error(1)
}

func (m *MockMemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	args := m.Called(ctx, memory, scope)
	return args.Error(0)
}

func (m *MockMemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	args := m.Called(ctx, query, topK)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.MemoryWithScore), args.Error(1)
}

func (m *MockMemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Memory), args.Error(1)
}

func (m *MockMemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	args := m.Called(ctx, memory)
	return args.Error(0)
}

func (m *MockMemoryStore) IncrementAccess(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockMemoryStore) GetByType(ctx context.Context, memoryType domain.MemoryType, limit int) ([]*domain.Memory, error) {
	args := m.Called(ctx, memoryType, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.Memory), args.Error(1)
}

func (m *MockMemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Int(1), args.Error(2)
	}
	return args.Get(0).([]*domain.Memory), args.Int(1), args.Error(2)
}

func (m *MockMemoryStore) Delete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockMemoryStore) DeleteBySession(ctx context.Context, sessionID string) error {
	args := m.Called(ctx, sessionID)
	return args.Error(0)
}

func (m *MockMemoryStore) ConfigureBank(ctx context.Context, sessionID string, config *domain.MemoryBankConfig) error {
	args := m.Called(ctx, sessionID, config)
	return args.Error(0)
}

func (m *MockMemoryStore) Reflect(ctx context.Context, sessionID string) (string, error) {
	args := m.Called(ctx, sessionID)
	return args.String(0), args.Error(1)
}

func (m *MockMemoryStore) AddMentalModel(ctx context.Context, model *domain.MentalModel) error {
	args := m.Called(ctx, model)
	return args.Error(0)
}

func (m *MockMemoryStore) InitSchema(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockGenerator is a mock implementation of domain.Generator
type MockGenerator struct {
	mock.Mock
}

func (m *MockGenerator) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	args := m.Called(ctx, prompt, opts)
	return args.String(0), args.Error(1)
}

func (m *MockGenerator) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	args := m.Called(ctx, prompt, opts, callback)
	return args.Error(0)
}

func (m *MockGenerator) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	args := m.Called(ctx, messages, tools, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.GenerationResult), args.Error(1)
}

func (m *MockGenerator) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	args := m.Called(ctx, messages, tools, opts, callback)
	return args.Error(0)
}

func (m *MockGenerator) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	args := m.Called(ctx, prompt, schema, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.StructuredResult), args.Error(1)
}

func (m *MockGenerator) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	args := m.Called(ctx, request)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.IntentResult), args.Error(1)
}

// MockEmbedder is a mock implementation of domain.Embedder
type MockEmbedder struct {
	mock.Mock
}

func (m *MockEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	args := m.Called(ctx, text)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]float64), args.Error(1)
}

func (m *MockEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	args := m.Called(ctx, texts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([][]float64), args.Error(1)
}

func TestService_RetrieveAndInject(t *testing.T) {
	ctx := context.Background()
	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	embedder := new(MockEmbedder)

	config := DefaultConfig()
	service := NewService(store, llm, embedder, config)

	t.Run("Skip retrieval for greetings", func(t *testing.T) {
		query := "hello"
		sessionID := "session-1"

		formatted, memories, err := service.RetrieveAndInject(ctx, query, sessionID)

		assert.NoError(t, err)
		assert.Empty(t, formatted)
		assert.Nil(t, memories)
		store.AssertNotCalled(t, "SearchByScope")
	})

	t.Run("Perform retrieval for information query", func(t *testing.T) {
		query := "what is the project status?"
		sessionID := "session-1"
		vector := []float64{0.1, 0.2, 0.3}

		embedder.On("Embed", ctx, query).Return(vector, nil)

		// Mock Entity Search (which calls Search)
		store.On("Search", ctx, vector, 3, 0.5).Return([]*domain.MemoryWithScore{}, nil)

		expectedMemories := []*domain.MemoryWithScore{
			{
				Memory: &domain.Memory{
					ID:        "mem-1",
					Type:      domain.MemoryTypeFact,
					Content:   "The project is 50% complete.",
					CreatedAt: time.Now(),
				},
				Score: 0.9,
			},
		}

		store.On("SearchByScope", ctx, vector, mock.Anything, mock.Anything).Return(expectedMemories, nil)
		store.On("IncrementAccess", ctx, "mem-1").Return(nil)

		formatted, memories, err := service.RetrieveAndInject(ctx, query, sessionID)

		assert.NoError(t, err)
		assert.Contains(t, formatted, "The project is 50% complete.")
		assert.Len(t, memories, 1)
		assert.Equal(t, "mem-1", memories[0].ID)

		embedder.AssertExpectations(t)
		store.AssertExpectations(t)
	})
	t.Run("NilEmbedder falls back to text search then list", func(t *testing.T) {
		query := "what is the project status?"
		isolatedStore := new(MockMemoryStore)
		nilEmbedService := NewService(isolatedStore, llm, nil, config)

		expectedMemories := []*domain.MemoryWithScore{
			{
				Memory: &domain.Memory{ID: "m1", Content: "Project is on track.", Type: domain.MemoryTypeFact},
				Score:  0.5,
			},
		}
		isolatedStore.On("SearchByText", ctx, query, mock.AnythingOfType("int")).Return(expectedMemories, nil)
		isolatedStore.On("IncrementAccess", ctx, mock.Anything).Return(nil).Maybe()

		formatted, memories, err := nilEmbedService.RetrieveAndInject(ctx, query, "session-no-embed")

		assert.NoError(t, err)
		assert.NotNil(t, memories)
		assert.Contains(t, formatted, "Project is on track.")
	})
}

func TestService_RetrieveAndInjectWithContextUsesScopeChain(t *testing.T) {
	ctx := context.Background()
	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	embedder := new(MockEmbedder)

	service := NewService(store, llm, embedder, DefaultConfig())
	query := "what is the shared squad status?"
	vector := []float64{0.1, 0.2, 0.3}
	queryContext := domain.MemoryQueryContext{
		SessionID: "session-1",
		AgentID:   "Assistant",
		SquadID:   "squad-alpha",
		UserID:    "user-1",
	}
	expectedScopes := DefaultScopeChain(queryContext.SessionID, queryContext.AgentID, queryContext.SquadID, queryContext.UserID).ToSlice()

	embedder.On("Embed", ctx, query).Return(vector, nil)
	store.On("Search", ctx, vector, 3, 0.5).Return([]*domain.MemoryWithScore{}, nil)
	store.On("SearchByScope", ctx, vector, expectedScopes, service.maxMemories*2).Return([]*domain.MemoryWithScore{
		{
			Memory: &domain.Memory{
				ID:        "mem-squad-1",
				Type:      domain.MemoryTypeObservation,
				Content:   "Squad alpha is blocked on deployment approval.",
				CreatedAt: time.Now(),
			},
			Score: 0.92,
		},
	}, nil)
	store.On("IncrementAccess", ctx, "mem-squad-1").Return(nil)

	formatted, memories, err := service.RetrieveAndInjectWithContext(ctx, query, queryContext)

	assert.NoError(t, err)
	assert.Len(t, memories, 1)
	assert.Contains(t, formatted, "Squad alpha is blocked on deployment approval.")
	store.AssertExpectations(t)
	embedder.AssertExpectations(t)
}

func TestService_Add(t *testing.T) {
	ctx := context.Background()
	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	embedder := new(MockEmbedder)

	service := NewService(store, llm, embedder, nil)

	t.Run("Add memory with embedding", func(t *testing.T) {
		memory := &domain.Memory{
			Content: "This is a new memory.",
			Type:    domain.MemoryTypeFact,
		}
		vector := []float64{0.5, 0.6, 0.7}

		embedder.On("Embed", ctx, memory.Content).Return(vector, nil)
		store.On("Store", ctx, mock.MatchedBy(func(m *domain.Memory) bool {
			return m.Content == memory.Content && len(m.Vector) > 0
		})).Return(nil)

		err := service.Add(ctx, memory)

		assert.NoError(t, err)
		embedder.AssertExpectations(t)
		store.AssertExpectations(t)
	})
}

func TestService_StoreIfWorthwhile(t *testing.T) {
	ctx := context.Background()
	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	embedder := new(MockEmbedder)

	service := NewService(store, llm, embedder, nil)

	t.Run("Store worthwhile memory", func(t *testing.T) {
		req := &domain.MemoryStoreRequest{
			SessionID:  "session-1",
			TaskGoal:   "Update project status",
			TaskResult: "The project is now 60% complete after implementing the memory module.",
		}

		structuredResult := &domain.StructuredResult{
			Data: map[string]interface{}{
				"should_store": true,
				"memories": []interface{}{
					map[string]interface{}{
						"type":       "fact",
						"content":    "Project status updated to 60%.",
						"importance": 0.9,
					},
				},
			},
			Raw:   `{"should_store": true, "memories": [{"type": "fact", "content": "Project status updated to 60%.", "importance": 0.9}]}`,
			Valid: true,
		}

		llm.On("GenerateStructured", ctx, mock.Anything, mock.Anything, mock.Anything).Return(structuredResult, nil)

		// Add() will be called internally, which calls Embed and Store
		embedder.On("Embed", ctx, "Project status updated to 60%.").Return([]float64{0.1, 0.2, 0.3}, nil)
		store.On("Store", ctx, mock.MatchedBy(func(m *domain.Memory) bool {
			return m.Content == "Project status updated to 60%."
		})).Return(nil)

		err := service.StoreIfWorthwhile(ctx, req)

		assert.NoError(t, err)
		llm.AssertExpectations(t)
		embedder.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("NoLLM skips classification and does not store", func(t *testing.T) {
		nilLLMService := NewService(store, nil, embedder, nil)
		req := &domain.MemoryStoreRequest{
			SessionID:  "session-nil-llm",
			TaskGoal:   "test",
			TaskResult: "result",
		}
		err := nilLLMService.StoreIfWorthwhile(ctx, req)
		assert.NoError(t, err)
		store.AssertNotCalled(t, "Store")
	})

	t.Run("Do not store if not worthwhile", func(t *testing.T) {
		req := &domain.MemoryStoreRequest{
			SessionID:  "session-1",
			TaskGoal:   "Say hello",
			TaskResult: "The user said hello.",
		}

		structuredResult := &domain.StructuredResult{
			Data: map[string]interface{}{
				"should_store": false,
				"memories":     []interface{}{},
			},
			Raw:   `{"should_store": false, "memories": []}`,
			Valid: true,
		}

		llm.On("GenerateStructured", ctx, mock.Anything, mock.Anything, mock.Anything).Return(structuredResult, nil)

		err := service.StoreIfWorthwhile(ctx, req)

		assert.NoError(t, err)
		llm.AssertExpectations(t)
		store.AssertNotCalled(t, "Store")
	})

	t.Run("Heuristic fallback stores direct declarative preference", func(t *testing.T) {
		isolatedStore := new(MockMemoryStore)
		isolatedLLM := new(MockGenerator)
		isolatedEmbedder := new(MockEmbedder)
		isolatedService := NewService(isolatedStore, isolatedLLM, isolatedEmbedder, nil)

		req := &domain.MemoryStoreRequest{
			SessionID:  "session-heuristic",
			AgentID:    "Assistant",
			TaskGoal:   "Alice prefers coffee over tea.",
			TaskResult: "Understood.",
		}

		isolatedLLM.On("GenerateStructured", ctx, mock.Anything, mock.Anything, mock.Anything).Return(&domain.StructuredResult{
			Raw:   `{"should_store": false, "memories": []}`,
			Valid: true,
		}, nil)
		isolatedEmbedder.On("Embed", ctx, "Alice prefers coffee over tea.").Return([]float64{0.9, 0.1, 0.2}, nil)
		isolatedStore.On("Store", ctx, mock.MatchedBy(func(m *domain.Memory) bool {
			return m.Content == "Alice prefers coffee over tea." &&
				m.Type == domain.MemoryTypePreference &&
				m.ScopeType == domain.MemoryScopeAgent &&
				m.ScopeID == "Assistant"
		})).Return(nil)

		err := isolatedService.StoreIfWorthwhile(ctx, req)

		assert.NoError(t, err)
		isolatedLLM.AssertExpectations(t)
		isolatedEmbedder.AssertExpectations(t)
		isolatedStore.AssertExpectations(t)
	})

	t.Run("Heuristic fallback does not store questions", func(t *testing.T) {
		isolatedStore := new(MockMemoryStore)
		isolatedLLM := new(MockGenerator)
		isolatedEmbedder := new(MockEmbedder)
		isolatedService := NewService(isolatedStore, isolatedLLM, isolatedEmbedder, nil)

		req := &domain.MemoryStoreRequest{
			SessionID:  "session-question",
			TaskGoal:   "What drink does Alice prefer?",
			TaskResult: "I don't know yet.",
		}

		err := isolatedService.StoreIfWorthwhile(ctx, req)

		assert.NoError(t, err)
		isolatedLLM.AssertNotCalled(t, "GenerateStructured")
		isolatedStore.AssertNotCalled(t, "Store")
		isolatedEmbedder.AssertNotCalled(t, "Embed")
	})

	t.Run("Question-like task goals never auto-store even if LLM says yes", func(t *testing.T) {
		isolatedStore := new(MockMemoryStore)
		isolatedLLM := new(MockGenerator)
		isolatedEmbedder := new(MockEmbedder)
		isolatedService := NewService(isolatedStore, isolatedLLM, isolatedEmbedder, nil)

		req := &domain.MemoryStoreRequest{
			SessionID:  "session-question-2",
			TaskGoal:   "What is my favorite snack? Reply with only the snack.",
			TaskResult: "cashew-7714",
		}

		err := isolatedService.StoreIfWorthwhile(ctx, req)

		assert.NoError(t, err)
		isolatedLLM.AssertNotCalled(t, "GenerateStructured")
		isolatedStore.AssertNotCalled(t, "Store")
		isolatedEmbedder.AssertNotCalled(t, "Embed")
	})

	t.Run("Preference memories default to agent scope when agent context exists", func(t *testing.T) {
		isolatedStore := new(MockMemoryStore)
		isolatedLLM := new(MockGenerator)
		isolatedEmbedder := new(MockEmbedder)
		isolatedService := NewService(isolatedStore, isolatedLLM, isolatedEmbedder, nil)

		req := &domain.MemoryStoreRequest{
			SessionID:  "session-2",
			AgentID:    "Assistant",
			SquadID:    "squad-alpha",
			TaskGoal:   "Remember the user's drink preference",
			TaskResult: "Alice prefers coffee over tea.",
		}

		structuredResult := &domain.StructuredResult{
			Raw:   `{"should_store": true, "memories": [{"type": "preference", "content": "Alice prefers coffee over tea.", "importance": 0.9}]}`,
			Valid: true,
		}

		isolatedLLM.On("GenerateStructured", ctx, mock.Anything, mock.Anything, mock.Anything).Return(structuredResult, nil)
		isolatedEmbedder.On("Embed", ctx, "Alice prefers coffee over tea.").Return([]float64{0.3, 0.2, 0.1}, nil)
		isolatedStore.On("Store", ctx, mock.MatchedBy(func(m *domain.Memory) bool {
			return m.Content == "Alice prefers coffee over tea." &&
				m.ScopeType == domain.MemoryScopeAgent &&
				m.ScopeID == "Assistant" &&
				m.SessionID == "agent:Assistant"
		})).Return(nil)

		err := isolatedService.StoreIfWorthwhile(ctx, req)

		assert.NoError(t, err)
		isolatedLLM.AssertExpectations(t)
		isolatedEmbedder.AssertExpectations(t)
		isolatedStore.AssertExpectations(t)
	})

	t.Run("LLM can promote shared facts from session to squad scope", func(t *testing.T) {
		isolatedStore := new(MockMemoryStore)
		isolatedLLM := new(MockGenerator)
		isolatedEmbedder := new(MockEmbedder)
		isolatedService := NewService(isolatedStore, isolatedLLM, isolatedEmbedder, nil)

		req := &domain.MemoryStoreRequest{
			SessionID:  "session-3",
			AgentID:    "Assistant",
			SquadID:    "squad-alpha",
			TaskGoal:   "Summarize the shared deployment coordination status",
			TaskResult: "The team agreed the deployment checklist is approved and shared with all squad members.",
		}

		structuredResult := &domain.StructuredResult{
			Raw:   `{"should_store": true, "memories": [{"type": "fact", "scope": "session", "promote_to": "squad", "content": "Deployment checklist approved for squad alpha.", "importance": 0.88, "scope_reason": "Observed in this session.", "promotion_reason": "Shared coordination state for the whole squad."}]}`,
			Valid: true,
		}

		isolatedLLM.On("GenerateStructured", ctx, mock.Anything, mock.Anything, mock.Anything).Return(structuredResult, nil)
		isolatedEmbedder.On("Embed", ctx, "Deployment checklist approved for squad alpha.").Return([]float64{0.4, 0.2, 0.6}, nil)
		isolatedStore.On("Store", ctx, mock.MatchedBy(func(m *domain.Memory) bool {
			if m.Content != "Deployment checklist approved for squad alpha." {
				return false
			}
			if m.ScopeType != domain.MemoryScopeSquad || m.ScopeID != "squad-alpha" || m.SessionID != "squad:squad-alpha" {
				return false
			}
			if promoted, ok := m.Metadata["promoted"].(bool); !ok || !promoted {
				return false
			}
			if from, ok := m.Metadata["promoted_from_scope"].(string); !ok || from != "session:session-3" {
				return false
			}
			if to, ok := m.Metadata["promoted_to_scope"].(string); !ok || to != "squad:squad-alpha" {
				return false
			}
			return true
		})).Return(nil)

		err := isolatedService.StoreIfWorthwhile(ctx, req)

		assert.NoError(t, err)
		isolatedLLM.AssertExpectations(t)
		isolatedEmbedder.AssertExpectations(t)
		isolatedStore.AssertExpectations(t)
	})
}
