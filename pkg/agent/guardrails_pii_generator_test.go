package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// recordingGenerator captures whatever a caller hands to the provider, which is
// the only way to tell redaction actually happened rather than being intended.
type recordingGenerator struct {
	prompts  []string
	messages [][]domain.Message
}

func (r *recordingGenerator) Generate(_ context.Context, prompt string, _ *domain.GenerationOptions) (string, error) {
	r.prompts = append(r.prompts, prompt)
	return "ok", nil
}

func (r *recordingGenerator) Stream(_ context.Context, prompt string, _ *domain.GenerationOptions, _ func(string)) error {
	r.prompts = append(r.prompts, prompt)
	return nil
}

func (r *recordingGenerator) GenerateWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	r.messages = append(r.messages, messages)
	return &domain.GenerationResult{Content: "ok"}, nil
}

func (r *recordingGenerator) StreamWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, _ domain.ToolCallCallback) error {
	r.messages = append(r.messages, messages)
	return nil
}

func (r *recordingGenerator) GenerateStructured(_ context.Context, prompt string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	r.prompts = append(r.prompts, prompt)
	return &domain.StructuredResult{}, nil
}

func (r *recordingGenerator) RecognizeIntent(_ context.Context, request string) (*domain.IntentResult, error) {
	r.prompts = append(r.prompts, request)
	return &domain.IntentResult{}, nil
}

func (r *recordingGenerator) sawText() string {
	var b strings.Builder
	for _, p := range r.prompts {
		b.WriteString(p)
		b.WriteString("\n")
	}
	for _, msgs := range r.messages {
		for _, m := range msgs {
			b.WriteString(m.Content)
			b.WriteString("\n")
			for _, part := range m.Parts {
				b.WriteString(part.Text)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// cvText is the shape PII actually arrives in: a CV contact block, with the
// mobile written the way people write it.
const cvText = `联系电话：138 0013 8000
备用电话：13800138000
电子邮箱：alice@example.com`

var cvSecrets = []string{"138 0013 8000", "13800138000", "alice@example.com"}

func assertNoSecrets(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range cvSecrets {
		if strings.Contains(text, secret) {
			t.Errorf("%s: %q reached the provider", where, secret)
		}
	}
}

// TestPIIGeneratorCoversEveryEntryPoint is the regression for the hole found on
// 2026-07-29: the planner's intent classification called GenerateStructured
// directly, so PII the conversation turn scrubbed still went out. Wrapping the
// Generator means every entry point is covered, including ones added later.
func TestPIIGeneratorCoversEveryEntryPoint(t *testing.T) {
	ctx := context.Background()
	msgs := []domain.Message{
		{Role: "system", Content: "you are an assistant"},
		{Role: "user", Content: cvText},
	}

	t.Run("Generate", func(t *testing.T) {
		rec := &recordingGenerator{}
		g := newPIIRedactingGenerator(rec, nil, RedactPartial)
		if _, err := g.Generate(ctx, cvText, nil); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, "Generate", rec.sawText())
	})

	t.Run("Stream", func(t *testing.T) {
		rec := &recordingGenerator{}
		g := newPIIRedactingGenerator(rec, nil, RedactPartial)
		if err := g.Stream(ctx, cvText, nil, func(string) {}); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, "Stream", rec.sawText())
	})

	t.Run("GenerateStructured (the intent classifier path)", func(t *testing.T) {
		rec := &recordingGenerator{}
		g := newPIIRedactingGenerator(rec, nil, RedactPartial)
		if _, err := g.GenerateStructured(ctx, "Classify this goal: "+cvText, nil, nil); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, "GenerateStructured", rec.sawText())
	})

	t.Run("RecognizeIntent", func(t *testing.T) {
		rec := &recordingGenerator{}
		g := newPIIRedactingGenerator(rec, nil, RedactPartial)
		if _, err := g.RecognizeIntent(ctx, cvText); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, "RecognizeIntent", rec.sawText())
	})

	t.Run("GenerateWithTools", func(t *testing.T) {
		rec := &recordingGenerator{}
		g := newPIIRedactingGenerator(rec, nil, RedactPartial)
		if _, err := g.GenerateWithTools(ctx, msgs, nil, nil); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, "GenerateWithTools", rec.sawText())
	})

	t.Run("StreamWithTools", func(t *testing.T) {
		rec := &recordingGenerator{}
		g := newPIIRedactingGenerator(rec, nil, RedactPartial)
		if err := g.StreamWithTools(ctx, msgs, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, "StreamWithTools", rec.sawText())
	})
}

// TestPIIGeneratorLeavesCallerDataAlone guards the copy-on-write contract: the
// persisted session must keep the real values, only the provider sees masks.
func TestPIIGeneratorLeavesCallerDataAlone(t *testing.T) {
	rec := &recordingGenerator{}
	g := newPIIRedactingGenerator(rec, nil, RedactPartial)
	msgs := []domain.Message{
		{Role: "user", Content: cvText, Parts: []domain.MessagePart{
			{Type: domain.MessagePartTypeText, Text: cvText},
		}},
	}
	if _, err := g.GenerateWithTools(context.Background(), msgs, nil, nil); err != nil {
		t.Fatal(err)
	}
	if msgs[0].Content != cvText {
		t.Error("the caller's message was mutated; redaction must work on a copy")
	}
	if msgs[0].Parts[0].Text != cvText {
		t.Error("the caller's message part was mutated")
	}
}

// TestPIIGeneratorPreservesSystemAndToolPairing keeps the wrapper from breaking
// a turn: the app-authored system prompt and tool-call ids must survive.
func TestPIIGeneratorPreservesSystemAndToolPairing(t *testing.T) {
	rec := &recordingGenerator{}
	g := newPIIRedactingGenerator(rec, nil, RedactPartial)
	msgs := []domain.Message{
		{Role: "system", Content: "contact ops at ops@example.com"},
		{Role: "assistant", ToolCalls: []domain.ToolCall{{ID: "call_1"}}},
		{Role: "tool", ToolCallID: "call_1", Content: "done"},
	}
	if _, err := g.GenerateWithTools(context.Background(), msgs, nil, nil); err != nil {
		t.Fatal(err)
	}
	sent := rec.messages[0]
	if sent[0].Content != "contact ops at ops@example.com" {
		t.Errorf("the system prompt is app-authored and must not be redacted, got %q", sent[0].Content)
	}
	if len(sent[1].ToolCalls) != 1 || sent[1].ToolCalls[0].ID != "call_1" {
		t.Error("tool calls must be preserved verbatim")
	}
	if sent[2].ToolCallID != "call_1" {
		t.Error("tool_call_id must be preserved verbatim")
	}
}

// TestPIIGeneratorBlockMode refuses instead of forwarding.
func TestPIIGeneratorBlockMode(t *testing.T) {
	rec := &recordingGenerator{}
	g := newPIIRedactingGenerator(rec, nil, RedactBlock)

	if _, err := g.Generate(context.Background(), cvText, nil); err == nil {
		t.Error("RedactBlock must refuse a prompt containing PII")
	}
	if _, err := g.GenerateStructured(context.Background(), cvText, nil, nil); err == nil {
		t.Error("RedactBlock must refuse a structured call containing PII")
	}
	if len(rec.prompts) != 0 {
		t.Errorf("nothing should have reached the provider, got %v", rec.prompts)
	}

	// Clean text still goes through.
	if _, err := g.Generate(context.Background(), "hello", nil); err != nil {
		t.Errorf("clean text must pass: %v", err)
	}
}

// TestPIIGeneratorIsIdempotent matters because the Runtime seam and this
// wrapper both run on a normal turn: masking twice must not corrupt the text.
func TestPIIGeneratorIsIdempotent(t *testing.T) {
	rec := &recordingGenerator{}
	g := newPIIRedactingGenerator(rec, nil, RedactPartial)
	once, _, _ := redactPII(cvText, detectorsFor(AllPIIKinds), RedactPartial)
	if _, err := g.Generate(context.Background(), once, nil); err != nil {
		t.Fatal(err)
	}
	if got := rec.prompts[0]; got != once {
		t.Errorf("already-redacted text changed:\n once: %q\n twice: %q", once, got)
	}
}

// TestPIIGeneratorPassthroughWhenNothingToDo keeps the zero-cost promise.
func TestPIIGeneratorPassthroughWhenNothingToDo(t *testing.T) {
	rec := &recordingGenerator{}
	if got := newPIIRedactingGenerator(rec, []PIIKind{"not_a_kind"}, RedactPartial); got != domain.Generator(rec) {
		t.Error("with no usable detectors the generator must be returned unwrapped")
	}
	if newPIIRedactingGenerator(nil, nil, RedactPartial) != nil {
		t.Error("a nil generator must stay nil")
	}
}
