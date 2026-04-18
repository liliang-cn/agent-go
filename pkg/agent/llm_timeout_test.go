package agent

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

func TestResolveLLMTurnTimeoutUsesConfigOverride(t *testing.T) {
	cfg := &config.Config{
		Agent: config.AgentConfig{
			LLMTurnTimeoutSeconds: 75,
		},
	}
	if got := resolveLLMTurnTimeout(cfg); got != 75*time.Second {
		t.Fatalf("expected 75s timeout from config override, got %v", got)
	}
}

func TestResolveLLMTurnTimeoutUsesEnvFallback(t *testing.T) {
	t.Setenv("AGENTGO_LLM_TURN_TIMEOUT_SECONDS", "210")
	if got := resolveLLMTurnTimeout(nil); got != 210*time.Second {
		t.Fatalf("expected 210s timeout from env fallback, got %v", got)
	}
}

func TestWithLLMTurnTimeoutKeepsShorterParentDeadline(t *testing.T) {
	parentCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	childCtx, childCancel := withLLMTurnTimeout(parentCtx, &config.Config{
		Agent: config.AgentConfig{LLMTurnTimeoutSeconds: 180},
	})
	defer childCancel()

	parentDeadline, ok := parentCtx.Deadline()
	if !ok {
		t.Fatal("expected parent deadline")
	}
	childDeadline, ok := childCtx.Deadline()
	if !ok {
		t.Fatal("expected child deadline")
	}
	if !childDeadline.Equal(parentDeadline) {
		t.Fatalf("expected child deadline %v to match parent deadline %v", childDeadline, parentDeadline)
	}
}
