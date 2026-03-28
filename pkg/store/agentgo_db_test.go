package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestAgentGoDB(t *testing.T) *AgentGoDB {
	t.Helper()

	db, err := NewAgentGoDB(filepath.Join(t.TempDir(), "agentgo.db"))
	if err != nil {
		t.Fatalf("new agentgo db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestGetSessionFallsBackToPersistedMessages(t *testing.T) {
	db := newTestAgentGoDB(t)

	if err := db.AddMessage("session-1", "user", "hello", nil); err != nil {
		t.Fatalf("add user message: %v", err)
	}
	if err := db.AddMessage("session-1", "assistant", "world", map[string]interface{}{"provider": "test"}); err != nil {
		t.Fatalf("add assistant message: %v", err)
	}

	session, err := db.GetSession("session-1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if len(session.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(session.Messages))
	}
	if session.Messages[0].Content != "hello" || session.Messages[1].Content != "world" {
		t.Fatalf("unexpected messages: %#v", session.Messages)
	}
}

func TestCountMessages(t *testing.T) {
	db := newTestAgentGoDB(t)

	if err := db.AddMessage("session-2", "user", "one", nil); err != nil {
		t.Fatalf("add first message: %v", err)
	}
	if err := db.AddMessage("session-2", "assistant", "two", nil); err != nil {
		t.Fatalf("add second message: %v", err)
	}

	count, err := db.CountMessages("session-2")
	if err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 messages, got %d", count)
	}
}

func TestIsSQLiteLockedError(t *testing.T) {
	if !isSQLiteLockedError(errors.New("database is locked (261)")) {
		t.Fatal("expected database is locked to be treated as retryable")
	}
	if !isSQLiteLockedError(errors.New("database table is locked: foo")) {
		t.Fatal("expected database table is locked to be treated as retryable")
	}
	if isSQLiteLockedError(errors.New("permission denied")) {
		t.Fatal("did not expect unrelated sqlite error to be treated as retryable")
	}
}

func TestNewAgentGoDBMigratesLegacySharedTasksColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agentgo.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite db: %v", err)
	}
	_, err = rawDB.Exec(`
		CREATE TABLE shared_tasks (
			id TEXT PRIMARY KEY,
			session_id TEXT,
			squad_id TEXT NOT NULL,
			squad_name TEXT,
			captain_name TEXT NOT NULL,
			agent_names TEXT NOT NULL,
			prompt TEXT NOT NULL,
			ack_message TEXT,
			status TEXT NOT NULL,
			queued_ahead INTEGER DEFAULT 0,
			result_text TEXT,
			results TEXT,
			created_at DATETIME NOT NULL,
			started_at DATETIME,
			finished_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("create legacy shared_tasks table: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw sqlite db: %v", err)
	}

	db, err := NewAgentGoDB(dbPath)
	if err != nil {
		t.Fatalf("NewAgentGoDB() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	columns, err := db.tableColumnSetLocked("shared_tasks")
	if err != nil {
		t.Fatalf("tableColumnSetLocked() error = %v", err)
	}
	if !columns["team_id"] {
		t.Fatalf("expected migrated shared_tasks to include team_id, got %#v", columns)
	}
	if !columns["team_name"] {
		t.Fatalf("expected migrated shared_tasks to include team_name, got %#v", columns)
	}
	if columns["squad_id"] {
		t.Fatalf("expected squad_id to be migrated away, got %#v", columns)
	}
	if columns["squad_name"] {
		t.Fatalf("expected squad_name to be migrated away, got %#v", columns)
	}
}
