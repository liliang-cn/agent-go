// Package wazero provides a WASM runtime using Wazero
package wazero

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	gojart "github.com/liliang-cn/agent-go/v2/pkg/ptc/runtime/goja"
)

// Runtime implements SandboxRuntime using Wazero WASM
type Runtime struct {
	mu       sync.RWMutex
	tools    map[string]ptc.ToolHandler
	config   *ptc.Config
	closed   bool
	maxCalls int

	// WASM runtime
	wazeroRT wazero.Runtime
}

// NewRuntime creates a new Wazero runtime
func NewRuntime() *Runtime {
	return NewRuntimeWithConfig(nil)
}

// NewRuntimeWithConfig creates a new Wazero runtime with configuration
func NewRuntimeWithConfig(config *ptc.Config) *Runtime {
	if config == nil {
		defaultCfg := ptc.DefaultConfig()
		config = &defaultCfg
	}

	return &Runtime{
		tools:    make(map[string]ptc.ToolHandler),
		config:   config,
		maxCalls: config.MaxToolCalls,
	}
}

// Type returns the runtime type
func (r *Runtime) Type() ptc.RuntimeType {
	return ptc.RuntimeWazero
}

// Execute runs code in the WASM sandbox
func (r *Runtime) Execute(ctx context.Context, req *ptc.ExecutionRequest) (*ptc.ExecutionResult, error) {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return nil, ptc.ErrSandboxClosed
	}
	r.mu.RUnlock()

	start := time.Now()
	result := &ptc.ExecutionResult{
		ID:        req.ID,
		ToolCalls: make([]ptc.ToolCallRecord, 0),
		Logs:      make([]string, 0),
	}

	// Initialize WASM runtime if needed
	if err := r.initRuntime(ctx); err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(start)
		return result, ptc.NewExecutionError(err, "init")
	}

	// Create execution state
	state := &executionState{
		tools:     r.tools,
		toolCalls: &result.ToolCalls,
		logs:      &result.Logs,
		callCount: 0,
		maxCalls:  r.maxCalls,
		ctx:       ctx,
	}

	// Check language type
	switch req.Language {
	case ptc.LanguageJavaScript:
		// JavaScript execution via QuickJS WASM
		if err := r.executeJS(ctx, req.Code, req.Context, state); err != nil {
			result.Error = err.Error()
			result.Duration = time.Since(start)
			return result, ptc.NewExecutionError(err, "execute")
		}
		result.Success = true
		result.Duration = time.Since(start)
		return result, nil

	case ptc.LanguageWasm:
		// WASM binary execution
		return r.executeWASM(ctx, req, result, start, state)

	default:
		result.Error = fmt.Sprintf("unsupported language: %s", req.Language)
		result.Duration = time.Since(start)
		return result, ptc.NewExecutionError(fmt.Errorf("unsupported language"), "execute")
	}
}

// RegisterTool registers a tool handler
func (r *Runtime) RegisterTool(name string, handler ptc.ToolHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = handler
	return nil
}

// UnregisterTool removes a tool handler
func (r *Runtime) UnregisterTool(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	return nil
}

// ListTools returns all registered tool names
func (r *Runtime) ListTools() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// SetSearchProvider sets the search provider for on-demand tool discovery
func (r *Runtime) SetSearchProvider(provider ptc.SearchProvider) {
	// Not implemented for native WASM yet, but implemented via JS bridge
}

// Close releases resources
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}
	r.closed = true

	if r.wazeroRT != nil {
		_ = r.wazeroRT.Close(context.Background())
		r.wazeroRT = nil
	}
	r.tools = nil
	return nil
}

// initRuntime initializes the WASM runtime
func (r *Runtime) initRuntime(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.wazeroRT != nil {
		return nil
	}

	// Create wazero runtime
	r.wazeroRT = wazero.NewRuntime(ctx)

	// Instantiate WASI for WASM modules
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r.wazeroRT); err != nil {
		return fmt.Errorf("failed to instantiate WASI: %w", err)
	}

	// Build host functions
	if err := r.buildHostFunctions(ctx); err != nil {
		return fmt.Errorf("failed to build host functions: %w", err)
	}

	return nil
}

// buildHostFunctions builds the host functions module
func (r *Runtime) buildHostFunctions(ctx context.Context) error {
	// Create host module builder
	builder := r.wazeroRT.NewHostModuleBuilder("env")

	// Tool call state for this runtime
	state := &executionState{
		tools:     r.tools,
		callCount: 0,
		maxCalls:  r.maxCalls,
	}

	// callTool function (string toolName, string argsJSON) -> string resultJSON
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			// Get arguments from stack
			toolNamePtr := uint32(stack[0])
			toolNameLen := uint32(stack[1])
			argsPtr := uint32(stack[2])
			argsLen := uint32(stack[3])

			// Read strings from memory
			mem := mod.Memory()
			toolName := readString(mem, toolNamePtr, toolNameLen)
			argsJSON := readString(mem, argsPtr, argsLen)

			// Parse arguments
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				args = make(map[string]interface{})
			}

			// Call the tool
			result := r.callTool(ctx, toolName, args, state)

			// Write result back to memory
			resultJSON, _ := json.Marshal(result)
			resultPtr, resultLen := writeString(mem, string(resultJSON))

			// Return pointer and length
			stack[0] = uint64(resultPtr)
			stack[1] = uint64(resultLen)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}).
		Export("callTool")

	// console.log function
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			msgPtr := uint32(stack[0])
			msgLen := uint32(stack[1])

			mem := mod.Memory()
			msg := readString(mem, msgPtr, msgLen)

			*state.logs = append(*state.logs, msg)
			stack[0] = 0
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("consoleLog")

	// Instantiate the host module
	if _, err := builder.Instantiate(ctx); err != nil {
		return fmt.Errorf("failed to instantiate host module: %w", err)
	}

	return nil
}

// callTool calls a tool handler
func (r *Runtime) callTool(ctx context.Context, toolName string, args map[string]interface{}, state *executionState) map[string]interface{} {
	state.mu.Lock()
	state.callCount++
	count := state.callCount
	state.mu.Unlock()

	if count > state.maxCalls {
		return map[string]interface{}{
			"error": "maximum tool calls exceeded",
		}
	}

	handler, ok := r.tools[toolName]
	if !ok {
		return map[string]interface{}{
			"error": fmt.Sprintf("tool '%s' not found", toolName),
		}
	}

	start := time.Now()
	result, err := handler(ctx, args)
	duration := time.Since(start)

	toolCall := ptc.ToolCallRecord{
		ToolName:  toolName,
		Arguments: args,
		Duration:  duration,
	}

	if err != nil {
		toolCall.Error = err.Error()
		return map[string]interface{}{
			"error": err.Error(),
		}
	}

	toolCall.Result = result
	*state.toolCalls = append(*state.toolCalls, toolCall)

	return map[string]interface{}{
		"success": true,
		"result":  result,
	}
}

// executeWASM executes a WASM binary
func (r *Runtime) executeWASM(ctx context.Context, req *ptc.ExecutionRequest, result *ptc.ExecutionResult, start time.Time, state *executionState) (*ptc.ExecutionResult, error) {
	// Compile the WASM module
	compiled, err := r.wazeroRT.CompileModule(ctx, []byte(req.Code))
	if err != nil {
		result.Error = fmt.Sprintf("failed to compile WASM: %v", err)
		result.Duration = time.Since(start)
		return result, ptc.NewExecutionError(err, "compile")
	}
	defer compiled.Close(ctx)

	// Instantiate the module
	mod, err := r.wazeroRT.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		result.Error = fmt.Sprintf("failed to instantiate WASM: %v", err)
		result.Duration = time.Since(start)
		return result, ptc.NewExecutionError(err, "instantiate")
	}
	defer mod.Close(ctx)

	// Look for and call the main/exported function
	entryPoints := []string{"_start", "main", "run", "execute"}
	var lastErr error

	for _, entry := range entryPoints {
		fn := mod.ExportedFunction(entry)
		if fn != nil {
			_, err = fn.Call(ctx)
			if err != nil {
				lastErr = err
				continue
			}
			result.Success = true
			result.Duration = time.Since(start)
			return result, nil
		}
	}

	if lastErr != nil {
		result.Error = lastErr.Error()
		result.Duration = time.Since(start)
		return result, ptc.NewExecutionError(lastErr, "execute")
	}

	result.Error = "no entry point found (tried: _start, main, run, execute)"
	result.Duration = time.Since(start)
	return result, ptc.NewExecutionError(fmt.Errorf("no entry point"), "execute")
}

// executeJS executes JavaScript code using Goja (with Wazero-level isolation)
// Note: For JavaScript execution, we use Goja internally because quickjs-emscripten
// requires Emscripten's JavaScript runtime which is complex to implement in pure Go.
// Wazero is primarily used for WASM binary execution.
func (r *Runtime) executeJS(ctx context.Context, code string, contextVars map[string]interface{}, state *executionState) error {
	// Create a Goja runtime for JavaScript execution
	// This provides better JavaScript compatibility while maintaining sandbox isolation
	jsRuntime := gojart.NewRuntimeWithConfig(r.config)
	defer jsRuntime.Close()

	// Register tools
	for name, handler := range r.tools {
		jsRuntime.RegisterTool(name, handler)
	}

	// Create execution request
	req := &ptc.ExecutionRequest{
		Code:     code,
		Language: ptc.LanguageJavaScript,
		Context:  contextVars,
		Timeout:  r.config.DefaultTimeout,
	}

	// Execute using Goja
	result, err := jsRuntime.Execute(ctx, req)
	if err != nil {
		return err
	}

	// Copy results to state
	if result != nil {
		*state.toolCalls = append(*state.toolCalls, result.ToolCalls...)
		*state.logs = append(*state.logs, result.Logs...)
	}

	return nil
}

// ExecuteWithWASM executes a pre-compiled WASM module with the given entry point
func (r *Runtime) ExecuteWithWASM(ctx context.Context, wasmBytes []byte, entryPoint string, args []interface{}) (interface{}, error) {
	if r.closed {
		return nil, ptc.ErrSandboxClosed
	}

	if err := r.initRuntime(ctx); err != nil {
		return nil, err
	}

	// Compile the WASM module
	compiled, err := r.wazeroRT.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to compile WASM: %w", err)
	}
	defer compiled.Close(context.Background())

	// Instantiate the module
	mod, err := r.wazeroRT.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate WASM: %w", err)
	}
	defer mod.Close(ctx)

	// Find and call the entry point function
	fn := mod.ExportedFunction(entryPoint)
	if fn == nil {
		return nil, fmt.Errorf("entry point '%s' not found", entryPoint)
	}

	// Convert args to uint64 for WASM
	stackArgs := make([]uint64, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case int:
			stackArgs[i] = uint64(v)
		case int32:
			stackArgs[i] = uint64(v)
		case int64:
			stackArgs[i] = uint64(v)
		case uint32:
			stackArgs[i] = uint64(v)
		case uint64:
			stackArgs[i] = v
		default:
			stackArgs[i] = 0
		}
	}

	results, err := fn.Call(ctx, stackArgs...)
	if err != nil {
		return nil, fmt.Errorf("WASM execution failed: %w", err)
	}

	if len(results) > 0 {
		return results[0], nil
	}
	return nil, nil
}
