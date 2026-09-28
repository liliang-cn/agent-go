package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func buildValidationService(t *testing.T, opts ...func(*Builder) *Builder) *Service {
	t.Helper()
	b := New("arg-validation-unit").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{})
	for _, o := range opts {
		b = o(b)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func callDirect(t *testing.T, svc *Service, name string, args map[string]interface{}) (interface{}, error) {
	t.Helper()
	res, err, _ := svc.executeDirectToolCall(context.Background(), svc.agent, nil, domain.ToolCall{
		ID: "c1", Type: "function",
		Function: domain.FunctionCall{Name: name, Arguments: args},
	}, DirectToolExecutionOptions{})
	return res, err
}

func TestToolArgValidationCases(t *testing.T) {
	svc := buildValidationService(t)
	var ran int
	var mu sync.Mutex
	handler := func(context.Context, map[string]interface{}) (interface{}, error) {
		mu.Lock()
		ran++
		mu.Unlock()
		return "ok", nil
	}
	schema := map[string]interface{}{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type":    "object",
		"properties": map[string]interface{}{
			"path":  map[string]interface{}{"type": "string", "format": "uri"},
			"count": map[string]interface{}{"type": "integer"},
			"note":  map[string]interface{}{"type": "string", "nullable": true},
			"opts": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"depth": map[string]interface{}{"type": "integer"},
				},
				"required": []interface{}{"depth"},
			},
			// A property literally called "format" must not be mistaken for
			// the keyword and dropped.
			"format": map[string]interface{}{"type": "string", "enum": []interface{}{"json", "text"}},
		},
		"required": []interface{}{"path"},
	}
	svc.AddToolWithMetadata("probe", "Probe.", schema, handler, ToolMetadata{})

	cases := []struct {
		name     string
		args     map[string]interface{}
		wantErr  []string // substrings; empty = must pass
		wantRuns int
	}{
		{"valid", map[string]interface{}{"path": "a"}, nil, 1},
		{"format is not asserted", map[string]interface{}{"path": "not a uri at all"}, nil, 1},
		{"missing required", map[string]interface{}{"count": 3}, []string{`"path"`, "required field is missing", "expected string", "NOT run"}, 0},
		{"nil args", nil, []string{`"path"`}, 0},
		{"wrong type", map[string]interface{}{"path": "a", "count": "three"}, []string{`"count"`, "integer"}, 0},
		{"integer as float", map[string]interface{}{"path": "a", "count": 3.0}, nil, 1},
		{"nullable honoured", map[string]interface{}{"path": "a", "note": nil}, nil, 1},
		{"null optional treated as absent", map[string]interface{}{"path": "a", "count": nil}, nil, 1},
		{"nested missing", map[string]interface{}{"path": "a", "opts": map[string]interface{}{}}, []string{`"opts.depth"`, "expected integer"}, 0},
		{"property named format still checked", map[string]interface{}{"path": "a", "format": "xml"}, []string{`"format"`}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			ran = 0
			mu.Unlock()
			_, err := callDirect(t, svc, "probe", tc.args)
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("valid call refused: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid call was not refused")
				}
				if !isToolArgumentError(err) {
					t.Fatalf("want a ToolArgumentError, got %T %v", err, err)
				}
				for _, sub := range tc.wantErr {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q lacks %q", err.Error(), sub)
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if ran != tc.wantRuns {
				t.Errorf("handler ran %d times, want %d", ran, tc.wantRuns)
			}
		})
	}
}

// Whatever the validator cannot judge, it lets through.
func TestToolArgValidationPassesWhatItCannotJudge(t *testing.T) {
	svc := buildValidationService(t)
	ran := 0
	handler := func(context.Context, map[string]interface{}) (interface{}, error) { ran++; return "ok", nil }

	svc.AddToolWithMetadata("remote_ref", "R.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"x": map[string]interface{}{"$ref": "https://example.invalid/schema.json"},
		},
		"required": []interface{}{"x"},
	}, handler, ToolMetadata{})
	svc.AddToolWithMetadata("malformed", "M.", map[string]interface{}{
		"type":     "OBJECT", // Gemini-style uppercase: not a valid 2020-12 type
		"required": []interface{}{"x"},
	}, handler, ToolMetadata{})
	svc.AddToolWithMetadata("no_schema", "N.", nil, handler, ToolMetadata{})

	for _, name := range []string{"remote_ref", "malformed", "no_schema"} {
		if _, err := callDirect(t, svc, name, map[string]interface{}{}); err != nil {
			t.Errorf("%s: a call the validator cannot judge was refused: %v", name, err)
		}
	}
	if ran != 3 {
		t.Errorf("handler ran %d times, want 3", ran)
	}
}

func TestToolArgValidationOptOut(t *testing.T) {
	svc := buildValidationService(t, func(b *Builder) *Builder { return b.WithToolArgValidation(false) })
	ran := 0
	svc.AddToolWithMetadata("probe", "P.", map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"path"},
	}, func(context.Context, map[string]interface{}) (interface{}, error) { ran++; return "ok", nil }, ToolMetadata{})
	if _, err := callDirect(t, svc, "probe", map[string]interface{}{}); err != nil {
		t.Fatalf("validation is off, call refused: %v", err)
	}
	if ran != 1 {
		t.Fatalf("handler ran %d times", ran)
	}
}

// The refusal reaches the model as the tool's error, never as an empty
// result — the one thing a model can only answer by repeating the call.
func TestToolArgValidationErrorReachesModel(t *testing.T) {
	err := &ToolArgumentError{Tool: "save_note", Issues: []ToolArgumentIssue{{Path: "path", Expected: "string", Message: "required field is missing"}}}
	got := toolResultToString(toolResultForModel(nil, err))
	if !strings.Contains(got, `field "path": required field is missing (expected string)`) {
		t.Fatalf("model sees %q", got)
	}
}
