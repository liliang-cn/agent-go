package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// blockingLLM answers every turn with a task_blocked tool call, which is how a
// model reports "I cannot do this" — the single most common terminal state in
// the 50-question benchmark (6/50 runs ended here).
type blockingLLM struct {
	blocker string
}

func (b *blockingLLM) Generate(ctx context.Context, p string, o *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (b *blockingLLM) Stream(ctx context.Context, p string, o *domain.GenerationOptions, cb func(string)) error {
	return nil
}

func (b *blockingLLM) blockedResult() *domain.GenerationResult {
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:   "call-block",
			Type: "function",
			Function: domain.FunctionCall{
				Name:      "task_blocked",
				Arguments: map[string]interface{}{"blocker": b.blocker},
			},
		}},
	}
}

func (b *blockingLLM) GenerateWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return b.blockedResult(), nil
}

func (b *blockingLLM) StreamWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(b.blockedResult())
}

func (b *blockingLLM) GenerateStructured(ctx context.Context, p string, s interface{}, o *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Raw: `{"forbid_tools":false,"deliverables":[]}`, Valid: true}, nil
}

func (b *blockingLLM) RecognizeIntent(ctx context.Context, r string) (*domain.IntentResult, error) {
	return nil, nil
}

// A blocked run is a considered outcome, not a crash: the agent decided it
// could not proceed and said why. That explanation has to survive all the way
// out to the caller. The 50-question benchmark had 6 runs end blocked with
// output="" — the user saw nothing at all, which is indistinguishable from the
// framework silently dying.
func TestBlockedRunCarriesBlockerTextToEveryEntryPoint(t *testing.T) {
	t.Parallel()

	const blocker = "I have no calendar tool, so I cannot book the hotel."

	newSvc := func(t *testing.T) *Service {
		t.Helper()
		svc, err := New("blocked-entry").
			WithConfig(testAgentConfig(t.TempDir())).
			WithLLM(&blockingLLM{blocker: blocker}).
			Build()
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		return svc
	}

	t.Run("Run", func(t *testing.T) {
		svc := newSvc(t)
		res, err := svc.Run(context.Background(), "book me a hotel")
		if err != nil {
			t.Fatalf("Run returned a transport error: %v", err)
		}
		if got := strings.TrimSpace(res.Text()); got == "" {
			t.Fatal("blocked run produced empty text; the blocker never reached the caller")
		}
		if !strings.Contains(res.Text(), "calendar tool") {
			t.Errorf("blocked text lost the blocker: %q", res.Text())
		}
	})

	t.Run("RunStream", func(t *testing.T) {
		svc := newSvc(t)
		events, err := svc.RunStream(context.Background(), "book me a hotel")
		if err != nil {
			t.Fatalf("RunStream: %v", err)
		}
		sawBlocked := false
		for evt := range events {
			if evt.Type == EventTypeBlocked {
				sawBlocked = true
				if strings.TrimSpace(evt.Content) == "" {
					t.Fatal("blocked event carried no text")
				}
			}
		}
		if !sawBlocked {
			t.Fatal("no blocked event emitted")
		}
	})

	t.Run("Ask", func(t *testing.T) {
		svc := newSvc(t)
		reply, _ := svc.Ask(context.Background(), "book me a hotel")
		if strings.TrimSpace(reply) == "" {
			t.Fatal("Ask returned an empty string for a blocked run; the caller has nothing to show the user")
		}
	})

	t.Run("Chat", func(t *testing.T) {
		svc := newSvc(t)
		res, err := svc.Chat(context.Background(), "book me a hotel")
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if strings.TrimSpace(res.Text()) == "" {
			t.Fatal("Chat returned empty text for a blocked run")
		}
	})

	t.Run("PromptScheduler", func(t *testing.T) {
		svc := newSvc(t)
		exec := NewPromptExecutor(svc)
		res, err := exec.Execute(context.Background(), map[string]string{ParamPrompt: "book me a hotel"})
		if err != nil {
			t.Fatalf("prompt executor: %v", err)
		}
		if strings.TrimSpace(res.Output) == "" {
			t.Fatal("a scheduled run that blocked reported no output; the host has nothing to notify with")
		}
	})
}

// task_blocked with no argument at all still has to produce text — silence is
// the worst possible outcome, since the user cannot tell a refusal from a crash.
func TestBlockedRunWithoutBlockerStillCarriesText(t *testing.T) {
	t.Parallel()

	svc, err := New("blocked-empty").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&blockingLLM{blocker: ""}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	res, err := svc.Run(context.Background(), "do something impossible")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Text()) == "" {
		t.Fatal("a blocked run with no stated blocker still must not be silent")
	}
}
