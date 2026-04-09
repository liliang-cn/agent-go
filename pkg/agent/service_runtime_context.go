package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"golang.org/x/sync/errgroup"
)

type prepareConversationOptions struct {
	includeIntent bool
	emitProgress  bool
}

type preparedConversationContext struct {
	intent         *IntentRecognitionResult
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

	if opts.includeIntent {
		g.Go(func() error {
			intent, err := s.recognizeIntent(groupCtx, goal, session)
			if err != nil {
				return err
			}
			prepared.intent = intent
			return nil
		})
	}

	// Skip pre-injected RAG context in PTC mode so the runtime keeps explicit
	// `rag_query` capability reachable via callTool()/execute_javascript.
	if s.ragProcessor != nil && !s.isPTCEnabled() {
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
