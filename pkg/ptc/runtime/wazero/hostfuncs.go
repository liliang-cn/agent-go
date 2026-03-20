package wazero

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

// executionState holds state during execution
type executionState struct {
	mu        sync.Mutex
	tools     map[string]ptc.ToolHandler
	toolCalls *[]ptc.ToolCallRecord
	logs      *[]string
	callCount int
	maxCalls  int
	ctx       context.Context
}

// HostFunctions provides host function implementations for WASM
type HostFunctions struct {
	state *executionState
	r     *Runtime
}

// NewHostFunctions creates new host functions
func NewHostFunctions(r *Runtime, state *executionState) *HostFunctions {
	return &HostFunctions{
		state: state,
		r:     r,
	}
}

// CallTool is a host function that calls a tool
func (h *HostFunctions) CallTool(ctx context.Context, mod api.Module, toolNamePtr, toolNameLen, argsPtr, argsLen uint32) (uint32, uint32) {
	h.state.mu.Lock()
	h.state.callCount++
	count := h.state.callCount
	h.state.mu.Unlock()

	// Check max calls
	if count > h.state.maxCalls {
		errJSON := `{"error": "maximum tool calls exceeded"}`
		ptr, len := writeString(mod.Memory(), errJSON)
		return ptr, len
	}

	// Read tool name and arguments
	mem := mod.Memory()
	toolName := readString(mem, toolNamePtr, toolNameLen)
	argsJSON := readString(mem, argsPtr, argsLen)

	// Parse arguments
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		args = make(map[string]interface{})
	}

	// Find tool handler
	h.r.mu.RLock()
	handler, ok := h.r.tools[toolName]
	h.r.mu.RUnlock()

	if !ok {
		errJSON := fmt.Sprintf(`{"error": "tool '%s' not found"}`, toolName)
		ptr, len := writeString(mem, errJSON)
		return ptr, len
	}

	// Execute tool
	start := time.Now()
	result, err := handler(ctx, args)
	duration := time.Since(start)

	// Record tool call
	toolCall := ptc.ToolCallRecord{
		ToolName:  toolName,
		Arguments: args,
		Duration:  duration,
	}

	if err != nil {
		toolCall.Error = err.Error()
		errJSON := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		ptr, len := writeString(mem, errJSON)
		*h.state.toolCalls = append(*h.state.toolCalls, toolCall)
		return ptr, len
	}

	toolCall.Result = result
	*h.state.toolCalls = append(*h.state.toolCalls, toolCall)

	// Return result
	resultJSON, _ := json.Marshal(map[string]interface{}{
		"success": true,
		"result":  result,
	})
	ptr, len := writeString(mem, string(resultJSON))
	return ptr, len
}

// ConsoleLog is a host function for logging
func (h *HostFunctions) ConsoleLog(ctx context.Context, mod api.Module, msgPtr, msgLen uint32) uint32 {
	mem := mod.Memory()
	msg := readString(mem, msgPtr, msgLen)
	*h.state.logs = append(*h.state.logs, msg)
	return 0
}

// ConsoleError is a host function for error logging
func (h *HostFunctions) ConsoleError(ctx context.Context, mod api.Module, msgPtr, msgLen uint32) uint32 {
	mem := mod.Memory()
	msg := readString(mem, msgPtr, msgLen)
	*h.state.logs = append(*h.state.logs, "ERROR: "+msg)
	return 0
}

// GetContextVar gets a context variable
func (h *HostFunctions) GetContextVar(ctx context.Context, mod api.Module, namePtr, nameLen uint32) (uint32, uint32) {
	mem := mod.Memory()
	name := readString(mem, namePtr, nameLen)

	// For now, return empty JSON
	// Context vars should be set via the QuickJS runner
	result := fmt.Sprintf(`{"name": "%s", "value": null}`, name)
	ptr, len := writeString(mem, result)
	return ptr, len
}

// ToolExists checks if a tool exists
func (h *HostFunctions) ToolExists(ctx context.Context, mod api.Module, namePtr, nameLen uint32) uint32 {
	mem := mod.Memory()
	name := readString(mem, namePtr, nameLen)

	h.r.mu.RLock()
	_, ok := h.r.tools[name]
	h.r.mu.RUnlock()

	if ok {
		return 1
	}
	return 0
}

// ListTools returns list of available tools
func (h *HostFunctions) ListTools(ctx context.Context, mod api.Module) (uint32, uint32) {
	h.r.mu.RLock()
	tools := make([]string, 0, len(h.r.tools))
	for name := range h.r.tools {
		tools = append(tools, name)
	}
	h.r.mu.RUnlock()

	result, _ := json.Marshal(tools)
	ptr, len := writeString(mod.Memory(), string(result))
	return ptr, len
}

// buildHostModule builds the host module with all functions
func buildHostModule(ctx context.Context, rt wazero.Runtime, hf *HostFunctions) error {
	builder := rt.NewHostModuleBuilder("ptc")

	// callTool(toolNamePtr, toolNameLen, argsPtr, argsLen) -> (resultPtr, resultLen)
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			toolNamePtr := uint32(stack[0])
			toolNameLen := uint32(stack[1])
			argsPtr := uint32(stack[2])
			argsLen := uint32(stack[3])

			resultPtr, resultLen := hf.CallTool(ctx, mod, toolNamePtr, toolNameLen, argsPtr, argsLen)
			stack[0] = uint64(resultPtr)
			stack[1] = uint64(resultLen)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}).
		Export("call_tool")

	// consoleLog(msgPtr, msgLen) -> void
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			msgPtr := uint32(stack[0])
			msgLen := uint32(stack[1])
			hf.ConsoleLog(ctx, mod, msgPtr, msgLen)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("console_log")

	// consoleError(msgPtr, msgLen) -> void
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			msgPtr := uint32(stack[0])
			msgLen := uint32(stack[1])
			hf.ConsoleError(ctx, mod, msgPtr, msgLen)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("console_error")

	// toolExists(namePtr, nameLen) -> i32
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			namePtr := uint32(stack[0])
			nameLen := uint32(stack[1])
			stack[0] = uint64(hf.ToolExists(ctx, mod, namePtr, nameLen))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("tool_exists")

	// listTools() -> (ptr, len)
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			ptr, length := hf.ListTools(ctx, mod)
			stack[0] = uint64(ptr)
			stack[1] = uint64(length)
		}), []api.ValueType{}, []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}).
		Export("list_tools")

	// Allocate memory function
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			size := uint32(stack[0])
			mem := mod.Memory()
			ptr := allocateMemory(mem, size)
			stack[0] = uint64(ptr)
		}), []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("alloc")

	_, err := builder.Instantiate(ctx)
	return err
}

// allocateMemory allocates memory in the WASM module
func allocateMemory(mem api.Memory, size uint32) uint32 {
	// Simple bump allocator at high memory address
	// In production, use proper allocator
	base := uint32(0x20000) // 128KB offset
	currentSize := mem.Size()

	// Use end of memory for allocation
	ptr := currentSize - size - 1024 // Leave some buffer
	if ptr < base {
		ptr = base
	}

	return ptr
}
