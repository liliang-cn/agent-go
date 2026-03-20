package memory

import (
	"math"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// ScoringConfig holds configuration for memory scoring
type ScoringConfig struct {
	// Recency settings
	RecencyWeight float64 // Weight for recency factor (default: 0.3)
	HalfLifeDays  float64 // Half-life in days for exponential decay (default: 30.0)
	EnableRecency bool    // Enable recency scoring

	// Importance settings
	ImportanceWeight float64 // Weight for importance factor (default: 0.3)
	MinImportance    float64 // Minimum importance factor (default: 0.7)
	EnableImportance bool    // Enable importance scoring

	// Length normalization settings
	LengthNormWeight float64 // Weight for length normalization (default: 0.1)
	AnchorLength     int     // Anchor length for normalization (default: 100)
	EnableLengthNorm bool    // Enable length normalization

	// Access boost settings
	AccessBoostWeight float64 // Weight for access boost (default: 0.1)
	EnableAccessBoost bool    // Enable access boost

	// Vector score weight
	VectorScoreWeight float64 // Weight for original vector similarity score (default: 0.2)
}

// DefaultScoringConfig returns default scoring configuration
func DefaultScoringConfig() *ScoringConfig {
	return &ScoringConfig{
		RecencyWeight:     0.3,
		HalfLifeDays:      30.0,
		EnableRecency:     true,
		ImportanceWeight:  0.3,
		MinImportance:     0.7,
		EnableImportance:  true,
		LengthNormWeight:  0.1,
		AnchorLength:      100,
		EnableLengthNorm:  true,
		AccessBoostWeight: 0.1,
		EnableAccessBoost: true,
		VectorScoreWeight: 0.2,
	}
}

// MemoryScorer calculates enhanced memory scores
type MemoryScorer struct {
	config *ScoringConfig
	now    func() time.Time // For testing
}

// NewMemoryScorer creates a new memory scorer
func NewMemoryScorer(config *ScoringConfig) *MemoryScorer {
	if config == nil {
		config = DefaultScoringConfig()
	}
	return &MemoryScorer{
		config: config,
		now:    time.Now,
	}
}

// Score calculates the enhanced score for a single memory
// Formula: score = baseScore * recencyFactor * importanceFactor * lengthFactor * accessFactor
func (s *MemoryScorer) Score(memory *domain.MemoryWithScore) float64 {
	if memory == nil {
		return 0
	}

	baseScore := memory.Score
	finalScore := baseScore

	// Apply recency factor
	if s.config.EnableRecency && memory.CreatedAt.IsZero() == false {
		recencyFactor := s.calculateRecencyFactor(memory.CreatedAt)
		finalScore = finalScore*(1-s.config.RecencyWeight) + baseScore*recencyFactor*s.config.RecencyWeight
	}

	// Apply importance factor
	if s.config.EnableImportance {
		importanceFactor := s.calculateImportanceFactor(memory.Importance)
		finalScore = finalScore*(1-s.config.ImportanceWeight) + baseScore*importanceFactor*s.config.ImportanceWeight
	}

	// Apply length normalization
	if s.config.EnableLengthNorm && len(memory.Content) > 0 {
		lengthFactor := s.calculateLengthFactor(len(memory.Content))
		finalScore = finalScore*(1-s.config.LengthNormWeight) + baseScore*lengthFactor*s.config.LengthNormWeight
	}

	// Apply access boost
	if s.config.EnableAccessBoost && memory.AccessCount > 0 {
		accessFactor := s.calculateAccessFactor(memory.AccessCount)
		finalScore = finalScore*(1-s.config.AccessBoostWeight) + baseScore*accessFactor*s.config.AccessBoostWeight
	}

	return finalScore
}

// ScoreAll scores and sorts all memories
func (s *MemoryScorer) ScoreAll(memories []*domain.MemoryWithScore) []*domain.MemoryWithScore {
	if len(memories) == 0 {
		return memories
	}

	// Calculate enhanced scores
	for _, m := range memories {
		m.Score = s.Score(m)
	}

	// Sort by score descending
	s.sortByScore(memories)

	return memories
}

// calculateRecencyFactor calculates time decay factor
// Uses exponential decay: exp(-ageDays / halfLife)
func (s *MemoryScorer) calculateRecencyFactor(createdAt time.Time) float64 {
	ageDays := s.now().Sub(createdAt).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}

	// Exponential decay
	halfLife := s.config.HalfLifeDays
	if halfLife <= 0 {
		halfLife = 30.0
	}

	decayFactor := math.Exp(-ageDays / halfLife)

	// Scale to [0.5, 1.0] range for smoother behavior
	return 0.5 + 0.5*decayFactor
}

// calculateImportanceFactor calculates importance factor
// Formula: min + (1-min) * importance
func (s *MemoryScorer) calculateImportanceFactor(importance float64) float64 {
	minFactor := s.config.MinImportance
	if minFactor < 0 {
		minFactor = 0
	}
	if minFactor > 1 {
		minFactor = 1
	}

	// Clamp importance to [0, 1]
	if importance < 0 {
		importance = 0
	}
	if importance > 1 {
		importance = 1
	}

	return minFactor + (1-minFactor)*importance
}

// calculateLengthFactor calculates length normalization factor
// Shorter content gets slight penalty, optimal around anchor length
// Formula: 1 / (1 + weight * log2(len/anchor))
func (s *MemoryScorer) calculateLengthFactor(contentLength int) float64 {
	anchor := s.config.AnchorLength
	if anchor <= 0 {
		anchor = 100
	}

	if contentLength <= 0 {
		return 0.5
	}

	// Normalize around anchor length
	ratio := float64(contentLength) / float64(anchor)

	// Log scale to avoid extreme values
	var logRatio float64
	if ratio > 0 {
		logRatio = math.Log2(ratio)
	}

	// Apply weight and clamp
	weight := s.config.LengthNormWeight
	factor := 1.0 / (1.0 + weight*math.Abs(logRatio))

	// Ensure factor is in reasonable range [0.5, 1.0]
	if factor < 0.5 {
		factor = 0.5
	}
	if factor > 1.0 {
		factor = 1.0
	}

	return factor
}

// calculateAccessFactor calculates access boost factor
// More accessed memories get slight boost
func (s *MemoryScorer) calculateAccessFactor(accessCount int) float64 {
	if accessCount <= 0 {
		return 0.8
	}

	// Logarithmic scaling to avoid excessive boost
	boost := 0.8 + 0.2*math.Min(1.0, math.Log2(float64(accessCount+1))/3.0)

	// Cap at 1.0
	if boost > 1.0 {
		boost = 1.0
	}

	return boost
}

// sortByScore sorts memories by score in descending order
func (s *MemoryScorer) sortByScore(memories []*domain.MemoryWithScore) {
	// Simple bubble sort (good enough for small lists)
	n := len(memories)
	for i := 0; i < n-1; i++ {
		for j := i + 1; j < n; j++ {
			if memories[i].Score < memories[j].Score {
				memories[i], memories[j] = memories[j], memories[i]
			}
		}
	}
}

// SetNowFunc sets the time function (for testing)
func (s *MemoryScorer) SetNowFunc(fn func() time.Time) {
	s.now = fn
}
