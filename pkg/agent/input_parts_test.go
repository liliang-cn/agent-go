package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// captureMsgsLLM records the messages it is asked to complete so a test can
// assert multimodal parts reached the model, then returns a plain final answer.
type captureMsgsLLM struct {
	msgs []domain.Message
}

func (l *captureMsgsLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *captureMsgsLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *captureMsgsLLM) GenerateWithTools(_ context.Context, msgs []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.msgs = msgs
	return &domain.GenerationResult{Content: "seen"}, nil
}
func (l *captureMsgsLLM) StreamWithTools(_ context.Context, msgs []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	l.msgs = msgs
	return cb(&domain.GenerationResult{Content: "seen"})
}
func (l *captureMsgsLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *captureMsgsLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

// userMessageWithParts returns the user message carrying multimodal parts, or
// the last user message if none carry parts (there can be extra user-role
// messages such as injected date reminders before the goal turn).
func userMessageWithParts(msgs []domain.Message) *domain.Message {
	var lastUser *domain.Message
	for i := range msgs {
		if msgs[i].Role != "user" {
			continue
		}
		lastUser = &msgs[i]
		if len(msgs[i].Parts) > 0 {
			return &msgs[i]
		}
	}
	return lastUser
}

func TestWithInputImagesAttachesImagePart(t *testing.T) {
	llm := &captureMsgsLLM{}
	svc, err := New("vision-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithVision(true).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	ch, err := svc.RunStreamWithOptions(context.Background(), "describe this",
		WithInputImages("/tmp/does-not-need-to-exist.png"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for range ch { // drain
	}

	um := userMessageWithParts(llm.msgs)
	if um == nil {
		t.Fatal("no user message reached the model")
	}
	if len(um.Parts) == 0 {
		t.Fatalf("user message has no multimodal parts; got Content=%q", um.Content)
	}
	var text, image int
	for _, p := range um.Parts {
		switch p.Type {
		case domain.MessagePartTypeText:
			text++
		case domain.MessagePartTypeImage:
			image++
			if p.Image == nil || p.Image.LocalPath == "" {
				t.Errorf("image part missing local path: %+v", p)
			}
		}
	}
	if text == 0 || image == 0 {
		t.Fatalf("expected >=1 text and >=1 image part, got text=%d image=%d", text, image)
	}
}

func TestWithInputPartsNoopWhenEmpty(t *testing.T) {
	llm := &captureMsgsLLM{}
	svc, err := New("plain-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	ch, err := svc.RunStreamWithOptions(context.Background(), "hello")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for range ch { // drain
	}
	um := userMessageWithParts(llm.msgs)
	if um == nil {
		t.Fatal("no user message")
	}
	if len(um.Parts) != 0 {
		t.Fatalf("expected no parts without WithInputImages, got %d", len(um.Parts))
	}
}
