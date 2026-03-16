package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/pkg/domain"
)

func TestBuildConversationMessagesIncludesSessionHistory(t *testing.T) {
	session := NewSession("agent-1")
	session.AddMessage(domainMessage("user", "今天有什么新闻？"))
	session.AddMessage(domainMessage("assistant", "我已经给你做了一版摘要。"))

	svc := &Service{}
	messages := svc.buildConversationMessages(session, "筛一版", "", "", "")

	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "今天有什么新闻？" {
		t.Fatalf("unexpected first message: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "我已经给你做了一版摘要。" {
		t.Fatalf("unexpected second message: %+v", messages[1])
	}
	if messages[2].Role != "user" || messages[2].Content != "筛一版" {
		t.Fatalf("unexpected new turn message: %+v", messages[2])
	}
}

func TestBuildConversationMessagesUsesSummaryWhenHistoryEmpty(t *testing.T) {
	svc := &Service{}
	messages := svc.buildConversationMessages(NewSession("agent-1"), "继续", "", "", "之前讨论了今天新闻摘要。")

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" {
		t.Fatalf("unexpected role: %+v", messages[0])
	}
	if messages[0].Content == "继续" {
		t.Fatalf("expected summary context message to be prepended, got %q", messages[0].Content)
	}
	if messages[1].Role != "user" || messages[1].Content != "继续" {
		t.Fatalf("unexpected final user turn: %+v", messages[1])
	}
}

func TestBuildConversationMessagesUsesRecentWindowAndOlderChronologicalContext(t *testing.T) {
	session := NewSession("agent-1")
	for i := 0; i < 10; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		session.AddMessage(domainMessage(role, "msg-"+string(rune('A'+i))))
	}

	svc := &Service{}
	messages := svc.buildConversationMessages(session, "继续", "", "", "重点摘要")

	// context + 4 older chronological + 6 recent window + current user turn
	if len(messages) != 12 {
		t.Fatalf("expected 12 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[1].Content != "msg-A" || messages[4].Content != "msg-D" {
		t.Fatalf("unexpected older chronological layout: %+v", messages[:5])
	}
	if messages[5].Content != "msg-E" || messages[10].Content != "msg-J" {
		t.Fatalf("unexpected recent window layout: first recent=%+v last recent=%+v", messages[5], messages[10])
	}
	if messages[11].Content != "继续" {
		t.Fatalf("expected final user turn at the end, got %+v", messages[11])
	}
}

func TestBuildConversationMessagesCreatesSeparateContextMessage(t *testing.T) {
	svc := &Service{}
	messages := svc.buildConversationMessages(NewSession("agent-1"), "继续", "RAG 片段", "Memory 片段", "摘要")

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Content == "继续" {
		t.Fatalf("expected context message before user goal, got %+v", messages[0])
	}
	if messages[1].Content != "继续" {
		t.Fatalf("expected final user message to remain plain goal, got %+v", messages[1])
	}
}

func domainMessage(role, content string) domain.Message {
	return domain.Message{Role: role, Content: content}
}
