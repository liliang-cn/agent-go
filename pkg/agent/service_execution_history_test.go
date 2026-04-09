package agent

import (
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestBuildConversationMessagesIncludesSessionHistory(t *testing.T) {
	session := NewSession("agent-1")
	session.AddMessage(domainMessage("user", "今天有什么新闻？"))
	session.AddMessage(domainMessage("assistant", "我已经给你做了一版摘要。"))

	svc := &Service{}
	messages := svc.buildConversationMessages(session, "筛一版", "", "", nil, "")

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || !containsText(messages[0].Content, "<system-reminder>") {
		t.Fatalf("unexpected first message: %+v", messages[0])
	}
	if messages[1].Role != "user" || messages[1].Content != "今天有什么新闻？" {
		t.Fatalf("unexpected second message: %+v", messages[1])
	}
	if messages[2].Role != "assistant" || messages[2].Content != "我已经给你做了一版摘要。" {
		t.Fatalf("unexpected third message: %+v", messages[2])
	}
	if messages[3].Role != "user" || messages[3].Content != "筛一版" {
		t.Fatalf("unexpected new turn message: %+v", messages[3])
	}
}

func TestBuildConversationMessagesUsesSummaryWhenHistoryEmpty(t *testing.T) {
	svc := &Service{}
	messages := svc.buildConversationMessages(NewSession("agent-1"), "继续", "", "", nil, "之前讨论了今天新闻摘要。")

	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || !containsText(messages[0].Content, "<system-reminder>") {
		t.Fatalf("unexpected role: %+v", messages[0])
	}
	if messages[1].Content == "继续" {
		t.Fatalf("expected summary context message to be prepended, got %q", messages[1].Content)
	}
	if messages[2].Role != "user" || messages[2].Content != "继续" {
		t.Fatalf("unexpected final user turn: %+v", messages[2])
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
	messages := svc.buildConversationMessages(session, "继续", "", "", nil, "重点摘要")

	// user meta context + summary context + 4 older chronological + 6 recent window + current user turn
	if len(messages) != 13 {
		t.Fatalf("expected 13 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || !containsText(messages[0].Content, "<system-reminder>") || messages[2].Content != "msg-A" || messages[5].Content != "msg-D" {
		t.Fatalf("unexpected older chronological layout: %+v", messages[:6])
	}
	if messages[6].Content != "msg-E" || messages[11].Content != "msg-J" {
		t.Fatalf("unexpected recent window layout: first recent=%+v last recent=%+v", messages[6], messages[11])
	}
	if messages[12].Content != "继续" {
		t.Fatalf("expected final user turn at the end, got %+v", messages[12])
	}
}

func TestBuildConversationMessagesCreatesSeparateContextMessage(t *testing.T) {
	svc := &Service{}
	messages := svc.buildConversationMessages(NewSession("agent-1"), "继续", "RAG 片段", "Memory 片段", nil, "摘要")

	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if !containsText(messages[0].Content, "<system-reminder>") {
		t.Fatalf("expected first message to be user context meta message, got %+v", messages[0])
	}
	if messages[1].Content == "继续" {
		t.Fatalf("expected context message before user goal, got %+v", messages[1])
	}
	if messages[2].Content != "继续" {
		t.Fatalf("expected final user message to remain plain goal, got %+v", messages[2])
	}
}

func TestBuildConversationMessagesFiltersHistoryByTaskID(t *testing.T) {
	session := NewSession("agent-1")
	session.SetContext(sessionContextTaskID, "task-2")
	session.AddMessage(domainMessageWithTask("user", "task-1-user", "task-1"))
	session.AddMessage(domainMessageWithTask("assistant", "task-1-assistant", "task-1"))
	session.AddMessage(domainMessageWithTask("user", "task-2-user", "task-2"))
	session.AddMessage(domainMessageWithTask("assistant", "task-2-assistant", "task-2"))

	svc := &Service{}
	messages := svc.buildConversationMessages(session, "继续", "", "", nil, "")

	joined := ""
	for _, msg := range messages {
		joined += msg.Content + "\n"
	}
	if strings.Contains(joined, "task-1-user") || strings.Contains(joined, "task-1-assistant") {
		t.Fatalf("expected task-1 history to be excluded, got %q", joined)
	}
	if !strings.Contains(joined, "task-2-user") || !strings.Contains(joined, "task-2-assistant") {
		t.Fatalf("expected task-2 history to remain, got %q", joined)
	}
}

func TestResolveConversationSummaryPrefersTaskSummary(t *testing.T) {
	session := NewSession("agent-1")
	session.SetSummary("global-summary")
	session.SetContext(sessionContextTaskID, "task-2")
	setTaskSummary(session, "task-2", "task-summary")

	if got := resolveConversationSummary(session); got != "task-summary" {
		t.Fatalf("resolveConversationSummary() = %q, want %q", got, "task-summary")
	}
}

func domainMessage(role, content string) domain.Message {
	return domain.Message{Role: role, Content: content}
}

func domainMessageWithTask(role, content, taskID string) domain.Message {
	return domain.Message{Role: role, Content: content, TaskID: taskID}
}

func containsText(text, needle string) bool {
	return strings.Contains(text, needle)
}
