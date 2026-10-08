package pool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReasoningEffortSentAndDroppedWhenRefused(t *testing.T) {
	var bodies []map[string]interface{}
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]interface{}
		_ = json.Unmarshal(raw, &m)
		bodies = append(bodies, m)
		if _, has := m["reasoning_effort"]; has && refuse {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unrecognized request argument supplied: reasoning_effort"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	c, err := NewClient("t", srv.URL, "", "m")
	if err != nil {
		t.Fatal(err)
	}
	c.SetReasoningEffort("low")
	body := map[string]interface{}{"model": "m", "messages": []interface{}{}}

	if _, err := c.doRequest(context.Background(), "/chat/completions", body); err != nil {
		t.Fatal(err)
	}
	if bodies[0]["reasoning_effort"] != "low" {
		t.Fatalf("effort not sent: %v", bodies[0])
	}
	if _, set := body["reasoning_effort"]; set {
		t.Fatal("caller's map was changed")
	}

	// A call that says how to reason is left alone.
	withThinking := map[string]interface{}{"model": "m", "thinking": map[string]interface{}{"type": "disabled"}}
	if _, err := c.doRequest(context.Background(), "/chat/completions", withThinking); err != nil {
		t.Fatal(err)
	}
	if _, set := bodies[1]["reasoning_effort"]; set {
		t.Fatal("effort sent alongside an explicit thinking switch")
	}

	// Refused once: retried without it, and not sent again.
	refuse = true
	if _, err := c.doRequest(context.Background(), "/chat/completions", body); err != nil {
		t.Fatalf("refusal not retried: %v", err)
	}
	if _, err := c.doRequest(context.Background(), "/chat/completions", body); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 5 {
		t.Fatalf("want 5 requests (send, thinking, refused, retry, plain), got %d", len(bodies))
	}
	if _, set := bodies[4]["reasoning_effort"]; set {
		t.Fatal("effort sent again after the upstream refused it")
	}
}
