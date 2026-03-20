package goja

import (
	"context"
	"encoding/json"

	"github.com/dop251/goja"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

// Bindings provides additional JavaScript bindings for the Goja runtime
type Bindings struct {
	vm    *goja.Runtime
	state *executionState
}

// NewBindings creates new bindings for a VM
func NewBindings(vm *goja.Runtime, state *executionState) *Bindings {
	return &Bindings{
		vm:    vm,
		state: state,
	}
}

// RegisterToolHelpers registers tool helper functions
func (b *Bindings) RegisterToolHelpers() error {
	// toolExists - check if a tool exists
	_ = b.vm.Set("toolExists", func(name string) bool {
		_, ok := b.state.tools[name]
		return ok
	})

	// listTools - list available tools
	_ = b.vm.Set("listTools", func() []string {
		names := make([]string, 0, len(b.state.tools))
		for name := range b.state.tools {
			names = append(names, name)
		}
		return names
	})

	// callToolSync - call a tool synchronously (same as callTool but explicit)
	_ = b.vm.Set("callToolSync", func(call goja.FunctionCall) goja.Value {
		return b.vm.ToValue(nil) // Will be overridden by main callTool
	})

	return nil
}

// RegisterUtilityHelpers registers utility functions
func (b *Bindings) RegisterUtilityHelpers() error {
	// typeof - enhanced typeof
	_ = b.vm.Set("typeof", func(v interface{}) string {
		if v == nil {
			return "null"
		}
		switch v.(type) {
		case bool:
			return "boolean"
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return "number"
		case string:
			return "string"
		case []interface{}:
			return "array"
		case map[string]interface{}:
			return "object"
		default:
			return "unknown"
		}
	})

	// isEmpty - check if value is empty
	_ = b.vm.Set("isEmpty", func(v interface{}) bool {
		if v == nil {
			return true
		}
		switch val := v.(type) {
		case string:
			return val == ""
		case []interface{}:
			return len(val) == 0
		case map[string]interface{}:
			return len(val) == 0
		default:
			return false
		}
	})

	return nil
}

// RegisterResultHelpers registers result handling functions
func (b *Bindings) RegisterResultHelpers() error {
	// success - create a success result
	_ = b.vm.Set("success", func(data interface{}) map[string]interface{} {
		return map[string]interface{}{
			"success": true,
			"data":    data,
		}
	})

	// failure - create a failure result
	_ = b.vm.Set("failure", func(err string) map[string]interface{} {
		return map[string]interface{}{
			"success": false,
			"error":   err,
		}
	})

	return nil
}

// RegisterAll registers all bindings
func (b *Bindings) RegisterAll() error {
	if err := b.RegisterToolHelpers(); err != nil {
		return err
	}
	if err := b.RegisterUtilityHelpers(); err != nil {
		return err
	}
	if err := b.RegisterResultHelpers(); err != nil {
		return err
	}
	return nil
}

// DefaultToolHandler creates a default tool handler for testing
func DefaultToolHandler(name string) ptc.ToolHandler {
	return func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{
			"tool":  name,
			"args":  args,
			"dummy": true,
		}, nil
	}
}

// MustMarshal marshals a value to JSON or panics
func MustMarshal(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// MustUnmarshal unmarshals JSON or panics
func MustUnmarshal(s string) interface{} {
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(err)
	}
	return v
}
