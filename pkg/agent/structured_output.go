package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// RunTyped runs the agent on goal with a structured-output constraint derived
// from T (via WithStructuredOutputType[T]) and unmarshals the final JSON answer
// into a value of T. It is the typed counterpart to Service.Run: the runtime
// forces the model to return JSON matching T's shape — provider-native where
// supported (Tier B), post-validated and re-prompted everywhere (Tier A) — and
// this returns the parsed value so callers skip the manual json.Unmarshal.
//
//	type Weather struct {
//	    City  string  `json:"city"`
//	    TempC float64 `json:"temp_c"`
//	}
//	w, err := agent.RunTyped[Weather](ctx, svc, "weather in Tokyo")
//
// Extra RunOptions are applied after the derived structured-output option, so a
// caller may override it (e.g. with a hand-written WithStructuredOutput schema,
// Strict mode, or a Description). Returns the zero value of T on any error.
func RunTyped[T any](ctx context.Context, svc *Service, goal string, opts ...RunOption) (T, error) {
	var zero T
	if svc == nil {
		return zero, fmt.Errorf("RunTyped: nil service")
	}
	allOpts := make([]RunOption, 0, len(opts)+1)
	allOpts = append(allOpts, WithStructuredOutputType[T]())
	allOpts = append(allOpts, opts...)

	// Route through the streaming runtime: that's where structured-output
	// enforcement is wired (Tier B response_format on each LLM call + the
	// Tier A re-prompting lint). The non-streaming Service.Run path does not
	// apply RunConfig.StructuredOutput, so RunTyped must not use it.
	events, err := svc.RunStreamWithOptions(ctx, goal, allOpts...)
	if err != nil {
		return zero, err
	}
	var final, blocked, errText string
	for evt := range events {
		switch evt.Type {
		case EventTypeComplete:
			final = evt.Content
		case EventTypeBlocked:
			blocked = evt.Content
		case EventTypeError:
			errText = evt.Content
		}
	}
	if strings.TrimSpace(blocked) != "" {
		return zero, fmt.Errorf("RunTyped: task blocked: %s", blocked)
	}
	if strings.TrimSpace(final) == "" && strings.TrimSpace(errText) != "" {
		return zero, fmt.Errorf("RunTyped: %s", errText)
	}

	payload := extractJSONPayload(final)
	if payload == "" {
		return zero, fmt.Errorf("RunTyped: final answer was not JSON: %q", truncateForError(final, 200))
	}

	var out T
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return zero, fmt.Errorf("RunTyped: decode final JSON into %T: %w", out, err)
	}
	return out, nil
}

func truncateForError(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// StructuredOutputSpec declares a JSON-shape constraint on the agent's
// final answer. The runtime threads the spec two ways:
//
//  1. Tier B (native): each LLM call carries the equivalent response_format
//     so providers that support OpenAI structured outputs return
//     guaranteed-valid JSON in one shot. Providers that reject the field
//     fall back via applyPoolRetryFallbacks / applyOpenAIRetryFallbacks.
//
//  2. Tier A (post-validation): a StructuredOutputLint runs on the final
//     text response and re-prompts the model with the validation error
//     when the JSON doesn't match — works on every provider, bounded by
//     the per-task lintRetryBudget.
//
// Use WithStructuredOutput(spec) for a raw JSON Schema, or
// WithStructuredOutputType[T]() / WithStructuredOutputFromValue(zero) to
// derive a schema from a Go struct via reflection.
type StructuredOutputSpec struct {
	// Name is the schema identifier required by OpenAI's json_schema mode.
	// Derived from the Go type name when omitted.
	Name string

	// Schema is the JSON Schema document the final answer must satisfy.
	// Required.
	Schema json.RawMessage

	// Strict makes lint failures block the task (task_blocked) instead of
	// returning best-effort output once the retry budget is exhausted.
	// Also forwarded to provider-side strict mode where supported.
	Strict bool

	// Description, when non-empty, is appended to the runtime's system
	// prompt as a hint to the model. Use this to tell the model what each
	// schema field means — schemas alone don't always convey intent.
	Description string
}

// WithStructuredOutput attaches a raw JSON Schema constraint to this run.
// Pass nil to remove a previously set spec.
//
// Example:
//
//	schema := json.RawMessage(`{
//	    "type": "object",
//	    "properties": {
//	        "city":      {"type": "string"},
//	        "temp_c":    {"type": "number"}
//	    },
//	    "required": ["city", "temp_c"]
//	}`)
//	svc.Run(ctx, "weather in Tokyo",
//	    agent.WithStructuredOutput(&agent.StructuredOutputSpec{
//	        Name:   "weather_report",
//	        Schema: schema,
//	    }),
//	)
func WithStructuredOutput(spec *StructuredOutputSpec) RunOption {
	return func(c *RunConfig) { c.StructuredOutput = spec }
}

// WithStructuredOutputType derives a JSON Schema from a Go type T using
// reflection and attaches it as a structured-output constraint. The schema
// captures field names (respecting `json` tags), field types, and the
// required set (fields not marked `omitempty`).
//
// Example:
//
//	type Weather struct {
//	    City  string  `json:"city"`
//	    TempC float64 `json:"temp_c"`
//	    Wet   bool    `json:"wet,omitempty"`
//	}
//	svc.Run(ctx, "weather in Tokyo",
//	    agent.WithStructuredOutputType[Weather](),
//	)
func WithStructuredOutputType[T any]() RunOption {
	var zero T
	return WithStructuredOutputFromValue(zero)
}

// WithStructuredOutputFromValue is the non-generic form of
// WithStructuredOutputType. Pass a zero value of the desired type
// (or a populated example — only the type matters).
func WithStructuredOutputFromValue(zero interface{}) RunOption {
	spec, err := SchemaSpecFromType(zero)
	if err != nil {
		// Defer the error to runtime so callers don't have to handle
		// errors at option construction time; a malformed spec will be
		// rejected when the runtime tries to register the lint.
		return func(c *RunConfig) {
			c.StructuredOutput = &StructuredOutputSpec{
				Name:   "invalid_schema",
				Schema: json.RawMessage(fmt.Sprintf(`{"$comment": "schema derivation failed: %s"}`, err.Error())),
			}
		}
	}
	return WithStructuredOutput(spec)
}

// SchemaSpecFromType reflects on a Go type and returns a
// StructuredOutputSpec whose Schema is a minimal JSON Schema describing
// that type. Exported for callers that want to inspect/tweak the spec
// before passing it to WithStructuredOutput.
//
// Supported field kinds: bool, int*, uint*, float*, string, slices,
// pointer-to-struct, nested struct. Maps with string keys are typed as
// `object` with `additionalProperties`. Unsupported kinds (channels,
// interfaces, etc.) return an error.
func SchemaSpecFromType(zero interface{}) (*StructuredOutputSpec, error) {
	t := reflect.TypeOf(zero)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return nil, fmt.Errorf("structured output: cannot derive schema from nil type")
	}
	schemaObj, err := goTypeToJSONSchema(t)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(schemaObj)
	if err != nil {
		return nil, fmt.Errorf("structured output: marshal schema: %w", err)
	}
	name := t.Name()
	if name == "" {
		name = "structured_response"
	}
	return &StructuredOutputSpec{
		Name:   name,
		Schema: raw,
	}, nil
}

// goTypeToJSONSchema is the reflection workhorse. Returns a generic
// map[string]interface{} so it can be Marshal'd to json.RawMessage.
func goTypeToJSONSchema(t reflect.Type) (map[string]interface{}, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Bool:
		return map[string]interface{}{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]interface{}{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]interface{}{"type": "number"}, nil
	case reflect.String:
		return map[string]interface{}{"type": "string"}, nil
	case reflect.Slice, reflect.Array:
		items, err := goTypeToJSONSchema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"type":  "array",
			"items": items,
		}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("structured output: map keys must be strings, got %s", t.Key().Kind())
		}
		values, err := goTypeToJSONSchema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"type":                 "object",
			"additionalProperties": values,
		}, nil
	case reflect.Struct:
		return structToJSONSchema(t)
	case reflect.Interface:
		// `interface{}` accepts anything — emit empty schema.
		return map[string]interface{}{}, nil
	default:
		return nil, fmt.Errorf("structured output: unsupported kind %s for type %s", t.Kind(), t.String())
	}
}

func structToJSONSchema(t reflect.Type) (map[string]interface{}, error) {
	props := map[string]interface{}{}
	var required []string

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, optional, skip := jsonFieldName(field)
		if skip {
			continue
		}

		fieldSchema, err := goTypeToJSONSchema(field.Type)
		if err != nil {
			return nil, fmt.Errorf("structured output: field %s: %w", field.Name, err)
		}

		// Carry a description tag if the user added one — helps the
		// model interpret ambiguous fields. Tag form: `desc:"..."`.
		if desc := strings.TrimSpace(field.Tag.Get("desc")); desc != "" {
			fieldSchema["description"] = desc
		}

		props[name] = fieldSchema
		if !optional {
			required = append(required, name)
		}
	}

	out := map[string]interface{}{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	// OpenAI strict mode requires additionalProperties: false. We always
	// emit it because providers that don't care will ignore it.
	out["additionalProperties"] = false
	return out, nil
}

// jsonFieldName mirrors the basic parsing rules for `json` struct tags:
//
//   - tag value "-" → skip
//   - tag value "name,omitempty" → name=name, optional=true
//   - missing tag → field's Go name, required
func jsonFieldName(field reflect.StructField) (name string, optional bool, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	if tag == "" {
		return field.Name, false, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = field.Name
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			optional = true
		}
	}
	return name, optional, false
}

// toDomainResponseFormat converts a StructuredOutputSpec into the shape
// the provider layer understands. Returns nil when the spec is empty so
// callers can pass it through unchecked.
func (s *StructuredOutputSpec) toDomainResponseFormat() *domain.ResponseFormat {
	if s == nil || len(s.Schema) == 0 {
		return nil
	}
	return &domain.ResponseFormat{
		Type:   "json_schema",
		Name:   s.Name,
		Schema: s.Schema,
		Strict: s.Strict,
	}
}

// buildStructuredOutputSystemHint renders the structured-output contract
// as a system message the runtime can prepend. Returns "" when the spec
// has no schema. Includes the spec's Description when set, to give the
// model field-level intent that a bare schema can't convey.
func buildStructuredOutputSystemHint(s *StructuredOutputSpec) string {
	if s == nil || len(s.Schema) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("=== STRUCTURED OUTPUT REQUIRED ===\n")
	b.WriteString("Your final answer MUST be a single JSON document matching the schema below.\n")
	b.WriteString("Do not wrap it in markdown code fences. Do not prefix or suffix with prose.\n")
	if desc := strings.TrimSpace(s.Description); desc != "" {
		b.WriteString("\nIntent: ")
		b.WriteString(desc)
		b.WriteString("\n")
	}
	b.WriteString("\nSchema:\n")
	// Re-marshal pretty-printed so the model sees a readable schema.
	var pretty bytes.Buffer
	if err := jsonIndent(&pretty, s.Schema); err == nil {
		b.Write(pretty.Bytes())
	} else {
		b.Write(s.Schema)
	}
	return b.String()
}

func jsonIndent(dst *bytes.Buffer, src json.RawMessage) error {
	return json.Indent(dst, src, "", "  ")
}
