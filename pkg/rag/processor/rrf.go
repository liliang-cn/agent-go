package processor

import (
	"sort"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// ReciprocalRankFusion merges two ranked lists of chunks (vector and keyword)
// using the Reciprocal Rank Fusion (RRF) algorithm with a standard constant k = 60.
func ReciprocalRankFusion(vectorChunks []domain.Chunk, keywordChunks []domain.Chunk, topK int) []domain.Chunk {
	const k = 60.0

	// Map to accumulate RRF scores and store chunk details
	scores := make(map[string]float64)
	chunkMap := make(map[string]domain.Chunk)

	// Process vector chunks
	for i, chunk := range vectorChunks {
		rank := float64(i + 1)
		scores[chunk.ID] += 1.0 / (k + rank)
		if _, exists := chunkMap[chunk.ID]; !exists {
			chunkMap[chunk.ID] = chunk
		}
	}

	// Process keyword chunks
	for i, chunk := range keywordChunks {
		rank := float64(i + 1)
		scores[chunk.ID] += 1.0 / (k + rank)
		if _, exists := chunkMap[chunk.ID]; !exists {
			chunkMap[chunk.ID] = chunk
		}
	}

	// Build the merged slice
	merged := make([]domain.Chunk, 0, len(chunkMap))
	for id, score := range scores {
		chunk := chunkMap[id]
		chunk.Score = score
		merged = append(merged, chunk)
	}

	// Sort descending by RRF score
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Score == merged[j].Score {
			return merged[i].ID < merged[j].ID // Deterministic tie breaker
		}
		return merged[i].Score > merged[j].Score
	})

	// Slice to topK
	if topK > 0 && len(merged) > topK {
		merged = merged[:topK]
	}

	return merged
}
