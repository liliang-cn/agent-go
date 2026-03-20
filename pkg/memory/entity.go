package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// EntityMemory handles storage and retrieval of entity information
type EntityMemory struct {
	store    domain.MemoryStore
	embedder domain.Embedder
}

// NewEntityMemory creates a new entity memory manager
func NewEntityMemory(store domain.MemoryStore, embedder domain.Embedder) *EntityMemory {
	return &EntityMemory{
		store:    store,
		embedder: embedder,
	}
}

// SaveEntity saves or updates information about an entity
func (em *EntityMemory) SaveEntity(ctx context.Context, entity domain.Entity) error {
	content := fmt.Sprintf("%s (%s): %s", entity.Name, entity.Type, entity.Description)
	if len(entity.Aliases) > 0 {
		content += fmt.Sprintf("\nAliases: %s", strings.Join(entity.Aliases, ", "))
	}

	memory := &domain.Memory{
		ID:         uuid.New().String(),
		Type:       domain.MemoryTypeFact,
		Content:    content,
		Importance: 0.8,
		Metadata: map[string]interface{}{
			"entity_name": entity.Name,
			"entity_type": entity.Type,
		},
	}

	return em.store.Store(ctx, memory)
}

// SearchEntities searches for entities related to a query
func (em *EntityMemory) SearchEntities(ctx context.Context, query string, topK int) ([]domain.Entity, error) {
	if em.embedder == nil {
		return nil, nil
	}

	vector, err := em.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	mems, err := em.store.Search(ctx, vector, topK, 0.5)
	if err != nil {
		return nil, err
	}

	var entities []domain.Entity
	for _, m := range mems {
		name, _ := m.Metadata["entity_name"].(string)
		entType, _ := m.Metadata["entity_type"].(string)
		if name != "" {
			entities = append(entities, domain.Entity{
				Name:        name,
				Type:        entType,
				Description: m.Content,
			})
		}
	}

	return entities, nil
}
