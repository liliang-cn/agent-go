package graphrag

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// QueryEngine handles GraphRAG queries
type QueryEngine struct {
	graphStore domain.GraphStore
	embedder   domain.Embedder
	config     *Config
}

// NewQueryEngine creates a new query engine
func NewQueryEngine(graphStore domain.GraphStore, embedder domain.Embedder, config *Config) *QueryEngine {
	return &QueryEngine{
		graphStore: graphStore,
		embedder:   embedder,
		config:     config,
	}
}

// Query performs a GraphRAG query
func (e *QueryEngine) Query(ctx context.Context, query string, topK int) (*GraphRAGResult, error) {
	if e.embedder == nil {
		return nil, fmt.Errorf("embedder not initialized")
	}

	result := &GraphRAGResult{
		UsedGraphSearch: false,
	}

	// Generate query embedding
	queryVector, err := e.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// Step 1: Hybrid search using vector similarity + graph proximity
	if e.graphStore != nil {
		// Use hybrid search from graph store
		hybridResults, err := e.graphStore.HybridSearch(ctx, queryVector, "", e.config.GraphQueryTopK)
		if err == nil && len(hybridResults) > 0 {
			result.UsedGraphSearch = true

			// Convert graph results to GraphEntityResult
			for _, hr := range hybridResults {
				if hr.Node != nil {
					result.GraphEntities = append(result.GraphEntities, GraphEntityResult{
						ID:          hr.Node.ID,
						Name:        hr.Node.ID, // Use ID as name fallback
						Type:        hr.Node.NodeType,
						Description: hr.Node.Content,
						Score:       hr.Score,
						Properties:  hr.Node.Properties,
					})
				}
			}
		}
	}

	// If no graph results, fall back to pure vector search
	if len(result.GraphEntities) == 0 {
		// This will be handled by the vector store in the main RAG flow
		result.UsedGraphSearch = false
	}

	// Step 2: Generate answer using graph context
	if len(result.GraphEntities) > 0 && e.config.EnableGraphRAG {
		answer, err := e.generateGraphAwareAnswer(ctx, query, result.GraphEntities)
		if err != nil {
			// Don't fail the whole query, just log the error
			fmt.Printf("[GraphRAG] Warning: failed to generate graph-aware answer: %v\n", err)
		} else {
			result.Answer = answer
		}
	}

	return result, nil
}

// generateGraphAwareAnswer generates an answer using graph context
func (e *QueryEngine) generateGraphAwareAnswer(ctx context.Context, query string, entities []GraphEntityResult) (string, error) {
	// Build context from entities
	var contextBuilder strings.Builder
	contextBuilder.WriteString("Relevant entities from knowledge graph:\n\n")

	for i, entity := range entities {
		contextBuilder.WriteString(fmt.Sprintf("%d. **%s** (%s)\n", i+1, entity.Name, entity.Type))
		if entity.Description != "" {
			contextBuilder.WriteString(fmt.Sprintf("   Description: %s\n", entity.Description))
		}
		if len(entity.Properties) > 0 {
			for k, v := range entity.Properties {
				contextBuilder.WriteString(fmt.Sprintf("   %s: %v\n", k, v))
			}
		}
		contextBuilder.WriteString("\n")
	}

	// In a full implementation, we would call the LLM here with a prompt like:
	// prompt := fmt.Sprintf(`Based on the following knowledge graph context, answer the user's question.
	// ## Question: %s
	// ## Knowledge Graph Context: %s
	// ## Instructions: ...`, query, contextBuilder.String())

	// For now, return the context as answer
	return contextBuilder.String(), nil
}

// SearchEntities searches for entities by name or type
func (e *QueryEngine) SearchEntities(ctx context.Context, query string, entityType string, limit int) ([]GraphEntityResult, error) {
	// TODO: Implement entity search
	return nil, fmt.Errorf("SearchEntities not implemented")
}

// GetEntityContext gets surrounding context for an entity (neighbors, paths)
func (e *QueryEngine) GetEntityContext(ctx context.Context, entityID string, depth int) (*EntityContext, error) {
	// TODO: Implement entity context retrieval
	return nil, fmt.Errorf("GetEntityContext not implemented")
}

// EntityContext holds context around an entity
type EntityContext struct {
	Entity   GraphEntityResult   `json:"entity"`
	Parents  []GraphEntityResult `json:"parents"`
	Children []GraphEntityResult `json:"children"`
	Paths    []GraphPathResult   `json:"paths"`
}
