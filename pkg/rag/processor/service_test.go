package processor

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Simple mock implementations for basic testing
type SimpleEmbedder struct {
	embedFunc func(ctx context.Context, text string) ([]float64, error)
}

func (s *SimpleEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	if s.embedFunc != nil {
		return s.embedFunc(ctx, text)
	}
	return []float64{0.1, 0.2, 0.3}, nil
}

func (s *SimpleEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	result := make([][]float64, len(texts))
	for i, text := range texts {
		v, err := s.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		result[i] = v
	}
	return result, nil
}

type SimpleGenerator struct {
	generateFunc func(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error)
}

func (s *SimpleGenerator) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	if s.generateFunc != nil {
		return s.generateFunc(ctx, prompt, opts)
	}
	return "Generated response", nil
}

func (s *SimpleGenerator) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	response, err := s.Generate(ctx, prompt, opts)
	if err != nil {
		return err
	}
	callback(response)
	return nil
}

func (s *SimpleGenerator) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{
		Content:  "Response with tools",
		Finished: true,
	}, nil
}

func (s *SimpleGenerator) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (s *SimpleGenerator) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{
		Intent:     domain.IntentAction,
		Confidence: 0.9,
		Reasoning:  "Test intent recognition",
	}, nil
}

func (s *SimpleGenerator) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{
		Data:  map[string]string{"result": "structured"},
		Raw:   `{"result": "structured"}`,
		Valid: true,
	}, nil
}

type SimpleChunker struct {
	splitFunc func(text string, opts domain.ChunkOptions) ([]string, error)
}

func (s *SimpleChunker) Split(text string, opts domain.ChunkOptions) ([]string, error) {
	if s.splitFunc != nil {
		return s.splitFunc(text, opts)
	}
	if len(text) <= opts.Size {
		return []string{text}, nil
	}
	return []string{text[:opts.Size], text[opts.Size:]}, nil
}

type SimpleVectorStore struct {
	chunks []domain.Chunk
}

func (s *SimpleVectorStore) Store(ctx context.Context, chunks []domain.Chunk) error {
	s.chunks = append(s.chunks, chunks...)
	return nil
}

func (s *SimpleVectorStore) Search(ctx context.Context, vector []float64, topK int) ([]domain.Chunk, error) {
	if len(s.chunks) == 0 {
		return []domain.Chunk{}, nil
	}
	if topK > len(s.chunks) {
		topK = len(s.chunks)
	}
	return s.chunks[:topK], nil
}

func (s *SimpleVectorStore) SearchWithFilters(ctx context.Context, vector []float64, topK int, filters map[string]interface{}) ([]domain.Chunk, error) {
	return s.Search(ctx, vector, topK)
}

func (s *SimpleVectorStore) SearchWithReranker(ctx context.Context, vector []float64, queryText string, topK int, strategy string, boost float64) ([]domain.Chunk, error) {
	return s.Search(ctx, vector, topK)
}

func (s *SimpleVectorStore) SearchWithDiversity(ctx context.Context, vector []float64, topK int, lambda float32) ([]domain.Chunk, error) {
	return s.Search(ctx, vector, topK)
}

func (s *SimpleVectorStore) Delete(ctx context.Context, documentID string) error {
	newChunks := make([]domain.Chunk, 0)
	for _, chunk := range s.chunks {
		if chunk.DocumentID != documentID {
			newChunks = append(newChunks, chunk)
		}
	}
	s.chunks = newChunks
	return nil
}

func (s *SimpleVectorStore) List(ctx context.Context) ([]domain.Document, error) {
	return []domain.Document{}, nil
}

func (s *SimpleVectorStore) Reset(ctx context.Context) error {
	s.chunks = nil
	return nil
}

func (s *SimpleVectorStore) GetGraphStore() domain.GraphStore {
	return nil
}

func (s *SimpleVectorStore) GetChatStore() domain.ChatStore {
	return nil
}

type SimpleDocumentStore struct {
	docs []domain.Document
}

func (s *SimpleDocumentStore) Store(ctx context.Context, doc domain.Document) error {
	s.docs = append(s.docs, doc)
	return nil
}

func (s *SimpleDocumentStore) Get(ctx context.Context, id string) (domain.Document, error) {
	for _, doc := range s.docs {
		if doc.ID == id {
			return doc, nil
		}
	}
	return domain.Document{}, nil
}

func (s *SimpleDocumentStore) List(ctx context.Context) ([]domain.Document, error) {
	return s.docs, nil
}

func (s *SimpleDocumentStore) Delete(ctx context.Context, id string) error {
	newDocs := make([]domain.Document, 0)
	for _, doc := range s.docs {
		if doc.ID != id {
			newDocs = append(newDocs, doc)
		}
	}
	s.docs = newDocs
	return nil
}

type SimpleMetadataExtractor struct {
	extractFunc func(ctx context.Context, content, model string) (map[string]interface{}, error)
}

func (s *SimpleMetadataExtractor) ExtractMetadata(ctx context.Context, content, model string) (*domain.ExtractedMetadata, error) {
	if s.extractFunc != nil {
		data, err := s.extractFunc(ctx, content, model)
		if err != nil {
			return nil, err
		}
		return &domain.ExtractedMetadata{
			Summary:      data["summary"].(string),
			Keywords:     data["keywords"].([]string),
			DocumentType: data["document_type"].(string),
		}, nil
	}
	return &domain.ExtractedMetadata{
		Summary:      "Test Document Summary",
		Keywords:     []string{"test", "document"},
		DocumentType: "text",
	}, nil
}

func createSimpleTestService() *Service {
	cfg := &config.Config{
		RAG: config.RAGConfig{
			Enabled: true,
		},
	}

	return New(
		&SimpleEmbedder{},
		&SimpleGenerator{},
		&SimpleChunker{},
		&SimpleVectorStore{},
		&SimpleDocumentStore{},
		cfg,
		&SimpleMetadataExtractor{},
		nil, // memoryService - not needed for basic tests
	)
}

func TestProcessorService_New(t *testing.T) {
	service := createSimpleTestService()
	if service == nil {
		t.Fatal("New returned nil service")
	}

	if service.embedder == nil {
		t.Error("Embedder not set")
	}

	if service.generator == nil {
		t.Error("Generator not set")
	}

	if service.chunker == nil {
		t.Error("Chunker not set")
	}

	if service.vectorStore == nil {
		t.Error("VectorStore not set")
	}

	if service.documentStore == nil {
		t.Error("DocumentStore not set")
	}

	if service.config == nil {
		t.Error("Config not set")
	}
}

func TestProcessorService_IngestBasic(t *testing.T) {
	service := createSimpleTestService()
	ctx := context.Background()

	req := domain.IngestRequest{
		Content: "This is test content for basic ingestion",
		Metadata: map[string]interface{}{
			"source": "test",
		},
		ChunkSize: 50,
		Overlap:   10,
	}

	resp, err := service.Ingest(ctx, req)
	if err != nil {
		t.Errorf("Ingest failed: %v", err)
	}

	if !resp.Success {
		t.Error("Expected successful ingestion")
	}

	if resp.ChunkCount == 0 {
		t.Error("Expected at least one chunk to be processed")
	}
}

func TestProcessorService_QueryBasic(t *testing.T) {
	service := createSimpleTestService()
	ctx := context.Background()

	// First ingest some content
	ingestReq := domain.IngestRequest{
		Content: "This is test content for querying",
	}

	_, err := service.Ingest(ctx, ingestReq)
	if err != nil {
		t.Fatalf("Failed to ingest content: %v", err)
	}

	// Now query
	queryReq := domain.QueryRequest{
		Query: "test query",
		TopK:  5,
	}

	resp, err := service.Query(ctx, queryReq)
	if err != nil {
		t.Errorf("Query failed: %v", err)
	}

	if resp.Answer == "" {
		t.Error("Expected non-empty answer")
	}

	// Sources might be empty if no vector search results
	// This is valid behavior for the simple mock
}

func TestProcessorService_QueryUsesSafeDefaultMaxTokens(t *testing.T) {
	var capturedMaxTokens int
	service := New(
		&SimpleEmbedder{},
		&SimpleGenerator{
			generateFunc: func(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
				capturedMaxTokens = opts.MaxTokens
				return "ok", nil
			},
		},
		&SimpleChunker{},
		&SimpleVectorStore{},
		&SimpleDocumentStore{},
		&config.Config{
			RAG: config.RAGConfig{
				Enabled: true,
			},
		},
		&SimpleMetadataExtractor{},
		nil,
	)

	ctx := context.Background()
	if _, err := service.Ingest(ctx, domain.IngestRequest{Content: "RAG max tokens test content"}); err != nil {
		t.Fatalf("Failed to ingest content: %v", err)
	}

	if _, err := service.Query(ctx, domain.QueryRequest{Query: "max tokens?"}); err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if capturedMaxTokens != 2000 {
		t.Fatalf("expected default max tokens 2000, got %d", capturedMaxTokens)
	}
}

func TestProcessorService_ListDocuments(t *testing.T) {
	service := createSimpleTestService()
	ctx := context.Background()

	// Initially should be empty
	docs, err := service.ListDocuments(ctx)
	if err != nil {
		t.Errorf("ListDocuments failed: %v", err)
	}

	if len(docs) != 0 {
		t.Error("Expected empty document list initially")
	}

	// Ingest a document
	req := domain.IngestRequest{
		Content: "Test document content",
	}

	_, err = service.Ingest(ctx, req)
	if err != nil {
		t.Fatalf("Failed to ingest document: %v", err)
	}

	// Now should have documents
	docs, err = service.ListDocuments(ctx)
	if err != nil {
		t.Errorf("ListDocuments failed after ingest: %v", err)
	}

	if len(docs) == 0 {
		t.Error("Expected at least one document after ingestion")
	}
}

func TestProcessorService_Reset(t *testing.T) {
	service := createSimpleTestService()
	ctx := context.Background()

	// Ingest some content first
	req := domain.IngestRequest{
		Content: "Content to be reset",
	}

	_, err := service.Ingest(ctx, req)
	if err != nil {
		t.Fatalf("Failed to ingest content: %v", err)
	}

	// Reset
	err = service.Reset(ctx)
	if err != nil {
		t.Errorf("Reset failed: %v", err)
	}

	// Verify reset worked
	_, err = service.ListDocuments(ctx)
	if err != nil {
		t.Errorf("ListDocuments failed after reset: %v", err)
	}

	// Reset might not clear everything in simple mock implementation
	// This test verifies reset doesn't crash
}

func TestProcessorService_IngestEmptyContent(t *testing.T) {
	service := createSimpleTestService()
	ctx := context.Background()

	req := domain.IngestRequest{
		Content: "",
	}

	resp, err := service.Ingest(ctx, req)
	// The error might be returned during validation
	if err != nil {
		// This is acceptable - empty content should cause an error
		return
	}

	if resp.Success {
		t.Error("Expected failed ingestion for empty content")
	}
}

func TestProcessorService_WithMetadataExtraction(t *testing.T) {
	cfg := &config.Config{
		RAG: config.RAGConfig{
			Enabled: true,
		},
	}

	service := New(
		&SimpleEmbedder{},
		&SimpleGenerator{},
		&SimpleChunker{},
		&SimpleVectorStore{},
		&SimpleDocumentStore{},
		cfg,
		&SimpleMetadataExtractor{
			extractFunc: func(ctx context.Context, content, model string) (map[string]interface{}, error) {
				return map[string]interface{}{
					"summary":       "Auto Title",
					"keywords":      []string{"auto"},
					"document_type": "auto",
				}, nil
			},
		},
		nil, // memoryService
	)

	ctx := context.Background()

	req := domain.IngestRequest{
		Content: "Content for metadata extraction test",
	}

	resp, err := service.Ingest(ctx, req)
	if err != nil {
		t.Errorf("Ingest with metadata extraction failed: %v", err)
	}

	if !resp.Success {
		t.Error("Expected successful ingestion with metadata extraction")
	}
}
