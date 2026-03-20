package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// BM25Config holds configuration for BM25 search
type BM25Config struct {
	K1 float64 // BM25 k1 parameter (default: 1.5)
	B  float64 // BM25 b parameter (default: 0.75)
}

// DefaultBM25Config returns default BM25 configuration
func DefaultBM25Config() *BM25Config {
	return &BM25Config{
		K1: 1.5,
		B:  0.75,
	}
}

// BM25Searcher implements BM25 full-text search
type BM25Searcher struct {
	dbPath string
	config *BM25Config
}

// NewBM25Searcher creates a new BM25 searcher
func NewBM25Searcher(dbPath string, config *BM25Config) *BM25Searcher {
	if config == nil {
		config = DefaultBM25Config()
	}
	return &BM25Searcher{
		dbPath: dbPath,
		config: config,
	}
}

// Search performs BM25 search
func (s *BM25Searcher) Search(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Get all documents for BM25 calculation
	rows, err := db.Query(`
		SELECT id, content, metadata, created_at
		FROM embeddings
		WHERE content IS NOT NULL AND content != ''
		ORDER BY created_at DESC
		LIMIT 10000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type doc struct {
		id        string
		content   string
		metadata  map[string]interface{}
		createdAt time.Time
	}

	var documents []doc
	var totalLength int
	for rows.Next() {
		var id, content, metadataJSON string
		var createdAt time.Time
		if err := rows.Scan(&id, &content, &metadataJSON, &createdAt); err != nil {
			continue
		}

		var metadata map[string]interface{}
		_ = json.Unmarshal([]byte(metadataJSON), &metadata)

		documents = append(documents, doc{
			id:        id,
			content:   content,
			metadata:  metadata,
			createdAt: createdAt,
		})
		totalLength += len(content)
	}

	if len(documents) == 0 {
		return nil, nil
	}

	// Calculate average document length
	avgDocLength := float64(totalLength) / float64(len(documents))

	// Tokenize query
	queryTerms := tokenize(query)
	if len(queryTerms) == 0 {
		return nil, nil
	}

	// Calculate document frequencies
	docFreq := make(map[string]int)
	for _, d := range documents {
		terms := tokenize(d.content)
		seen := make(map[string]bool)
		for _, t := range terms {
			if !seen[t] {
				docFreq[t]++
				seen[t] = true
			}
		}
	}

	// Calculate BM25 scores
	type scoredDoc struct {
		doc   doc
		score float64
	}

	var results []scoredDoc
	for _, d := range documents {
		score := s.calculateBM25(d.content, queryTerms, docFreq, len(documents), avgDocLength)
		if score > 0 {
			results = append(results, scoredDoc{doc: d, score: score})
		}
	}

	// Sort by score
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[i].score < results[j].score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	// Return top K
	if len(results) > topK {
		results = results[:topK]
	}

	// Convert to domain types
	var memories []*domain.MemoryWithScore
	for _, r := range results {
		bankID, _ := r.doc.metadata["bank_id"].(string)
		memType, _ := r.doc.metadata["type"].(string)

		memories = append(memories, &domain.MemoryWithScore{
			Memory: &domain.Memory{
				ID:        r.doc.id,
				Content:   r.doc.content,
				Metadata:  r.doc.metadata,
				SessionID: bankID,
				Type:      domain.MemoryType(memType),
				CreatedAt: r.doc.createdAt,
			},
			Score: r.score,
		})
	}

	return memories, nil
}

// calculateBM25 calculates BM25 score for a document
func (s *BM25Searcher) calculateBM25(docContent string, queryTerms []string, docFreq map[string]int, totalDocs int, avgDocLength float64) float64 {
	docTerms := tokenize(docContent)
	docLength := float64(len(docTerms))

	// Calculate term frequencies in document
	termFreq := make(map[string]int)
	for _, t := range docTerms {
		termFreq[t]++
	}

	var score float64
	k1 := s.config.K1
	b := s.config.B

	for _, qt := range queryTerms {
		tf := float64(termFreq[qt])
		if tf == 0 {
			continue
		}

		df := float64(docFreq[qt])
		if df == 0 {
			continue
		}

		// IDF calculation
		idf := math.Log((float64(totalDocs) - df + 0.5) / (df + 0.5))
		if idf < 0 {
			idf = 0
		}

		// BM25 term score
		numerator := tf * (k1 + 1)
		denominator := tf + k1*(1-b+b*(docLength/avgDocLength))

		score += idf * (numerator / denominator)
	}

	return score
}

// tokenize tokenizes text into terms
func tokenize(text string) []string {
	// Simple tokenization: lowercase and split on whitespace/punctuation
	text = strings.ToLower(text)

	// Replace punctuation with spaces
	replacer := strings.NewReplacer(
		".", " ",
		",", " ",
		"!", " ",
		"?", " ",
		";", " ",
		":", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
		"{", " ",
		"}", " ",
		"\"", " ",
		"'", " ",
		"-", " ",
		"_", " ",
		"/", " ",
		"\\", " ",
		"\n", " ",
		"\t", " ",
	)
	text = replacer.Replace(text)

	// Split and filter empty strings
	words := strings.Fields(text)
	var terms []string
	for _, w := range words {
		if len(w) > 1 { // Skip single characters
			terms = append(terms, w)
		}
	}

	return terms
}

// RRFFusion performs Reciprocal Rank Fusion
// k is the RRF parameter (default: 60)
func RRFFusion(vector, bm25 []*domain.MemoryWithScore, k float64) []*domain.MemoryWithScore {
	if k <= 0 {
		k = 60.0
	}

	// Calculate RRF scores
	scores := make(map[string]float64)
	memories := make(map[string]*domain.MemoryWithScore)

	// Vector results
	for rank, m := range vector {
		if m == nil || m.Memory == nil {
			continue
		}
		id := m.ID
		if id == "" {
			id = m.Content
		}
		scores[id] += 1.0 / (k + float64(rank+1))
		if _, exists := memories[id]; !exists {
			memories[id] = m
		}
	}

	// BM25 results
	for rank, m := range bm25 {
		if m == nil || m.Memory == nil {
			continue
		}
		id := m.ID
		if id == "" {
			id = m.Content
		}
		scores[id] += 1.0 / (k + float64(rank+1))
		if _, exists := memories[id]; !exists {
			memories[id] = m
		}
	}

	// Build result list
	var results []*domain.MemoryWithScore
	for id, score := range scores {
		if m, exists := memories[id]; exists {
			result := &domain.MemoryWithScore{
				Memory: m.Memory,
				Score:  score,
			}
			results = append(results, result)
		}
	}

	// Sort by RRF score
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[i].Score < results[j].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}
