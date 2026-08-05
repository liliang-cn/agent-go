package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

func TestRegisterDeliverableToolsAndScan(t *testing.T) {
	sb, err := sandbox.NewLocal()
	if err != nil {
		t.Fatalf("new local sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	ctx := context.Background()

	// Lay out a few files + a nested dir + the snapshot tarball (which must be skipped).
	if err := sb.WriteFile(ctx, "report.md", []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(ctx, "data.json", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(ctx, "sub/shot.png", []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(ctx, "snapshot.tar.gz", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ScanDeliverables(ctx, sb)
	if err != nil {
		t.Fatalf("ScanDeliverables: %v", err)
	}
	byType := map[string]Deliverable{}
	for _, d := range got {
		byType[d.Type] = d
		if d.Path == "snapshot.tar.gz" {
			t.Fatalf("snapshot tarball should be skipped")
		}
	}
	for _, typ := range []string{"md", "json", "png"} {
		if _, ok := byType[typ]; !ok {
			t.Fatalf("expected a %q deliverable, got %+v", typ, got)
		}
	}

	svc := &Service{toolRegistry: NewToolRegistry()}
	RegisterDeliverableTools(svc, sb)
	if !svc.toolRegistry.Has("list_deliverables") {
		t.Fatal("expected list_deliverables to be registered")
	}
	data := mustOK(t, "list_deliverables", callTool(t, svc, "list_deliverables", map[string]interface{}{}))
	if items, _ := data["deliverables"].([]map[string]interface{}); len(items) != 3 {
		t.Fatalf("expected 3 deliverables (tarball skipped), got %+v", data)
	}
}
