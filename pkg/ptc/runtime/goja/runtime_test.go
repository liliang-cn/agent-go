package goja

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

func TestRuntime_Type(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	if runtime.Type() != ptc.RuntimeGoja {
		t.Errorf("expected runtime type %s, got %s", ptc.RuntimeGoja, runtime.Type())
	}
}

func TestRuntime_ExecuteSimple(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code:     "1 + 1",
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	if result.ReturnValue == nil {
		t.Error("expected return value")
	}

	// The return value should be 2
	if rv, ok := result.ReturnValue.(int64); ok {
		if rv != 2 {
			t.Errorf("expected return value 2, got %d", rv)
		}
	} else if rv, ok := result.ReturnValue.(float64); ok {
		if rv != 2 {
			t.Errorf("expected return value 2, got %f", rv)
		}
	}
}

func TestRuntime_ConsoleLog(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code:     "console.log('hello'); console.log('world');",
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	if len(result.Logs) != 2 {
		t.Errorf("expected 2 logs, got %d", len(result.Logs))
	}

	if result.Logs[0] != "hello" {
		t.Errorf("expected first log 'hello', got %s", result.Logs[0])
	}
}

func TestRuntime_ContextVariables(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code:     "x + y",
		Language: ptc.LanguageJavaScript,
		Context: map[string]interface{}{
			"x": 10,
			"y": 20,
		},
		Timeout: 10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
}

func TestRuntime_ToolRegistration(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	// Register a test tool
	err := runtime.RegisterTool("test_tool", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"result": "ok"}, nil
	})
	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	tools := runtime.ListTools()
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}

	if tools[0] != "test_tool" {
		t.Errorf("expected tool 'test_tool', got %s", tools[0])
	}
}

func TestRuntime_CallTool(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	// Register a test tool
	err := runtime.RegisterTool("add", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		a, _ := args["a"].(float64)
		b, _ := args["b"].(float64)
		return a + b, nil
	})
	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code:     "callTool('add', {a: 5, b: 3})",
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	if len(result.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(result.ToolCalls))
	}

	if result.ToolCalls[0].ToolName != "add" {
		t.Errorf("expected tool name 'add', got %s", result.ToolCalls[0].ToolName)
	}
}

func TestRuntime_Timeout(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code:     "while(true) {}",
		Language: ptc.LanguageJavaScript,
		Timeout:  100 * time.Millisecond,
	}

	_, err := runtime.Execute(ctx, req)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestRuntime_SyntaxError(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code:     "invalid javascript syntax {{{",
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err == nil {
		t.Error("expected syntax error")
	}

	if result.Success {
		t.Error("expected failure")
	}
}

func TestRuntime_Closed(t *testing.T) {
	runtime := NewRuntime()

	// Close the runtime
	if err := runtime.Close(); err != nil {
		t.Fatalf("failed to close: %v", err)
	}

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code:     "1 + 1",
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	_, err := runtime.Execute(ctx, req)
	if err != ptc.ErrSandboxClosed {
		t.Errorf("expected ErrSandboxClosed, got %v", err)
	}
}

func TestNormalizeForJS(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		wantType string // "map", "slice", "string", "same"
	}{
		{"nil value", nil, "same"},
		{"integer", 42, "same"},
		{"plain string", "hello world", "string"},
		{"empty string", "", "string"},
		{"JSON object", `{"key": "value", "count": 3}`, "map"},
		{"JSON array", `[1, 2, 3]`, "slice"},
		{"JSON with whitespace", `  {"key": "value"}  `, "map"},
		{"invalid JSON object-like", `{not valid json}`, "string"},
		{"string starting with letter", `hello {"key": 1}`, "string"},
		{"number string", `42`, "string"},
		{"boolean string", `true`, "string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeForJS(tt.input)
			switch tt.wantType {
			case "map":
				if _, ok := result.(map[string]interface{}); !ok {
					t.Errorf("expected map[string]interface{}, got %T: %v", result, result)
				}
			case "slice":
				if _, ok := result.([]interface{}); !ok {
					t.Errorf("expected []interface{}, got %T: %v", result, result)
				}
			case "string":
				if _, ok := result.(string); !ok {
					t.Errorf("expected string, got %T: %v", result, result)
				}
			case "same":
				if result != tt.input {
					t.Errorf("expected same value %v, got %v", tt.input, result)
				}
			}
		})
	}
}

func TestNormalizeForJS_GoStruct(t *testing.T) {
	type Member struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	}

	t.Run("bare struct", func(t *testing.T) {
		input := Member{ID: "emp_001", Name: "Alice", Role: "Engineer"}
		result := normalizeForJS(input)

		m, ok := result.(map[string]interface{})
		if !ok {
			t.Fatalf("expected map[string]interface{}, got %T: %v", result, result)
		}
		if m["id"] != "emp_001" {
			t.Errorf("expected id=emp_001, got %v", m["id"])
		}
		if m["name"] != "Alice" {
			t.Errorf("expected name=Alice, got %v", m["name"])
		}
		if m["role"] != "Engineer" {
			t.Errorf("expected role=Engineer, got %v", m["role"])
		}
		// Ensure capitalized Go field names are NOT present
		if _, exists := m["ID"]; exists {
			t.Error("should not have capitalized field 'ID'")
		}
	})

	t.Run("slice of structs", func(t *testing.T) {
		input := []Member{
			{ID: "emp_001", Name: "Alice", Role: "Engineer"},
			{ID: "emp_002", Name: "Bob", Role: "Designer"},
		}
		result := normalizeForJS(input)

		s, ok := result.([]interface{})
		if !ok {
			t.Fatalf("expected []interface{}, got %T: %v", result, result)
		}
		if len(s) != 2 {
			t.Fatalf("expected 2 elements, got %d", len(s))
		}
		first, ok := s[0].(map[string]interface{})
		if !ok {
			t.Fatalf("expected map element, got %T", s[0])
		}
		if first["id"] != "emp_001" {
			t.Errorf("expected id=emp_001, got %v", first["id"])
		}
	})

	t.Run("map with nested structs", func(t *testing.T) {
		input := map[string]interface{}{
			"department": "engineering",
			"members": []Member{
				{ID: "emp_001", Name: "Alice", Role: "Engineer"},
			},
			"count": 1,
		}
		result := normalizeForJS(input)

		m, ok := result.(map[string]interface{})
		if !ok {
			t.Fatalf("expected map, got %T", result)
		}
		if m["department"] != "engineering" {
			t.Errorf("expected department=engineering, got %v", m["department"])
		}

		members, ok := m["members"].([]interface{})
		if !ok {
			t.Fatalf("expected []interface{} for members, got %T: %v", m["members"], m["members"])
		}
		first, ok := members[0].(map[string]interface{})
		if !ok {
			t.Fatalf("expected map element, got %T", members[0])
		}
		if first["id"] != "emp_001" {
			t.Errorf("expected id=emp_001, got %v", first["id"])
		}
	})

	t.Run("primitives pass through", func(t *testing.T) {
		if v := normalizeForJS(42); v != 42 {
			t.Errorf("int: expected 42, got %v", v)
		}
		if v := normalizeForJS(3.14); v != 3.14 {
			t.Errorf("float64: expected 3.14, got %v", v)
		}
		if v := normalizeForJS(true); v != true {
			t.Errorf("bool: expected true, got %v", v)
		}
		if v := normalizeForJS(nil); v != nil {
			t.Errorf("nil: expected nil, got %v", v)
		}
	})
}

func TestRuntime_CallToolJSONStringResult(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	// Simulate an MCP tool that returns a JSON string (like websearch_basic does)
	err := runtime.RegisterTool("mcp_websearch", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		// This is what MCP tools actually return: a JSON-encoded string
		return `{"results": [{"title": "Result 1", "url": "https://example.com/1"}, {"title": "Result 2", "url": "https://example.com/2"}], "query": "test"}`, nil
	})
	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code: `
const res = callTool('mcp_websearch', {query: 'test'});
// This should work because the JSON string is auto-parsed into an object
return {
	count: res.results.length,
	firstTitle: res.results[0].title,
	query: res.query
};
`,
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	rv, ok := result.ReturnValue.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map return value, got %T: %v", result.ReturnValue, result.ReturnValue)
	}

	// Verify the JS code could access parsed fields
	if count, ok := rv["count"].(int64); !ok || count != 2 {
		t.Errorf("expected count=2, got %v (%T)", rv["count"], rv["count"])
	}
	if title, ok := rv["firstTitle"].(string); !ok || title != "Result 1" {
		t.Errorf("expected firstTitle='Result 1', got %v", rv["firstTitle"])
	}
	if query, ok := rv["query"].(string); !ok || query != "test" {
		t.Errorf("expected query='test', got %v", rv["query"])
	}
}

func TestRuntime_CallToolMapResult(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	// Tool that returns a native Go map (not a string) — should still work
	err := runtime.RegisterTool("native_tool", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{
			"status": "ok",
			"data":   []interface{}{"a", "b", "c"},
		}, nil
	})
	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code: `
const res = callTool('native_tool', {});
return { status: res.status, count: res.data.length };
`,
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	rv, ok := result.ReturnValue.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map return value, got %T: %v", result.ReturnValue, result.ReturnValue)
	}
	if rv["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", rv["status"])
	}
}

func TestRuntime_ToolDataReturnsJSArray(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	err := runtime.RegisterTool("list_tool", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{
			"success": true,
			"data": []map[string]interface{}{
				{"name": "hello.go", "type": "file"},
				{"name": "pkg", "type": "directory"},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code: `
const res = callTool('list_tool', {});
const files = toolData(res);
const names = [];
files.forEach(file => names.push(file.name));
return {
	isArray: Array.isArray(files),
	count: files.length,
	first: files[0].name,
	joined: files.map(file => file.name).join(',')
};
`,
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	rv, ok := result.ReturnValue.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map return value, got %T: %v", result.ReturnValue, result.ReturnValue)
	}
	if rv["isArray"] != true {
		t.Fatalf("expected files to be a JS array, got %v", rv["isArray"])
	}
	if count, ok := rv["count"].(int64); !ok || count != 2 {
		t.Fatalf("expected count=2, got %v (%T)", rv["count"], rv["count"])
	}
	if rv["first"] != "hello.go" {
		t.Fatalf("expected first=hello.go, got %v", rv["first"])
	}
	if rv["joined"] != "hello.go,pkg" {
		t.Fatalf("expected joined names, got %v", rv["joined"])
	}
}

func TestRuntime_SearchAndCallToolIsUnavailable(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code:     `searchAndCallTool('filesystem', '列出当前目录')`,
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err == nil {
		t.Fatal("expected execution error")
	}
	if result == nil {
		t.Fatal("expected execution result")
	}
	if !strings.Contains(result.Error, "searchAndCallTool is not defined") {
		t.Fatalf("expected searchAndCallTool undefined error, got %q", result.Error)
	}
}

func TestRuntime_MCPToolResultIsWrappedForJS(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	err := runtime.RegisterTool("mcp_filesystem_list_directory", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return "Directory listing for: /tmp", nil
	})
	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code: `
const res = callTool('mcp_filesystem_list_directory', { path: '.' });
return {
	ok: res.success,
	error: res.error,
	data: toolData(res),
	data_text: res.data_text
};
`,
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	rv, ok := result.ReturnValue.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map return value, got %T: %v", result.ReturnValue, result.ReturnValue)
	}
	if rv["ok"] != true {
		t.Fatalf("expected wrapped MCP result to expose success=true, got %v", rv["ok"])
	}
	data, ok := rv["data"].([]interface{})
	if !ok {
		t.Fatalf("expected structured directory entries, got %T: %v", rv["data"], rv["data"])
	}
	if len(data) != 0 {
		t.Fatalf("expected no parsed entries from header-only listing, got %v", data)
	}
	if rv["data_text"] != "Directory listing for: /tmp" {
		t.Fatalf("expected wrapped MCP result raw text, got %v", rv["data_text"])
	}
}

func TestRuntime_CallToolResolvesCamelCaseToolName(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	err := runtime.RegisterTool("mcp_filesystem_list_directory", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return "Directory listing for: /tmp\n\n[FILE] app.go (file:///tmp/app.go) - 42 bytes", nil
	})
	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code: `
const res = callTool('mcp_filesystem_listFiles', { path: '.' });
return {
	ok: toolOk(res),
	first: toolData(res)[0]
};
`,
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	rv, ok := result.ReturnValue.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map return value, got %T: %v", result.ReturnValue, result.ReturnValue)
	}
	if rv["ok"] != true {
		t.Fatalf("expected ok=true, got %v", rv["ok"])
	}
	first, ok := rv["first"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected first entry map, got %T: %v", rv["first"], rv["first"])
	}
	if first["name"] != "app.go" {
		t.Fatalf("expected parsed entry name, got %v", first["name"])
	}
}

func TestRuntime_CallToolPlainStringResult(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	// Tool that returns a plain (non-JSON) string — should remain a string
	err := runtime.RegisterTool("echo_tool", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return "Hello, world!", nil
	})
	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	ctx := context.Background()
	req := &ptc.ExecutionRequest{
		Code: `
const res = callTool('echo_tool', {});
return res;
`,
		Language: ptc.LanguageJavaScript,
		Timeout:  10 * time.Second,
	}

	result, err := runtime.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	if result.ReturnValue != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %v", result.ReturnValue)
	}
}
