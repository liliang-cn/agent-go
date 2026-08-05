package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// blockedNoArgsLLM emits a bare `task_blocked` tool call with no arguments and
// no assistant text — exactly what a model does when it gives up on a request
// it has no capability for ("order a pizza", "call the client") but forgets to
// fill in the required `blocker` field.
//
// The streaming interceptor deliberately ignores a terminal tool call whose
// args have not accumulated yet (see buildStreamingTurnCallbacks), so this
// lands in the post-turn recovery path in Runtime.Run.
type blockedNoArgsLLM struct{}

func (b *blockedNoArgsLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (b *blockedNoArgsLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (b *blockedNoArgsLLM) blockedResult() *domain.GenerationResult {
	return &domain.GenerationResult{
		Content: "",
		ToolCalls: []domain.ToolCall{{
			ID:   "call-blocked-1",
			Type: "function",
			Function: domain.FunctionCall{
				Name:      "task_blocked",
				Arguments: map[string]interface{}{},
			},
		}},
	}
}

func (b *blockedNoArgsLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return b.blockedResult(), nil
}

func (b *blockedNoArgsLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return callback(b.blockedResult())
}

func (b *blockedNoArgsLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return structuredJSON(map[string]interface{}{}), nil
}

func (b *blockedNoArgsLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

// A blocked run must never reach the user as an empty string. When the model
// calls task_blocked without a `blocker` argument the runtime has nothing to
// report, but silence is the worst possible answer: the user asked for
// something, and got back nothing at all. Fall back to a human-readable
// sentence so the turn reads as a refusal rather than a crash.
func TestBlockedRunWithoutBlockerArgStillHasText(t *testing.T) {
	t.Parallel()

	svc, err := New("blocked-text-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&blockedNoArgsLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "order me a pizza")
	if err != nil {
		t.Fatalf("RunStream failed: %v", err)
	}

	var sawBlocked bool
	var blocked string
	for evt := range events {
		if evt.Type == EventTypeBlocked {
			sawBlocked = true
			blocked = evt.Content
		}
	}

	if !sawBlocked {
		t.Fatal("expected an EventTypeBlocked event")
	}
	if blocked == "" {
		t.Fatal("blocked run reported an empty message to the user; expected a human-readable fallback")
	}
}
