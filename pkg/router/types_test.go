package router

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
)

type fallbackIntentTestLLM struct {
	response string
}

func (f *fallbackIntentTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return f.response, nil
}

func (f *fallbackIntentTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (f *fallbackIntentTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: ""}, nil
}

func (f *fallbackIntentTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (f *fallbackIntentTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Raw: f.response, Valid: true}, nil
}

func (f *fallbackIntentTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestFallbackLLMRecognizerParseRecognitionResult_JSON(t *testing.T) {
	r := &FallbackLLMRecognizer{promptManager: prompt.NewManager()}
	intent, err := r.parseRecognitionResult(`{"intent_type":"web_search","confidence":0.92,"topic":"weather"}`, "weather")
	if err != nil {
		t.Fatalf("parseRecognitionResult() error = %v", err)
	}
	if intent.IntentType != "web_search" || intent.Confidence != 0.92 || intent.Topic != "weather" {
		t.Fatalf("unexpected intent: %+v", intent)
	}
}

func TestFallbackLLMRecognizerParseRecognitionResult_LineFormat(t *testing.T) {
	r := &FallbackLLMRecognizer{promptManager: prompt.NewManager()}
	intent, err := r.parseRecognitionResult("intent_type: memory_save\nconfidence: 0.8\ntopic: tea", "tea")
	if err != nil {
		t.Fatalf("parseRecognitionResult() error = %v", err)
	}
	if intent.IntentType != "memory_save" {
		t.Fatalf("expected memory_save, got %+v", intent)
	}
}

func TestFallbackLLMRecognizerBasicFallback(t *testing.T) {
	r := &FallbackLLMRecognizer{promptManager: prompt.NewManager()}
	intent := r.basicFallback("What's the latest weather in Beijing today?")
	if intent.IntentType != "web_search" {
		t.Fatalf("expected web_search, got %+v", intent)
	}
	if !intent.RequiresTools || intent.PreferredAgent != "Operator" {
		t.Fatalf("expected execution hints on fallback, got %+v", intent)
	}
}
