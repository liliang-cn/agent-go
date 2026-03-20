package usage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestModelConstructors(t *testing.T) {
	conv := NewConversation("Title")
	if conv.ID == "" || conv.Title != "Title" || conv.CreatedAt.IsZero() || conv.UpdatedAt.IsZero() {
		t.Fatalf("unexpected conversation: %+v", conv)
	}

	msg := NewMessage(conv.ID, "user", "hello", 12)
	if msg.ID == "" || msg.ConversationID != conv.ID || msg.TokenCount != 12 {
		t.Fatalf("unexpected message: %+v", msg)
	}

	record := NewUsageRecord(conv.ID, msg.ID, CallTypeLLM)
	if record.ID == "" || record.CallType != CallTypeLLM || record.CreatedAt.IsZero() {
		t.Fatalf("unexpected usage record: %+v", record)
	}
}

func TestRAGConstructorsAndMetrics(t *testing.T) {
	req := domain.QueryRequest{
		Query:        "what is the project status?",
		TopK:         4,
		Temperature:  0.2,
		MaxTokens:    256,
		ShowSources:  true,
		ShowThinking: true,
		ToolsEnabled: true,
	}
	query := NewRAGQueryRecord("conv-1", "msg-1", req)
	if query.Query != req.Query || query.TopK != 4 || query.Success {
		t.Fatalf("unexpected rag query record: %+v", query)
	}

	chunk := domain.Chunk{
		ID:         "chunk-1",
		DocumentID: "doc-1",
		Content:    "Chunk content",
		Score:      0.91,
		Metadata: map[string]interface{}{
			"source_file": "notes.md",
			"chunk_index": float64(2),
			"char_start":  float64(10),
			"char_end":    float64(25),
		},
	}
	hit := NewRAGChunkHit("query-1", chunk, 1)
	if hit.SourceFile != "notes.md" || hit.ChunkIndex != 2 || hit.CharEnd != 25 {
		t.Fatalf("unexpected chunk hit metadata: %+v", hit)
	}

	toolCall := domain.ExecutedToolCall{
		ToolCall: domain.ToolCall{
			ID:   "tool-1",
			Type: "function",
			Function: domain.FunctionCall{
				Name: "mcp_websearch_websearch_basic",
				Arguments: map[string]interface{}{
					"query": "agentgo",
				},
			},
		},
		Result:  map[string]interface{}{"ok": true},
		Success: true,
		Elapsed: "150ms",
	}
	ragToolCall := NewRAGToolCall("query-1", toolCall)
	if ragToolCall.ID == "" || ragToolCall.UUID != ragToolCall.ID || ragToolCall.Duration != 150 {
		t.Fatalf("unexpected rag tool call: %+v", ragToolCall)
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(ragToolCall.Arguments), &args); err != nil {
		t.Fatalf("expected valid tool args json: %v", err)
	}

	hits := []RAGChunkHit{
		{Score: 0.9, UsedInGeneration: true},
		{Score: 0.7, UsedInGeneration: true},
		{Score: 0.3, UsedInGeneration: false},
	}
	retrieval := CalculateRetrievalMetrics(hits)
	if retrieval.AverageScore <= 0 || retrieval.TopScore != 0.9 || len(retrieval.ScoreDistribution) != 5 {
		t.Fatalf("unexpected retrieval metrics: %+v", retrieval)
	}

	query.Answer = "A concise answer"
	quality := CalculateQualityMetrics(*query, hits)
	if quality.AnswerLength != len(query.Answer) {
		t.Fatalf("unexpected answer length: %+v", quality)
	}
	if quality.SourceUtilization <= 0 || quality.ConfidenceScore <= 0 || quality.HallucinationRisk >= 1 {
		t.Fatalf("unexpected quality metrics: %+v", quality)
	}

	emptyRetrieval := CalculateRetrievalMetrics(nil)
	if emptyRetrieval.TopScore != 0 || len(emptyRetrieval.ScoreDistribution) != 0 {
		t.Fatalf("unexpected empty retrieval metrics: %+v", emptyRetrieval)
	}

	emptyQuality := CalculateQualityMetrics(RAGQueryRecord{Answer: ""}, nil)
	if emptyQuality.HallucinationRisk != 0.9 {
		t.Fatalf("expected high hallucination risk without hits, got %+v", emptyQuality)
	}
}

func TestMarshalMetadataHelpers(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
		At   int64  `json:"at"`
	}

	in := payload{Name: "agentgo", At: time.Now().Unix()}
	raw, err := MarshalMetadata(in)
	if err != nil {
		t.Fatalf("marshal metadata failed: %v", err)
	}

	var out payload
	if err := UnmarshalMetadata(raw, &out); err != nil {
		t.Fatalf("unmarshal metadata failed: %v", err)
	}
	if out.Name != in.Name || out.At != in.At {
		t.Fatalf("unexpected metadata roundtrip: %+v", out)
	}

	if err := UnmarshalMetadata("", &out); err != nil {
		t.Fatalf("expected empty metadata to be tolerated: %v", err)
	}
}
