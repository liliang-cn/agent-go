package graphrag

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Config holds GraphRAG configuration
type Config struct {
	// EnableGraphRAG enables GraphRAG processing (entity extraction, knowledge graph)
	EnableGraphRAG bool `mapstructure:"enable_graphrag" json:"enable_graphrag"`

	// EntityTypes defines the types of entities to extract
	EntityTypes []string `mapstructure:"entity_types" json:"entity_types"`

	// ExtractionPrompt is the prompt used for entity extraction
	ExtractionPrompt string `mapstructure:"extraction_prompt" json:"extraction_prompt"`

	// MaxConcurrentExtractions limits concurrent LLM calls for extraction
	MaxConcurrentExtractions int `mapstructure:"max_concurrent_extractions" json:"max_concurrent_extractions"`

	// MinEntityLength minimum character length for entity
	MinEntityLength int `mapstructure:"min_entity_length" json:"min_entity_length"`

	//社区检测相关配置
	CommunityDetectionEnabled bool   `mapstructure:"community_detection" json:"community_detection"`
	CommunityAlgorithm        string `mapstructure:"community_algorithm" json:"community_algorithm"` // "louvain", "leiden"

	// Graph query configuration
	GraphQueryTopK int     `mapstructure:"graph_query_topk" json:"graph_query_topk"`
	GraphPrompt    string  `mapstructure:"graph_prompt" json:"graph_prompt"`
	VectorWeight   float64 `mapstructure:"vector_weight" json:"vector_weight"`
	GraphWeight    float64 `mapstructure:"graph_weight" json:"graph_weight"`
}

// DefaultConfig returns default GraphRAG configuration
func DefaultConfig() *Config {
	return &Config{
		EnableGraphRAG:            false,
		EntityTypes:               []string{"person", "organization", "location", "concept", "event", "product"},
		MaxConcurrentExtractions:  3,
		MinEntityLength:           2,
		CommunityDetectionEnabled: true,
		CommunityAlgorithm:        "louvain",
		GraphQueryTopK:            10,
		VectorWeight:              0.7,
		GraphWeight:               0.3,
	}
}

// Service handles GraphRAG operations
type Service struct {
	config       *Config
	generator    domain.Generator
	embedder     domain.Embedder
	graphStore   domain.GraphStore
	extractor    *EntityExtractor
	graphBuilder *GraphBuilder
	queryEngine  *QueryEngine

	// Caches for performance
	entityCache map[string]*CachedEntity
	cacheMutex  sync.RWMutex
}

// CachedEntity represents a cached entity with vector
type CachedEntity struct {
	ID        string
	Name      string
	Type      string
	Vector    []float64
	CreatedAt time.Time
}

// NewService creates a new GraphRAG service
func NewService(
	config *Config,
	generator domain.Generator,
	embedder domain.Embedder,
	graphStore domain.GraphStore,
) *Service {
	if config == nil {
		config = DefaultConfig()
	}

	s := &Service{
		config:      config,
		generator:   generator,
		embedder:    embedder,
		graphStore:  graphStore,
		entityCache: make(map[string]*CachedEntity),
	}

	// Initialize components
	if generator != nil {
		s.extractor = NewEntityExtractor(generator, config.EntityTypes)
	}

	if graphStore != nil {
		s.graphBuilder = NewGraphBuilder(graphStore)
		s.queryEngine = NewQueryEngine(graphStore, embedder, config)
	}

	return s
}

// ProcessDocument extracts entities and builds knowledge graph from document chunks
func (s *Service) ProcessDocument(ctx context.Context, chunks []domain.Chunk, documentID string) error {
	if !s.config.EnableGraphRAG {
		return nil
	}

	if s.extractor == nil || s.graphBuilder == nil {
		return fmt.Errorf("graphrag not properly initialized: missing extractor or graph builder")
	}

	log.Printf("[GraphRAG] Processing document %s with %d chunks", documentID, len(chunks))

	// Step 1: Extract entities from chunks
	allEntities, allRelations, err := s.extractEntities(ctx, chunks)
	if err != nil {
		return fmt.Errorf("entity extraction failed: %w", err)
	}

	log.Printf("[GraphRAG] Extracted %d entities and %d relations", len(allEntities), len(allRelations))

	// Step 2: Generate embeddings for entities
	if s.embedder != nil {
		if err := s.generateEntityEmbeddings(ctx, allEntities); err != nil {
			log.Printf("[GraphRAG] Warning: failed to generate entity embeddings: %v", err)
		}
	}

	// Step 3: Build knowledge graph
	if err := s.graphBuilder.BuildGraph(ctx, allEntities, allRelations, documentID); err != nil {
		return fmt.Errorf("graph building failed: %w", err)
	}

	log.Printf("[GraphRAG] Successfully built knowledge graph for document %s", documentID)
	return nil
}

// extractEntities extracts entities and relations from chunks using LLM
func (s *Service) extractEntities(ctx context.Context, chunks []domain.Chunk) ([]ExtractedEntity, []ExtractedRelation, error) {
	var allEntities []ExtractedEntity
	var allRelations []ExtractedRelation

	// Concurrency control
	sem := make(chan struct{}, s.config.MaxConcurrentExtractions)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []string

	for _, chunk := range chunks {
		// Skip very small chunks
		if len(chunk.Content) < 50 {
			continue
		}

		wg.Add(1)
		go func(c domain.Chunk) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Extract entities from chunk
			entities, relations, err := s.extractor.Extract(ctx, c.Content)
			if err != nil {
				mu.Lock()
				errs = append(errs, err.Error())
				mu.Unlock()
				return
			}

			// Add source chunk info
			for i := range entities {
				entities[i].SourceChunkID = c.ID
				entities[i].DocumentID = c.DocumentID
			}
			for i := range relations {
				relations[i].SourceChunkID = c.ID
			}

			mu.Lock()
			allEntities = append(allEntities, entities...)
			allRelations = append(allRelations, relations...)
			mu.Unlock()
		}(chunk)
	}

	wg.Wait()

	if len(errs) > 0 {
		log.Printf("[GraphRAG] Entity extraction had %d errors: %s", len(errs), strings.Join(errs, "; "))
	}

	// Deduplicate entities
	allEntities = s.deduplicateEntities(allEntities)
	allRelations = s.deduplicateRelations(allRelations)

	return allEntities, allRelations, nil
}

// deduplicateEntities removes duplicate entities based on name and type
func (s *Service) deduplicateEntities(entities []ExtractedEntity) []ExtractedEntity {
	seen := make(map[string]ExtractedEntity)
	for _, e := range entities {
		key := strings.ToLower(e.Name) + ":" + strings.ToLower(e.Type)
		if existing, ok := seen[key]; ok {
			// Merge sources
			existing.Sources = append(existing.Sources, e.Sources...)
			existing.SourceChunkIDs = append(existing.SourceChunkIDs, e.SourceChunkIDs...)
			seen[key] = existing
		} else {
			seen[key] = e
		}
	}

	result := make([]ExtractedEntity, 0, len(seen))
	for _, e := range seen {
		// Remove duplicates in sources
		srcSeen := make(map[string]bool)
		var uniqueSources []string
		for _, src := range e.Sources {
			if !srcSeen[src] {
				srcSeen[src] = true
				uniqueSources = append(uniqueSources, src)
			}
		}
		e.Sources = uniqueSources
		result = append(result, e)
	}

	return result
}

// deduplicateRelations removes duplicate relations
func (s *Service) deduplicateRelations(relations []ExtractedRelation) []ExtractedRelation {
	seen := make(map[string]ExtractedRelation)
	for _, r := range relations {
		key := strings.ToLower(r.Source) + "|" + strings.ToLower(r.Target) + "|" + strings.ToLower(r.Type)
		if existing, ok := seen[key]; ok {
			existing.Weight += r.Weight
			existing.Sources = append(existing.Sources, r.Sources...)
			seen[key] = existing
		} else {
			seen[key] = r
		}
	}

	result := make([]ExtractedRelation, 0, len(seen))
	for _, r := range seen {
		result = append(result, r)
	}

	return result
}

// generateEntityEmbeddings creates vector embeddings for entities
func (s *Service) generateEntityEmbeddings(ctx context.Context, entities []ExtractedEntity) error {
	// Prepare texts for embedding
	texts := make([]string, len(entities))
	for i, e := range entities {
		// Create descriptive text for embedding
		texts[i] = fmt.Sprintf("%s: %s - %s", e.Name, e.Type, e.Description)
	}

	// Batch embed
	vectors, err := s.embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return err
	}

	// Assign vectors to entities
	for i := range entities {
		if i < len(vectors) {
			entities[i].Vector = vectors[i]

			// Cache entity
			s.cacheEntity(&entities[i])
		}
	}

	return nil
}

// cacheEntity caches an entity for quick lookup
func (s *Service) cacheEntity(entity *ExtractedEntity) {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()

	key := strings.ToLower(entity.Name) + ":" + strings.ToLower(entity.Type)
	s.entityCache[key] = &CachedEntity{
		ID:        entity.ID,
		Name:      entity.Name,
		Type:      entity.Type,
		Vector:    entity.Vector,
		CreatedAt: time.Now(),
	}
}

// Query performs GraphRAG query - combines vector search with graph search
func (s *Service) Query(ctx context.Context, query string, topK int) (*GraphRAGResult, error) {
	if s.queryEngine == nil {
		return nil, fmt.Errorf("query engine not initialized")
	}

	return s.queryEngine.Query(ctx, query, topK)
}

// GetGraphStats returns statistics about the knowledge graph
func (s *Service) GetGraphStats(ctx context.Context) (*GraphStats, error) {
	if s.graphStore == nil {
		return nil, fmt.Errorf("graph store not initialized")
	}

	stats := &GraphStats{
		TotalNodes:  0,
		TotalEdges:  0,
		EntityTypes: make(map[string]int),
		Communities: 0,
		LastUpdated: time.Now(),
	}

	// For now, return basic stats
	// TODO: Implement actual stats collection from graph store
	stats.TotalNodes = len(s.entityCache)

	return stats, nil
}

// ClearGraph clears all data from the knowledge graph
func (s *Service) ClearGraph(ctx context.Context) error {
	if s.graphStore == nil {
		return fmt.Errorf("graph store not initialized")
	}

	// Clear cache
	s.cacheMutex.Lock()
	s.entityCache = make(map[string]*CachedEntity)
	s.cacheMutex.Unlock()

	// TODO: Implement actual graph clear
	log.Println("[GraphRAG] Graph cleared")

	return nil
}

// GenerateEntityID creates a unique ID for an entity
func GenerateEntityID(name, entityType string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	seed := fmt.Sprintf("%s:%s", normalized, strings.ToLower(strings.TrimSpace(entityType)))
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed)).String()
}

// GraphStats holds knowledge graph statistics
type GraphStats struct {
	TotalNodes  int            `json:"total_nodes"`
	TotalEdges  int            `json:"total_edges"`
	EntityTypes map[string]int `json:"entity_types"`
	Communities int            `json:"communities"`
	LastUpdated time.Time      `json:"last_updated"`
}

// GraphRAGResult holds the result of a GraphRAG query
type GraphRAGResult struct {
	Answer          string              `json:"answer"`
	VectorSources   []domain.Chunk      `json:"vector_sources"`
	GraphEntities   []GraphEntityResult `json:"graph_entities"`
	GraphPaths      []GraphPathResult   `json:"graph_paths"`
	UsedGraphSearch bool                `json:"used_graph_search"`
}

// GraphEntityResult represents an entity from graph search
type GraphEntityResult struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Type        string                 `json:"type"`
	Description string                 `json:"description"`
	Score       float64                `json:"score"`
	Properties  map[string]interface{} `json:"properties"`
}

// GraphPathResult represents a path between entities
type GraphPathResult struct {
	Entities []string `json:"entities"`
	Edges    []string `json:"edges"`
	Length   int      `json:"length"`
	Weight   float64  `json:"weight"`
}
