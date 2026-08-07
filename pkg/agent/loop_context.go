// Context assembly for the loop — concept 4 of the seven.
//
// Everything the model sees before its own turn is built here: memory and RAG
// retrieval, task-scoped history filtering, the recent/older split, the skill
// reminder, and compaction. The loop (runtime.go) drives; this file decides
// what goes into the messages.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/prompt"
	"golang.org/x/sync/errgroup"
)

type prepareConversationOptions struct {
	includeIntent bool
	emitProgress  bool
}

type preparedConversationContext struct {
	ragContext     string
	memoryContext  string
	skillReminder  *skillReminder
	memoryMemories []*domain.MemoryWithScore
	memoryLogic    string
	queryContext   domain.MemoryQueryContext
	summary        string
	messages       []domain.Message
}

func (s *Service) prepareConversationContext(ctx context.Context, goal string, session *Session, opts prepareConversationOptions) preparedConversationContext {
	prepared := preparedConversationContext{
		queryContext: s.resolveMemoryQueryContext(session),
	}
	if session != nil {
		s.rememberMemoryQueryContext(session, prepared.queryContext)
		prepared.summary = resolveConversationSummary(session)
	}

	g, groupCtx := errgroup.WithContext(ctx)

	if s.ragProcessor != nil {
		g.Go(func() error {
			if opts.emitProgress {
				s.emitProgress("thinking", "🔍 Searching knowledge base...", 0, "")
			}
			ragContext, err := s.performRAGQuery(groupCtx, goal)
			if err == nil {
				prepared.ragContext = ragContext
				if opts.emitProgress && ragContext != "" {
					s.emitProgress("tool_result", fmt.Sprintf("✓ Found %d relevant documents", countDocuments(ragContext)), 0, "")
				}
			}
			return nil
		})
	}

	if s.memoryService != nil {
		g.Go(func() error {
			memoryContext, memoryMemories, memoryLogic, err := s.memoryService.RetrieveAndInjectWithContextAndLogic(groupCtx, goal, prepared.queryContext)
			if err != nil {
				return err
			}
			prepared.memoryContext = memoryContext
			prepared.memoryMemories = memoryMemories
			prepared.memoryLogic = memoryLogic
			return nil
		})
	}

	if s.skillsService != nil {
		g.Go(func() error {
			prepared.skillReminder = s.buildRelevantSkillReminder(groupCtx, goal, session)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		s.logger.Warn("conversation context collection partial failure", slog.Any("error", err))
	}

	prepared.messages = s.buildConversationMessages(session, goal, prepared.ragContext, prepared.memoryContext, prepared.skillReminder, prepared.summary)
	return prepared
}

// CompactMessages compacts the conversation history using LLM summarization.
// Returns a new message list with summarized content.
func (s *Service) CompactMessages(ctx context.Context, messages []domain.Message) ([]domain.Message, error) {
	if len(messages) == 0 {
		return messages, nil
	}
	discoveredTools := extractDiscoveredToolNames(messages, "")

	// Build conversation text for summarization (similar to CompactSession)
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
		compactPrompt = "You are a helpful assistant that summarizes long conversations. Your goal is to extract key points and important information from the conversation, keeping it concise but comprehensive."
	}

	// Build full prompt
	fullPrompt := fmt.Sprintf("%s\n\nConversation to summarize:\n%s\n\nPlease provide a concise summary of the key points:", compactPrompt, conversationText.String())

	// Generate summary using LLM
	summary, err := s.llmService.Generate(ctx, fullPrompt, nil)
	if err != nil {
		return nil, fmt.Errorf("compaction failed: %w", err)
	}

	// Rebuild messages with summary as the first user message
	compacted := []domain.Message{
		{Role: "user", Content: fmt.Sprintf("[Earlier conversation summarized: %s]", summary)},
	}
	if len(discoveredTools) > 0 {
		compacted = append(compacted, domain.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + appendDiscoveredToolsSnapshot("", discoveredTools) + "\n</system-reminder>",
		})
	}

	// Add recent messages (last 2 to maintain context)
	if len(messages) > 2 {
		compacted = append(compacted, messages[len(messages)-2:]...)
	}

	return compacted, nil
}

func appendToolResultsAnalysisPrompt(messages []domain.Message) []domain.Message {
	return append(messages, domain.Message{
		Role:    "user",
		Content: toolResultsAnalysisPrompt,
	})
}

// buildConversationMessages constructs the next-turn user message and prepends prior session history when available.
func buildSkillReminderMessage(session *Session, reminder *skillReminder) *domain.Message {
	if reminder == nil || strings.TrimSpace(reminder.Text) == "" {
		return nil
	}
	markRelevantSkillsSent(session, reminder.Names)
	return &domain.Message{
		Role:    "user",
		Content: "<system-reminder>\n" + strings.TrimSpace(reminder.Text) + "\n</system-reminder>",
		TaskID:  currentTaskID(session),
	}
}

func (s *Service) buildConversationMessages(session *Session, goal, ragContext, memoryContext string, skillReminder *skillReminder, summary string) []domain.Message {
	history := make([]domain.Message, 0)
	if session != nil {
		history = historyForTask(session.GetMessages(), currentTaskID(session))
	}

	olderMessages, recentMessages := splitConversationHistory(history, recentConversationWindow, olderConversationLimit)
	messages := make([]domain.Message, 0, len(olderMessages)+len(recentMessages)+4)
	if userCtxMsg := buildUserContextMetaMessage(s.buildUserContext()); userCtxMsg != nil {
		messages = append(messages, *userCtxMsg)
	}
	if skillMsg := buildSkillReminderMessage(session, skillReminder); skillMsg != nil {
		messages = append(messages, *skillMsg)
	}
	if contextMsg := buildConversationContextMessage(summary, memoryContext, ragContext); contextMsg != nil {
		messages = append(messages, *contextMsg)
	}
	messages = append(messages, olderMessages...)
	messages = append(messages, recentMessages...)
	messages = append(messages, withTaskID(domain.Message{Role: "user", Content: goal}, currentTaskID(session)))
	return messages
}

func historyForTask(history []domain.Message, taskID string) []domain.Message {
	if strings.TrimSpace(taskID) == "" || len(history) == 0 {
		return history
	}
	filtered := make([]domain.Message, 0, len(history))
	for _, msg := range history {
		if strings.TrimSpace(msg.TaskID) == taskID {
			filtered = append(filtered, msg)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func splitConversationHistory(history []domain.Message, recentWindow, olderLimit int) ([]domain.Message, []domain.Message) {
	if len(history) == 0 {
		return nil, nil
	}
	if recentWindow <= 0 {
		recentWindow = recentConversationWindow
	}
	if olderLimit < 0 {
		olderLimit = 0
	}

	if len(history) <= recentWindow {
		return nil, append([]domain.Message(nil), history...)
	}

	recentStart := len(history) - recentWindow
	recent := append([]domain.Message(nil), history[recentStart:]...)
	older := history[:recentStart]
	if olderLimit > 0 && len(older) > olderLimit {
		older = older[len(older)-olderLimit:]
	}
	return append([]domain.Message(nil), older...), recent
}

func buildConversationContextMessage(summary, memoryContext, ragContext string) *domain.Message {
	var sections []string

	if trimmed := strings.TrimSpace(summary); trimmed != "" {
		sections = append(sections, "--- Latest Summary / Key Info ---\n"+trimmed)
	}
	if trimmed := strings.TrimSpace(memoryContext); trimmed != "" {
		sections = append(sections, "--- Relevant Context From Memory ---\n"+trimmed)
	}
	if trimmed := strings.TrimSpace(ragContext); trimmed != "" {
		sections = append(sections, "--- Relevant Documents From Knowledge Base ---\n"+trimmed)
	}
	if len(sections) == 0 {
		return nil
	}

	content := strings.Join(sections, "\n\n") + "\n\nUse the context above when responding to the next user message."
	return &domain.Message{
		Role:    "user",
		Content: content,
		TaskID:  "",
	}
}

// appendToolRoundToMessages appends the assistant message and tool result messages.
func (s *Service) appendToolRoundToMessages(messages []domain.Message, taskID string, result *domain.GenerationResult, toolResults []ToolExecutionResult) []domain.Message {
	messages = append(messages, withTaskID(domain.Message{
		Role:             "assistant",
		Content:          result.Content,
		ReasoningContent: result.ReasoningContent,
		ToolCalls:        result.ToolCalls,
		ResponseID:       result.ID,
	}, taskID))
	for _, tr := range toolResults {
		res := tr.Result
		// Multimodal: a tool result carrying image data is split into an
		// image part (attached to a follow-up user message the model can
		// actually see) and a cleaned text result. No-op for text results.
		imageParts, res := extractToolImageParts(res)
		resStr := toolResultToString(res)
		messages = append(messages, withTaskID(domain.Message{
			Role:       "tool",
			Content:    resStr,
			ToolCallID: tr.ToolCallID,
		}, taskID))
		// Vision: surface any image the tool produced as a follow-up user
		// message with an image part, since providers reject images in
		// tool-role messages. Only runs when WithVision is enabled and the
		// result actually carried an image.
		if len(imageParts) > 0 {
			parts := append([]domain.MessagePart{
				domain.TextPart("Image output from tool " + tr.ToolName + ":"),
			}, imageParts...)
			messages = append(messages, withTaskID(domain.Message{
				Role:  "user",
				Parts: parts,
			}, taskID))
		}
	}
	return messages
}
