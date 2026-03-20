package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewVectorStore(t *testing.T) {
	// Create a temporary directory for test databases
	tmpDir, err := os.MkdirTemp("", "factory_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name        string
		config      StoreConfig
		shouldError bool
		errorMsg    string
	}{
		{
			name: "SQLite store with explicit path",
			config: StoreConfig{
				Type: "sqlite",
				Parameters: map[string]interface{}{
					"db_path": filepath.Join(tmpDir, "test1.db"),
				},
			},
			shouldError: false,
		},
		{
			name: "SQLite store with cortexdb alias",
			config: StoreConfig{
				Type: "cortexdb",
				Parameters: map[string]interface{}{
					"db_path": filepath.Join(tmpDir, "test2.db"),
				},
			},
			shouldError: false,
		},
		{
			name: "SQLite store with empty db_path uses default",
			config: StoreConfig{
				Type: "sqlite",
				Parameters: map[string]interface{}{
					"db_path": "",
				},
			},
			shouldError: false, // Will use default path ./.agentgo/data/rag.db
		},
		{
			name: "Unsupported store type",
			config: StoreConfig{
				Type: "unsupported",
				Parameters: map[string]interface{}{
					"some_param": "value",
				},
			},
			shouldError: true,
			errorMsg:    "unsupported vector store type: unsupported",
		},
		{
			name: "Empty store type",
			config: StoreConfig{
				Type:       "",
				Parameters: map[string]interface{}{},
			},
			shouldError: true,
			errorMsg:    "unsupported vector store type: ",
		},
		{
			name: "Qdrant store (connection expected to fail)",
			config: StoreConfig{
				Type: "qdrant",
				Parameters: map[string]interface{}{
					"url":        "http://localhost:6333",
					"api_key":    "test-key",
					"collection": "test-collection",
				},
			},
			shouldError: true,
			errorMsg:    "", // Expect any connection error since no server is running
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create default directory if using default path
			if tt.config.Type == "sqlite" || tt.config.Type == "cortexdb" {
				if tt.config.Parameters == nil || tt.config.Parameters["db_path"] == nil || tt.config.Parameters["db_path"] == "" {
					// Create default directory for this test
					err := os.MkdirAll("./.agentgo/data", 0755)
					require.NoError(t, err)
					defer os.RemoveAll("./.agentgo")
				}
			}

			store, err := NewVectorStore(tt.config)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.EqualError(t, err, tt.errorMsg)
				}
				assert.Nil(t, store)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, store)

				// Clean up the store
				if sqliteStore, ok := store.(*SQLiteStore); ok {
					defer sqliteStore.Close()
				}

				// Verify it's actually a SQLiteStore
				_, ok := store.(*SQLiteStore)
				assert.True(t, ok, "Expected SQLiteStore implementation")
			}
		})
	}
}

func TestNewVectorStore_ParameterTypes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "factory_param_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name        string
		parameters  map[string]interface{}
		shouldError bool
		description string
	}{
		{
			name: "String db_path",
			parameters: map[string]interface{}{
				"db_path": filepath.Join(tmpDir, "string_path.db"),
			},
			shouldError: false,
			description: "Should handle string db_path",
		},
		{
			name: "Non-string db_path",
			parameters: map[string]interface{}{
				"db_path": 12345,
			},
			shouldError: false, // Falls back to default path
			description: "Should fall back to default when db_path is not a string",
		},
		{
			name: "Null db_path",
			parameters: map[string]interface{}{
				"db_path": nil,
			},
			shouldError: false, // Falls back to default path
			description: "Should fall back to default when db_path is nil",
		},
		{
			name: "Additional parameters (ignored)",
			parameters: map[string]interface{}{
				"db_path":    filepath.Join(tmpDir, "extra_params.db"),
				"dimensions": 1536,
				"extra":      "ignored",
			},
			shouldError: false,
			description: "Should ignore extra parameters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create default directory if db_path would fall back to default
			needsDefaultDir := false
			if dbPath, ok := tt.parameters["db_path"]; !ok || dbPath == nil || dbPath == "" {
				needsDefaultDir = true
			} else if _, isString := dbPath.(string); !isString {
				needsDefaultDir = true
			}

			if needsDefaultDir {
				err := os.MkdirAll("./.agentgo/data", 0755)
				require.NoError(t, err)
				defer os.RemoveAll("./.agentgo")
			}

			config := StoreConfig{
				Type:       "sqlite",
				Parameters: tt.parameters,
			}

			store, err := NewVectorStore(config)

			if tt.shouldError {
				assert.Error(t, err, tt.description)
				assert.Nil(t, store)
			} else {
				assert.NoError(t, err, tt.description)
				assert.NotNil(t, store)

				// Clean up
				if sqliteStore, ok := store.(*SQLiteStore); ok {
					defer sqliteStore.Close()
				}
			}
		})
	}
}

func TestNewDocumentStoreFor(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "doc_store_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Run("SQLiteStore returns DocumentStore", func(t *testing.T) {
		// Create a SQLiteStore
		dbPath := filepath.Join(tmpDir, "doc_test.db")
		vectorStore, err := NewSQLiteStore(dbPath, "hnsw")
		require.NoError(t, err)
		defer vectorStore.Close()

		// Get document store for it
		docStore := NewDocumentStoreFor(vectorStore)
		assert.NotNil(t, docStore, "Should return a DocumentStore for SQLiteStore")

		// Verify it's actually a DocumentStore
		_, ok := docStore.(*DocumentStore)
		assert.True(t, ok, "Should be a DocumentStore instance")
	})

	t.Run("Non-SQLiteStore returns nil", func(t *testing.T) {
		// Create a mock store that's not SQLiteStore
		mockStore := &mockVectorStore{}

		// Get document store for it
		docStore := NewDocumentStoreFor(mockStore)
		assert.Nil(t, docStore, "Should return nil for non-SQLiteStore")
	})
}

// Mock implementation for testing
type mockVectorStore struct{}

func (m *mockVectorStore) Store(ctx context.Context, chunks []domain.Chunk) error {
	return nil
}

func (m *mockVectorStore) Search(ctx context.Context, vector []float64, topK int) ([]domain.Chunk, error) {
	return nil, nil
}

func (m *mockVectorStore) SearchWithFilters(ctx context.Context, vector []float64, topK int, filters map[string]interface{}) ([]domain.Chunk, error) {
	return nil, nil
}

func (m *mockVectorStore) SearchWithReranker(ctx context.Context, vector []float64, queryText string, topK int, strategy string, boost float64) ([]domain.Chunk, error) {
	return nil, nil
}

func (m *mockVectorStore) SearchWithDiversity(ctx context.Context, vector []float64, topK int, lambda float32) ([]domain.Chunk, error) {
	return nil, nil
}

func (m *mockVectorStore) Delete(ctx context.Context, documentID string) error {
	return nil
}

func (m *mockVectorStore) List(ctx context.Context) ([]domain.Document, error) {
	return nil, nil
}

func (m *mockVectorStore) Reset(ctx context.Context) error {
	return nil
}

func (m *mockVectorStore) GetGraphStore() domain.GraphStore {
	return nil
}

func (m *mockVectorStore) GetChatStore() domain.ChatStore {
	return nil
}

func TestStoreConfig(t *testing.T) {
	t.Run("Config structure", func(t *testing.T) {
		config := StoreConfig{
			Type: "sqlite",
			Parameters: map[string]interface{}{
				"db_path":    "/path/to/db",
				"dimensions": 1536,
			},
		}

		assert.Equal(t, "sqlite", config.Type)
		assert.Equal(t, "/path/to/db", config.Parameters["db_path"])
		assert.Equal(t, 1536, config.Parameters["dimensions"])
	})

	t.Run("Empty config", func(t *testing.T) {
		config := StoreConfig{}
		assert.Empty(t, config.Type)
		assert.Nil(t, config.Parameters)
	})
}

func BenchmarkNewVectorStore(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "bench_factory")
	require.NoError(b, err)
	defer os.RemoveAll(tmpDir)

	config := StoreConfig{
		Type: "sqlite",
		Parameters: map[string]interface{}{
			"db_path": filepath.Join(tmpDir, "bench.db"),
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store, err := NewVectorStore(config)
		if err != nil {
			b.Fatal(err)
		}
		if sqliteStore, ok := store.(*SQLiteStore); ok {
			sqliteStore.Close()
		}
	}
}

func TestFactoryConcurrency(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "concurrent_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Test concurrent store creation
	numGoroutines := 10
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			config := StoreConfig{
				Type: "sqlite",
				Parameters: map[string]interface{}{
					"db_path": filepath.Join(tmpDir, fmt.Sprintf("concurrent_%d.db", id)),
				},
			}

			store, err := NewVectorStore(config)
			assert.NoError(t, err)
			assert.NotNil(t, store)

			if sqliteStore, ok := store.(*SQLiteStore); ok {
				defer sqliteStore.Close()
			}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}
}
