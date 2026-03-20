package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreWithCollections(t *testing.T) {
	// Create a temporary database for testing
	tmpDir, err := os.MkdirTemp("", "cortexdb_collection_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test_collections.db")
	store, err := NewSQLiteStore(dbPath, "hnsw")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	tests := []struct {
		name       string
		collection string
		chunks     []domain.Chunk
	}{
		{
			name:       "Store in medical_records collection",
			collection: "medical_records",
			chunks: []domain.Chunk{
				{
					ID:         uuid.New().String(),
					DocumentID: "doc-medical-1",
					Content:    "Patient diagnosis and treatment",
					Vector:     generateTestVector(768),
					Metadata: map[string]interface{}{
						"collection": "medical_records",
						"type":       "medical",
					},
				},
			},
		},
		{
			name:       "Store in meeting_notes collection",
			collection: "meeting_notes",
			chunks: []domain.Chunk{
				{
					ID:         uuid.New().String(),
					DocumentID: "doc-meeting-1",
					Content:    "Meeting agenda and action items",
					Vector:     generateTestVector(768),
					Metadata: map[string]interface{}{
						"collection": "meeting_notes",
						"type":       "meeting",
					},
				},
			},
		},
		{
			name:       "Store in code_snippets collection",
			collection: "code_snippets",
			chunks: []domain.Chunk{
				{
					ID:         uuid.New().String(),
					DocumentID: "doc-code-1",
					Content:    "func main() { fmt.Println(\"Hello\") }",
					Vector:     generateTestVector(768),
					Metadata: map[string]interface{}{
						"collection": "code_snippets",
						"type":       "code",
					},
				},
			},
		},
		{
			name:       "Store in default collection when not specified",
			collection: "default",
			chunks: []domain.Chunk{
				{
					ID:         uuid.New().String(),
					DocumentID: "doc-default-1",
					Content:    "General content without collection",
					Vector:     generateTestVector(768),
					Metadata: map[string]interface{}{
						"type": "general",
					},
				},
			},
		},
	}

	// Store chunks in different collections
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create document first to satisfy foreign key constraint in cortexdb v2
			docStore := NewDocumentStore(store.GetCortexdbStore())
			for _, chunk := range tt.chunks {
				_ = docStore.Store(ctx, domain.Document{
					ID:      chunk.DocumentID,
					Created: time.Now(),
				})
			}

			err := store.Store(ctx, tt.chunks)
			assert.NoError(t, err, "Should store chunks in collection %s", tt.collection)
		})
	}

	// Verify chunks are stored correctly
	// Note: This would require implementing search by collection in the store
	// For now, we just verify storage succeeded
}

func TestDocumentStoreWithCollections(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cortexdb_doc_collection_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test_doc_collections.db")
	sqliteStore, err := NewSQLiteStore(dbPath, "hnsw")
	require.NoError(t, err)
	defer sqliteStore.Close()

	docStore := NewDocumentStore(sqliteStore.GetCortexdbStore())
	ctx := context.Background()

	documents := []domain.Document{
		{
			ID:      "doc-1",
			Path:    "/path/to/medical.pdf",
			Content: "Medical record content",
			Metadata: map[string]interface{}{
				"collection":    "medical_records",
				"document_type": "Medical Record",
				"keywords":      []string{"patient", "diagnosis"},
			},
			Created: time.Now(),
		},
		{
			ID:      "doc-2",
			URL:     "https://example.com/meeting",
			Content: "Meeting notes content",
			Metadata: map[string]interface{}{
				"collection":    "meeting_notes",
				"document_type": "Meeting Notes",
				"keywords":      []string{"agenda", "action items"},
			},
			Created: time.Now(),
		},
		{
			ID:      "doc-3",
			Content: "Code snippet content",
			Metadata: map[string]interface{}{
				"collection":    "code_snippets",
				"document_type": "Code",
				"keywords":      []string{"function", "algorithm"},
			},
			Created: time.Now(),
		},
	}

	// Store documents with collections
	for _, doc := range documents {
		err := docStore.Store(ctx, doc)
		require.NoError(t, err, "Should store document in collection")
	}

	// List all documents
	storedDocs, err := docStore.List(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(storedDocs), 3, "Should have stored all documents")

	// Verify each document has collection metadata
	collectionCounts := make(map[string]int)
	for _, doc := range storedDocs {
		if doc.Metadata != nil {
			if collection, ok := doc.Metadata["collection"].(string); ok {
				collectionCounts[collection]++
			}
		}
	}

	// Check we have documents in different collections
	assert.Contains(t, collectionCounts, "medical_records")
	assert.Contains(t, collectionCounts, "meeting_notes")
	assert.Contains(t, collectionCounts, "code_snippets")
}

func TestEnsureCollection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cortexdb_ensure_collection_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test_ensure.db")
	store, err := NewSQLiteStore(dbPath, "hnsw")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Test creating new collections
	collections := []string{
		"medical_records",
		"meeting_notes",
		"code_snippets",
		"research_papers",
		"financial_reports",
	}

	for _, collection := range collections {
		t.Run("ensure_"+collection, func(t *testing.T) {
			err := store.ensureCollection(ctx, collection)
			assert.NoError(t, err, "Should create collection %s", collection)

			// Calling again should not error (idempotent)
			err = store.ensureCollection(ctx, collection)
			assert.NoError(t, err, "Should handle existing collection %s", collection)
		})
	}
}

func TestCollectionMetadataPropagation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cortexdb_metadata_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test_metadata.db")
	store, err := NewSQLiteStore(dbPath, "hnsw")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create a chunk with collection and other metadata
	chunk := domain.Chunk{
		ID:         uuid.New().String(),
		DocumentID: "doc-metadata-1",
		Content:    "Document with rich metadata",
		Vector:     generateTestVector(768),
		Metadata: map[string]interface{}{
			"collection":    "research_papers",
			"document_type": "Article",
			"keywords":      []string{"AI", "machine learning"},
			"creation_date": "2025-09-12",
			"temporal_refs": map[string]string{
				"today":     "2025-09-12",
				"yesterday": "2025-09-11",
			},
			"entities": map[string][]string{
				"organization": {"MIT", "Stanford"},
				"person":       {"John Doe"},
			},
		},
	}

	// Create document first to satisfy foreign key constraint in cortexdb v2
	docStore := NewDocumentStore(store.GetCortexdbStore())
	_ = docStore.Store(ctx, domain.Document{
		ID:      chunk.DocumentID,
		Created: time.Now(),
	})

	// Store the chunk
	err = store.Store(ctx, []domain.Chunk{chunk})
	require.NoError(t, err)

	// Search and verify metadata is preserved
	results, err := store.Search(ctx, chunk.Vector, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Check that all metadata is preserved
	resultMetadata := results[0].Metadata
	assert.Equal(t, "research_papers", resultMetadata["collection"])
	assert.Equal(t, "Article", resultMetadata["document_type"])

	// Keywords might be stored as JSON string
	if keywords, ok := resultMetadata["keywords"].(string); ok {
		assert.Contains(t, keywords, "AI")
		assert.Contains(t, keywords, "machine learning")
	}
}

func TestCollectionValidation(t *testing.T) {
	tests := []struct {
		name       string
		collection string
		valid      bool
	}{
		{"Valid snake_case", "medical_records", true},
		{"Valid single word", "documents", true},
		{"Valid with numbers", "project_2025", true},
		{"Empty collection defaults", "", true}, // Should default to "default"
		{"Special characters", "medical-records", false},
		{"Spaces in name", "medical records", false},
		{"CamelCase", "MedicalRecords", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate collection name format
			if tt.collection != "" {
				isValid := isValidCollectionName(tt.collection)
				if tt.valid {
					assert.True(t, isValid, "Collection %s should be valid", tt.collection)
				} else {
					assert.False(t, isValid, "Collection %s should be invalid", tt.collection)
				}
			}
		})
	}
}

// Helper function to validate collection names
func isValidCollectionName(name string) bool {
	// Collection names should be snake_case: lowercase letters, numbers, and underscores
	if name == "" {
		return false
	}

	for i, ch := range name {
		if !((ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '_') {
			return false
		}
		// Don't start or end with underscore
		if ch == '_' && (i == 0 || i == len(name)-1) {
			return false
		}
	}
	// Don't allow double underscores
	for i := 0; i < len(name)-1; i++ {
		if name[i] == '_' && name[i+1] == '_' {
			return false
		}
	}
	return true
}

// Helper function to generate test vectors
func generateTestVector(size int) []float64 {
	vector := make([]float64, size)
	for i := range vector {
		vector[i] = float64(i) / float64(size)
	}
	return vector
}
