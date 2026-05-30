package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/blevesearch/bleve/v2"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type BleveStore struct {
	mu    sync.RWMutex
	index bleve.Index
	path  string
}

type bleveDoc struct {
	ID         string `json:"id"`
	DocumentID string `json:"document_id"`
	Content    string `json:"content"`
	Metadata   string `json:"metadata"` // Serialized JSON string to prevent Bleve flattening/conversion issues
}

func NewBleveStore(path string) (*BleveStore, error) {
	var index bleve.Index
	var err error

	// If index path doesn't exist, create it with a custom mapping
	if _, err = os.Stat(path); os.IsNotExist(err) {
		mapping := bleve.NewIndexMapping()

		docMapping := bleve.NewDocumentMapping()

		// ID mapping: exact match (keyword analyzer)
		idMapping := bleve.NewTextFieldMapping()
		idMapping.Store = true
		idMapping.Index = true
		idMapping.Analyzer = "keyword"
		docMapping.AddFieldMappingsAt("id", idMapping)

		// Document ID mapping: exact match (keyword analyzer)
		docIDMapping := bleve.NewTextFieldMapping()
		docIDMapping.Store = true
		docIDMapping.Index = true
		docIDMapping.Analyzer = "keyword"
		docMapping.AddFieldMappingsAt("document_id", docIDMapping)

		// Content mapping: standard analyzer (for full-text search)
		contentMapping := bleve.NewTextFieldMapping()
		contentMapping.Store = true
		contentMapping.Index = true
		contentMapping.Analyzer = "standard"
		docMapping.AddFieldMappingsAt("content", contentMapping)

		// Metadata mapping: store but do not index (prevent bloating & parsing errors)
		metaMapping := bleve.NewTextFieldMapping()
		metaMapping.Store = true
		metaMapping.Index = false
		docMapping.AddFieldMappingsAt("metadata", metaMapping)

		mapping.AddDocumentMapping("_default", docMapping)

		index, err = bleve.New(path, mapping)
		if err != nil {
			return nil, fmt.Errorf("failed to create bleve index: %w", err)
		}
	} else {
		index, err = bleve.Open(path)
		if err != nil {
			return nil, fmt.Errorf("failed to open bleve index: %w", err)
		}
	}

	return &BleveStore{
		index: index,
		path:  path,
	}, nil
}

func (s *BleveStore) Store(ctx context.Context, chunks []domain.Chunk) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	batch := s.index.NewBatch()
	for _, chunk := range chunks {
		metaBytes, err := json.Marshal(chunk.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal chunk metadata: %w", err)
		}

		doc := bleveDoc{
			ID:         chunk.ID,
			DocumentID: chunk.DocumentID,
			Content:    chunk.Content,
			Metadata:   string(metaBytes),
		}

		if err := batch.Index(chunk.ID, doc); err != nil {
			return fmt.Errorf("failed to add chunk %s to batch: %w", chunk.ID, err)
		}
	}

	return s.index.Batch(batch)
}

func (s *BleveStore) Search(ctx context.Context, query string, topK int) ([]domain.Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if topK <= 0 {
		topK = 5
	}

	// Create a query string query or a match query
	// Using NewMatchQuery to search the content field
	q := bleve.NewMatchQuery(query)
	q.SetField("content")

	searchReq := bleve.NewSearchRequest(q)
	searchReq.Size = topK
	searchReq.Fields = []string{"id", "document_id", "content", "metadata"}

	searchRes, err := s.index.Search(searchReq)
	if err != nil {
		return nil, fmt.Errorf("failed to search bleve index: %w", err)
	}

	chunks := make([]domain.Chunk, 0, len(searchRes.Hits))
	for _, hit := range searchRes.Hits {
		id, _ := hit.Fields["id"].(string)
		docID, _ := hit.Fields["document_id"].(string)
		content, _ := hit.Fields["content"].(string)
		metaStr, _ := hit.Fields["metadata"].(string)

		var metadata map[string]interface{}
		if metaStr != "" {
			_ = json.Unmarshal([]byte(metaStr), &metadata)
		}

		chunk := domain.Chunk{
			ID:         id,
			DocumentID: docID,
			Content:    content,
			Metadata:   metadata,
			Score:      hit.Score,
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

func (s *BleveStore) Delete(ctx context.Context, documentID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Find all chunks associated with documentID using a TermQuery (exact match on document_id field)
	termQuery := bleve.NewTermQuery(documentID)
	termQuery.SetField("document_id")

	searchReq := bleve.NewSearchRequest(termQuery)
	searchReq.Size = 10000 // A large enough number to find all document chunks
	searchReq.Fields = []string{"id"}

	searchRes, err := s.index.Search(searchReq)
	if err != nil {
		return fmt.Errorf("failed to search document chunks for deletion: %w", err)
	}

	if len(searchRes.Hits) == 0 {
		return nil
	}

	batch := s.index.NewBatch()
	for _, hit := range searchRes.Hits {
		batch.Delete(hit.ID)
	}

	return s.index.Batch(batch)
}

func (s *BleveStore) Reset(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.index.Close(); err != nil {
		return fmt.Errorf("failed to close index during reset: %w", err)
	}

	if err := os.RemoveAll(s.path); err != nil {
		return fmt.Errorf("failed to delete index directory: %w", err)
	}

	// Recreate mapping
	mapping := bleve.NewIndexMapping()

	docMapping := bleve.NewDocumentMapping()

	idMapping := bleve.NewTextFieldMapping()
	idMapping.Store = true
	idMapping.Index = true
	idMapping.Analyzer = "keyword"
	docMapping.AddFieldMappingsAt("id", idMapping)

	docIDMapping := bleve.NewTextFieldMapping()
	docIDMapping.Store = true
	docIDMapping.Index = true
	docIDMapping.Analyzer = "keyword"
	docMapping.AddFieldMappingsAt("document_id", docIDMapping)

	contentMapping := bleve.NewTextFieldMapping()
	contentMapping.Store = true
	contentMapping.Index = true
	contentMapping.Analyzer = "standard"
	docMapping.AddFieldMappingsAt("content", contentMapping)

	metaMapping := bleve.NewTextFieldMapping()
	metaMapping.Store = true
	metaMapping.Index = false
	docMapping.AddFieldMappingsAt("metadata", metaMapping)

	mapping.AddDocumentMapping("_default", docMapping)

	index, err := bleve.New(s.path, mapping)
	if err != nil {
		return fmt.Errorf("failed to recreate bleve index during reset: %w", err)
	}

	s.index = index
	return nil
}
