package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A provider that rejects json_schema, over a reasoning model that writes
// nothing until its budget reaches needs.
func structuredProvider(t *testing.T, needs int) (*OpenAILLMProvider, *[]int, *int) {
	t.Helper()
	var mu sync.Mutex
	var budgets []int
	schemaHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if _, ok := body["response_format"]; ok {
			schemaHits++
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This response_format type is unavailable now","type":"invalid_request_error"}}`)
			return
		}
		budget := 0
		if v, ok := body["max_completion_tokens"].(float64); ok {
			budget = int(v)
		}
		budgets = append(budgets, budget)
		finish, content := "stop", `{"ok":true}`
		if budget > 0 && budget < needs {
			finish, content = "length", ""
		}
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":%q,"message":{"role":"assistant","content":%q}}]}`, finish, content)
	}))
	t.Cleanup(srv.Close)
	gen, err := NewOpenAILLMProvider(&domain.OpenAIProviderConfig{BaseURL: srv.URL, APIKey: "k", LLMModel: "m"})
	if err != nil {
		t.Fatal(err)
	}
	return gen.(*OpenAILLMProvider), &budgets, &schemaHits
}

func TestProviderStructuredEscalatesAndRemembersRejection(t *testing.T) {
	p, budgets, schemaHits := structuredProvider(t, 1500)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		var out map[string]interface{}
		res, err := p.GenerateStructured(ctx, "classify", &out, &domain.GenerationOptions{Temperature: 0, MaxTokens: 400})
		if err != nil {
			t.Fatalf("GenerateStructured: %v (budgets %v)", err, *budgets)
		}
		if res.Raw != `{"ok":true}` {
			t.Fatalf("raw = %q", res.Raw)
		}
	}
	if *schemaHits != 1 {
		t.Fatalf("json_schema sent %d times, want once", *schemaHits)
	}
	if want := "[400 1600 400 1600]"; fmt.Sprint(*budgets) != want {
		t.Fatalf("budgets = %v, want %s", *budgets, want)
	}
}
