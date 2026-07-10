package agent

import (
	"context"
	"strings"
	"testing"
)

// A sub-agent's own LLM turns must get the same PII redaction as the main loop
// (the guardrail seam was wired into subagent.go, not just runtime.go).
func TestSubAgentInputGuardrailRedactsPII(t *testing.T) {
	llm := &captureMsgsLLM{}
	svc, err := New("sub-guard").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithPIIRedaction().
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	sa := NewSubAgent(SubAgentConfig{
		Agent:    svc.agent,
		Service:  svc,
		Goal:     "登记：身份证 110101199003074610，手机 13812345678",
		MaxTurns: 1,
	})
	if _, err := sa.Run(context.Background()); err != nil {
		t.Fatalf("sub-agent run: %v", err)
	}

	var joined strings.Builder
	for _, m := range llm.msgs {
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	seen := joined.String()
	if seen == "" {
		t.Fatal("sub-agent never called the LLM")
	}
	if strings.Contains(seen, "110101199003074610") || strings.Contains(seen, "13812345678") {
		t.Fatalf("raw PII leaked to the sub-agent LLM: %q", seen)
	}
	if !strings.Contains(seen, "登记") {
		t.Fatalf("expected the (redacted) goal to reach the LLM, got %q", seen)
	}
}
