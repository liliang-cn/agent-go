package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// drawingLLM answers with a picture and no text, which is what a model asked
// to draw actually returns: content null, the image in a sibling field.
type drawingLLM struct{ scriptedLLM }

func (d *drawingLLM) drawn() *domain.GenerationResult {
	return &domain.GenerationResult{
		FinishReason: "stop",
		Parts: []domain.MessagePart{{
			Type:  domain.MessagePartTypeImage,
			Image: &domain.MessageImage{Base64: "AAAA", MIMEType: "image/png"},
		}},
	}
}

func (d *drawingLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return d.drawn(), nil
}

func (d *drawingLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(d.drawn())
}

// A run whose whole answer is a picture must reach the caller as a picture —
// not as an empty answer the lint layer rejects as a refusal.
func TestARunThatDrawsIsNotAnEmptyAnswer(t *testing.T) {
	svc, err := New("painter").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&drawingLLM{}).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	res, err := svc.Run(context.Background(), "Draw a green triangle.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.OutputParts) == 0 {
		t.Fatalf("the drawn image never reached the result: %+v", res)
	}
	if res.OutputParts[0].Image == nil || res.OutputParts[0].Image.Base64 != "AAAA" {
		t.Errorf("output part = %+v", res.OutputParts[0])
	}
	if res.Blocked {
		t.Errorf("a picture was rejected as an empty answer: stop=%s", res.StopReason)
	}
}

// The lint that guards emptiness has to see the picture, or a drawing agent
// gets told it refused and the run blocks when the retries run out.
func TestNonEmptyFinalAnswerAcceptsAPicture(t *testing.T) {
	lint := NonEmptyFinalAnswer()
	if ok, _ := lint.Check("", LintContext{}); ok {
		t.Error("an empty answer with nothing else should still be rejected")
	}
	if ok, reason := lint.Check("", LintContext{
		OutputParts: []domain.MessagePart{{Type: domain.MessagePartTypeImage}},
	}); !ok {
		t.Errorf("an answer that is a picture was rejected: %s", reason)
	}
}

// Attaching input is one option per kind, and they accumulate.
func TestInputPartOptions(t *testing.T) {
	cfg := DefaultRunConfig()
	WithInputImages("a.png", "", "b.jpg")(cfg)
	WithInputAudio("c.wav")(cfg)
	WithInputFiles("d.pdf")(cfg)
	if len(cfg.InputParts) != 4 {
		t.Fatalf("got %d parts, want 4 (blank paths skipped)", len(cfg.InputParts))
	}
	kinds := map[domain.MessagePartType]int{}
	for _, p := range cfg.InputParts {
		kinds[p.Type]++
	}
	if kinds[domain.MessagePartTypeImage] != 2 || kinds[domain.MessagePartTypeAudio] != 1 || kinds[domain.MessagePartTypeFile] != 1 {
		t.Errorf("kinds = %+v", kinds)
	}
}
