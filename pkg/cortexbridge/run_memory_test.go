package cortexbridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

func newTestDB(t *testing.T) *cortexdb.DB {
	t.Helper()
	db, err := cortexdb.Open(cortexdb.DefaultConfig(filepath.Join(t.TempDir(), "cortex.db")))
	if err != nil {
		t.Fatalf("open cortexdb: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Capture then recall, end to end, deterministically — no LLM, no embedder.
func TestRunMemoryCaptureThenRecall(t *testing.T) {
	rm := NewRunMemory(newTestDB(t))
	ctx := context.Background()

	final := "Investigation done.\n" +
		"DECISION: Increase payments-api `db_pool_size` from 5 to 25.\n" +
		"Some trailing prose."
	if err := rm.CaptureRun(ctx, "payments-api is slow, decide a fix", final); err != nil {
		t.Fatalf("capture: %v", err)
	}

	got, err := rm.RecallForRun(ctx, "payments-api timing out — what did we decide?")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(got, "db_pool_size") {
		t.Fatalf("recall must surface the captured decision, got:\n%s", got)
	}
}

// A run with no marker line writes nothing — and recall stays empty.
func TestRunMemoryIgnoresMarkerlessRuns(t *testing.T) {
	rm := NewRunMemory(newTestDB(t))
	ctx := context.Background()

	if err := rm.CaptureRun(ctx, "small talk", "Sure, here are three fun facts about otters."); err != nil {
		t.Fatalf("capture: %v", err)
	}
	got, err := rm.RecallForRun(ctx, "what did we decide about otters?")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if strings.Contains(got, "otters") {
		t.Fatalf("markerless run must not be captured, got:\n%s", got)
	}
}

func TestIdentifiers(t *testing.T) {
	got := identifiers("DECISION: raise `db_pool_size` on payments-api; ignore /tmp/path and normal words")
	want := map[string]bool{"db_pool_size": true, "payments-api": true}
	for w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Fatalf("identifiers() = %v, missing %q", got, w)
		}
	}
	for _, g := range got {
		if strings.Contains(g, "/") {
			t.Fatalf("identifiers() must skip paths, got %v", got)
		}
	}
}
