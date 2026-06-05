package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// jsonReplyLLM is a mock generator whose every turn returns a fixed JSON
// string with no tool calls, so Service.Run completes immediately with that
// JSON as the final answer.
type jsonReplyLLM struct{ json string }

func (m *jsonReplyLLM) Generate(_ context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	return m.json, nil
}
func (m *jsonReplyLLM) Stream(_ context.Context, _ string, _ *domain.GenerationOptions, cb func(string)) error {
	cb(m.json)
	return nil
}
func (m *jsonReplyLLM) GenerateWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: m.json}, nil
}
func (m *jsonReplyLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(&domain.GenerationResult{Content: m.json})
}
func (m *jsonReplyLLM) GenerateStructured(_ context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Data: []byte(m.json)}, nil
}
func (m *jsonReplyLLM) RecognizeIntent(_ context.Context, _ string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.95}, nil
}

func TestRunTyped(t *testing.T) {
	type Weather struct {
		City  string  `json:"city"`
		TempC float64 `json:"temp_c"`
		Wet   bool    `json:"wet,omitempty"`
	}

	cases := []struct {
		name string
		json string
	}{
		{"bare_json", `{"city":"Tokyo","temp_c":21.5,"wet":true}`},
		{"fenced_json", "```json\n{\"city\":\"Tokyo\",\"temp_c\":21.5}\n```"},
		{"json_with_prose", `Here you go: {"city":"Tokyo","temp_c":21.5} — enjoy.`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := New("typed-agent").
				WithPTC(false).
				WithConfig(testAgentConfig(t.TempDir())).
				WithLLM(&jsonReplyLLM{json: tc.json}).
				Build()
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			defer svc.Close()

			w, err := RunTyped[Weather](context.Background(), svc, "weather in Tokyo")
			if err != nil {
				t.Fatalf("RunTyped: %v", err)
			}
			if w.City != "Tokyo" || w.TempC != 21.5 {
				t.Fatalf("parsed = %+v, want City=Tokyo TempC=21.5", w)
			}
		})
	}
}

func TestRunTypedNonJSONErrors(t *testing.T) {
	type Out struct {
		Answer string `json:"answer"`
	}
	svc, err := New("typed-agent-bad").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&jsonReplyLLM{json: "totally not json"}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// The structured-output lint will re-prompt (and the mock keeps returning
	// non-JSON), so RunTyped should surface an error rather than a zero value
	// pretending to be success.
	if _, err := RunTyped[Out](context.Background(), svc, "do it"); err == nil {
		t.Fatal("expected an error for non-JSON output, got nil")
	}
}
