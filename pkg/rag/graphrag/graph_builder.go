package graphrag

import (
	"context"
	"fmt"
	"log"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// GraphBuilder builds and maintains the knowledge graph
type GraphBuilder struct {
	graphStore domain.GraphStore
}

// NewGraphBuilder creates a new graph builder
func NewGraphBuilder(graphStore domain.GraphStore) *GraphBuilder {
	return &GraphBuilder{
		graphStore: graphStore,
	}
}

// BuildGraph builds the knowledge graph from entities and relations
func (b *GraphBuilder) BuildGraph(ctx context.Context, entities []ExtractedEntity, relations []ExtractedRelation, documentID string) error {
	if b.graphStore == nil {
		return fmt.Errorf("graph store not initialized")
	}

	// Initialize graph schema if needed
	if err := b.graphStore.InitGraphSchema(ctx); err != nil {
		log.Printf("[GraphRAG] Warning: failed to init graph schema: %v", err)
	}

	// Upsert entities as graph nodes
	for _, entity := range entities {
		// Add document ID to properties
		props := make(map[string]interface{})
		for k, v := range entity.Properties {
			props[k] = v
		}
		props["document_id"] = documentID
		props["description"] = entity.Description
		props["confidence"] = entity.Confidence
		if len(entity.Sources) > 0 {
			props["sources"] = entity.Sources
		}

		node := domain.GraphNode{
			ID:         entity.ID,
			Content:    entity.Description,
			NodeType:   entity.Type,
			Properties: props,
			Vector:     entity.Vector,
		}

		if err := b.graphStore.UpsertNode(ctx, node); err != nil {
			log.Printf("[GraphRAG] Warning: failed to upsert node %s: %v", entity.Name, err)
		}
	}

	// Upsert relations as graph edges
	for _, rel := range relations {
		// Generate edge ID
		edgeID := fmt.Sprintf("%s->%s->%s", rel.Source, rel.Type, rel.Target)

		props := make(map[string]interface{})
		for k, v := range rel.Properties {
			props[k] = v
		}
		props["document_id"] = documentID

		edge := domain.GraphEdge{
			ID:         edgeID,
			FromNodeID: GenerateEntityID(rel.Source, ""),
			ToNodeID:   GenerateEntityID(rel.Target, ""),
			EdgeType:   rel.Type,
			Weight:     rel.Weight,
			Properties: props,
		}

		if err := b.graphStore.UpsertEdge(ctx, edge); err != nil {
			log.Printf("[GraphRAG] Warning: failed to upsert edge %s: %v", edgeID, err)
		}
	}

	log.Printf("[GraphRAG] Built graph with %d nodes and %d edges", len(entities), len(relations))
	return nil
}

// AddEntity adds a single entity to the graph
func (b *GraphBuilder) AddEntity(ctx context.Context, entity *ExtractedEntity) error {
	if b.graphStore == nil {
		return fmt.Errorf("graph store not initialized")
	}

	node := domain.GraphNode{
		ID:         entity.ID,
		Content:    entity.Description,
		NodeType:   entity.Type,
		Properties: entity.Properties,
		Vector:     entity.Vector,
	}

	return b.graphStore.UpsertNode(ctx, node)
}

// AddRelation adds a single relation to the graph
func (b *GraphBuilder) AddRelation(ctx context.Context, relation *ExtractedRelation) error {
	if b.graphStore == nil {
		return fmt.Errorf("graph store not initialized")
	}

	edgeID := fmt.Sprintf("%s->%s->%s", relation.Source, relation.Type, relation.Target)

	edge := domain.GraphEdge{
		ID:         edgeID,
		FromNodeID: GenerateEntityID(relation.Source, ""),
		ToNodeID:   GenerateEntityID(relation.Target, ""),
		EdgeType:   relation.Type,
		Weight:     relation.Weight,
		Properties: relation.Properties,
	}

	return b.graphStore.UpsertEdge(ctx, edge)
}

// GetEntity retrieves an entity from the graph
func (b *GraphBuilder) GetEntity(ctx context.Context, entityID string) (*domain.GraphNode, error) {
	// TODO: Implement GetNode if available in graph store
	return nil, fmt.Errorf("GetEntity not implemented")
}

// GetNeighbors retrieves neighboring entities
func (b *GraphBuilder) GetNeighbors(ctx context.Context, entityID string, edgeType string, depth int) ([]*domain.GraphNode, error) {
	// TODO: Implement neighborhood traversal
	return nil, fmt.Errorf("GetNeighbors not implemented")
}
