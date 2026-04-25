package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestResolveMemoryQueryContext(t *testing.T) {
	svc := &Service{
		agent:              NewAgent("Responder"),
		memoryScopeAgentID: "Responder",
		memoryScopeTeamID:  "team-alpha",
	}
	session := NewSession("agent-1")
	session.SetContext(sessionContextMemoryUserScope, "user-1")

	queryContext := svc.resolveMemoryQueryContext(session)
	if queryContext.SessionID != session.GetID() {
		t.Fatalf("expected session id %q, got %q", session.GetID(), queryContext.SessionID)
	}
	if queryContext.AgentID != "Responder" {
		t.Fatalf("expected agent scope Responder, got %q", queryContext.AgentID)
	}
	if queryContext.TeamID != "team-alpha" {
		t.Fatalf("expected team scope team-alpha, got %q", queryContext.TeamID)
	}
	if queryContext.UserID != "user-1" {
		t.Fatalf("expected user scope user-1, got %q", queryContext.UserID)
	}
}

func TestRememberMemoryQueryContext(t *testing.T) {
	svc := &Service{}
	session := NewSession("agent-1")

	svc.rememberMemoryQueryContext(session, domain.MemoryQueryContext{
		AgentID: "Responder",
		TeamID:  "team-alpha",
		UserID:  "user-1",
	})

	if value, ok := session.GetContext(sessionContextMemoryAgentScope); !ok || value != "Responder" {
		t.Fatalf("expected agent scope to be stored in session context, got %v %v", value, ok)
	}
	if value, ok := session.GetContext(sessionContextMemoryTeamScope); !ok || value != "team-alpha" {
		t.Fatalf("expected team scope to be stored in session context, got %v %v", value, ok)
	}
	if value, ok := session.GetContext(sessionContextMemoryUserScope); !ok || value != "user-1" {
		t.Fatalf("expected user scope to be stored in session context, got %v %v", value, ok)
	}
}

func TestResolveMemoryQueryContextFromContextPreservesInheritedScopeForBuiltInAgent(t *testing.T) {
	svc := &Service{
		agent:              NewAgent("Dispatcher"),
		memoryScopeAgentID: "Dispatcher",
		memoryScopeTeamID:  "team-alpha",
	}

	session := NewSession("session-1")
	session.SetContext(sessionContextMemoryAgentScope, "Dispatcher")
	session.SetContext(sessionContextMemoryTeamScope, "team-alpha")

	ctx := withCurrentSession(context.Background(), session)
	ctx = withCurrentAgent(ctx, NewAgent("Archivist"))

	queryContext := svc.resolveMemoryQueryContextFromContext(ctx)
	if queryContext.AgentID != "Dispatcher" {
		t.Fatalf("expected inherited agent scope Dispatcher, got %q", queryContext.AgentID)
	}
	if queryContext.TeamID != "team-alpha" {
		t.Fatalf("expected inherited team scope team-alpha, got %q", queryContext.TeamID)
	}
}

func TestResolveMemoryQueryContextFromContextUsesCurrentScopeForCustomAgent(t *testing.T) {
	svc := &Service{
		agent:              NewAgent("Dispatcher"),
		memoryScopeAgentID: "Dispatcher",
	}

	session := NewSession("session-1")
	session.SetContext(sessionContextMemoryAgentScope, "Dispatcher")

	ctx := withCurrentSession(context.Background(), session)
	ctx = withCurrentAgent(ctx, NewAgent("ReleasePlanner"))

	queryContext := svc.resolveMemoryQueryContextFromContext(ctx)
	if queryContext.AgentID != "ReleasePlanner" {
		t.Fatalf("expected current custom agent scope ReleasePlanner, got %q", queryContext.AgentID)
	}
}
