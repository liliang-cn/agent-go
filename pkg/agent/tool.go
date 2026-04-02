// Package agent provides the core agent primitives for AgentGo.
// This file defines the Tool type and the ergonomic NewTool[T] generic constructor
// that auto-generates a JSON Schema from a typed params struct.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Tool is a self-contained tool definition: schema + handler.
// Created via NewTool[T] (typed, struct-based) or ToolBuilder (fluent).
type Tool struct {
	name              string
	description       string
	parameters        map[string]interface{} // JSON Schema object
	handler           func(context.Context, map[string]interface{}) (interface{}, error)
	deferLoading      bool
	readOnly          bool
	concurrencySafe   bool
	destructive       bool
	interruptBehavior string
}

// Name returns the tool name.
func (t *Tool) Name() string { return t.name }

// Description returns the tool description.
func (t *Tool) Description() string { return t.description }

// Parameters returns the JSON Schema map (passed directly to the LLM SDK).
func (t *Tool) Parameters() map[string]interface{} { return t.parameters }

// DeferLoading returns whether the tool should be excluded from initial context.
func (t *Tool) DeferLoading() bool        { return t.deferLoading }
func (t *Tool) ReadOnly() bool            { return t.readOnly }
func (t *Tool) ConcurrencySafe() bool     { return t.concurrencySafe }
func (t *Tool) Destructive() bool         { return t.destructive }
func (t *Tool) InterruptBehavior() string { return t.interruptBehavior }

// Handler returns the raw map-based handler (used internally by agent/ptc router).
func (t *Tool) Handler() func(context.Context, map[string]interface{}) (interface{}, error) {
	return t.handler
}

// toToolDefinition converts a Tool into the domain type expected by providers/LLMs.
func (t *Tool) toToolDefinition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        t.name,
			Description: t.description,
			Parameters:  t.parameters,
		},
		DeferLoading: t.deferLoading,
	}
}

func (t *Tool) WithReadOnly(readOnly bool) *Tool {
	t.readOnly = readOnly
	if readOnly && !t.concurrencySafe {
		t.concurrencySafe = true
	}
	return t
}

func (t *Tool) WithConcurrencySafe(concurrencySafe bool) *Tool {
	t.concurrencySafe = concurrencySafe
	return t
}

func (t *Tool) WithDestructive(destructive bool) *Tool {
	t.destructive = destructive
	return t
}

func (t *Tool) WithInterruptBehavior(interruptBehavior string) *Tool {
	t.interruptBehavior = strings.TrimSpace(interruptBehavior)
	return t
}

// NewTool creates a Tool from a typed handler whose parameter type T carries
// JSON Schema information via struct field tags.
//
// Tag conventions (all optional except json):
//
//	json:"field_name"           parameter name (required — must be a valid JSON key)
//	desc:"..."                  parameter description
//	required:"true"             marks the parameter as required
//	enum:"a,b,c"                comma-separated allowed values
//	minimum:"0"                 numeric minimum (integer or float)
//	maximum:"100"               numeric maximum
//
// The Go type of the field determines the JSON Schema "type":
//
//	string         → "string"
//	int, int*, float* → "number"
//	bool           → "boolean"
//	[]T            → "array"
//	struct         → "object"
//
// Example:
//
//	type WeatherParams struct {
//	    Location string `json:"location" desc:"City name" required:"true"`
//	    Units    string `json:"units"    desc:"celsius or fahrenheit" enum:"celsius,fahrenheit"`
//	}
//
//	svc.Register(agent.NewTool("get_weather", "Get current weather",
//	    func(ctx context.Context, p *WeatherParams) (any, error) {
//	        return fetchWeather(p.Location, p.Units)
//	    },
//	))
func NewTool[T any](name, description string, typedHandler func(context.Context, *T) (any, error)) *Tool {
	var zero T
	schema := schemaFromStruct(reflect.TypeOf(zero))

	// Wrap the typed handler in the generic map-based signature so it plugs
	// directly into the existing agent/ptc machinery.
	rawHandler := func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		var params T
		// Round-trip through JSON to coerce map → struct (handles type mismatches gracefully).
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("tool %s: marshal args: %w", name, err)
		}
		if err := json.Unmarshal(b, &params); err != nil {
			return nil, fmt.Errorf("tool %s: unmarshal params: %w", name, err)
		}
		return typedHandler(ctx, &params)
	}

	return &Tool{
		name:        name,
		description: description,
		parameters:  schema,
		handler:     rawHandler,
	}
}

// schemaFromStruct derives a JSON Schema object from a Go struct type.
// Only exported fields with a "json" tag are included.
func schemaFromStruct(t reflect.Type) map[string]interface{} {
	// Dereference pointer if needed
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		// Fallback: return empty object schema
		return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}

	properties := make(map[string]interface{})
	var required []string

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}
		// Strip json tag options like omitempty
		jsonName := strings.Split(jsonTag, ",")[0]
		if jsonName == "" || jsonName == "-" {
			continue
		}

		propSchema := buildFieldSchema(field)
		properties[jsonName] = propSchema

		if field.Tag.Get("required") == "true" {
			required = append(required, jsonName)
		}
	}

	schema := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// buildFieldSchema constructs the JSON Schema property object for a single struct field.
func buildFieldSchema(field reflect.StructField) map[string]interface{} {
	prop := map[string]interface{}{
		"type": goTypeToJSONSchemaType(field.Type),
	}

	if desc := field.Tag.Get("desc"); desc != "" {
		prop["description"] = desc
	}

	if enumStr := field.Tag.Get("enum"); enumStr != "" {
		parts := strings.Split(enumStr, ",")
		vals := make([]interface{}, 0, len(parts))
		for _, p := range parts {
			vals = append(vals, strings.TrimSpace(p))
		}
		prop["enum"] = vals
	}

	schemaType := prop["type"].(string)
	if schemaType == "number" || schemaType == "integer" {
		if minStr := field.Tag.Get("minimum"); minStr != "" {
			if v, err := strconv.ParseFloat(minStr, 64); err == nil {
				prop["minimum"] = v
			}
		}
		if maxStr := field.Tag.Get("maximum"); maxStr != "" {
			if v, err := strconv.ParseFloat(maxStr, 64); err == nil {
				prop["maximum"] = v
			}
		}
	}

	// For array types, add items schema
	if schemaType == "array" {
		ft := field.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array {
			elemType := ft.Elem()
			prop["items"] = map[string]interface{}{
				"type": goTypeToJSONSchemaType(elemType),
			}
		}
	}

	return prop
}

// goTypeToJSONSchemaType maps a Go reflect.Type to a JSON Schema type string.
func goTypeToJSONSchemaType(t reflect.Type) string {
	// Dereference pointers
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct:
		return "object"
	case reflect.Map:
		return "object"
	default:
		return "string"
	}
}
