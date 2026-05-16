package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestSchemaSpecFromType_BasicStruct(t *testing.T) {
	t.Parallel()

	type Weather struct {
		City   string  `json:"city"`
		TempC  float64 `json:"temp_c" desc:"temperature in Celsius"`
		Cloudy bool    `json:"cloudy,omitempty"`
	}

	spec, err := SchemaSpecFromType(Weather{})
	if err != nil {
		t.Fatalf("SchemaSpecFromType: %v", err)
	}
	if spec.Name != "Weather" {
		t.Fatalf("Name = %q, want %q", spec.Name, "Weather")
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(spec.Schema, &doc); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	if doc["type"] != "object" {
		t.Fatalf("schema type = %v", doc["type"])
	}
	props, _ := doc["properties"].(map[string]interface{})
	if _, ok := props["city"]; !ok {
		t.Fatalf("city missing from properties: %v", props)
	}
	tempSchema, _ := props["temp_c"].(map[string]interface{})
	if tempSchema["description"] != "temperature in Celsius" {
		t.Fatalf("temp_c description not propagated: %v", tempSchema)
	}
	required, _ := doc["required"].([]interface{})
	gotRequired := map[string]bool{}
	for _, r := range required {
		if s, ok := r.(string); ok {
			gotRequired[s] = true
		}
	}
	if !gotRequired["city"] || !gotRequired["temp_c"] {
		t.Fatalf("required missing fields: %v", required)
	}
	if gotRequired["cloudy"] {
		t.Fatalf("omitempty field should not be required: %v", required)
	}
	if doc["additionalProperties"] != false {
		t.Fatalf("additionalProperties should be false, got %v", doc["additionalProperties"])
	}
}

func TestSchemaSpecFromType_Nested(t *testing.T) {
	t.Parallel()
	type Inner struct {
		Code int `json:"code"`
	}
	type Outer struct {
		Items []Inner `json:"items"`
		Tags  map[string]string
	}
	spec, err := SchemaSpecFromType(Outer{})
	if err != nil {
		t.Fatalf("SchemaSpecFromType: %v", err)
	}
	var doc map[string]interface{}
	_ = json.Unmarshal(spec.Schema, &doc)
	props := doc["properties"].(map[string]interface{})
	items := props["items"].(map[string]interface{})
	if items["type"] != "array" {
		t.Fatalf("items.type = %v", items["type"])
	}
	innerSchema := items["items"].(map[string]interface{})
	if innerSchema["type"] != "object" {
		t.Fatalf("items.items.type = %v", innerSchema["type"])
	}
	tags := props["Tags"].(map[string]interface{})
	if tags["type"] != "object" {
		t.Fatalf("Tags.type = %v", tags["type"])
	}
}

func TestStructuredOutputLint_PassAndFail(t *testing.T) {
	t.Parallel()

	spec := &StructuredOutputSpec{
		Name: "weather",
		Schema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "city":   {"type": "string"},
                "temp_c": {"type": "number"}
            },
            "required": ["city", "temp_c"],
            "additionalProperties": false
        }`),
	}
	lint := NewStructuredOutputLint(spec)
	if lint == nil {
		t.Fatal("NewStructuredOutputLint returned nil for valid spec")
	}

	ok, reason := lint.Check(`{"city": "Tokyo", "temp_c": 18.4}`, LintContext{})
	if !ok {
		t.Fatalf("expected pass, got reason: %s", reason)
	}

	// Missing required field
	ok, reason = lint.Check(`{"city": "Tokyo"}`, LintContext{})
	if ok {
		t.Fatal("expected fail on missing required field")
	}
	if !strings.Contains(reason, "temp_c") && !strings.Contains(reason, "required") {
		t.Fatalf("reason should mention missing field, got: %s", reason)
	}

	// Wrong type
	ok, reason = lint.Check(`{"city": "Tokyo", "temp_c": "hot"}`, LintContext{})
	if ok {
		t.Fatal("expected fail on wrong type")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}

	// Non-JSON text
	ok, reason = lint.Check(`The temperature is 18 degrees.`, LintContext{})
	if ok {
		t.Fatal("expected fail on non-JSON text")
	}
	if !strings.Contains(reason, "JSON") {
		t.Fatalf("reason should mention JSON, got: %s", reason)
	}
}

func TestExtractJSONPayload(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare object", `{"a":1}`, `{"a":1}`},
		{"bare array", `[1,2]`, `[1,2]`},
		{"prose then object", `Sure, here it is: {"a":1} done`, `{"a":1}`},
		{"json fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"plain fence", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"nested braces", `{"a":{"b":2}}`, `{"a":{"b":2}}`},
		{"string with brace", `{"a":"}"}`, `{"a":"}"}`},
		{"empty", ``, ``},
		{"pure prose", `no JSON here`, ``},
	}
	for _, tc := range cases {
		got := extractJSONPayload(tc.in)
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// scriptedStructuredLLM serves a sequence of replies and captures the
// GenerationOptions used on each call, so tests can verify that
// response_format is being threaded through to providers.
type scriptedStructuredLLM struct {
	mu          sync.Mutex
	replies     []string
	calls       int32
	seenFormats []*domain.ResponseFormat
}

func (l *scriptedStructuredLLM) next() string {
	idx := int(atomic.AddInt32(&l.calls, 1)) - 1
	if idx >= len(l.replies) {
		idx = len(l.replies) - 1
	}
	if idx < 0 {
		return ""
	}
	return l.replies[idx]
}

func (l *scriptedStructuredLLM) Generate(ctx context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	return l.next(), nil
}
func (l *scriptedStructuredLLM) Stream(ctx context.Context, _ string, _ *domain.GenerationOptions, _ func(string)) error {
	return nil
}
func (l *scriptedStructuredLLM) GenerateWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.recordOpts(opts)
	return &domain.GenerationResult{Content: l.next()}, nil
}
func (l *scriptedStructuredLLM) StreamWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	l.recordOpts(opts)
	return cb(&domain.GenerationResult{Content: l.next()})
}
func (l *scriptedStructuredLLM) GenerateStructured(ctx context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *scriptedStructuredLLM) RecognizeIntent(ctx context.Context, _ string) (*domain.IntentResult, error) {
	return nil, nil
}

func (l *scriptedStructuredLLM) recordOpts(opts *domain.GenerationOptions) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if opts == nil {
		l.seenFormats = append(l.seenFormats, nil)
		return
	}
	if opts.ResponseFormat == nil {
		l.seenFormats = append(l.seenFormats, nil)
		return
	}
	c := *opts.ResponseFormat
	l.seenFormats = append(l.seenFormats, &c)
}

// TestRuntime_StructuredOutput_FullFlow exercises the end-to-end path:
// invalid JSON on first turn triggers the lint, runtime re-prompts, model
// returns valid JSON on retry, run completes. Also verifies the runtime
// pushed ResponseFormat onto the LLM call.
func TestRuntime_StructuredOutput_FullFlow(t *testing.T) {
	llm := &scriptedStructuredLLM{
		replies: []string{
			`I think it is hot in Tokyo today.`, // first try — fails lint (no JSON)
			`{"city": "Tokyo", "temp_c": 28.5}`, // retry — passes
		},
	}

	svc, err := New("structured-test-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	type Weather struct {
		City  string  `json:"city"`
		TempC float64 `json:"temp_c"`
	}
	spec, err := SchemaSpecFromType(Weather{})
	if err != nil {
		t.Fatalf("schema spec: %v", err)
	}

	events, err := svc.RunStreamWithOptions(context.Background(), "weather in Tokyo",
		WithStructuredOutput(spec),
	)
	if err != nil {
		t.Fatalf("RunStreamWithOptions: %v", err)
	}
	final, blocked, _ := collectStreamContent(t, events)
	if blocked != "" {
		t.Fatalf("expected completion after retry, got blocked=%q", blocked)
	}
	if !strings.Contains(final, `"city"`) || !strings.Contains(final, `"Tokyo"`) {
		t.Fatalf("final does not contain expected JSON: %q", final)
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.seenFormats) < 1 {
		t.Fatal("LLM was never called with options")
	}
	rf := llm.seenFormats[0]
	if rf == nil {
		t.Fatal("ResponseFormat was not threaded through to LLM call")
	}
	if rf.Type != "json_schema" {
		t.Fatalf("ResponseFormat.Type = %q, want %q", rf.Type, "json_schema")
	}
	if rf.Name != "Weather" {
		t.Fatalf("ResponseFormat.Name = %q, want %q", rf.Name, "Weather")
	}
}

func TestStructuredOutputSystemHint_IncludesSchemaAndIntent(t *testing.T) {
	t.Parallel()
	spec := &StructuredOutputSpec{
		Name:        "weather",
		Schema:      json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		Description: "Return the current weather for the requested city.",
	}
	hint := buildStructuredOutputSystemHint(spec)
	if !strings.Contains(hint, "STRUCTURED OUTPUT REQUIRED") {
		t.Errorf("hint missing banner")
	}
	if !strings.Contains(hint, "Return the current weather") {
		t.Errorf("hint missing description: %s", hint)
	}
	if !strings.Contains(hint, `"city"`) {
		t.Errorf("hint missing schema body: %s", hint)
	}
}
