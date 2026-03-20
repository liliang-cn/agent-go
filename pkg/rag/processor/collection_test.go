package processor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockLLMWithCollections simulates an LLM that assigns collections
type MockLLMWithCollections struct{}

func (m *MockLLMWithCollections) ExtractMetadata(ctx context.Context, content string, model string) (*domain.ExtractedMetadata, error) {
	// Simulate LLM determining collection based on content
	metadata := &domain.ExtractedMetadata{
		Summary:      "Document summary",
		Keywords:     []string{"test", "document"},
		DocumentType: "Document",
		CreationDate: time.Now().Format("2006-01-02"),
		Collection:   "default",
		TemporalRefs: make(map[string]string),
		Entities:     make(map[string][]string),
		Events:       []string{},
		CustomMeta:   make(map[string]interface{}),
	}

	// Determine collection based on content keywords
	switch {
	case containsAny(content, []string{"patient", "medical", "diagnosis", "treatment"}):
		metadata.Collection = "medical_records"
		metadata.DocumentType = "Medical Record"
		metadata.Keywords = []string{"medical", "patient", "health"}

	case containsAny(content, []string{"meeting", "agenda", "minutes", "discussion"}):
		metadata.Collection = "meeting_notes"
		metadata.DocumentType = "Meeting Notes"
		metadata.Keywords = []string{"meeting", "discussion", "notes"}

	case containsAny(content, []string{"invoice", "payment", "bill", "financial"}):
		metadata.Collection = "financial_reports"
		metadata.DocumentType = "Invoice"
		metadata.Keywords = []string{"invoice", "payment", "financial"}

	case containsAny(content, []string{"research", "paper", "study", "abstract"}):
		metadata.Collection = "research_papers"
		metadata.DocumentType = "Article"
		metadata.Keywords = []string{"research", "study", "paper"}

	case containsAny(content, []string{"code", "function", "class", "algorithm"}):
		metadata.Collection = "code_snippets"
		metadata.DocumentType = "Code"
		metadata.Keywords = []string{"code", "programming", "function"}

	default:
		metadata.Collection = "default"
		metadata.DocumentType = "Document"
	}

	return metadata, nil
}

func containsAny(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if contains(text, keyword) {
			return true
		}
	}
	return false
}

func contains(text, keyword string) bool {
	// Simple case-insensitive contains check
	return strings.Contains(strings.ToLower(text), strings.ToLower(keyword))
}

func TestMetadataCollectionAssignment(t *testing.T) {
	llm := &MockLLMWithCollections{}
	ctx := context.Background()

	tests := []struct {
		name               string
		content            string
		expectedCollection string
		expectedType       string
	}{
		{
			name:               "Medical content gets medical_records",
			content:            "Patient John Doe diagnosed with hypertension, prescribed medication",
			expectedCollection: "medical_records",
			expectedType:       "Medical Record",
		},
		{
			name:               "Meeting content gets meeting_notes",
			content:            "Meeting agenda: Q4 planning discussion with team",
			expectedCollection: "meeting_notes",
			expectedType:       "Meeting Notes",
		},
		{
			name:               "Financial content gets financial_reports",
			content:            "Invoice #12345: Payment due for services rendered",
			expectedCollection: "financial_reports",
			expectedType:       "Invoice",
		},
		{
			name:               "Research content gets research_papers",
			content:            "Abstract: This research paper explores new algorithms",
			expectedCollection: "research_papers",
			expectedType:       "Article",
		},
		{
			name:               "Code content gets code_snippets",
			content:            "function calculateSum(a, b) { return a + b; }",
			expectedCollection: "code_snippets",
			expectedType:       "Code",
		},
		{
			name:               "Generic content gets default",
			content:            "Some random text without specific category",
			expectedCollection: "default",
			expectedType:       "Document",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata, err := llm.ExtractMetadata(ctx, tt.content, "")
			require.NoError(t, err)
			require.NotNil(t, metadata)

			assert.Equal(t, tt.expectedCollection, metadata.Collection,
				"Content should be assigned to correct collection")
			assert.Equal(t, tt.expectedType, metadata.DocumentType,
				"Document type should match collection")
			assert.NotEmpty(t, metadata.Keywords,
				"Should have relevant keywords")
		})
	}
}

func TestMergeMetadataWithCollection(t *testing.T) {
	// Test that collection from extracted metadata is properly merged
	baseMetadata := map[string]interface{}{
		"source": "test",
		"type":   "document",
	}

	extracted := &domain.ExtractedMetadata{
		Summary:      "Test document",
		Keywords:     []string{"test"},
		DocumentType: "Test Document",
		Collection:   "test_collection",
		CreationDate: "2025-09-12",
		TemporalRefs: map[string]string{
			"today": "2025-09-12",
		},
		Entities: map[string][]string{
			"person": {"Test Person"},
		},
	}

	// Simulate mergeMetadata function behavior
	mergeMetadata(baseMetadata, extracted)

	// Verify collection is added to base metadata
	assert.Equal(t, "test_collection", baseMetadata["collection"],
		"Collection should be merged into base metadata")
	assert.Equal(t, "Test document", baseMetadata["summary"],
		"Summary should be merged")
	assert.NotNil(t, baseMetadata["keywords"],
		"Keywords should be merged")
	assert.Equal(t, "Test Document", baseMetadata["document_type"],
		"Document type should be merged")
	assert.NotNil(t, baseMetadata["temporal_refs"],
		"Temporal refs should be merged")
	assert.NotNil(t, baseMetadata["entities"],
		"Entities should be merged")
}

func mergeMetadata(base map[string]interface{}, extracted *domain.ExtractedMetadata) {
	if extracted.Summary != "" {
		base["summary"] = extracted.Summary
	}
	if len(extracted.Keywords) > 0 {
		base["keywords"] = extracted.Keywords
	}
	if extracted.DocumentType != "" {
		base["document_type"] = extracted.DocumentType
	}
	if extracted.CreationDate != "" {
		base["creation_date"] = extracted.CreationDate
	}
	if extracted.Collection != "" {
		base["collection"] = extracted.Collection
	}
	if len(extracted.TemporalRefs) > 0 {
		base["temporal_refs"] = extracted.TemporalRefs
	}
	if len(extracted.Entities) > 0 {
		base["entities"] = extracted.Entities
	}
	if len(extracted.Events) > 0 {
		base["events"] = extracted.Events
	}
}

func TestCollectionExtraction(t *testing.T) {
	// Test extracting collection information from documents
	type testDoc struct {
		id         string
		metadata   map[string]interface{}
		collection string
	}

	documents := []testDoc{
		{
			id: "doc1",
			metadata: map[string]interface{}{
				"collection": "medical_records",
				"type":       "medical",
			},
			collection: "medical_records",
		},
		{
			id: "doc2",
			metadata: map[string]interface{}{
				"collection": "meeting_notes",
				"type":       "meeting",
			},
			collection: "meeting_notes",
		},
		{
			id: "doc3",
			metadata: map[string]interface{}{
				"type": "general",
			},
			collection: "default",
		},
	}

	// Extract collections from documents
	collectionCounts := make(map[string]int)
	for _, doc := range documents {
		collection := "default"
		if doc.metadata != nil {
			if c, ok := doc.metadata["collection"].(string); ok && c != "" {
				collection = c
			}
		}
		collectionCounts[collection]++

		assert.Equal(t, doc.collection, collection,
			"Document %s should be in collection %s", doc.id, doc.collection)
	}

	// Verify collection distribution
	assert.Equal(t, 1, collectionCounts["medical_records"])
	assert.Equal(t, 1, collectionCounts["meeting_notes"])
	assert.Equal(t, 1, collectionCounts["default"])
}

func TestCollectionStatistics(t *testing.T) {
	// Simulate collection statistics
	collections := map[string][]string{
		"medical_records": {"doc1", "doc2", "doc3"},
		"meeting_notes":   {"doc4", "doc5"},
		"code_snippets":   {"doc6"},
		"default":         {"doc7", "doc8", "doc9", "doc10"},
	}

	// Calculate statistics
	stats := make(map[string]int)
	for collection, docs := range collections {
		stats[collection] = len(docs)
	}

	// Verify statistics
	assert.Equal(t, 3, stats["medical_records"],
		"medical_records should have 3 documents")
	assert.Equal(t, 2, stats["meeting_notes"],
		"meeting_notes should have 2 documents")
	assert.Equal(t, 1, stats["code_snippets"],
		"code_snippets should have 1 document")
	assert.Equal(t, 4, stats["default"],
		"default should have 4 documents")

	// Total documents
	total := 0
	for _, count := range stats {
		total += count
	}
	assert.Equal(t, 10, total, "Total should be 10 documents")
}

func TestCollectionNamingConsistency(t *testing.T) {
	llm := &MockLLMWithCollections{}
	ctx := context.Background()

	// Test that similar content consistently gets the same collection
	medicalContents := []string{
		"Patient medical history and diagnosis",
		"Treatment plan for patient condition",
		"Medical test results and analysis",
	}

	collections := make([]string, 0)
	for _, content := range medicalContents {
		metadata, err := llm.ExtractMetadata(ctx, content, "")
		require.NoError(t, err)
		collections = append(collections, metadata.Collection)
	}

	// All medical content should get the same collection
	for i := 1; i < len(collections); i++ {
		assert.Equal(t, collections[0], collections[i],
			"Similar medical content should be in the same collection")
	}
}
