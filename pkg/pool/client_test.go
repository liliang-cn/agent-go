package pool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/pkg/domain"
)

func TestClientGenerateWithToolsIncludesNativeWebSearch(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client, err := NewClient("test", server.URL, "test-key", "gpt-4o-mini")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	result, err := client.GenerateWithTools(context.Background(), []domain.Message{
		{Role: "user", Content: "latest news"},
	}, nil, &domain.GenerationOptions{
		WebSearchMode:        domain.WebSearchModeNative,
		WebSearchContextSize: "low",
	})
	if err != nil {
		t.Fatalf("GenerateWithTools: %v", err)
	}
	if result.Content != "ok" {
		t.Fatalf("unexpected content: %q", result.Content)
	}

	webSearchOptions, ok := captured["web_search_options"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected web_search_options in request, got %v", captured)
	}
	if got := webSearchOptions["search_context_size"]; got != "low" {
		t.Fatalf("unexpected search_context_size: %v", got)
	}
}

func TestClientGenerateWithToolsRetriesAutoWithoutNativeWebSearch(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		requestCount.Add(1)

		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if _, ok := payload["web_search_options"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported parameter: web_search_options"}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fallback ok"}}]}`))
	}))
	defer server.Close()

	client, err := NewClient("test", server.URL, "test-key", "gpt-4o-mini")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	result, err := client.GenerateWithTools(context.Background(), []domain.Message{
		{Role: "user", Content: "today's news"},
	}, nil, &domain.GenerationOptions{
		WebSearchMode:        domain.WebSearchModeAuto,
		WebSearchContextSize: "medium",
	})
	if err != nil {
		t.Fatalf("GenerateWithTools: %v", err)
	}
	if result.Content != "fallback ok" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("expected 2 requests, got %d", requestCount.Load())
	}
}
