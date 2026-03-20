// Package goja provides a JavaScript runtime using the Goja interpreter
package goja

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

// wrapCode wraps user code to allow top-level return statements.
//
// Goja treats RunString input as a script (not a module), so bare `return`
// at the top level is a SyntaxError. Wrapping in an IIFE fixes this.
//
// We use a synchronous IIFE (not async) because callTool is synchronous.
// An async IIFE would return a Promise, and synchronous throws inside it
// become rejections that are harder to debug.
//
// We only wrap when necessary to preserve the behaviour of plain expressions
// like "1 + 1" which otherwise return the expression value directly.
func wrapCode(code string) string {
	trimmed := strings.TrimSpace(code)
	// Already wrapped by caller — leave as-is.
	if strings.HasPrefix(trimmed, "(function") || strings.HasPrefix(trimmed, "(async function") {
		return code
	}
	// Check whether the code needs wrapping.
	needsWrap := containsTopLevelKeyword(trimmed, "return")
	if !needsWrap {
		return code
	}
	// Wrap in synchronous IIFE so top-level `return` is valid.
	return "(function() {\n" + code + "\n})()"
}

// containsTopLevelKeyword reports whether keyword appears at the top level of
// code (i.e. not inside a nested function or block string).
// This is a fast heuristic, not a full parser.
func containsTopLevelKeyword(code, keyword string) bool {
	depth := 0 // brace depth
	n := len(code)
	kw := keyword + " "
	kwSemi := keyword + ";"
	kwNL := keyword + "\n"
	for i := 0; i < n; i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case '/':
			// Skip line comments
			if i+1 < n && code[i+1] == '/' {
				for i < n && code[i] != '\n' {
					i++
				}
			}
		}
		if depth == 0 && i+len(keyword) <= n {
			chunk := code[i : i+len(keyword)]
			if chunk == keyword {
				// Check what follows the keyword
				after := ""
				if i+len(keyword) < n {
					after = string(code[i+len(keyword)])
				}
				if strings.Contains(" ;\n\r(", after) || after == "" {
					_ = kw
					_ = kwSemi
					_ = kwNL
					return true
				}
			}
		}
	}
	return false
}

// Runtime implements SandboxRuntime using Goja
type Runtime struct {
	mu             sync.RWMutex
	tools          map[string]ptc.ToolHandler
	searchProvider ptc.SearchProvider
	closed         bool
	maxCalls       int
}

var (
	toolCamelBoundaryRe   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	toolNonWordRe         = regexp.MustCompile(`[^a-z0-9]+`)
	filesystemListEntryRe = regexp.MustCompile(`^\[(DIR|FILE)\]\s+(.+?) \((file://[^)]+)\)(?: - (\d+) bytes)?$`)
)

// NewRuntime creates a new Goja runtime
func NewRuntime() *Runtime {
	return &Runtime{
		tools:    make(map[string]ptc.ToolHandler),
		maxCalls: 20,
	}
}

// ... rest of Runtime methods ...

// SetSearchProvider sets the search provider for on-demand tool discovery
func (r *Runtime) SetSearchProvider(provider ptc.SearchProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.searchProvider = provider
}

// NewRuntimeWithConfig creates a new Goja runtime with configuration
func NewRuntimeWithConfig(config *ptc.Config) *Runtime {
	r := NewRuntime()
	if config != nil && config.MaxToolCalls > 0 {
		r.maxCalls = config.MaxToolCalls
	}
	return r
}

// Type returns the runtime type
func (r *Runtime) Type() ptc.RuntimeType {
	return ptc.RuntimeGoja
}

// Execute runs JavaScript code in the sandbox
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

	// Create a new VM for this execution
	vm := goja.New()

	// Set up execution state
	r.mu.RLock()
	state := &executionState{
		tools:     r.tools,
		toolCalls: &result.ToolCalls,
		logs:      &result.Logs,
		callCount: 0,
		maxCalls:  r.maxCalls,
		ctx:       ctx,
	}
	r.mu.RUnlock()

	// Register built-in functions
	r.registerBuiltins(vm, state)

	// Inject context variables
	for name, value := range req.Context {
		if err := vm.Set(name, value); err != nil {
			result.Error = fmt.Sprintf("failed to inject context variable '%s': %v", name, err)
			result.Duration = time.Since(start)
			return result, ptc.NewExecutionError(err, "execute")
		}
	}

	// Wrap code to allow top-level return statements
	wrappedCode := wrapCode(req.Code)

	// Execute the code
	done := make(chan struct{})
	var execErr error
	var returnValue goja.Value

	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				execErr = fmt.Errorf("panic: %v", recovered)
			}
		}()

		returnValue, execErr = vm.RunString(wrappedCode)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Execution completed
	case <-ctx.Done():
		result.Error = ptc.ErrExecutionTimeout.Error()
		result.Duration = time.Since(start)
		return result, ptc.ErrExecutionTimeout
	case <-time.After(req.Timeout):
		result.Error = ptc.ErrExecutionTimeout.Error()
		result.Duration = time.Since(start)
		return result, ptc.ErrExecutionTimeout
	}

	if execErr != nil {
		result.Error = execErr.Error()
		result.Duration = time.Since(start)
		return result, ptc.NewExecutionError(execErr, "execute")
	}

	// Extract return value - handle Promise (from async functions)
	if returnValue != nil && !goja.IsUndefined(returnValue) {
		exported := returnValue.Export()
		if promise, ok := exported.(*goja.Promise); ok {
			switch promise.State() {
			case goja.PromiseStateFulfilled:
				if v := promise.Result(); v != nil && !goja.IsUndefined(v) {
					result.ReturnValue = v.Export()
				}
			case goja.PromiseStateRejected:
				if v := promise.Result(); v != nil {
					result.Error = fmt.Sprintf("async rejection: %v", v.Export())
					result.Duration = time.Since(start)
					return result, ptc.NewExecutionError(fmt.Errorf("%s", result.Error), "execute")
				}
			}
		} else {
			result.ReturnValue = exported
		}
	}

	// Get console output as main output
	if len(*state.logs) > 0 {
		result.Output = (*state.logs)[len(*state.logs)-1]
	}

	result.Success = true
	result.Duration = time.Since(start)
	return result, nil
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

// Close releases resources
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.tools = nil
	return nil
}

// executionState holds state during execution
type executionState struct {
	tools     map[string]ptc.ToolHandler
	toolCalls *[]ptc.ToolCallRecord
	logs      *[]string
	callCount int
	maxCalls  int
	ctx       context.Context
}

// registerBuiltins registers built-in functions
func (r *Runtime) registerBuiltins(vm *goja.Runtime, state *executionState) {
	// Console object
	console := vm.NewObject()
	_ = console.Set("log", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, arg := range call.Arguments {
			parts[i] = fmt.Sprintf("%v", arg.Export())
		}
		*state.logs = append(*state.logs, strings.Join(parts, " "))
		return goja.Undefined()
	})
	_ = console.Set("error", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, arg := range call.Arguments {
			parts[i] = fmt.Sprintf("%v", arg.Export())
		}
		*state.logs = append(*state.logs, "ERROR: "+strings.Join(parts, " "))
		return goja.Undefined()
	})
	_ = vm.Set("console", console)

	// callTool function
	_ = vm.Set("callTool", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(vm.NewTypeError("callTool requires at least 1 argument"))
		}

		toolName := call.Arguments[0].String()

		// Check call limit
		state.callCount++
		if state.callCount > state.maxCalls {
			panic(vm.NewTypeError("maximum tool calls exceeded"))
		}

		// Get tool handler
		handler, ok := state.tools[toolName]
		if !ok {
			if resolved, found := resolveToolName(toolName, state.tools); found {
				toolName = resolved
				handler = state.tools[toolName]
				ok = true
			}
		}
		if !ok {
			panic(vm.NewTypeError(fmt.Sprintf("tool '%s' not found", toolName)))
		}

		// Parse arguments
		args := make(map[string]interface{})
		if len(call.Arguments) > 1 {
			argVal := call.Arguments[1].Export()
			switch v := argVal.(type) {
			case map[string]interface{}:
				args = v
			case string:
				// Try to parse as JSON
				if err := json.Unmarshal([]byte(v), &args); err != nil {
					args = map[string]interface{}{"value": v}
				}
			default:
				// Try to convert via JSON
				jsonBytes, err := json.Marshal(v)
				if err == nil {
					_ = json.Unmarshal(jsonBytes, &args)
				}
			}
		}

		// Call tool
		start := time.Now()
		toolRecord := ptc.ToolCallRecord{
			ToolName:  toolName,
			Arguments: args,
		}

		result, err := handler(state.ctx, args)
		toolRecord.Duration = time.Since(start)

		if err != nil {
			toolRecord.Error = err.Error()
		} else {
			toolRecord.Result = result
		}

		*state.toolCalls = append(*state.toolCalls, toolRecord)

		if err != nil {
			panic(vm.NewTypeError(fmt.Sprintf("tool '%s' failed: %v", toolName, err)))
		}

		// Normalize result for JS consumption:
		// - JSON strings (from MCP tools) are parsed into native objects
		// - Go structs are JSON-roundtripped so field names use json tags (lowercase)
		result = normalizeForJS(result)
		if strings.HasPrefix(toolName, "mcp_") {
			result = normalizeMCPToolResult(toolName, result)
		}

		return toJSValue(vm, result)
	})

	_ = vm.Set("toolData", func(raw interface{}) interface{} {
		if m, ok := raw.(map[string]interface{}); ok {
			if data, exists := m["data"]; exists {
				return toJSValue(vm, normalizeForJS(data))
			}
		}

		return toJSValue(vm, normalizeForJS(raw))
	})

	_ = vm.Set("toolOk", func(raw interface{}) bool {
		if m, ok := raw.(map[string]interface{}); ok {
			if success, exists := m["success"].(bool); exists {
				return success
			}
		}

		return true
	})

	// sleep function (for convenience)
	_ = vm.Set("sleep", func(ms int64) {
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-state.ctx.Done():
		}
	})

	// JSON helper (note: this overrides the auto-injected JSON global with explicit parse/stringify)
	jsonObj := vm.NewObject()
	_ = jsonObj.Set("parse", func(s string) interface{} {
		var result interface{}
		if err := json.Unmarshal([]byte(s), &result); err != nil {
			panic(vm.NewTypeError(err.Error()))
		}
		return result
	})
	_ = jsonObj.Set("stringify", func(v interface{}) string {
		b, err := json.Marshal(v)
		if err != nil {
			panic(vm.NewTypeError(err.Error()))
		}
		return string(b)
	})
	_ = vm.Set("JSON", jsonObj)
}

// normalizeForJS converts a Go value into a form that Goja exposes with the
// correct (JSON-tag) field names. This handles two cases:
//
//  1. String values that are JSON-encoded (e.g. from MCP tools) — parsed into
//     map/slice so JS code can access fields directly.
//  2. Go structs and slices of structs — JSON-roundtripped so that JSON tag names
//     (lowercase) are used instead of Go field names (capitalized). Without this,
//     Goja's vm.ToValue reflects Go field names, making e.g. member.id undefined
//     when the Go struct has `ID string \`json:"id"\“.
//
// Primitive types (nil, bool, numbers) pass through unchanged.
func normalizeForJS(value interface{}) interface{} {
	if value == nil {
		return nil
	}

	switch v := value.(type) {
	case string:
		return parseJSONString(v)
	case bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return value
	case map[string]interface{}:
		// Map keys are already lowercase strings, but values may contain
		// Go structs (e.g. []TeamMember inside {"members": ...}).
		return normalizeMap(v)
	case []interface{}:
		return normalizeSlice(v)
	default:
		// Likely a Go struct, named type, or slice of structs.
		// JSON-roundtrip to convert field names via json tags.
		return jsonRoundTrip(value)
	}
}

// parseJSONString attempts to parse a JSON string into a native Go type.
// Returns the original string if it's not valid JSON or doesn't look like
// a JSON object/array.
func parseJSONString(s string) interface{} {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) == 0 {
		return s
	}
	first := trimmed[0]
	if first != '{' && first != '[' {
		return s
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return s
	}
	return parsed
}

// normalizeMap recursively normalizes map values that may contain Go structs.
func normalizeMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = normalizeForJS(v)
	}
	return out
}

// normalizeSlice recursively normalizes slice elements.
func normalizeSlice(s []interface{}) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = normalizeForJS(v)
	}
	return out
}

// jsonRoundTrip marshals a Go value to JSON and unmarshals it back, converting
// struct field names to their json tag equivalents. Returns the original value
// if marshaling or unmarshaling fails.
func jsonRoundTrip(value interface{}) interface{} {
	b, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized interface{}
	if err := json.Unmarshal(b, &normalized); err != nil {
		return value
	}
	return normalized
}

func normalizeMCPToolResult(toolName string, result interface{}) interface{} {
	if isStandardToolEnvelope(result) {
		if m, ok := result.(map[string]interface{}); ok {
			return normalizeMCPToolEnvelope(toolName, m)
		}
		return result
	}
	switch result.(type) {
	case map[string]interface{}, []interface{}:
		return result
	}

	return normalizeMCPToolEnvelope(toolName, map[string]interface{}{
		"success": true,
		"data":    result,
		"error":   nil,
	})
}

func isStandardToolEnvelope(result interface{}) bool {
	m, ok := result.(map[string]interface{})
	if !ok {
		return false
	}

	_, hasSuccess := m["success"]
	_, hasData := m["data"]
	_, hasError := m["error"]

	return hasSuccess || hasData || hasError
}

func toJSValue(vm *goja.Runtime, value interface{}) goja.Value {
	switch v := value.(type) {
	case nil:
		return goja.Null()
	case map[string]interface{}:
		obj := vm.NewObject()
		for key, item := range v {
			_ = obj.Set(key, toJSValue(vm, item))
		}
		return obj
	case []interface{}:
		arr := vm.NewArray()
		for i, item := range v {
			_ = arr.Set(strconv.Itoa(i), toJSValue(vm, item))
		}
		return arr
	default:
		return vm.ToValue(v)
	}
}

func normalizeMCPToolEnvelope(toolName string, envelope map[string]interface{}) map[string]interface{} {
	if toolName == "mcp_filesystem_list_directory" {
		if text, ok := envelope["data"].(string); ok {
			envelope["data_text"] = text
			envelope["data"] = parseFilesystemListDirectory(text)
		}
	}
	return envelope
}

func parseFilesystemListDirectory(raw string) []interface{} {
	lines := strings.Split(raw, "\n")
	items := make([]interface{}, 0)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		matches := filesystemListEntryRe.FindStringSubmatch(line)
		if len(matches) == 0 {
			continue
		}

		itemType := strings.ToLower(matches[1])
		name := matches[2]
		uri := matches[3]
		path := strings.TrimPrefix(uri, "file://")

		entry := map[string]interface{}{
			"name": name,
			"type": itemType,
			"uri":  uri,
			"path": path,
		}
		if matches[4] != "" {
			if size, err := strconv.Atoi(matches[4]); err == nil {
				entry["size_bytes"] = size
			}
		}
		items = append(items, entry)
	}

	return items
}

func resolveToolName(name string, tools map[string]ptc.ToolHandler) (string, bool) {
	if _, ok := tools[name]; ok {
		return name, true
	}

	candidates := make([]string, 0, len(tools))
	for candidate := range tools {
		candidates = append(candidates, candidate)
	}

	if resolved := resolveClosestToolName(name, candidates); resolved != "" {
		return resolved, true
	}
	return "", false
}

func resolveClosestToolName(name string, candidates []string) string {
	if name == "" || len(candidates) == 0 {
		return ""
	}

	normalizedTarget := normalizeToolName(name)
	if normalizedTarget == "" {
		return ""
	}

	bestName := ""
	bestScore := 0
	targetTokens := strings.Split(normalizedTarget, "_")

	for _, candidate := range candidates {
		normalizedCandidate := normalizeToolName(candidate)
		if normalizedCandidate == normalizedTarget {
			return candidate
		}
		score := scoreToolNameMatch(targetTokens, strings.Split(normalizedCandidate, "_"))
		if score > bestScore {
			bestScore = score
			bestName = candidate
		}
	}

	if bestScore < 4 {
		return ""
	}
	return bestName
}

func normalizeToolName(name string) string {
	name = toolCamelBoundaryRe.ReplaceAllString(name, `${1}_${2}`)
	name = strings.ToLower(name)
	name = toolNonWordRe.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	name = strings.ReplaceAll(name, "__", "_")
	return name
}

func scoreToolNameMatch(target, candidate []string) int {
	if len(target) == 0 || len(candidate) == 0 {
		return 0
	}

	score := 0
	for i := 0; i < len(target) && i < len(candidate); i++ {
		if target[i] != candidate[i] {
			break
		}
		score += 3
	}

	candidateSet := make(map[string]struct{}, len(candidate))
	for _, token := range candidate {
		candidateSet[token] = struct{}{}
	}
	for _, token := range target {
		if _, ok := candidateSet[token]; ok {
			score++
		}
	}

	return score
}
