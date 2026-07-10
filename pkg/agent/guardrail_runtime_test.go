package agent

import (
	"context"
	"strings"
	"testing"
)

// drain reads events and returns the terminal event (last Complete/Blocked).
func drainForTerminal(ch <-chan *Event) *Event {
	var last *Event
	for ev := range ch {
		if ev.Type == EventTypeComplete || ev.Type == EventTypeBlocked {
			last = ev
		}
	}
	return last
}

func TestPIIRedactionInputReachesLLMRedacted(t *testing.T) {
	llm := &captureMsgsLLM{}
	svc, err := New("pii-input").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithPIIRedaction().
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	prompt := "my email is alice@example.com and mobile 13812345678, remember it"
	ch, err := svc.RunStreamWithOptions(context.Background(), prompt)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for range ch { // drain
	}

	if len(llm.msgs) == 0 {
		t.Fatal("LLM received no messages")
	}
	// The provider must never have seen the raw PII.
	var joined strings.Builder
	for _, m := range llm.msgs {
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	seen := joined.String()
	if strings.Contains(seen, "alice@example.com") {
		t.Errorf("provider saw raw email:\n%s", seen)
	}
	if strings.Contains(seen, "13812345678") {
		t.Errorf("provider saw raw mobile:\n%s", seen)
	}
	if !strings.Contains(seen, "a***@example.com") {
		t.Errorf("expected partially-redacted email in provider input:\n%s", seen)
	}
	if !strings.Contains(seen, "138****5678") {
		t.Errorf("expected partially-redacted mobile in provider input:\n%s", seen)
	}

	// Local session must retain the ORIGINAL text (redaction is send-only).
	sess, err := svc.store.GetSession(svc.CurrentSessionID())
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	foundOriginal := false
	for _, m := range sess.GetMessages() {
		if strings.Contains(m.Content, "alice@example.com") && strings.Contains(m.Content, "13812345678") {
			foundOriginal = true
		}
	}
	if !foundOriginal {
		t.Errorf("local session should retain original PII, but it was scrubbed")
	}
}

func TestPIIRedactionBlockStopsRun(t *testing.T) {
	llm := &captureMsgsLLM{}
	svc, err := New("pii-block").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithPIIRedaction(WithPIIMode(RedactBlock)).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	ch, err := svc.RunStreamWithOptions(context.Background(), "ssn 123-45-6789 please process")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	term := drainForTerminal(ch)
	if term == nil || term.Type != EventTypeBlocked {
		t.Fatalf("expected blocked terminal event, got %+v", term)
	}
	// A blocking input guardrail must refuse BEFORE the model is called.
	if len(llm.msgs) != 0 {
		t.Errorf("blocking guardrail should stop the run before the LLM, but LLM saw %d messages", len(llm.msgs))
	}
}

func TestNoGuardrailsZeroOverhead(t *testing.T) {
	llm := &captureMsgsLLM{}
	svc, err := New("no-guardrails").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if svc.Guardrails() != nil {
		t.Fatalf("expected nil guardrail chain when unused")
	}
	ch, err := svc.RunStreamWithOptions(context.Background(), "my email is alice@example.com")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for range ch {
	}
	// Without guardrails the raw text flows through untouched.
	var joined strings.Builder
	for _, m := range llm.msgs {
		joined.WriteString(m.Content)
	}
	if !strings.Contains(joined.String(), "alice@example.com") {
		t.Errorf("without guardrails the LLM should see raw input")
	}
}
