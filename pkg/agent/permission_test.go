package agent

import (
	"context"
	"testing"
)

func TestDefaultPermissionPolicy(t *testing.T) {
	t.Parallel()

	if !DefaultPermissionPolicy(PermissionRequest{ToolName: "write_file"}) {
		t.Fatal("expected write_file to require permission")
	}
	if DefaultPermissionPolicy(PermissionRequest{ToolName: "rag_query"}) {
		t.Fatal("expected rag_query to not require permission")
	}
}

func TestAuthorizeTool(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	if err := svc.authorizeTool(context.Background(), PermissionRequest{ToolName: "write_file"}); err != nil {
		t.Fatalf("expected nil handler to allow, got %v", err)
	}

	called := false
	svc.SetPermissionPolicy(DefaultPermissionPolicy)
	svc.SetPermissionHandler(func(ctx context.Context, req PermissionRequest) (*PermissionResponse, error) {
		called = true
		return &PermissionResponse{Allowed: false, Reason: "denied"}, nil
	})

	err := svc.authorizeTool(context.Background(), PermissionRequest{ToolName: "write_file"})
	if err == nil {
		t.Fatal("expected denied permission to fail")
	}
	if !called {
		t.Fatal("expected permission handler to be called")
	}

	called = false
	err = svc.authorizeTool(context.Background(), PermissionRequest{ToolName: "rag_query"})
	if err != nil {
		t.Fatalf("expected low-risk tool to bypass permission handler, got %v", err)
	}
	if called {
		t.Fatal("expected permission handler to be skipped for low-risk tool")
	}
}

func TestDefaultPermissionPolicy_UsesMetadata(t *testing.T) {
	t.Parallel()

	if DefaultPermissionPolicy(PermissionRequest{ToolName: "custom_read", ReadOnly: true}) {
		t.Fatal("expected readOnly tool to bypass permission")
	}
	if !DefaultPermissionPolicy(PermissionRequest{ToolName: "custom_delete", Destructive: true}) {
		t.Fatal("expected destructive tool to require permission")
	}
}

func TestAuthorizeTool_UsesMetadata(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	called := false
	svc.SetPermissionPolicy(DefaultPermissionPolicy)
	svc.SetPermissionHandler(func(ctx context.Context, req PermissionRequest) (*PermissionResponse, error) {
		called = true
		return &PermissionResponse{Allowed: true}, nil
	})

	if err := svc.authorizeTool(context.Background(), PermissionRequest{
		ToolName: "custom_read",
		ReadOnly: true,
	}); err != nil {
		t.Fatalf("expected readOnly tool to bypass handler, got %v", err)
	}
	if called {
		t.Fatal("expected permission handler to be skipped for readOnly tool")
	}

	if err := svc.authorizeTool(context.Background(), PermissionRequest{
		ToolName:    "custom_delete",
		Destructive: true,
	}); err != nil {
		t.Fatalf("expected destructive tool to be allowed after handler approval, got %v", err)
	}
	if !called {
		t.Fatal("expected permission handler to be invoked for destructive tool")
	}
}

func TestServiceCancel_BlockedByRunningBlockingTool(t *testing.T) {
	t.Parallel()

	svc := &Service{
		inProgressTools: make(map[string]int),
	}
	cancelled := false
	svc.cancelFunc = func() { cancelled = true }

	_, end := svc.beginToolExecution("memory_save", nil)
	defer end()

	if svc.Cancel() {
		t.Fatal("expected cancel to be blocked while blocking tool is running")
	}
	if cancelled {
		t.Fatal("expected cancel func not to be called while blocked")
	}
}

func TestServiceCancel_AllowsCancelableTool(t *testing.T) {
	t.Parallel()

	svc := &Service{
		inProgressTools: make(map[string]int),
		toolRegistry:    NewToolRegistry(),
	}
	cancelled := false
	svc.cancelFunc = func() { cancelled = true }
	svc.toolRegistry.RegisterWithMetadata(
		makeToolDef("rag_query", "RAG"),
		nil,
		CategoryRAG,
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)

	_, end := svc.beginToolExecution("rag_query", nil)
	defer end()

	if !svc.Cancel() {
		t.Fatal("expected cancel to succeed for cancelable tool")
	}
	if !cancelled {
		t.Fatal("expected cancel func to be called")
	}
}
