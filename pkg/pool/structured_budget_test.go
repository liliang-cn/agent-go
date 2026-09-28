package pool

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

// structuredServer plays a provider that rejects json_schema, and a reasoning
// model that writes nothing until its budget reaches `needs`.
type structuredServer struct {
	mu        sync.Mutex
	needs     int
	schemaHit int
	budgets   []int
}

func (s *structuredServer) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := body["response_format"]; ok {
		s.schemaHit++
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"This response_format type is unavailable now"}}`)
		return
	}
	budget := 0
	if v, ok := body["max_tokens"].(float64); ok {
		budget = int(v)
	}
	s.budgets = append(s.budgets, budget)
	finish, content := "stop", `{"ok":true}`
	if budget > 0 && budget < s.needs {
		finish, content = "length", ""
	}
	fmt.Fprintf(w, `{"choices":[{"finish_reason":%q,"message":{"content":%q}}]}`, finish, content)
}

func newStructuredClient(t *testing.T, s *structuredServer) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	c, err := NewClient("p", srv.URL, "", "m")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A reasoning model that spends a 400-token cap before writing is re-asked
// with a larger budget instead of reported as having given no JSON.
func TestStructuredEscalatesATruncatedEmptyReply(t *testing.T) {
	s := &structuredServer{needs: 1500}
	c := newStructuredClient(t, s)
	res, err := c.GenerateStructured(context.Background(), "classify", map[string]any{"type": "object"}, &domain.GenerationOptions{MaxTokens: 400})
	if err != nil {
		t.Fatalf("GenerateStructured: %v (budgets %v)", err, s.budgets)
	}
	if res.Raw != `{"ok":true}` {
		t.Fatalf("raw = %q", res.Raw)
	}
	if want := []int{400, 1600}; fmt.Sprint(s.budgets) != fmt.Sprint(want) {
		t.Fatalf("budgets = %v, want %v", s.budgets, want)
	}
}

// Escalation is bounded: a reply that never fits fails after two steps.
func TestStructuredEscalationIsBounded(t *testing.T) {
	s := &structuredServer{needs: 1 << 30}
	c := newStructuredClient(t, s)
	if _, err := c.GenerateStructured(context.Background(), "classify", map[string]any{"type": "object"}, &domain.GenerationOptions{MaxTokens: 400}); err == nil {
		t.Fatal("expected an error when no budget fits")
	}
	if want := []int{400, 1600, 6400}; fmt.Sprint(s.budgets) != fmt.Sprint(want) {
		t.Fatalf("budgets = %v, want %v", s.budgets, want)
	}
}

// A provider that refused json_schema once is not asked again.
func TestStructuredRemembersASchemaRejection(t *testing.T) {
	s := &structuredServer{}
	c := newStructuredClient(t, s)
	for i := 0; i < 3; i++ {
		if _, err := c.GenerateStructured(context.Background(), "classify", map[string]any{"type": "object"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if s.schemaHit != 1 {
		t.Fatalf("json_schema sent %d times, want once", s.schemaHit)
	}
}
