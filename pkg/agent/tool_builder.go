// Package agent — fluent builder API for constructing Tool instances dynamically
// without needing a typed params struct.
//
// Usage:
//
//	tool := agent.BuildTool("get_weather").
//	    Description("Get current weather for a city").
//	    Param("location", agent.TypeString, "City name", agent.Required()).
//	    Param("units", agent.TypeString, "celsius or fahrenheit",
//	        agent.Enum("celsius", "fahrenheit")).
//	    Handler(func(ctx context.Context, args map[string]interface{}) (any, error) {
//	        return fetchWeather(args["location"].(string))
//	    }).
//	    Build()
//
//	svc.Register(tool)
package agent

import (
	"context"
	"fmt"
)

// ParamType is a JSON Schema primitive type string.
type ParamType string

const (
	TypeString  ParamType = "string"
	TypeNumber  ParamType = "number"
	TypeInteger ParamType = "integer"
	TypeBoolean ParamType = "boolean"
	TypeArray   ParamType = "array"
	TypeObject  ParamType = "object"
)

// paramDef holds the full definition of a single parameter.
type paramDef struct {
	name        string
	paramType   ParamType
	description string
	required    bool
	enum        []interface{}
	minimum     *float64
	maximum     *float64
	items       *itemsDef // for TypeArray
}

// itemsDef describes the items schema for array params.
type itemsDef struct {
	paramType ParamType
}

// ParamOption is a functional option applied to a paramDef.
type ParamOption func(*paramDef)

// Required marks the parameter as required in the JSON Schema.
func Required() ParamOption {
	return func(p *paramDef) { p.required = true }
}

// Enum restricts the parameter to a set of allowed string values.
func Enum(values ...string) ParamOption {
	return func(p *paramDef) {
		p.enum = make([]interface{}, len(values))
		for i, v := range values {
			p.enum[i] = v
		}
	}
}

// Min sets the JSON Schema "minimum" constraint (for number / integer params).
func Min(v float64) ParamOption {
	return func(p *paramDef) { p.minimum = &v }
}

// Max sets the JSON Schema "maximum" constraint (for number / integer params).
func Max(v float64) ParamOption {
	return func(p *paramDef) { p.maximum = &v }
}

// Items sets the element type for an array parameter.
func Items(t ParamType) ParamOption {
	return func(p *paramDef) { p.items = &itemsDef{paramType: t} }
}

// ToolBuilder is a fluent builder that constructs a *Tool step-by-step.
// All Param() calls return the same *ToolBuilder so the chain stays on one type.
type ToolBuilder struct {
	name              string
	description       string
	params            []paramDef
	handler           func(context.Context, map[string]interface{}) (interface{}, error)
	deferLoading      bool
	readOnly          bool
	concurrencySafe   bool
	destructive       bool
	interruptBehavior string
}

// BuildTool starts a new ToolBuilder chain for a tool with the given name.
func BuildTool(name string) *ToolBuilder {
	return &ToolBuilder{name: name}
}

// Description sets the human-readable description for the tool.
func (b *ToolBuilder) Description(desc string) *ToolBuilder {
	b.description = desc
	return b
}

// Deferred marks the tool as deferred loading (only loaded via search_tools).
func (b *ToolBuilder) Deferred(deferLoading bool) *ToolBuilder {
	b.deferLoading = deferLoading
	return b
}

func (b *ToolBuilder) ReadOnly(readOnly bool) *ToolBuilder {
	b.readOnly = readOnly
	if readOnly && !b.concurrencySafe {
		b.concurrencySafe = true
	}
	return b
}

func (b *ToolBuilder) ConcurrencySafe(concurrencySafe bool) *ToolBuilder {
	b.concurrencySafe = concurrencySafe
	return b
}

func (b *ToolBuilder) Destructive(destructive bool) *ToolBuilder {
	b.destructive = destructive
	return b
}

func (b *ToolBuilder) InterruptBehavior(interruptBehavior string) *ToolBuilder {
	b.interruptBehavior = interruptBehavior
	return b
}

// Param adds a parameter to the tool schema.
//
//	b.Param("location", agent.TypeString, "City name", agent.Required())
//	b.Param("limit",    agent.TypeInteger, "Max results", agent.Min(1), agent.Max(100))
func (b *ToolBuilder) Param(name string, t ParamType, description string, opts ...ParamOption) *ToolBuilder {
	p := paramDef{
		name:        name,
		paramType:   t,
		description: description,
	}
	for _, opt := range opts {
		opt(&p)
	}
	b.params = append(b.params, p)
	return b
}

// Handler sets the function that will be called when the tool is invoked.
func (b *ToolBuilder) Handler(fn func(context.Context, map[string]interface{}) (interface{}, error)) *ToolBuilder {
	b.handler = fn
	return b
}

// Build finalises the builder and returns a *Tool ready to be passed to Service.Register.
// Panics if name or handler are missing — this is a programming error detectable at startup.
func (b *ToolBuilder) Build() *Tool {
	if b.name == "" {
		panic("agent.ToolBuilder: name must not be empty")
	}
	if b.handler == nil {
		panic(fmt.Sprintf("agent.ToolBuilder: handler is required for tool %q", b.name))
	}

	return &Tool{
		name:              b.name,
		description:       b.description,
		parameters:        b.buildSchema(),
		handler:           b.handler,
		deferLoading:      b.deferLoading,
		readOnly:          b.readOnly,
		concurrencySafe:   b.concurrencySafe,
		destructive:       b.destructive,
		interruptBehavior: b.interruptBehavior,
	}
}

// buildSchema converts the accumulated paramDef slice into a JSON Schema object
// (map[string]interface{}) compatible with domain.ToolFunction.Parameters.
func (b *ToolBuilder) buildSchema() map[string]interface{} {
	properties := make(map[string]interface{}, len(b.params))
	var required []string

	for _, p := range b.params {
		prop := map[string]interface{}{
			"type": string(p.paramType),
		}
		if p.description != "" {
			prop["description"] = p.description
		}
		if len(p.enum) > 0 {
			prop["enum"] = p.enum
		}
		if p.minimum != nil {
			prop["minimum"] = *p.minimum
		}
		if p.maximum != nil {
			prop["maximum"] = *p.maximum
		}
		if p.items != nil {
			prop["items"] = map[string]interface{}{
				"type": string(p.items.paramType),
			}
		}

		properties[p.name] = prop

		if p.required {
			required = append(required, p.name)
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
