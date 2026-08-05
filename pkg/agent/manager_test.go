package agent

import (
	"path/filepath"
	"testing"
)

func TestNewStoreAppliesSQLitePragmas(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	defer store.GetAgentGoDB().GetDB().Close()

	var journalMode string
	if err := store.GetAgentGoDB().GetDB().QueryRow(`PRAGMA journal_mode;`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode failed: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("expected WAL mode, got %q", journalMode)
	}

	var busyTimeout int
	if err := store.GetAgentGoDB().GetDB().QueryRow(`PRAGMA busy_timeout;`).Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout failed: %v", err)
	}
	// pkg/store sets PRAGMA busy_timeout=5000 when it opens the DB.
	const wantBusyTimeoutMillis = 5000
	if busyTimeout < wantBusyTimeoutMillis {
		t.Fatalf("expected busy_timeout >= %d, got %d", wantBusyTimeoutMillis, busyTimeout)
	}
}
