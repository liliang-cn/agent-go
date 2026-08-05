package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/resource"
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

func TestResourceRegistryCRUD(t *testing.T) {
	db := newTestAgentGoDB(t)
	res := resource.Resource{
		ID:        "tool:test",
		Kind:      resource.KindTool,
		Name:      "test",
		Execution: resource.ExecutionDirectOnly,
		Metadata:  map[string]any{"category": "custom"},
	}
	if err := db.SaveResource(res); err != nil {
		t.Fatalf("save resource: %v", err)
	}
	resources, err := db.ListResources()
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if resources[0].ID != res.ID || resources[0].Execution != resource.ExecutionDirectOnly {
		t.Fatalf("unexpected resource: %+v", resources[0])
	}
	if resources[0].Metadata["category"] != "custom" {
		t.Fatalf("metadata did not round-trip: %+v", resources[0].Metadata)
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
