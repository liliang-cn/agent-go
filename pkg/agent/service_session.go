package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/prompt"
)

// GetSession retrieves a session by ID
func (s *Service) GetSession(sessionID string) (*Session, error) {
	return s.store.GetSession(sessionID)
}

// AppendSessionMessages writes messages into a session without running the
// model, creating the session if it does not exist yet.
//
// For the exchanges a host carries out on the model's behalf and around it: a
// question the person addressed to a different agent and that agent's reply,
// a notice the host posted, anything that happened in the conversation but not
// in a turn. Without this, such an exchange was visible on screen and absent
// from the record — the next turn could not see what the other agent had said,
// and reopening the conversation showed a hole where it had been.
//
// Messages are appended in order and saved once. The save merges rather than
// overwrites (see Store.SaveSession), so this is safe alongside a run that is
// writing to the same session.
func (s *Service) AppendSessionMessages(sessionID string, msgs ...domain.Message) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("append session messages: empty session id")
	}
	if len(msgs) == 0 {
		return nil
	}
	sess, err := s.store.GetSession(sessionID)
	if err != nil || sess == nil {
		agentID := ""
		if s.agent != nil {
			agentID = s.agent.Name()
		}
		sess = NewSessionWithID(sessionID, agentID)
	}
	for _, m := range msgs {
		sess.AddMessage(m)
	}
	return s.store.SaveSession(sess)
}

// GetPlan retrieves a plan by ID
func (s *Service) GetPlan(planID string) (*Plan, error) {
	return s.store.GetPlan(planID)
}

// ListSessions returns all sessions
func (s *Service) ListSessions(limit int) ([]*Session, error) {
	return s.store.ListSessions(limit)
}

// ListPlans returns plans for a session
func (s *Service) ListPlans(sessionID string, limit int) ([]*Plan, error) {
	return s.store.ListPlans(sessionID, limit)
}

// Chat sends a message with auto-generated session UUID.
// This is the simplest API for conversational AI with memory.
//
// Example:
//
//	svc, _ := agent.New(&agent.AgentConfig{Name: "assistant"})
//	result, _ := svc.Chat(ctx, "My name is Alice")
//	result, _ = svc.Chat(ctx, "What's my name?") // Will remember "Alice"
//
// Chat sends a message and returns the full ExecutionResult (with session, sources, PTC details).
// For simple text extraction, call result.Text() on the returned value.
//
// Example:
//
//	result, err := svc.Chat(ctx, "My name is Alice")
//	fmt.Println(result.Text()) // "Hi Alice! How can I help you?"
func (s *Service) Chat(ctx context.Context, message string) (*ExecutionResult, error) {
	s.sessionMu.Lock()
	if s.currentSessionID == "" {
		s.currentSessionID = uuid.New().String()
	}
	sessionID := s.currentSessionID
	s.sessionMu.Unlock()

	return s.Run(ctx, message, WithSessionID(sessionID))
}

// Ask sends a one-off message and returns the agent's reply as a plain string.
// It is the simplest possible API for library integrations — no session management,
// no rich result struct. Each call is independent (no conversation history).
//
//	reply, err := svc.Ask(ctx, "What is the capital of France?")
//	// reply == "The capital of France is Paris."
//
// For multi-turn conversations with memory, use Chat() instead.
func (s *Service) Ask(ctx context.Context, message string) (string, error) {
	result, err := s.Run(ctx, message)
	if err != nil {
		return "", err
	}
	return result.Text(), result.Err()
}

// Stream sends a message and streams the agent's reply token-by-token as a <-chan string.
// The channel is closed when the response is complete. Errors are swallowed; for
// full event details (tool calls, sources, errors) use RunStream() instead.
//
//	for token := range svc.Stream(ctx, "Write a poem") {
//	    fmt.Print(token)
//	}
//
// For multi-turn conversations, use ChatStream() instead.
func (s *Service) Stream(ctx context.Context, message string) <-chan string {
	ch := make(chan string, 32)
	events, err := s.RunStream(ctx, message)
	if err != nil {
		close(ch)
		return ch
	}
	go func() {
		defer close(ch)
		for evt := range events {
			if evt.Type == EventTypePartial && evt.Content != "" {
				select {
				case ch <- evt.Content:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

// CurrentSessionID returns the current session UUID used by Chat()
func (s *Service) CurrentSessionID() string {
	s.sessionMu.RLock()
	defer s.sessionMu.RUnlock()
	return s.currentSessionID
}

// SetSessionID sets a specific session ID for Chat() to use
func (s *Service) SetSessionID(sessionID string) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	s.currentSessionID = sessionID
}

// ResetSession clears the current session and starts a new one with a new UUID
func (s *Service) ResetSession() {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	s.currentSessionID = uuid.New().String()
}

// CompactSession summarizes a session into key points using LLM
func (s *Service) CompactSession(ctx context.Context, sessionID string) (string, error) {
	// Load session
	session, err := s.store.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to load session: %w", err)
	}

	messages := session.GetMessages()
	if len(messages) == 0 {
		return "", nil
	}
	taskID := currentTaskID(session)
	if taskID != "" {
		if filtered := historyForTask(messages, taskID); len(filtered) > 0 {
			messages = filtered
		}
	}
	discoveredTools := extractDiscoveredToolNames(messages, resolveConversationSummary(session))

	// Build conversation text for summarization
	var conversationText strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			conversationText.WriteString(fmt.Sprintf("User: %s\n", stripDiscoveredToolsTag(msg.Content)))
		case "assistant":
			conversationText.WriteString(fmt.Sprintf("Responder: %s\n", msg.Content))
		}
	}

	// Get compact prompt template
	compactPrompt := s.promptManager.Get(prompt.LLMCompact)
	if compactPrompt == "" {
		compactPrompt = "You are a helpful assistant that summarizes long conversations. Your goal is to extract key points and important information from the conversation, keeping it concise but comprehensive. Focus on what was discussed, what decisions were made, and any important context that should be preserved."
	}

	// Build full prompt
	fullPrompt := fmt.Sprintf("%s\n\nConversation to summarize:\n%s\n\nPlease provide a concise summary of the key points:", compactPrompt, conversationText.String())

	// Generate summary using LLM
	summary, err := s.llmService.Generate(ctx, fullPrompt, nil)
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	// Update session
	summary = appendDiscoveredToolsSnapshot(summary, discoveredTools)
	if taskID != "" {
		setTaskSummary(session, taskID, summary)
	} else {
		session.SetSummary(summary)
	}
	if err := s.store.SaveSession(session); err != nil {
		return "", fmt.Errorf("failed to save session summary: %w", err)
	}

	return resolveConversationSummary(session), nil
}
