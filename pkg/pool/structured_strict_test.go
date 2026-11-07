package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The schema a caller hands GenerateStructured goes out with every object
// closed and every property required: OpenAI's strict structured outputs
// answer 400 invalid_json_schema otherwise. The caller's own map is not
// rewritten.
func TestGenerateStructuredSendsStrictSchema(t *testing.T) {
	var sent map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"a\":null}"}}]}`)
	}))
	defer srv.Close()
	c, err := NewClient("p", srv.URL, "", "m")
	if err != nil {
		t.Fatal(err)
	}
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"a": map[string]interface{}{"type": "string"}},
	}
	if _, err := c.GenerateStructured(context.Background(), "p", schema, nil); err != nil {
		t.Fatal(err)
	}
	js := sent["response_format"].(map[string]interface{})["json_schema"].(map[string]interface{})
	sch := js["schema"].(map[string]interface{})
	if sch["additionalProperties"] != false {
		t.Fatalf("schema not closed: %v", sch)
	}
	if req, _ := sch["required"].([]interface{}); len(req) != 1 || req[0] != "a" {
		t.Fatalf("required = %v", sch["required"])
	}
	if _, has := schema["required"]; has {
		t.Fatal("caller's schema was modified")
	}
}
