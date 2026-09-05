package agent

import (
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func appendTestService(t *testing.T) *Service {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Service{store: store}
}

// An exchange the host carried out around the model — a question addressed to
// another agent and that agent's reply — has to land in the session, or the
// next turn cannot see it and reopening the conversation shows a hole.
func TestAppendSessionMessagesCreatesAndExtendsASession(t *testing.T) {
	s := appendTestService(t)

	err := s.AppendSessionMessages("conv-1",
		domain.Message{Role: "user", Content: "@hermes what is on node-e?"},
		domain.Message{Role: "assistant", Content: "**@hermes** · sds@node-e · 4.3s\n\nopenclaw, pi and me."},
	)
	if err != nil {
		t.Fatalf("append into a session that did not exist: %v", err)
	}
	sess, err := s.GetSession("conv-1")
	if err != nil || sess == nil {
		t.Fatalf("the session was not created: %v", err)
	}
	got := sess.GetMessages()
	if len(got) != 2 || got[0].Role != "user" || got[1].Role != "assistant" {
		t.Fatalf("messages = %+v, want the user question then the reply, in order", got)
	}
	if got[1].Content != "**@hermes** · sds@node-e · 4.3s\n\nopenclaw, pi and me." {
		t.Errorf("the reply was altered on the way in: %q", got[1].Content)
	}

	// A second append extends, it does not replace.
	if err := s.AppendSessionMessages("conv-1", domain.Message{Role: "user", Content: "thanks"}); err != nil {
		t.Fatal(err)
	}
	sess, _ = s.GetSession("conv-1")
	if n := len(sess.GetMessages()); n != 3 {
		t.Fatalf("after a second append there are %d messages, want 3", n)
	}
}

func TestAppendSessionMessagesRefusesNothingToPointAt(t *testing.T) {
	s := appendTestService(t)
	if err := s.AppendSessionMessages("   ", domain.Message{Role: "user", Content: "x"}); err == nil {
		t.Error("an empty session id was accepted")
	}
	// Nothing to append is not an error, and must not create an empty session.
	if err := s.AppendSessionMessages("conv-empty"); err != nil {
		t.Fatalf("appending nothing errored: %v", err)
	}
	if sess, err := s.GetSession("conv-empty"); err == nil && sess != nil && len(sess.GetMessages()) == 0 {
		t.Error("appending nothing created an empty session")
	}
}
