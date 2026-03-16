package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/pkg/domain"
)

func TestResolveMemoryQueryContext(t *testing.T) {
	svc := &Service{
		agent:              NewAgent("Assistant"),
		memoryScopeAgentID: "Assistant",
		memoryScopeSquadID: "squad-alpha",
	}
	session := NewSession("agent-1")
	session.SetContext(sessionContextMemoryUserScope, "user-1")

	queryContext := svc.resolveMemoryQueryContext(session)
	if queryContext.SessionID != session.GetID() {
		t.Fatalf("expected session id %q, got %q", session.GetID(), queryContext.SessionID)
	}
	if queryContext.AgentID != "Assistant" {
		t.Fatalf("expected agent scope Assistant, got %q", queryContext.AgentID)
	}
	if queryContext.SquadID != "squad-alpha" {
		t.Fatalf("expected squad scope squad-alpha, got %q", queryContext.SquadID)
	}
	if queryContext.UserID != "user-1" {
		t.Fatalf("expected user scope user-1, got %q", queryContext.UserID)
	}
}

func TestRememberMemoryQueryContext(t *testing.T) {
	svc := &Service{}
	session := NewSession("agent-1")

	svc.rememberMemoryQueryContext(session, domain.MemoryQueryContext{
		AgentID: "Assistant",
		SquadID: "squad-alpha",
		UserID:  "user-1",
	})

	if value, ok := session.GetContext(sessionContextMemoryAgentScope); !ok || value != "Assistant" {
		t.Fatalf("expected agent scope to be stored in session context, got %v %v", value, ok)
	}
	if value, ok := session.GetContext(sessionContextMemorySquadScope); !ok || value != "squad-alpha" {
		t.Fatalf("expected squad scope to be stored in session context, got %v %v", value, ok)
	}
	if value, ok := session.GetContext(sessionContextMemoryUserScope); !ok || value != "user-1" {
		t.Fatalf("expected user scope to be stored in session context, got %v %v", value, ok)
	}
}
