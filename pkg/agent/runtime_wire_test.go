package agent

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// wireLLM answers after a first attempt the provider refused with a 429, as
// an HTTP client would report it to the call's Wire.
type wireLLM struct{ chattyLLM }

func (l *wireLLM) StreamWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	w := trace.WireOf(ctx)
	w.Request("POST /chat/completions")
	w.Response(429, "Retry-After 1")
	w.Retry("HTTP 429", 0)
	w.Request("POST /chat/completions")
	w.Response(200, "")
	w.Read(12)
	return cb(&domain.GenerationResult{Content: "Done.", FinishReason: "stop"})
}

type wireObserver struct {
	BaseObserver
	mu   sync.Mutex
	seen []ModelWireInfo
}

func (o *wireObserver) OnModelWire(_ context.Context, info ModelWireInfo) {
	o.mu.Lock()
	o.seen = append(o.seen, info)
	o.mu.Unlock()
}

// What happens to the answering call on the wire reaches the run's events,
// an observer that asks for it, the trace file and the activity log. Before,
// a person saw "requesting model output" and then nothing until the answer,
// however many attempts and refusals lay between.
func TestTheAnsweringCallsWireReachesEventsAndObservers(t *testing.T) {
	obs := &wireObserver{}
	var file, activity bytes.Buffer
	svc, err := New("wired").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&wireLLM{}).
		WithObserver(obs).
		WithObserver(NewTraceWriter(&file)).
		WithObserver(NewActivityLog(&activity)).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	events, err := svc.RunStreamWithOptions(context.Background(), "say done", WithConstraintExtraction(false))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for ev := range events {
		if ev.Type == EventTypeModelWire {
			lines = append(lines, ev.Content)
			if ev.StateDelta["kind"] == nil || ev.StateDelta["purpose"] != "turn" {
				t.Fatalf("a wire event without its fields: %+v", ev.StateDelta)
			}
		}
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"HTTP 429", "Retry-After 1", "retrying: HTTP 429", "request sent (attempt 2)", "first bytes of the answer"} {
		if !strings.Contains(got, want) {
			t.Fatalf("events miss %q:\n%s", want, got)
		}
	}
	obs.mu.Lock()
	n, purpose := len(obs.seen), ""
	if n > 0 {
		purpose = obs.seen[0].Purpose
	}
	obs.mu.Unlock()
	if n != len(lines) || purpose != "turn" {
		t.Fatalf("observer heard %d (%q), events %d", n, purpose, len(lines))
	}
	if !strings.Contains(file.String(), `"event":"model_wire"`) || !strings.Contains(file.String(), `"status":429`) {
		t.Fatalf("trace file:\n%s", file.String())
	}
	if !strings.Contains(activity.String(), "wire     turn HTTP 429") || strings.Contains(activity.String(), "first bytes") {
		t.Fatalf("activity log:\n%s", activity.String())
	}
}
