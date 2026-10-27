package pool

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// Anthropic's Claude 5.5 refuses any request with a temperature. The first
// call drops it and goes again; the calls after it leave it out from the
// start; streaming and not alike.
func TestAModelThatTakesNoTemperatureIsAskedWithout(t *testing.T) {
	var withTemp, total atomic.Int32
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		total.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["temperature"]; ok {
			withTemp.Add(1)
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request_error","message":"` + "`temperature`" + ` is deprecated for this model.","type":"invalid_request_error"}}`))
			return
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	})
	var said []string
	ctx := trace.WithWire(context.Background(), trace.NewWire(func(e trace.WireEvent) { said = append(said, e.Kind+": "+e.Text) }))
	opts := &domain.GenerationOptions{Temperature: 0.3}
	msgs := []domain.Message{{Role: "user", Content: "hi"}}
	if err := c.StreamWithTools(ctx, msgs, nil, opts, func(*domain.GenerationResult) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if withTemp.Load() != 1 || total.Load() != 2 {
		t.Fatalf("first call: %d with temperature of %d", withTemp.Load(), total.Load())
	}
	if !strings.Contains(strings.Join(said, "\n"), "retry: retrying: upstream refused temperature") {
		t.Fatalf("the wire did not say why it went again: %v", said)
	}
	if _, err := c.GenerateWithTools(context.Background(), msgs, nil, opts); err != nil {
		t.Fatal(err)
	}
	if err := c.StreamWithTools(context.Background(), msgs, nil, opts, func(*domain.GenerationResult) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if withTemp.Load() != 1 || total.Load() != 4 {
		t.Fatalf("after: %d with temperature of %d — the refusal was not remembered", withTemp.Load(), total.Load())
	}
}

func TestOnlyATemperatureRefusalCounts(t *testing.T) {
	for msg, want := range map[string]bool{
		"API error (status 400): `temperature` is deprecated for this model.":           true,
		"API error (status 400): Unsupported value: 'temperature' does not support 0.3": true,
		"API error (status 500): temperature deprecated":                                false,
		"API error (status 400): max_tokens is too large":                               false,
	} {
		if got := isTemperatureRejection(errString(msg)); got != want {
			t.Errorf("%q: %v", msg, got)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
