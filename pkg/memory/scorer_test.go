package memory

import (
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestMemoryScorer_Score(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		config   *ScoringConfig
		memory   *domain.MemoryWithScore
		minScore float64
		maxScore float64
	}{
		{
			name:   "fresh high importance memory",
			config: DefaultScoringConfig(),
			memory: &domain.MemoryWithScore{
				Memory: &domain.Memory{
					ID:         "1",
					Content:    "This is an important memory about testing",
					Importance: 0.9,
					CreatedAt:  now.Add(-1 * time.Hour), // 1 hour ago
				},
				Score: 0.8,
			},
			minScore: 0.5,
			maxScore: 1.0,
		},
		{
			name:   "old low importance memory",
			config: DefaultScoringConfig(),
			memory: &domain.MemoryWithScore{
				Memory: &domain.Memory{
					ID:         "2",
					Content:    "Old memory",
					Importance: 0.3,
					CreatedAt:  now.Add(-60 * 24 * time.Hour), // 60 days ago
				},
				Score: 0.5,
			},
			minScore: 0.0,
			maxScore: 0.7,
		},
		{
			name:   "memory with high access count",
			config: DefaultScoringConfig(),
			memory: &domain.MemoryWithScore{
				Memory: &domain.Memory{
					ID:          "3",
					Content:     "Frequently accessed memory",
					Importance:  0.5,
					AccessCount: 10,
					CreatedAt:   now.Add(-10 * 24 * time.Hour),
				},
				Score: 0.6,
			},
			minScore: 0.4,
			maxScore: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scorer := NewMemoryScorer(tt.config)
			score := scorer.Score(tt.memory)

			if score < tt.minScore || score > tt.maxScore {
				t.Errorf("Score() = %v, want between %v and %v", score, tt.minScore, tt.maxScore)
			}
		})
	}
}

func TestMemoryScorer_RecencyFactor(t *testing.T) {
	now := time.Now()
	config := DefaultScoringConfig()
	scorer := NewMemoryScorer(config)
	scorer.SetNowFunc(func() time.Time { return now })

	tests := []struct {
		name      string
		createdAt time.Time
		minFactor float64
		maxFactor float64
	}{
		{
			name:      "just created",
			createdAt: now,
			minFactor: 0.9,
			maxFactor: 1.0,
		},
		{
			name:      "30 days old (half life)",
			createdAt: now.Add(-30 * 24 * time.Hour),
			minFactor: 0.6,
			maxFactor: 0.75,
		},
		{
			name:      "60 days old",
			createdAt: now.Add(-60 * 24 * time.Hour),
			minFactor: 0.5,
			maxFactor: 0.7,
		},
		{
			name:      "365 days old",
			createdAt: now.Add(-365 * 24 * time.Hour),
			minFactor: 0.5,
			maxFactor: 0.55,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factor := scorer.calculateRecencyFactor(tt.createdAt)
			if factor < tt.minFactor || factor > tt.maxFactor {
				t.Errorf("recencyFactor() = %v, want between %v and %v", factor, tt.minFactor, tt.maxFactor)
			}
		})
	}
}

func TestMemoryScorer_ImportanceFactor(t *testing.T) {
	config := DefaultScoringConfig()
	scorer := NewMemoryScorer(config)

	tests := []struct {
		importance float64
		minFactor  float64
		maxFactor  float64
	}{
		{importance: 1.0, minFactor: 0.95, maxFactor: 1.0},
		{importance: 0.5, minFactor: 0.8, maxFactor: 0.9},
		{importance: 0.0, minFactor: 0.65, maxFactor: 0.75},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			factor := scorer.calculateImportanceFactor(tt.importance)
			if factor < tt.minFactor || factor > tt.maxFactor {
				t.Errorf("importanceFactor(%v) = %v, want between %v and %v", tt.importance, factor, tt.minFactor, tt.maxFactor)
			}
		})
	}
}

func TestMemoryScorer_ScoreAll(t *testing.T) {
	now := time.Now()
	config := DefaultScoringConfig()
	scorer := NewMemoryScorer(config)

	memories := []*domain.MemoryWithScore{
		{
			Memory: &domain.Memory{
				ID:         "1",
				Content:    "Low importance old memory",
				Importance: 0.2,
				CreatedAt:  now.Add(-100 * 24 * time.Hour),
			},
			Score: 0.9,
		},
		{
			Memory: &domain.Memory{
				ID:         "2",
				Content:    "High importance new memory",
				Importance: 0.9,
				CreatedAt:  now.Add(-1 * time.Hour),
			},
			Score: 0.7,
		},
		{
			Memory: &domain.Memory{
				ID:         "3",
				Content:    "Medium memory",
				Importance: 0.5,
				CreatedAt:  now.Add(-10 * 24 * time.Hour),
			},
			Score: 0.8,
		},
	}

	result := scorer.ScoreAll(memories)

	if len(result) != 3 {
		t.Errorf("ScoreAll() returned %d memories, want 3", len(result))
	}

	// Check that results are sorted by score
	for i := 1; i < len(result); i++ {
		if result[i-1].Score < result[i].Score {
			t.Errorf("ScoreAll() not sorted: result[%d].Score (%v) < result[%d].Score (%v)", i-1, result[i-1].Score, i, result[i].Score)
		}
	}
}
