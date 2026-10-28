package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
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

	got := mergeMessagesForSave(stored, 0, appended)
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
	got := mergeMessagesForSave(stored, 0, []domain.Message{msg("user", "Q"), msg("assistant", "A")})
	if len(got) != 2 {
		t.Errorf("re-saving the same turns should change nothing, got %d: %+v", len(got), got)
	}

	// Same text under a different tool_call_id is a different turn.
	a := domain.Message{Role: "tool", Content: "ok", ToolCallID: "call_1"}
	b := domain.Message{Role: "tool", Content: "ok", ToolCallID: "call_2"}
	got = mergeMessagesForSave([]domain.Message{a}, 0, []domain.Message{b})
	if len(got) != 2 {
		t.Errorf("distinct tool_call_ids must both survive, got %d", len(got))
	}

	// Nothing appended: the stored history is returned untouched.
	got = mergeMessagesForSave(stored, 0, nil)
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
	if nilSession.appendedSince() != nil {
		t.Error("a nil session should report nothing rather than panic")
	}
	nilSession.setBaseline(3) // must not panic
}

// Saying the same thing twice in one conversation is two turns. Deduplicating
// plain text against the whole history dropped the second "好的", and the
// clock line each turn sends: two turns in the same minute carry the same
// one, and losing the second broke the next turn's cached prefix. Only what
// was stored after this copy was loaded can be a retried save of its own.
func TestRepeatingEarlierWordsIsKept(t *testing.T) {
	stored := []domain.Message{msg("user", "好的"), msg("assistant", "A1")}
	got := mergeMessagesForSave(stored, 2, []domain.Message{msg("user", "好的"), msg("assistant", "A2")})
	if len(got) != 4 || got[2].Content != "好的" {
		t.Fatalf("merged = %+v, want the second 好的 kept", got)
	}
	// A tool result is still one per call, wherever it already is.
	tool := domain.Message{Role: "tool", Content: "ok", ToolCallID: "call_1"}
	got = mergeMessagesForSave([]domain.Message{tool}, 1, []domain.Message{tool})
	if len(got) != 1 {
		t.Fatalf("a tool result was stored twice: %+v", got)
	}
}
