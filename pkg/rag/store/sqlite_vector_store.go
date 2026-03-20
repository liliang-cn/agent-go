package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/cortexdb/v2/pkg/core"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/graph"
)

type SQLiteStore struct {
	db       *cortexdb.DB
	cortexdb *core.SQLiteStore
}

func NewSQLiteStore(dbPath string, indexType string) (*SQLiteStore, error) {
	// Use cortexdb v2.7.0's new API
	config := cortexdb.Config{
		Path:       dbPath,
		Dimensions: 0, // Auto-detect dimensions
	}

	switch strings.ToLower(indexType) {
	case "ivf":
		config.IndexType = core.IndexTypeIVF
	case "flat":
		config.IndexType = core.IndexTypeFlat
	default:
		config.IndexType = core.IndexTypeHNSW // Default
	}

	db, err := cortexdb.Open(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create cortexdb database: %w", err)
	}

	// Initialize graph schema
	ctx := context.Background()
	if err := db.Graph().InitGraphSchema(ctx); err != nil {
		// Log error but continue as graph might not be needed immediately
		fmt.Printf("Warning: Failed to init graph schema: %v\n", err)
	}

	// Get the vector store from the database
	vectorStore := db.Vector()

	// Type assert to get the concrete SQLiteStore
	sqliteStore, ok := vectorStore.(*core.SQLiteStore)
	if !ok {
		db.Close()
		return nil, fmt.Errorf("failed to get SQLiteStore from vector store")
	}

	return &SQLiteStore{
		db:       db,
		cortexdb: sqliteStore,
	}, nil
}

func (s *SQLiteStore) GetGraphStore() domain.GraphStore {
	return &SQLiteGraphStore{
		graph: s.db.Graph(),
	}
}

func (s *SQLiteStore) GetChatStore() domain.ChatStore {
	return &SQLiteChatStore{
		store: s.cortexdb,
	}
}

type SQLiteChatStore struct {
	store *core.SQLiteStore
}

func (s *SQLiteChatStore) CreateSession(ctx context.Context, session *domain.ChatSession) error {
	coreSession := &core.Session{
		ID:        session.ID,
		Metadata:  session.Metadata,
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
	}
	return s.store.CreateSession(ctx, coreSession)
}

func (s *SQLiteChatStore) GetSession(ctx context.Context, id string) (*domain.ChatSession, error) {
	sess, err := s.store.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &domain.ChatSession{
		ID:        sess.ID,
		Metadata:  sess.Metadata,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
	}, nil
}

func (s *SQLiteChatStore) AddMessage(ctx context.Context, msg *domain.ChatMessage) error {
	var vector []float32
	if len(msg.Vector) > 0 {
		vector = make([]float32, len(msg.Vector))
		for i, v := range msg.Vector {
			vector[i] = float32(v)
		}
	}

	coreMsg := &core.Message{
		ID:        msg.ID,
		SessionID: msg.SessionID,
		Role:      msg.Role,
		Content:   msg.Content,
		Vector:    vector,
		Metadata:  msg.Metadata,
		CreatedAt: msg.CreatedAt,
	}
	return s.store.AddMessage(ctx, coreMsg)
}

func (s *SQLiteChatStore) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]*domain.ChatMessage, error) {
	msgs, err := s.store.GetSessionHistory(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}

	result := make([]*domain.ChatMessage, len(msgs))
	for i, msg := range msgs {
		var vector []float64
		if len(msg.Vector) > 0 {
			vector = make([]float64, len(msg.Vector))
			for i, v := range msg.Vector {
				vector[i] = float64(v)
			}
		}

		result[i] = &domain.ChatMessage{
			ID:        msg.ID,
			SessionID: msg.SessionID,
			Role:      msg.Role,
			Content:   msg.Content,
			Vector:    vector,
			Metadata:  msg.Metadata,
			CreatedAt: msg.CreatedAt,
		}
	}
	return result, nil
}

func (s *SQLiteChatStore) SearchChatHistory(ctx context.Context, queryVec []float64, sessionID string, limit int) ([]*domain.ChatMessage, error) {
	var vector []float32
	if len(queryVec) > 0 {
		vector = make([]float32, len(queryVec))
		for i, v := range queryVec {
			vector[i] = float32(v)
		}
	}

	msgs, err := s.store.SearchChatHistory(ctx, vector, sessionID, limit)
	if err != nil {
		return nil, err
	}

	result := make([]*domain.ChatMessage, len(msgs))
	for i, msg := range msgs {
		var msgVector []float64
		if len(msg.Vector) > 0 {
			msgVector = make([]float64, len(msg.Vector))
			for i, v := range msg.Vector {
				msgVector[i] = float64(v)
			}
		}

		result[i] = &domain.ChatMessage{
			ID:        msg.ID,
			SessionID: msg.SessionID,
			Role:      msg.Role,
			Content:   msg.Content,
			Vector:    msgVector,
			Metadata:  msg.Metadata,
			CreatedAt: msg.CreatedAt,
		}
	}
	return result, nil
}

func (s *SQLiteChatStore) InitChatSchema(ctx context.Context) error {
	// The core.SQLiteStore automatically initializes schema if Init is called
	// But we might want to ensure it explicitly or if there are specific chat tables
	// core.Init() creates chat tables
	// We can't call Init again easily, but we can assume it's done in NewSQLiteStore
	return nil
}

type SQLiteGraphStore struct {
	graph *graph.GraphStore
}

func (s *SQLiteGraphStore) UpsertNode(ctx context.Context, node domain.GraphNode) error {
	// Convert vector to float32
	var vector []float32
	if len(node.Vector) > 0 {
		vector = make([]float32, len(node.Vector))
		for i, v := range node.Vector {
			vector[i] = float32(v)
		}
	}

	gNode := &graph.GraphNode{
		ID:         node.ID,
		Vector:     vector,
		Content:    node.Content,
		NodeType:   node.NodeType,
		Properties: node.Properties,
	}

	return s.graph.UpsertNode(ctx, gNode)
}

func (s *SQLiteGraphStore) UpsertEdge(ctx context.Context, edge domain.GraphEdge) error {
	gEdge := &graph.GraphEdge{
		ID:         edge.ID,
		FromNodeID: edge.FromNodeID,
		ToNodeID:   edge.ToNodeID,
		EdgeType:   edge.EdgeType,
		Weight:     edge.Weight,
		Properties: edge.Properties,
	}

	return s.graph.UpsertEdge(ctx, gEdge)
}

func (s *SQLiteGraphStore) InitGraphSchema(ctx context.Context) error {
	return s.graph.InitGraphSchema(ctx)
}

func (s *SQLiteGraphStore) HybridSearch(ctx context.Context, vector []float64, startNodeID string, topK int) ([]domain.HybridSearchResult, error) {
	// Convert vector
	var queryVector []float32
	if len(vector) > 0 {
		queryVector = make([]float32, len(vector))
		for i, v := range vector {
			queryVector[i] = float32(v)
		}
	}

	query := &graph.HybridQuery{
		Vector:      queryVector,
		StartNodeID: startNodeID,
		TopK:        topK,
	}

	results, err := s.graph.HybridSearch(ctx, query)
	if err != nil {
		return nil, err
	}

	domainResults := make([]domain.HybridSearchResult, len(results))
	for i, res := range results {
		// Convert graph node to domain node
		var dNode *domain.GraphNode
		if res.Node != nil {
			nodeVector := make([]float64, len(res.Node.Vector))
			for j, v := range res.Node.Vector {
				nodeVector[j] = float64(v)
			}
			dNode = &domain.GraphNode{
				ID:         res.Node.ID,
				Content:    res.Node.Content,
				NodeType:   res.Node.NodeType,
				Properties: res.Node.Properties,
				Vector:     nodeVector,
			}
		}

		domainResults[i] = domain.HybridSearchResult{
			Node:        dNode,
			Score:       res.CombinedScore,
			VectorScore: res.VectorScore,
			GraphScore:  res.GraphScore,
		}
	}

	return domainResults, nil
}

func (s *SQLiteStore) Store(ctx context.Context, chunks []domain.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	for _, chunk := range chunks {
		if len(chunk.Vector) == 0 {
			continue
		}

		// Convert []float64 to []float32 for cortexdb
		vector := make([]float32, len(chunk.Vector))
		for i, v := range chunk.Vector {
			vector[i] = float32(v)
		}

		// Convert metadata to string map, handling slices and maps as JSON strings
		metadata := make(map[string]string)
		if chunk.Metadata != nil {
			for k, v := range chunk.Metadata {
				switch val := v.(type) {
				case []string, map[string]interface{}, []interface{}:
					jsonBytes, err := json.Marshal(val)
					if err == nil {
						metadata[k] = string(jsonBytes)
					} else {
						metadata[k] = fmt.Sprintf("%v", v)
					}
				default:
					metadata[k] = fmt.Sprintf("%v", v)
				}
			}
		}

		// Mark as chunk for filtering during search
		metadata["_type"] = "chunk"

		// Determine collection from metadata
		collection := "default"
		if collectionName, ok := chunk.Metadata["collection"].(string); ok && collectionName != "" {
			collection = collectionName
			// Ensure collection exists
			if err := s.ensureCollection(ctx, collection); err != nil {
				return fmt.Errorf("failed to ensure collection %s: %w", collection, err)
			}
		}

		embedding := &core.Embedding{
			ID:         chunk.ID,
			Vector:     vector,
			Content:    chunk.Content,
			DocID:      chunk.DocumentID,
			Collection: collection,
			Metadata:   metadata,
		}

		if err := s.cortexdb.Upsert(ctx, embedding); err != nil {
			return fmt.Errorf("%w: failed to store chunk in collection %s: %v", domain.ErrVectorStoreFailed, collection, err)
		}
	}

	return nil
}

func (s *SQLiteStore) Search(ctx context.Context, vector []float64, topK int) ([]domain.Chunk, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("%w: empty query vector", domain.ErrInvalidInput)
	}

	if topK <= 0 {
		topK = 5
	}

	// Check if there are any vectors in the database first
	count, err := s.getVectorCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to check vector count: %v", domain.ErrVectorStoreFailed, err)
	}

	if count == 0 {
		// Return empty results if no vectors exist
		return []domain.Chunk{}, nil
	}

	// Convert []float64 to []float32 for cortexdb
	queryVector := make([]float32, len(vector))
	for i, v := range vector {
		queryVector[i] = float32(v)
	}

	// Use SearchWithFilter to exclude document metadata (v0.8.0 fixes dimension bug)
	filters := map[string]interface{}{
		"_type": "chunk", // Only return chunks, not document metadata
	}

	results, err := s.cortexdb.SearchWithFilter(ctx, queryVector, core.SearchOptions{
		TopK:      topK,
		Threshold: 0.0, // Return all results, let caller filter
	}, filters)
	if err != nil {
		return nil, fmt.Errorf("%w: search failed: %v", domain.ErrVectorStoreFailed, err)
	}

	chunks := make([]domain.Chunk, len(results))
	for i, result := range results {
		// Convert []float32 back to []float64
		resultVector := make([]float64, len(result.Vector))
		for j, v := range result.Vector {
			resultVector[j] = float64(v)
		}

		// Convert metadata back to interface{}
		metadata := make(map[string]interface{})
		for k, v := range result.Metadata {
			metadata[k] = v
		}

		chunks[i] = domain.Chunk{
			ID:         result.ID,
			DocumentID: result.DocID,
			Content:    result.Content,
			Vector:     resultVector,
			Score:      float64(result.Score),
			Metadata:   metadata,
		}
	}

	return chunks, nil
}

func (s *SQLiteStore) SearchWithFilters(ctx context.Context, vector []float64, topK int, filters map[string]interface{}) ([]domain.Chunk, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("%w: empty query vector", domain.ErrInvalidInput)
	}

	if topK <= 0 {
		topK = 5
	}

	// Check if there are any vectors in the database first
	count, err := s.getVectorCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to check vector count: %v", domain.ErrVectorStoreFailed, err)
	}

	if count == 0 {
		// Return empty results if no vectors exist
		return []domain.Chunk{}, nil
	}

	// Convert []float64 to []float32 for cortexdb
	queryVector := make([]float32, len(vector))
	for i, v := range vector {
		queryVector[i] = float32(v)
	}

	// Use cortexdb's native SearchWithFilter method if filters are provided
	if len(filters) > 0 {
		// Add chunk type filter to existing filters to avoid document metadata
		chunkFilters := make(map[string]interface{})
		for k, v := range filters {
			chunkFilters[k] = v
		}
		chunkFilters["_type"] = "chunk"

		results, err := s.cortexdb.SearchWithFilter(ctx, queryVector, core.SearchOptions{
			TopK:      topK,
			Threshold: 0.0, // Return all results, let caller filter
		}, chunkFilters)
		if err != nil {
			return nil, fmt.Errorf("%w: search with filter failed: %v", domain.ErrVectorStoreFailed, err)
		}

		chunks := make([]domain.Chunk, len(results))
		for i, result := range results {
			// Convert []float32 back to []float64
			resultVector := make([]float64, len(result.Vector))
			for j, v := range result.Vector {
				resultVector[j] = float64(v)
			}

			// Convert metadata back to interface{}
			metadata := make(map[string]interface{})
			for k, v := range result.Metadata {
				metadata[k] = v
			}

			chunks[i] = domain.Chunk{
				ID:         result.ID,
				DocumentID: result.DocID,
				Content:    result.Content,
				Vector:     resultVector,
				Score:      float64(result.Score),
				Metadata:   metadata,
			}
		}

		return chunks, nil
	}

	// If no filters, use regular search
	return s.Search(ctx, vector, topK)
}

func (s *SQLiteStore) SearchWithReranker(ctx context.Context, vector []float64, queryText string, topK int, strategy string, boost float64) ([]domain.Chunk, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("%w: empty query vector", domain.ErrInvalidInput)
	}

	// Convert []float64 to []float32 for cortexdb
	queryVector := make([]float32, len(vector))
	for i, v := range vector {
		queryVector[i] = float32(v)
	}

	var reranker core.Reranker
	switch strings.ToLower(strategy) {
	case "keyword":
		reranker = core.NewKeywordMatchReranker(boost)
	case "rrf":
		reranker = core.NewReciprocalRankFusionReranker(60)
	default:
		return s.Search(ctx, vector, topK)
	}

	opts := core.RerankOptions{
		TopK:      topK,
		Threshold: 0.0,
	}

	results, err := s.cortexdb.SearchWithReranker(ctx, queryVector, queryText, reranker, opts)
	if err != nil {
		return nil, fmt.Errorf("%w: search with reranker failed: %v", domain.ErrVectorStoreFailed, err)
	}

	return s.toDomainChunks(results), nil
}

func (s *SQLiteStore) SearchWithDiversity(ctx context.Context, vector []float64, topK int, lambda float32) ([]domain.Chunk, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("%w: empty query vector", domain.ErrInvalidInput)
	}

	// Convert []float64 to []float32 for cortexdb
	queryVector := make([]float32, len(vector))
	for i, v := range vector {
		queryVector[i] = float32(v)
	}

	opts := core.DiversitySearchOptions{
		Lambda: lambda,
		Method: core.DiversityMMR,
		SearchOptions: core.SearchOptions{
			TopK: topK,
		},
	}

	results, err := s.cortexdb.SearchWithDiversity(ctx, queryVector, opts)
	if err != nil {
		return nil, fmt.Errorf("%w: search with diversity failed: %v", domain.ErrVectorStoreFailed, err)
	}

	return s.toDomainChunks(results), nil
}

func (s *SQLiteStore) toDomainChunks(results []core.ScoredEmbedding) []domain.Chunk {
	chunks := make([]domain.Chunk, len(results))
	for i, result := range results {
		// Convert []float32 back to []float64
		resultVector := make([]float64, len(result.Vector))
		for j, v := range result.Vector {
			resultVector[j] = float64(v)
		}

		// Convert metadata back to interface{}
		metadata := make(map[string]interface{})
		for k, v := range result.Metadata {
			metadata[k] = v
		}

		chunks[i] = domain.Chunk{
			ID:         result.ID,
			DocumentID: result.DocID,
			Content:    result.Content,
			Vector:     resultVector,
			Score:      float64(result.Score),
			Metadata:   metadata,
		}
	}
	return chunks
}

func (s *SQLiteStore) Delete(ctx context.Context, documentID string) error {
	if documentID == "" {
		return fmt.Errorf("%w: empty document ID", domain.ErrInvalidInput)
	}

	if err := s.cortexdb.DeleteByDocID(ctx, documentID); err != nil {
		return fmt.Errorf("%w: failed to delete document: %v", domain.ErrVectorStoreFailed, err)
	}

	return nil
}

func (s *SQLiteStore) List(ctx context.Context) ([]domain.Document, error) {
	// Try using ListDocumentsWithInfo first (cortexdb v2.7.0)
	docInfos, err := s.cortexdb.ListDocumentsWithInfo(ctx)
	if err != nil {
		// Fall back to old method if ListDocumentsWithInfo is not available
		return s.listWithFallback(ctx)
	}

	if len(docInfos) == 0 {
		return []domain.Document{}, nil
	}

	documents := make([]domain.Document, 0, len(docInfos))

	for _, docInfo := range docInfos {
		// Get the document details using GetByDocID
		embeddings, err := s.cortexdb.GetByDocID(ctx, docInfo.DocID)
		if err != nil {
			continue // Skip this document if we can't get its embeddings
		}

		// Find the document metadata embedding
		for _, embedding := range embeddings {
			if embedding.Metadata["_type"] == "document" {
				// Parse created time
				var created time.Time
				if createdStr, ok := embedding.Metadata["_created"]; ok {
					if parsed, err := time.Parse("2006-01-02T15:04:05Z07:00", createdStr); err == nil {
						created = parsed
					}
				}

				doc := domain.Document{
					ID:      embedding.DocID,
					Path:    embedding.Metadata["_path"],
					URL:     embedding.Metadata["_url"],
					Content: embedding.Content,
					Created: created,
				}

				// Copy non-internal metadata
				doc.Metadata = make(map[string]interface{})
				for k, v := range embedding.Metadata {
					if !strings.HasPrefix(k, "_") {
						doc.Metadata[k] = v
					}
				}

				documents = append(documents, doc)
				break
			}
		}
	}

	return documents, nil
}

// Fallback method for compatibility
func (s *SQLiteStore) listWithFallback(ctx context.Context) ([]domain.Document, error) {
	// Try GetDocumentsByType
	embeddings, err := s.cortexdb.GetDocumentsByType(ctx, "document")
	if err != nil {
		return nil, fmt.Errorf("%w: failed to get documents: %v", domain.ErrVectorStoreFailed, err)
	}

	if len(embeddings) == 0 {
		// Final fallback: ListDocuments + GetByDocID
		docIDs, err := s.cortexdb.ListDocuments(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to list documents: %v", domain.ErrVectorStoreFailed, err)
		}

		documentMap := make(map[string]domain.Document)

		for _, docID := range docIDs {
			docEmbeddings, err := s.cortexdb.GetByDocID(ctx, docID)
			if err != nil {
				continue // Skip this document if we can't get its embeddings
			}

			// Find the document metadata embedding
			for _, embedding := range docEmbeddings {
				if embedding.Metadata["_type"] == "document" {
					// Parse created time
					var created time.Time
					if createdStr, ok := embedding.Metadata["_created"]; ok {
						if parsed, err := time.Parse("2006-01-02T15:04:05Z07:00", createdStr); err == nil {
							created = parsed
						}
					}

					doc := domain.Document{
						ID:      embedding.DocID,
						Path:    embedding.Metadata["_path"],
						URL:     embedding.Metadata["_url"],
						Content: embedding.Content,
						Created: created,
					}

					// Copy non-internal metadata
					doc.Metadata = make(map[string]interface{})
					for k, v := range embedding.Metadata {
						if !strings.HasPrefix(k, "_") {
							doc.Metadata[k] = v
						}
					}

					documentMap[embedding.DocID] = doc
					break
				}
			}
		}

		// Convert map to slice
		documents := make([]domain.Document, 0, len(documentMap))
		for _, doc := range documentMap {
			documents = append(documents, doc)
		}

		return documents, nil
	}

	// Process GetDocumentsByType results
	documentMap := make(map[string]domain.Document)

	for _, embedding := range embeddings {
		if embedding.Metadata["_type"] == "document" {
			// Parse created time
			var created time.Time
			if createdStr, ok := embedding.Metadata["_created"]; ok {
				if parsed, err := time.Parse("2006-01-02T15:04:05Z07:00", createdStr); err == nil {
					created = parsed
				}
			}

			doc := domain.Document{
				ID:      embedding.DocID,
				Path:    embedding.Metadata["_path"],
				URL:     embedding.Metadata["_url"],
				Content: embedding.Content,
				Created: created,
			}

			// Copy non-internal metadata
			doc.Metadata = make(map[string]interface{})
			for k, v := range embedding.Metadata {
				if !strings.HasPrefix(k, "_") {
					doc.Metadata[k] = v
				}
			}

			documentMap[embedding.DocID] = doc
		}
	}

	// Convert map to slice
	documents := make([]domain.Document, 0, len(documentMap))
	for _, doc := range documentMap {
		documents = append(documents, doc)
	}

	return documents, nil
}

func (s *SQLiteStore) Reset(ctx context.Context) error {
	// Use the new Clear method from cortexdb v2.7.0
	if err := s.cortexdb.Clear(ctx); err != nil {
		return fmt.Errorf("%w: failed to clear store: %v", domain.ErrVectorStoreFailed, err)
	}
	return nil
}

// getVectorCount returns the number of vectors in the database
func (s *SQLiteStore) getVectorCount(ctx context.Context) (int64, error) {
	// Since cortexdb doesn't have a Count method, we'll do a simple check
	// by trying to search with a dummy vector and see if we get results
	// For a more accurate count, we could query the database directly
	documents, err := s.cortexdb.ListDocuments(ctx)
	if err != nil {
		return 0, err
	}
	return int64(len(documents)), nil
}

func (s *SQLiteStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return s.cortexdb.Close()
}

// ensureCollection ensures a collection exists, creating it if necessary
func (s *SQLiteStore) ensureCollection(ctx context.Context, name string) error {
	// Check if collection already exists
	collections, err := s.cortexdb.ListCollections(ctx)
	if err != nil {
		return fmt.Errorf("failed to list collections: %w", err)
	}

	for _, col := range collections {
		if col.Name == name {
			return nil // Collection already exists
		}
	}

	// Create the collection with auto-detect dimensions (0)
	_, err = s.cortexdb.CreateCollection(ctx, name, 0)
	if err != nil {
		// Check if it's an "already exists" error - if so, ignore it
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create collection %s: %w", name, err)
	}

	return nil
}

// DocumentStore is a simple wrapper that uses cortexdb for document storage too
type DocumentStore struct {
	cortexdb *core.SQLiteStore
}

func NewDocumentStore(cortexdbStore *core.SQLiteStore) *DocumentStore {
	return &DocumentStore{
		cortexdb: cortexdbStore,
	}
}

func (s *DocumentStore) Store(ctx context.Context, doc domain.Document) error {
	// Map domain.Document to core.Document
	coreDoc := &core.Document{
		ID:        doc.ID,
		Title:     doc.Path, // Using path as title for now
		SourceURL: doc.URL,
		Content:   doc.Content,
		Version:   1,
		Metadata:  doc.Metadata,
		CreatedAt: doc.Created,
		UpdatedAt: doc.Created,
	}

	if err := s.cortexdb.CreateDocument(ctx, coreDoc); err != nil {
		return fmt.Errorf("%w: failed to store document: %v", domain.ErrDocumentStoreFailed, err)
	}

	return nil
}

func (s *DocumentStore) Get(ctx context.Context, id string) (domain.Document, error) {
	doc, err := s.cortexdb.GetDocument(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return domain.Document{}, domain.ErrDocumentNotFound
		}
		return domain.Document{}, fmt.Errorf("%w: failed to get document: %v", domain.ErrDocumentStoreFailed, err)
	}

	return domain.Document{
		ID:       doc.ID,
		Path:     doc.Title,
		URL:      doc.SourceURL,
		Content:   doc.Content,
		Metadata: doc.Metadata,
		Created:  doc.CreatedAt,
	}, nil
}

func (s *DocumentStore) List(ctx context.Context) ([]domain.Document, error) {
	docs, err := s.cortexdb.ListDocumentsWithFilter(ctx, "", 1000)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to list documents: %v", domain.ErrDocumentStoreFailed, err)
	}

	result := make([]domain.Document, len(docs))
	for i, doc := range docs {
		result[i] = domain.Document{
			ID:       doc.ID,
			Path:     doc.Title,
			URL:      doc.SourceURL,
		Content:   doc.Content,
			Metadata: doc.Metadata,
			Created:  doc.CreatedAt,
		}
	}

	return result, nil
}

func (s *DocumentStore) Delete(ctx context.Context, id string) error {
	if err := s.cortexdb.DeleteDocument(ctx, id); err != nil {
		return fmt.Errorf("%w: failed to delete document: %v", domain.ErrDocumentStoreFailed, err)
	}
	return nil
}

func (s *DocumentStore) Update(ctx context.Context, doc domain.Document) error {
	// Map domain.Document to core.Document
	coreDoc := &core.Document{
		ID:        doc.ID,
		Title:     doc.Path, // Using path as title for now
		SourceURL: doc.URL,
		Content:   doc.Content,
		Version:   1,
		Metadata:  doc.Metadata,
		CreatedAt: doc.Created,
		UpdatedAt: time.Now(),
	}

	if err := s.cortexdb.UpdateDocument(ctx, coreDoc); err != nil {
		return fmt.Errorf("%w: failed to update document: %v", domain.ErrDocumentStoreFailed, err)
	}

	return nil
}

// ensureCollection ensures a collection exists, creating it if necessary
func (s *DocumentStore) ensureCollection(ctx context.Context, name string) error {
	// Check if collection already exists
	collections, err := s.cortexdb.ListCollections(ctx)
	if err != nil {
		return fmt.Errorf("failed to list collections: %w", err)
	}

	for _, col := range collections {
		if col.Name == name {
			return nil // Collection already exists
		}
	}

	// Create the collection with auto-detect dimensions (0)
	_, err = s.cortexdb.CreateCollection(ctx, name, 0)
	if err != nil {
		// Check if it's an "already exists" error - if so, ignore it
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create collection %s: %w", name, err)
	}

	return nil
}

// Reset removes all documents from the document store
func (s *DocumentStore) Reset(ctx context.Context) error {
	// Since documents are stored in the same cortexdb database,
	// we need to delete all entries with _type = "document"
	// For now, we'll clear the entire store as it's simpler
	// and documents/vectors are typically reset together
	if err := s.cortexdb.Clear(ctx); err != nil {
		return fmt.Errorf("failed to reset document store: %w", err)
	}
	return nil
}

// Helper function to get cortexdb client for DocumentStore creation
func (s *SQLiteStore) GetCortexdbStore() *core.SQLiteStore {
	return s.cortexdb
}

// Helper function to get the DB for other uses
func (s *SQLiteStore) GetDB() *cortexdb.DB {
	return s.db
}
