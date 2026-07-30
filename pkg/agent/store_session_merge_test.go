package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func msg(role, content string) domain.Message {
	return domain.Message{Role: role, Content: content}
}

// mergeMessagesForSave is what keeps two overlapping runs from erasing each
// other. The failure it prevents was observed: a history of "user: B, user: B"
// with both answers gone.
func TestMergeMessagesForSaveIsAdditive(t *testing.T) {
	stored := []domain.Message{msg("user", "Q-A"), msg("assistant", "A-A")}
	appended := []domain.Message{msg("user", "Q-B"), msg("assistant", "A-B")}

	got := mergeMessagesForSave(stored, appended)
	if len(got) != 4 {
		t.Fatalf("merged length = %d, want 4: %+v", len(got), got)
	}
	for i, want := range []string{"Q-A", "A-A", "Q-B", "A-B"} {
		if got[i].Content != want {
			t.Errorf("message %d = %q, want %q", i, got[i].Content, want)
		}
	}
}

func TestMergeMessagesForSaveDeduplicates(t *testing.T) {
	stored := []domain.Message{msg("user", "Q"), msg("assistant", "A")}

	// A retried save must not double-write the same turns.
	got := mergeMessagesForSave(stored, []domain.Message{msg("user", "Q"), msg("assistant", "A")})
	if len(got) != 2 {
		t.Errorf("re-saving the same turns should change nothing, got %d: %+v", len(got), got)
	}

	// Same text under a different tool_call_id is a different turn.
	a := domain.Message{Role: "tool", Content: "ok", ToolCallID: "call_1"}
	b := domain.Message{Role: "tool", Content: "ok", ToolCallID: "call_2"}
	got = mergeMessagesForSave([]domain.Message{a}, []domain.Message{b})
	if len(got) != 2 {
		t.Errorf("distinct tool_call_ids must both survive, got %d", len(got))
	}

	// Nothing appended: the stored history is returned untouched.
	got = mergeMessagesForSave(stored, nil)
	if len(got) != 2 {
		t.Errorf("no additions should mean no change, got %d", len(got))
	}
}

// The baseline is what tells a save which turns are its own.
func TestSessionBaselineTracksAppendedTurns(t *testing.T) {
	s := &Session{ID: "s1"}
	s.AddMessage(msg("user", "loaded-1"))
	s.AddMessage(msg("assistant", "loaded-2"))
	s.setBaseline(2)

	if got := s.baseline(); got != 2 {
		t.Errorf("baseline = %d, want 2", got)
	}
	if got := s.appendedSince(); len(got) != 0 {
		t.Errorf("nothing appended yet, got %+v", got)
	}

	s.AddMessage(msg("user", "new-1"))
	s.AddMessage(msg("assistant", "new-2"))

	appended := s.appendedSince()
	if len(appended) != 2 || appended[0].Content != "new-1" || appended[1].Content != "new-2" {
		t.Errorf("appendedSince = %+v, want the two new turns", appended)
	}

	// A nil session must not panic — saves run on whatever the caller holds.
	var nilSession *Session
	if nilSession.baseline() != 0 || nilSession.appendedSince() != nil {
		t.Error("a nil session should report nothing rather than panic")
	}
	nilSession.setBaseline(3) // must not panic
}
