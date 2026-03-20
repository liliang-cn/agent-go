package wazero

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

func TestRuntime_Type(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	if runtime.Type() != ptc.RuntimeWazero {
		t.Errorf("expected runtime type %s, got %s", ptc.RuntimeWazero, runtime.Type())
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

func TestRuntime_UnregisterTool(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	// Register a test tool
	runtime.RegisterTool("test_tool", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return nil, nil
	})

	// Unregister it
	if err := runtime.UnregisterTool("test_tool"); err != nil {
		t.Fatalf("failed to unregister tool: %v", err)
	}

	tools := runtime.ListTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools after unregister, got %d", len(tools))
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

func TestMemoryAllocator(t *testing.T) {
	allocator := NewMemoryAllocator(0x10000, 0x10000) // 64KB base, 64KB size

	// Allocate some memory
	ptr1 := allocator.Allocate(100)
	if ptr1 < 0x10000 {
		t.Errorf("expected pointer >= 0x10000, got 0x%x", ptr1)
	}

	// Allocate more memory
	ptr2 := allocator.Allocate(200)
	if ptr2 <= ptr1 {
		t.Errorf("expected second pointer > first, got 0x%x <= 0x%x", ptr2, ptr1)
	}

	// Reset and allocate again
	allocator.Reset()
	ptr3 := allocator.Allocate(100)
	if ptr3 != 0x10000 {
		t.Errorf("expected pointer 0x10000 after reset, got 0x%x", ptr3)
	}
}

func TestMemoryAllocator_OutOfMemory(t *testing.T) {
	allocator := NewMemoryAllocator(0x10000, 100) // 64KB base, 100 bytes size

	// Try to allocate more than available
	ptr := allocator.Allocate(200)
	if ptr != 0 {
		t.Errorf("expected 0 for out of memory, got 0x%x", ptr)
	}
}

// Note: Full execution tests require QuickJS WASM binary
// These tests verify the runtime structure and tool management
