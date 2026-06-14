package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

// TestWorkspaceArchiveRoundtrip exercises the full path used by checkpoint
// workspace snapshots: sandbox.Snapshot → bytes → List/Extract, and restore
// into a fresh sandbox.
func TestWorkspaceArchiveRoundtrip(t *testing.T) {
	ctx := context.Background()
	sb, err := sandbox.NewLocal()
	if err != nil {
		t.Fatalf("new sandbox: %v", err)
	}
	defer sb.Close()

	if err := sb.WriteFile(ctx, "report.md", []byte("# hello\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if err := sb.WriteFile(ctx, "data/notes.txt", []byte("note"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	archive := snapshotWorkspaceBytes(ctx, sb)
	if len(archive) == 0 {
		t.Fatal("snapshotWorkspaceBytes returned no data")
	}

	entries, err := ListArchive(archive)
	if err != nil {
		t.Fatalf("ListArchive: %v", err)
	}
	got := map[string]int64{}
	for _, e := range entries {
		if !e.Dir {
			got[e.Path] = e.Size
		}
	}
	if _, ok := got["report.md"]; !ok {
		t.Fatalf("report.md not in archive: %v", got)
	}
	if _, ok := got["data/notes.txt"]; !ok {
		t.Fatalf("data/notes.txt not in archive: %v", got)
	}

	// Extract to disk and verify contents.
	dest := t.TempDir()
	if err := ExtractArchive(archive, dest); err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "report.md"))
	if err != nil || string(b) != "# hello\n" {
		t.Fatalf("extracted report.md mismatch: %q err=%v", string(b), err)
	}

	// Restore into a fresh sandbox.
	sb2, err := sandbox.NewLocal()
	if err != nil {
		t.Fatalf("new sandbox2: %v", err)
	}
	defer sb2.Close()
	if err := restoreWorkspaceBytes(ctx, sb2, archive); err != nil {
		t.Fatalf("restoreWorkspaceBytes: %v", err)
	}
	rb, err := sb2.ReadFile(ctx, "report.md")
	if err != nil || string(rb) != "# hello\n" {
		t.Fatalf("restored report.md mismatch: %q err=%v", string(rb), err)
	}
}

func TestSnapshotWorkspaceBytes_NilSandbox(t *testing.T) {
	if snapshotWorkspaceBytes(context.Background(), nil) != nil {
		t.Fatal("expected nil for nil sandbox")
	}
}
