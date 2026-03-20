package usage

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// RAGQueryRecord represents a detailed record of a RAG query
type RAGQueryRecord struct {
	ID             string  `json:"id" db:"id"`
	ConversationID string  `json:"conversation_id" db:"conversation_id"`
	MessageID      string  `json:"message_id" db:"message_id"`
	Query          string  `json:"query" db:"query"`
	Answer         string  `json:"answer" db:"answer"`
	TopK           int     `json:"top_k" db:"top_k"`
	Temperature    float64 `json:"temperature" db:"temperature"`
	MaxTokens      int     `json:"max_tokens" db:"max_tokens"`
	ShowSources    bool    `json:"show_sources" db:"show_sources"`
	ShowThinking   bool    `json:"show_thinking" db:"show_thinking"`
	ToolsEnabled   bool    `json:"tools_enabled" db:"tools_enabled"`

	// Performance metrics
	TotalLatency   int64 `json:"total_latency" db:"total_latency"`     // Total query time in ms
	RetrievalTime  int64 `json:"retrieval_time" db:"retrieval_time"`   // Retrieval phase time in ms
	GenerationTime int64 `json:"generation_time" db:"generation_time"` // Generation phase time in ms

	// Results
	ChunksFound    int    `json:"chunks_found" db:"chunks_found"`         // Number of chunks retrieved
	ToolCallsCount int    `json:"tool_calls_count" db:"tool_calls_count"` // Number of tool calls made
	Success        bool   `json:"success" db:"success"`
	ErrorMessage   string `json:"error_message" db:"error_message"`

	// Token tracking
	InputTokens   int     `json:"input_tokens" db:"input_tokens"`     // Tokens in the input (query + context)
	OutputTokens  int     `json:"output_tokens" db:"output_tokens"`   // Tokens in the generated output
	TotalTokens   int     `json:"total_tokens" db:"total_tokens"`     // Total tokens used
	EstimatedCost float64 `json:"estimated_cost" db:"estimated_cost"` // Estimated cost in USD
	Model         string  `json:"model" db:"model"`                   // Model used for generation

	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// RAGChunkHit represents a single chunk that was retrieved for a query
type RAGChunkHit struct {
	ID               string  `json:"id" db:"id"`
	RAGQueryID       string  `json:"rag_query_id" db:"rag_query_id"`
	ChunkID          string  `json:"chunk_id" db:"chunk_id"`
	DocumentID       string  `json:"document_id" db:"document_id"`
	Content          string  `json:"content" db:"content"`
	Score            float64 `json:"score" db:"score"`                           // Similarity score
	Rank             int     `json:"rank" db:"rank"`                             // Rank in the result set (1, 2, 3, ...)
	UsedInGeneration bool    `json:"used_in_generation" db:"used_in_generation"` // Whether this chunk was actually used

	// Metadata
	SourceFile string `json:"source_file" db:"source_file"`
	ChunkIndex int    `json:"chunk_index" db:"chunk_index"` // Index within the source document
	CharStart  int    `json:"char_start" db:"char_start"`   // Start position in original document
	CharEnd    int    `json:"char_end" db:"char_end"`       // End position in original document

	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// RAGToolCall represents a tool call made during RAG processing
type RAGToolCall struct {
	ID           string `json:"id" db:"id"`
	UUID         string `json:"uuid"` // Same as ID, for frontend compatibility
	RAGQueryID   string `json:"rag_query_id" db:"rag_query_id"`
	ToolName     string `json:"tool_name" db:"tool_name"`
	Arguments    string `json:"arguments" db:"arguments"` // JSON string
	Result       string `json:"result" db:"result"`       // JSON string
	Success      bool   `json:"success" db:"success"`
	ErrorMessage string `json:"error_message" db:"error_message"`
	Duration     int64  `json:"duration" db:"duration"` // Execution time in ms

	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// RAGQueryVisualization represents the complete visualization data for a RAG query
type RAGQueryVisualization struct {
	Query     RAGQueryRecord `json:"query"`
	ChunkHits []RAGChunkHit  `json:"chunk_hits"`
	ToolCalls []RAGToolCall  `json:"tool_calls"`

	// Analytics
	RetrievalMetrics RAGRetrievalMetrics `json:"retrieval_metrics"`
	QualityMetrics   RAGQualityMetrics   `json:"quality_metrics"`
}

// RAGRetrievalMetrics provides insights into the retrieval performance
type RAGRetrievalMetrics struct {
	AverageScore      float64       `json:"average_score"`
	TopScore          float64       `json:"top_score"`
	ScoreDistribution []ScoreBucket `json:"score_distribution"`
	DiversityScore    float64       `json:"diversity_score"` // How diverse are the retrieved chunks
	CoverageScore     float64       `json:"coverage_score"`  // How well do chunks cover the query
}

// RAGQualityMetrics provides insights into the answer quality
type RAGQualityMetrics struct {
	AnswerLength      int     `json:"answer_length"`
	SourceUtilization float64 `json:"source_utilization"` // Percentage of sources actually used
	ConfidenceScore   float64 `json:"confidence_score"`   // Estimated confidence
	HallucinationRisk float64 `json:"hallucination_risk"` // Risk of hallucination
	FactualityScore   float64 `json:"factuality_score"`   // Estimated factuality
}

// ScoreBucket represents a bucket in score distribution
type ScoreBucket struct {
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Count int     `json:"count"`
}

// RAGSearchFilter represents filters for searching RAG queries
type RAGSearchFilter struct {
	ConversationID string    `json:"conversation_id,omitempty"`
	Query          string    `json:"query,omitempty"`
	MinScore       float64   `json:"min_score,omitempty"`
	MaxScore       float64   `json:"max_score,omitempty"`
	StartTime      time.Time `json:"start_time,omitempty"`
	EndTime        time.Time `json:"end_time,omitempty"`
	ToolsUsed      []string  `json:"tools_used,omitempty"`
	Limit          int       `json:"limit,omitempty"`
	Offset         int       `json:"offset,omitempty"`
}

// NewRAGQueryRecord creates a new RAG query record
func NewRAGQueryRecord(conversationID, messageID string, request domain.QueryRequest) *RAGQueryRecord {
	return &RAGQueryRecord{
		ID:             uuid.New().String(),
		ConversationID: conversationID,
		MessageID:      messageID,
		Query:          request.Query,
		TopK:           request.TopK,
		Temperature:    request.Temperature,
		MaxTokens:      request.MaxTokens,
		ShowSources:    request.ShowSources,
		ShowThinking:   request.ShowThinking,
		ToolsEnabled:   request.ToolsEnabled,
		Success:        false, // Will be updated when query completes
		CreatedAt:      time.Now(),
	}
}

// NewRAGChunkHit creates a new chunk hit record
func NewRAGChunkHit(ragQueryID string, chunk domain.Chunk, rank int) *RAGChunkHit {
	hit := &RAGChunkHit{
		ID:         uuid.New().String(),
		RAGQueryID: ragQueryID,
		ChunkID:    chunk.ID,
		DocumentID: chunk.DocumentID,
		Content:    chunk.Content,
		Score:      chunk.Score,
		Rank:       rank,
		CreatedAt:  time.Now(),
	}

	// Extract metadata if available
	if chunk.Metadata != nil {
		if sourceFile, ok := chunk.Metadata["source_file"].(string); ok {
			hit.SourceFile = sourceFile
		}
		if chunkIndex, ok := chunk.Metadata["chunk_index"].(float64); ok {
			hit.ChunkIndex = int(chunkIndex)
		}
		if charStart, ok := chunk.Metadata["char_start"].(float64); ok {
			hit.CharStart = int(charStart)
		}
		if charEnd, ok := chunk.Metadata["char_end"].(float64); ok {
			hit.CharEnd = int(charEnd)
		}
	}

	return hit
}

// NewRAGToolCall creates a new tool call record
func NewRAGToolCall(ragQueryID string, toolCall domain.ExecutedToolCall) *RAGToolCall {
	// Convert arguments map to JSON string
	argsJSON, _ := json.Marshal(toolCall.Function.Arguments)
	// Convert result interface to JSON string
	resultJSON, _ := json.Marshal(toolCall.Result)

	// Parse duration from elapsed string (e.g., "1.23s" -> 1230 ms)
	var durationMS int64
	if toolCall.Elapsed != "" {
		// Simple parsing - assuming format is like "1.23s"
		if d, err := time.ParseDuration(toolCall.Elapsed); err == nil {
			durationMS = d.Milliseconds()
		}
	}

	id := uuid.New().String()
	return &RAGToolCall{
		ID:           id,
		UUID:         id, // Same as ID, for frontend compatibility
		RAGQueryID:   ragQueryID,
		ToolName:     toolCall.Function.Name,
		Arguments:    string(argsJSON),
		Result:       string(resultJSON),
		Success:      toolCall.Success,
		ErrorMessage: toolCall.Error,
		Duration:     durationMS,
		CreatedAt:    time.Now(),
	}
}

// CalculateRetrievalMetrics calculates metrics for the retrieval results
func CalculateRetrievalMetrics(hits []RAGChunkHit) RAGRetrievalMetrics {
	if len(hits) == 0 {
		return RAGRetrievalMetrics{}
	}

	// Calculate average and top scores
	var totalScore float64
	var topScore float64
	for _, hit := range hits {
		totalScore += hit.Score
		if hit.Score > topScore {
			topScore = hit.Score
		}
	}
	averageScore := totalScore / float64(len(hits))

	// Create score distribution (5 buckets)
	buckets := make([]ScoreBucket, 5)
	for i := 0; i < 5; i++ {
		buckets[i] = ScoreBucket{
			Min: float64(i) * 0.2,
			Max: float64(i+1) * 0.2,
		}
	}

	for _, hit := range hits {
		bucketIndex := int(hit.Score * 5)
		if bucketIndex >= 5 {
			bucketIndex = 4
		}
		buckets[bucketIndex].Count++
	}

	// Calculate diversity (simplified - based on score variance)
	var variance float64
	for _, hit := range hits {
		variance += (hit.Score - averageScore) * (hit.Score - averageScore)
	}
	variance /= float64(len(hits))
	diversityScore := variance // Higher variance = more diversity

	// Coverage score (simplified - based on top scores)
	coverageScore := averageScore
	if topScore > 0.8 {
		coverageScore += 0.1
	}

	return RAGRetrievalMetrics{
		AverageScore:      averageScore,
		TopScore:          topScore,
		ScoreDistribution: buckets,
		DiversityScore:    diversityScore,
		CoverageScore:     coverageScore,
	}
}

// RAGAnalytics provides comprehensive analytics about RAG performance
type RAGAnalytics struct {
	// Basic metrics
	TotalQueries int     `json:"total_queries"`
	SuccessRate  float64 `json:"success_rate"`
	AvgLatency   float64 `json:"avg_latency"`
	AvgChunks    float64 `json:"avg_chunks"`
	AvgScore     float64 `json:"avg_score"`

	// Performance distribution
	FastQueries   int `json:"fast_queries"`   // < 1s
	MediumQueries int `json:"medium_queries"` // 1-5s
	SlowQueries   int `json:"slow_queries"`   // > 5s

	// Quality distribution
	HighQualityQueries   int `json:"high_quality_queries"`   // score > 0.8
	MediumQualityQueries int `json:"medium_quality_queries"` // score 0.5-0.8
	LowQualityQueries    int `json:"low_quality_queries"`    // score < 0.5

	// Time range
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
}

// CalculateQualityMetrics calculates quality metrics for the answer
func CalculateQualityMetrics(query RAGQueryRecord, hits []RAGChunkHit) RAGQualityMetrics {
	answerLength := len(query.Answer)

	// Calculate source utilization
	usedSources := 0
	for _, hit := range hits {
		if hit.UsedInGeneration {
			usedSources++
		}
	}

	sourceUtilization := 0.0
	if len(hits) > 0 {
		sourceUtilization = float64(usedSources) / float64(len(hits))
	}

	// Simplified quality estimates (would need more sophisticated analysis in practice)
	confidenceScore := sourceUtilization * 0.8 // Higher source utilization = higher confidence
	if len(hits) > 0 {
		avgScore := 0.0
		for _, hit := range hits {
			avgScore += hit.Score
		}
		avgScore /= float64(len(hits))
		confidenceScore += avgScore * 0.2
	}

	// Hallucination risk (inverse of confidence and source quality)
	hallucinationRisk := 1.0 - confidenceScore
	if len(hits) == 0 {
		hallucinationRisk = 0.9 // High risk if no sources
	}

	// Factuality score (based on source quality and utilization)
	factualityScore := sourceUtilization * 0.6
	if len(hits) > 0 && hits[0].Score > 0.7 {
		factualityScore += 0.3
	}

	return RAGQualityMetrics{
		AnswerLength:      answerLength,
		SourceUtilization: sourceUtilization,
		ConfidenceScore:   confidenceScore,
		HallucinationRisk: hallucinationRisk,
		FactualityScore:   factualityScore,
	}
}
