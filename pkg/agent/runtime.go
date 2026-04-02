package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	memorypkg "github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/usage"
)

type backgroundMemoryWriter interface {
	EnqueueStoreIfWorthwhile(req *domain.MemoryStoreRequest) bool
}

// errTaskComplete is a sentinel returned from the StreamWithTools callback to
// stop streaming as soon as task_complete is detected. It is NOT a real error.
var errTaskComplete = errors.New("task_complete signalled")

// Runtime orchestrates the event loop for agent execution
type Runtime struct {
	svc          *Service
	eventChan    chan *Event
	currentAgent *Agent
	session      *Session
	cfg          *RunConfig
	sources      []domain.Chunk // Collect RAG sources during execution
}

// NewRuntime creates a new runtime instance
func NewRuntime(svc *Service, session *Session, cfg *RunConfig) *Runtime {
	return &Runtime{
		svc:          svc,
		eventChan:    make(chan *Event, 100), // Buffer events
		currentAgent: svc.resolveCurrentAgent(session),
		session:      session,
		cfg:          cfg,
	}
}

// RunStream starts the event loop and returns a read-only channel of events
func (r *Runtime) RunStream(ctx context.Context, goal string) <-chan *Event {
	go r.loop(ctx, goal)
	return r.eventChan
}

// loop is the core event loop
func (r *Runtime) loop(ctx context.Context, goal string) {
	defer func() {
		close(r.eventChan)
	}()

	r.emit(EventTypeStart, fmt.Sprintf("Starting task: %s", goal))

	// --- DEBUG: LOG AGENT CONFIGURATION ---
	if r.debugEnabled() {
		var sb strings.Builder
		info := r.svc.Info()
		fmt.Fprintf(&sb, "AGENT:    %s (%s)\n", info.Name, info.ID)
		fmt.Fprintf(&sb, "MODEL:    %s\n", info.Model)
		fmt.Fprintf(&sb, "BASEURL:  %s\n", info.BaseURL)
		fmt.Fprintf(&sb, "FEATURES: RAG:%v, MCP:%v, Skills:%v, PTC:%v, Memory:%v\n",
			info.RAGEnabled, info.MCPEnabled, info.SkillsEnabled, info.PTCEnabled, info.MemoryEnabled)
		r.emitDebug(0, "config", sb.String())
	}

	// 1. Prepare context (Memory & RAG) — with a timeout so a slow embedding
	// model or unreachable LLM doesn't block the entire run forever.
	prepCtx, prepCancel := context.WithTimeout(ctx, 30*time.Second)
	defer prepCancel()
	prepared := r.svc.prepareConversationContext(prepCtx, goal, r.session, prepareConversationOptions{includeIntent: true})

	// 2. Build initial messages using the same layered assembly strategy as the non-streaming path.
	const maxRounds = 20
	state := newQueryLoopState(goal, prepared.messages, prepared.intent, maxRounds)
	state.setStage(TurnStagePreparingContext, "starting turn setup", 0)
	r.emitLoopState(state)

	messages := state.Messages
	// Ensure the current user message is in the session before starting
	r.session.AddMessage(domain.Message{Role: "user", Content: goal})
	for round := 0; round < maxRounds; round++ {
		// Check cancellation
		if ctx.Err() != nil {
			r.emit(EventTypeError, "Execution cancelled")
			return
		}

		state.beginRound()
		r.emit(EventTypeThinking, "Thinking...")
		state.setStage(TurnStageAwaitingModel, "requesting model output", 0)
		r.emitLoopState(state)

		// 3. Build model inputs for CURRENT agent
		tools, genMessages := r.svc.prepareTurnInputs(ctx, r.currentAgent, messages, goal)

		// --- DEBUG: LOG FULL PROMPT + TOOLS ---
		if r.debugEnabled() {
			var promptBuilder strings.Builder
			info := r.svc.Info()
			fmt.Fprintf(&promptBuilder, "MODEL: %s (%s)\n", info.Model, info.BaseURL)
			if sections := formatSystemPromptSectionsForDebug(r.svc.buildSystemPromptSections(ctx, r.currentAgent, systemPromptOptions{includePTC: r.svc.ptcIntegration != nil})); sections != "" {
				fmt.Fprintf(&promptBuilder, "%s\n\n", sections)
			}
			// Token estimation
			tc := usage.NewTokenCounter()
			promptTokens := tc.EstimateConversationTokens(toUsageMessages(genMessages), info.Model)
			// Tools list
			fmt.Fprintf(&promptBuilder, "=== TOOLS (%d) ===\n", len(tools))
			for _, t := range tools {
				fmt.Fprintf(&promptBuilder, "  • %s: %s\n", t.Function.Name, t.Function.Description)
			}
			fmt.Fprintf(&promptBuilder, "\n=== MESSAGES (%d) ===\n", len(genMessages))
			for _, m := range genMessages {
				fmt.Fprintf(&promptBuilder, "[%s]:\n%s\n", strings.ToUpper(m.Role), m.Content)
			}
			fmt.Fprintf(&promptBuilder, "\n=== TOKENS ===\n")
			fmt.Fprintf(&promptBuilder, "Prompt tokens (est.): %d\n", promptTokens)
			r.emitDebug(round+1, "prompt", promptBuilder.String())
		}

		// 5. LLM Call (Streaming)
		toolCallDetected := false
		// taskCompleteTriggered signals task_complete was detected mid-stream.
		// We break out of StreamWithTools early by returning an error from the
		// callback; the runtime then checks this flag to avoid treating it as a
		// real error.
		var taskCompleteResult string
		taskCompleteTriggered := false

		result, lastResponseID, err := r.svc.streamToolTurn(ctx, genMessages, tools, r.svc.toolGenerationOptions(0.3, 2000, toolChoiceForIntent(state.Intent, round)), StreamTurnCallbacks{
			OnToolCall: func(tc domain.ToolCall) error {
				if tc.Function.Name == "task_complete" {
					r.emitToolCall(tc.Function.Name, tc.Function.Arguments, "")
					if res, ok := tc.Function.Arguments["result"].(string); ok && res != "" {
						taskCompleteResult = res
					}
					taskCompleteTriggered = true
					return errTaskComplete
				}
				return nil
			},
			OnReasoning: func(text string) {
				r.emit(EventTypeThinking, text)
			},
			OnPartial: func(text string) {
				r.emit(EventTypePartial, text)
			},
			OnFirstToolCall: func() {
				if !toolCallDetected {
					r.emit(EventTypeThinking, "Planning tool usage...")
					toolCallDetected = true
				}
			},
		})

		// task_complete detected in stream — terminate immediately.
		if taskCompleteTriggered {
			final := taskCompleteResult
			if final == "" && result != nil {
				final = result.Content
			}
			if r.debugEnabled() {
				var respBuilder strings.Builder
				if result != nil {
					fmt.Fprintf(&respBuilder, "CONTENT: %s\n", result.Content)
				} else {
					fmt.Fprintf(&respBuilder, "CONTENT: \n")
				}
				if result != nil && len(result.ToolCalls) > 0 {
					fmt.Fprintf(&respBuilder, "TOOL CALLS:\n")
					for _, tc := range result.ToolCalls {
						fmt.Fprintf(&respBuilder, "  - %s(%v)\n", tc.Function.Name, tc.Function.Arguments)
					}
				}
				r.emitDebug(round+1, "response", respBuilder.String())
			}
			r.emitToolResult("task_complete", final, nil, "")
			r.completeRun(goal, final, messages, true)
			return
		}

		if err != nil {
			r.emit(EventTypeError, fmt.Sprintf("LLM error: %v", err))
			return
		}
		state.noteResponse(lastResponseID)

		// --- DEBUG: LOG LLM RESPONSE ---
		if r.debugEnabled() {
			var respBuilder strings.Builder
			info := r.svc.Info()
			tc := usage.NewTokenCounter()
			respTokens := tc.EstimateTokens(result.Content, info.Model)
			fmt.Fprintf(&respBuilder, "CONTENT: %s\n", result.Content)
			if len(result.ToolCalls) > 0 {
				fmt.Fprintf(&respBuilder, "TOOL CALLS:\n")
				for _, tc := range result.ToolCalls {
					fmt.Fprintf(&respBuilder, "  - %s(%v)\n", tc.Function.Name, tc.Function.Arguments)
				}
			}
			fmt.Fprintf(&respBuilder, "\n=== MESSAGES IN HISTORY (%d) ===\n", len(messages))
			for i, m := range messages {
				fmt.Fprintf(&respBuilder, " [%d] %s: %s\n", i, strings.ToUpper(m.Role), m.Content)
			}
			fmt.Fprintf(&respBuilder, "\n=== TOKENS ===\n")
			fmt.Fprintf(&respBuilder, "Response tokens (est.): %d\n", respTokens)
			r.emitDebug(round+1, "response", respBuilder.String())
		}

		// 6. Handle Result
		if len(result.ToolCalls) > 0 {
			// Double check for task_complete in case it was not intercepted during stream
			for _, tc := range result.ToolCalls {
				if tc.Function.Name == "task_complete" {
					final := result.Content
					if res, ok := tc.Function.Arguments["result"].(string); ok && res != "" {
						final = res
					}
					r.completeRun(goal, final, nil, false)
					return
				}
			}

			// Note: task_complete is intercepted at stream level above and never
			// reaches this point. All remaining tool calls are real work items.

			result.ToolCalls = r.overridePTCToolCallsFromContent(round, result.Content, result.ToolCalls)
			result.ToolCalls = normalizeToolCalls(result.ToolCalls)
			streamResult := &domain.GenerationResult{
				ID:        lastResponseID,
				Content:   result.Content,
				ToolCalls: result.ToolCalls,
			}
			nextAgent, filteredToolCalls, duplicateToolResults, fallback, handoff := r.svc.prepareToolRound(ctx, &messages, r.currentAgent, r.session, streamResult, state.PrevToolCalls, round)
			if handoff {
				r.currentAgent = nextAgent
				state.Transition = "handoff"
				state.TransitionReason = "agent handoff requested"
				r.emit(EventTypeHandoff, fmt.Sprintf("Transferred to %s", r.currentAgent.Name()))
				state.noteRoundCompleted()
				continue
			}
			if fallback != "" {
				r.completeRun(goal, fallback, nil, false)
				return
			}
			if len(filteredToolCalls) == 0 {
				if len(duplicateToolResults) > 0 {
					messages = r.svc.appendToolRoundToMessages(messages, streamResult, duplicateToolResults)
					state.Messages = messages
					state.recordToolResults(duplicateToolResults)
				}
				state.noteRoundCompleted()
				continue
			}
			streamResult.ToolCalls = filteredToolCalls
			state.setStage(TurnStageHandlingTools, "executing tool batch", len(filteredToolCalls))
			r.emitLoopState(state)

			toolResults, err := r.svc.executeToolCallsWithOptions(ctx, r.currentAgent, r.session, filteredToolCalls, ToolExecutionCallbacks{
				OnToolCall: func(name string, args map[string]interface{}, interruptBehavior string) {
					r.emitToolCall(name, args, interruptBehavior)
				},
				OnToolResult: func(name string, result interface{}, err error, interruptBehavior string) {
					r.emitToolResult(name, result, err, interruptBehavior)
				},
				OnToolState: func(name string, state string, interruptBehavior string) {
					r.emitToolState(name, state, interruptBehavior)
				},
				EventSink: r.forwardSubAgentEvent,
				Debug:     r.debugEnabled(),
			}, true)
			if err != nil {
				r.emit(EventTypeError, fmt.Sprintf("Tool execution error: %v", err))
				return
			}
			messages = r.svc.appendToolRoundToMessages(messages, streamResult, append(duplicateToolResults, toolResults...))
			state.Messages = messages
			state.recordToolResults(append(duplicateToolResults, toolResults...))

			// In non-PTC mode, encourage the model to process results and move towards completion
			isPTCToolRound := r.svc.isPTCEnabled() && len(filteredToolCalls) == 1 && filteredToolCalls[0].Function.Name == "execute_javascript"
			if !isPTCToolRound {
				state.setStage(TurnStageAwaitingAnswer, "waiting for final answer after tool results", len(filteredToolCalls))
				r.emitLoopState(state)
				messages = append(messages, domain.Message{
					Role:    "user",
					Content: "Analyze the tool results above. If you have fulfilled the user's request, provide your final answer and call task_complete. Otherwise, continue with the necessary next steps.",
				})
				state.Messages = messages
			}

		} else {
			if nextMessages, handled := r.handlePTCTextFallback(ctx, result.Content, messages); handled {
				messages = nextMessages
				state.Messages = messages
				state.noteRoundCompleted()
				continue // next round → LLM synthesises answer
			}

			r.completeRun(goal, result.Content, messages, true)
			return
		}
		state.noteRoundCompleted()
	}
}

// executeToolOrHandoff executes a tool call and handles agent switching
func (r *Runtime) executeToolOrHandoff(ctx context.Context, tc domain.ToolCall) (interface{}, error, bool) {
	ctx = withEventSink(ctx, r.forwardSubAgentEvent)
	ctx = withRunDebug(ctx, r.debugEnabled())
	ctx = withCurrentSession(ctx, r.session)
	return r.svc.executeDirectToolCall(ctx, r.currentAgent, r.session, tc, DirectToolExecutionOptions{
		OnHandoff: func(targetAgent *Agent, reason interface{}) {
			r.emit(EventTypeHandoff, fmt.Sprintf("Transferring to %s: %v", targetAgent.Name(), reason))
			r.currentAgent = targetAgent
		},
	})
}

func (r *Runtime) saveToMemory(ctx context.Context, goal, result string) {
	if r.svc.memoryService != nil {
		queryContext := r.svc.resolveMemoryQueryContext(r.session)
		intent := &IntentRecognitionResult{}
		if r.svc.planner != nil {
			intent = r.svc.planner.fallbackIntentRecognition(goal)
		}
		if isExplicitMemorySaveIntent(goal, intent) && !r.svc.hasRunMemorySaved() {
			content := extractExplicitMemorySaveContent(goal)
			if strings.TrimSpace(content) == "" {
				content = goal
			}

			scope := memorypkg.AgentScope(queryContext.AgentID)
			if scope.ID == "" && queryContext.SessionID != "" {
				scope = memorypkg.SessionScope(queryContext.SessionID)
			}

			memType := domain.MemoryTypeFact
			goalLower := strings.ToLower(goal)
			if strings.HasPrefix(goalLower, "my favorite") ||
				strings.HasPrefix(goalLower, "i prefer") ||
				strings.Contains(goalLower, "preference is") {
				memType = domain.MemoryTypePreference
			}

			if err := r.svc.memoryService.Add(ctx, &domain.Memory{
				Type:       memType,
				SessionID:  memorypkg.ToBankID(scope),
				ScopeType:  scope.Type,
				ScopeID:    scope.ID,
				Content:    content,
				Importance: 0.8,
				Metadata: map[string]interface{}{
					"source": "user_direct",
				},
			}); err != nil {
				r.svc.logger.Warn("failed to store explicit memory after stream run", slog.String("error", err.Error()))
			} else {
				log.Printf("[Agent] Stored to memory: %s", content)
			}
		}
		req := &domain.MemoryStoreRequest{
			SessionID:  r.session.GetID(),
			AgentID:    queryContext.AgentID,
			TeamID:     queryContext.TeamID,
			UserID:     queryContext.UserID,
			TaskGoal:   goal,
			TaskResult: result,
		}
		if writer, ok := r.svc.memoryService.(backgroundMemoryWriter); ok && writer.EnqueueStoreIfWorthwhile(req) {
			// queued to background durable-memory worker
		} else if err := r.svc.memoryService.StoreIfWorthwhile(ctx, req); err != nil {
			r.svc.logger.Warn("failed to store memory after run", slog.String("error", err.Error()))
		}
	}
}

func (r *Runtime) collectAllSources() []domain.Chunk {
	allSources := append([]domain.Chunk(nil), r.sources...)
	r.svc.ragSourcesMu.RLock()
	if len(r.svc.ragSources) > 0 {
		allSources = append(allSources, r.svc.ragSources...)
	}
	r.svc.ragSourcesMu.RUnlock()
	return allSources
}

func (r *Runtime) clearCollectedSources() {
	r.svc.ragSourcesMu.Lock()
	r.svc.ragSources = nil
	r.svc.ragSourcesMu.Unlock()
}

func (r *Runtime) persistMessages(messages []domain.Message) {
	if len(messages) == 0 {
		return
	}
	for _, msg := range messages {
		r.session.AddMessage(msg)
	}
	if err := r.svc.store.SaveSession(r.session); err != nil {
		r.svc.logger.Warn("failed to save session history", slog.String("error", err.Error()))
	}
}

func (r *Runtime) completeRun(goal, content string, messages []domain.Message, persistHistory bool) {
	r.emitTurnState(TurnStageCompleted, "run completed", 0, 0, nil)
	r.eventChan <- &Event{
		ID:        uuid.New().String(),
		Type:      EventTypeComplete,
		AgentName: r.currentAgent.Name(),
		AgentID:   r.currentAgent.ID(),
		Content:   content,
		Sources:   r.collectAllSources(),
		Timestamp: time.Now(),
	}
	if persistHistory {
		r.persistMessages(messages)
	}
	r.clearCollectedSources()
	go r.saveToMemory(context.Background(), goal, content)
}

func (r *Runtime) emitLoopState(state *queryLoopState) {
	if state == nil {
		return
	}
	stateDelta := map[string]interface{}{
		"turn_stage":         state.Stage,
		"transition_reason":  state.TransitionReason,
		"transition":         state.Transition,
		"round":              state.CurrentRound,
		"tool_call_count":    state.PendingToolCount,
		"total_tool_calls":   state.TotalToolCalls,
		"interruptible":      !r.svc.hasBlockingToolInProgress(),
		"budget_max_rounds":  state.Budget.MaxRounds,
		"budget_used_rounds": state.Budget.CompletedRounds,
		"budget_remaining":   state.Budget.RemainingRounds,
		"budget_tokens":      state.Budget.EstimatedTokens,
		"compaction_count":   state.Budget.CompactionCount,
		"recovery_count":     state.Budget.RecoveryCount,
	}
	if state.Intent != nil {
		stateDelta["intent_type"] = state.Intent.IntentType
		stateDelta["preferred_agent"] = state.Intent.PreferredAgent
		stateDelta["requires_tools"] = state.Intent.RequiresTools
	}
	r.eventChan <- &Event{
		ID:         uuid.New().String(),
		Type:       EventTypeStateUpdate,
		AgentName:  r.currentAgent.Name(),
		AgentID:    r.currentAgent.ID(),
		Content:    state.TransitionReason,
		StateDelta: stateDelta,
		Timestamp:  time.Now(),
	}
}

func (r *Runtime) emitTurnState(stage, reason string, round int, toolCount int, intent *IntentRecognitionResult) {
	stateDelta := map[string]interface{}{
		"turn_stage":        stage,
		"transition_reason": reason,
		"round":             round,
		"tool_call_count":   toolCount,
		"interruptible":     !r.svc.hasBlockingToolInProgress(),
	}
	if intent != nil {
		stateDelta["intent_type"] = intent.IntentType
		stateDelta["preferred_agent"] = intent.PreferredAgent
		stateDelta["requires_tools"] = intent.RequiresTools
		stateDelta["transition"] = intent.Transition
	}
	r.eventChan <- &Event{
		ID:         uuid.New().String(),
		Type:       EventTypeStateUpdate,
		AgentName:  r.currentAgent.Name(),
		AgentID:    r.currentAgent.ID(),
		Content:    reason,
		StateDelta: stateDelta,
		Timestamp:  time.Now(),
	}
}

// Helpers to emit events
func (r *Runtime) emit(t EventType, content string) {
	r.eventChan <- &Event{
		ID:        uuid.New().String(),
		Type:      t,
		AgentName: r.currentAgent.Name(),
		AgentID:   r.currentAgent.ID(),
		Content:   content,
		Timestamp: time.Now(),
	}
}

func (r *Runtime) emitToolCall(name string, args map[string]interface{}, interruptBehavior string) {
	r.eventChan <- &Event{
		ID:        uuid.New().String(),
		Type:      EventTypeToolCall,
		AgentName: r.currentAgent.Name(),
		ToolName:  name,
		ToolArgs:  args,
		Timestamp: time.Now(),
	}
	if interruptBehavior == InterruptBehaviorBlock {
		blockingCount := r.svc.blockingToolCount()
		r.eventChan <- &Event{
			ID:        uuid.New().String(),
			Type:      EventTypeStateUpdate,
			AgentName: r.currentAgent.Name(),
			AgentID:   r.currentAgent.ID(),
			Content:   fmt.Sprintf("Running non-interruptible tool: %s (%d blocking)", name, blockingCount),
			StateDelta: map[string]interface{}{
				"interruptible":       false,
				"active_tool":         name,
				"interrupt_behavior":  interruptBehavior,
				"blocking_tool_count": blockingCount,
			},
			Timestamp: time.Now(),
		}
	}
}

func (r *Runtime) emitToolResult(name string, res interface{}, err error, interruptBehavior string) {
	evt := &Event{
		ID:         uuid.New().String(),
		Type:       EventTypeToolResult,
		AgentName:  r.currentAgent.Name(),
		ToolName:   name,
		ToolResult: res,
		Timestamp:  time.Now(),
	}
	if err != nil {
		// You might want a specific error event or just include error in content
		evt.Content = err.Error()
	}
	r.eventChan <- evt
	if interruptBehavior == InterruptBehaviorBlock {
		blockingCount := r.svc.blockingToolCount()
		r.eventChan <- &Event{
			ID:        uuid.New().String(),
			Type:      EventTypeStateUpdate,
			AgentName: r.currentAgent.Name(),
			AgentID:   r.currentAgent.ID(),
			Content:   fmt.Sprintf("Tool finished: %s (%d blocking remaining)", name, blockingCount),
			StateDelta: map[string]interface{}{
				"interruptible":       blockingCount == 0,
				"active_tool":         name,
				"interrupt_behavior":  interruptBehavior,
				"blocking_tool_count": blockingCount,
			},
			Timestamp: time.Now(),
		}
	}
}

func (r *Runtime) emitToolState(name string, state string, interruptBehavior string) {
	r.eventChan <- &Event{
		ID:        uuid.New().String(),
		Type:      EventTypeStateUpdate,
		AgentName: r.currentAgent.Name(),
		AgentID:   r.currentAgent.ID(),
		Content:   fmt.Sprintf("Tool %s is %s", name, state),
		StateDelta: map[string]interface{}{
			"tool_name":          name,
			"tool_state":         state,
			"interrupt_behavior": interruptBehavior,
			"interruptible":      !r.svc.hasBlockingToolInProgress(),
		},
		Timestamp: time.Now(),
	}
}

func (r *Runtime) emitDebug(round int, debugType string, content string) {
	r.eventChan <- &Event{
		ID:        uuid.New().String(),
		Type:      EventTypeDebug,
		AgentName: r.currentAgent.Name(),
		Round:     round,
		DebugType: debugType,
		Content:   content,
		Timestamp: time.Now(),
	}
}

func (r *Runtime) debugEnabled() bool {
	return r.svc.debug || (r.cfg != nil && r.cfg.Debug)
}

// toUsageMessages converts domain messages to usage messages for token counting.
func toUsageMessages(messages []domain.Message) []usage.Message {
	result := make([]usage.Message, len(messages))
	for i, m := range messages {
		result[i] = usage.Message{
			Role:    m.Role,
			Content: m.Content,
		}
	}
	return result
}
