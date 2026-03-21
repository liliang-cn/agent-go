package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
	"github.com/liliang-cn/agent-go/v2/pkg/usage"
	"golang.org/x/sync/errgroup"
)

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
	// Determine initial agent
	currentAgent := svc.agent
	if session.AgentID != "" && svc.registry != nil {
		if a, ok := svc.registry.GetAgent(session.AgentID); ok {
			currentAgent = a
		}
	}

	return &Runtime{
		svc:          svc,
		eventChan:    make(chan *Event, 100), // Buffer events
		currentAgent: currentAgent,
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
	memoryContext, ragContext := r.prepareContext(prepCtx, goal)

	// 2. Build initial messages using the same layered assembly strategy as the non-streaming path.
	summary := ""
	if r.session != nil {
		summary = r.session.Summary
	}
	messages := r.svc.buildConversationMessages(r.session, goal, ragContext, memoryContext, summary)
	// Ensure the current user message is in the session before starting
	r.session.AddMessage(domain.Message{Role: "user", Content: goal})

	const maxRounds = 20
	for round := 0; round < maxRounds; round++ {
		// Check cancellation
		if ctx.Err() != nil {
			r.emit(EventTypeError, "Execution cancelled")
			return
		}

		r.emit(EventTypeThinking, "Thinking...")

		// 3. Collect tools for CURRENT agent
		tools := r.svc.collectAllAvailableTools(ctx, r.currentAgent)

		// 4. Build System Prompt for CURRENT agent
		systemMsg := r.svc.buildSystemPrompt(ctx, r.currentAgent)
		genMessages := append([]domain.Message{{Role: "system", Content: systemMsg}}, messages...)

		// --- DEBUG: LOG FULL PROMPT + TOOLS ---
		if r.debugEnabled() {
			var promptBuilder strings.Builder
			info := r.svc.Info()
			fmt.Fprintf(&promptBuilder, "MODEL: %s (%s)\n", info.Model, info.BaseURL)
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
		var fullContent strings.Builder
		var toolCalls []domain.ToolCall
		toolCallDetected := false
		// taskCompleteTriggered signals task_complete was detected mid-stream.
		// We break out of StreamWithTools early by returning an error from the
		// callback; the runtime then checks this flag to avoid treating it as a
		// real error.
		var taskCompleteResult string
		taskCompleteTriggered := false

		var lastResponseID string
		err := r.svc.llmService.StreamWithTools(ctx, genMessages, tools, r.svc.toolGenerationOptions(0.3, 2000, ""), func(delta *domain.GenerationResult) error {
			if delta.ID != "" {
				lastResponseID = delta.ID
			}
			// 0. Hardwired: intercept task_complete at stream level regardless of
			//    PTC mode, system prompt overrides, or tool search configuration.
			//    When the model emits a task_complete tool call, capture the result
			//    and signal early termination by returning a sentinel error.
			for _, tc := range delta.ToolCalls {
				if tc.Function.Name == "task_complete" {
					toolCalls = delta.ToolCalls
					r.emitToolCall(tc.Function.Name, tc.Function.Arguments)
					if r, ok := tc.Function.Arguments["result"].(string); ok && r != "" {
						taskCompleteResult = r
					}
					taskCompleteTriggered = true
					return errTaskComplete
				}
			}

			// 1. Handle Reasoning (The "Thinking" Stream)
			if delta.ReasoningContent != "" {
				r.emit(EventTypeThinking, delta.ReasoningContent)
			}

			// 2. Handle Content (The "Output" Stream)
			if delta.Content != "" {
				fullContent.WriteString(delta.Content)
				r.emit(EventTypePartial, delta.Content)
			}

			// 3. Handle Tool Calls detection
			if len(delta.ToolCalls) > 0 {
				if !toolCallDetected {
					r.emit(EventTypeThinking, "Planning tool usage...")
					toolCallDetected = true
				}
				toolCalls = delta.ToolCalls
			}
			return nil
		})

		// task_complete detected in stream — terminate immediately.
		if taskCompleteTriggered {
			result := taskCompleteResult
			if result == "" {
				result = fullContent.String()
			}
			if r.debugEnabled() {
				var respBuilder strings.Builder
				fmt.Fprintf(&respBuilder, "CONTENT: %s\n", fullContent.String())
				if len(toolCalls) > 0 {
					fmt.Fprintf(&respBuilder, "TOOL CALLS:\n")
					for _, tc := range toolCalls {
						fmt.Fprintf(&respBuilder, "  - %s(%v)\n", tc.Function.Name, tc.Function.Arguments)
					}
				}
				r.emitDebug(round+1, "response", respBuilder.String())
			}
			r.emitToolResult("task_complete", result, nil)
			allSources := r.sources
			r.svc.ragSourcesMu.RLock()
			allSources = append(allSources, r.svc.ragSources...)
			r.svc.ragSourcesMu.RUnlock()
			r.svc.ragSourcesMu.Lock()
			r.svc.ragSources = nil
			r.svc.ragSourcesMu.Unlock()
			r.eventChan <- &Event{
				ID:        uuid.New().String(),
				Type:      EventTypeComplete,
				AgentName: r.currentAgent.Name(),
				AgentID:   r.currentAgent.ID(),
				Content:   result,
				Sources:   allSources,
				Timestamp: time.Now(),
			}
			// Persist session history to agentgo.db
			if len(messages) > 0 {
				for _, msg := range messages {
					r.session.AddMessage(msg)
				}
			}
			if err := r.svc.store.SaveSession(r.session); err != nil {
				r.svc.logger.Warn("failed to save session history", slog.String("error", err.Error()))
			}
			go r.saveToMemory(context.Background(), goal, result)
			return
		}

		if err != nil {
			r.emit(EventTypeError, fmt.Sprintf("LLM error: %v", err))
			return
		}

		// --- DEBUG: LOG LLM RESPONSE ---
		if r.debugEnabled() {
			var respBuilder strings.Builder
			info := r.svc.Info()
			tc := usage.NewTokenCounter()
			respTokens := tc.EstimateTokens(fullContent.String(), info.Model)
			fmt.Fprintf(&respBuilder, "CONTENT: %s\n", fullContent.String())
			if len(toolCalls) > 0 {
				fmt.Fprintf(&respBuilder, "TOOL CALLS:\n")
				for _, tc := range toolCalls {
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
		if len(toolCalls) > 0 {
			// Double check for task_complete in case it was not intercepted during stream
			for _, tc := range toolCalls {
				if tc.Function.Name == "task_complete" {
					result := fullContent.String()
					if res, ok := tc.Function.Arguments["result"].(string); ok && res != "" {
						result = res
					}
					allSources := r.sources
					r.svc.ragSourcesMu.RLock()
					allSources = append(allSources, r.svc.ragSources...)
					r.svc.ragSourcesMu.RUnlock()
					r.svc.ragSourcesMu.Lock()
					r.svc.ragSources = nil
					r.svc.ragSourcesMu.Unlock()
					r.eventChan <- &Event{
						ID:        uuid.New().String(),
						Type:      EventTypeComplete,
						AgentName: r.currentAgent.Name(),
						AgentID:   r.currentAgent.ID(),
						Content:   result,
						Sources:   allSources,
						Timestamp: time.Now(),
					}
					go r.saveToMemory(context.Background(), goal, result)
					return
				}
			}

			// Note: task_complete is intercepted at stream level above and never
			// reaches this point. All remaining tool calls are real work items.

			// PTC fix: some models (e.g. gpt-5.2) emit valid JS as text content
			// and then issue a broken execute_javascript tool call with garbage code.
			// When PTC is active and the text stream contains valid JS, override the
			// tool call's code with the sanitised text-stream code.
			if r.svc.isPTCEnabled() {
				content := fullContent.String()
				isCode := r.svc.ptcIntegration.IsCodeResponse(content)
				if r.debugEnabled() {
					r.emitDebug(round+1, "ptc_override", fmt.Sprintf("IsCodeResponse=%v contentLen=%d", isCode, len(content)))
				}
				if isCode {
					extracted := r.svc.ptcIntegration.ExtractCode(content)
					if r.debugEnabled() {
						r.emitDebug(round+1, "ptc_override", fmt.Sprintf("Extracted code len=%d", len(extracted)))
					}
					extracted = sanitiseJSCode(extracted)
					if r.debugEnabled() {
						r.emitDebug(round+1, "ptc_override", fmt.Sprintf("Sanitised code len=%d", len(extracted)))
					}
					if extracted != "" {
						for i, tc := range toolCalls {
							if tc.Function.Name == "execute_javascript" {
								if toolCalls[i].Function.Arguments == nil {
									toolCalls[i].Function.Arguments = make(map[string]interface{})
								}
								toolCalls[i].Function.Arguments["code"] = extracted
								if r.debugEnabled() {
									r.emitDebug(round+1, "ptc_override", fmt.Sprintf("Replaced execute_javascript payload for tool call %d", i))
								}
							}
						}
					}
				}
			}

			// Ensure every tool call has an ID before building the assistant message —
			// some OpenAI-compatible providers omit the id field, which causes
			// tool results to be silently dropped when matched back.
			for i := range toolCalls {
				if toolCalls[i].ID == "" {
					toolCalls[i].ID = domain.NormalizeToolCallID(fmt.Sprintf("%s_%d", toolCalls[i].Function.Name, i))
					continue
				}
				toolCalls[i].ID = domain.NormalizeToolCallID(toolCalls[i].ID)
			}

			// Add assistant's tool call message to history
			messages = append(messages, domain.Message{
				Role:       "assistant",
				Content:    fullContent.String(),
				ToolCalls:  toolCalls,
				ResponseID: lastResponseID,
			})

			// 7. Process Tool Calls (Parallel Execution)
			handoffOccurred := false

			// Use errgroup for parallel tool execution
			g, groupCtx := errgroup.WithContext(ctx)
			toolResults := make([]struct {
				Content    string
				IsHandoff  bool
				ToolCallID string
				ToolName   string
				Result     interface{}
				Error      error
			}, len(toolCalls))

			for i, tc := range toolCalls {
				idx, toolCall := i, tc

				// Handle Handoff immediately (sequential) as it changes state
				if strings.HasPrefix(toolCall.Function.Name, "transfer_to_") {
					res, err, isHandoff := r.executeToolViaSubAgent(ctx, toolCall)
					toolResults[idx].Content = toolResultToString(res)
					if err != nil {
						toolResults[idx].Content = fmt.Sprintf("Error: %v", err)
					}
					toolResults[idx].IsHandoff = isHandoff
					toolResults[idx].ToolCallID = toolCall.ID
					toolResults[idx].ToolName = toolCall.Function.Name
					toolResults[idx].Result = res
					toolResults[idx].Error = err
					if isHandoff {
						handoffOccurred = true
					}
					continue
				}

				// Parallel execute independent tools
				g.Go(func() error {
					r.emitToolCall(toolCall.Function.Name, toolCall.Function.Arguments)
					res, err, isHandoff := r.executeToolViaSubAgent(groupCtx, toolCall)

					content := ""
					if err != nil {
						content = fmt.Sprintf("Error: %v", err)
					} else {
						content = toolResultToString(res)
					}

					toolResults[idx].Content = content
					toolResults[idx].IsHandoff = isHandoff
					toolResults[idx].ToolCallID = toolCall.ID
					toolResults[idx].ToolName = toolCall.Function.Name
					toolResults[idx].Result = res
					toolResults[idx].Error = err

					r.emitToolResult(toolCall.Function.Name, res, err)
					return nil
				})
			}

			_ = g.Wait()

			// Collect all results into messages
			for _, tr := range toolResults {
				if tr.ToolCallID == "" {
					continue
				} // Skip if not handled (shouldn't happen)

				if tr.IsHandoff {
					r.session.AgentID = r.currentAgent.ID()
					messages = append(messages, domain.Message{
						Role:       "tool",
						ToolCallID: tr.ToolCallID,
						Content:    fmt.Sprintf("Transferred to %s", r.currentAgent.Name()),
					})
				} else {
					messages = append(messages, domain.Message{
						Role:       "tool",
						ToolCallID: tr.ToolCallID,
						Content:    tr.Content,
					})
				}
			}

			if handoffOccurred {
				continue
			}

			// In non-PTC mode, encourage the model to process results and move towards completion
			isPTCToolRound := r.svc.isPTCEnabled() && len(toolCalls) == 1 && toolCalls[0].Function.Name == "execute_javascript"
			if !isPTCToolRound {
				messages = append(messages, domain.Message{
					Role:    "user",
					Content: "Analyze the tool results above. If you have fulfilled the user's request, provide your final answer and call task_complete. Otherwise, continue with the necessary next steps.",
				})
			}

		} else {
			// PTC fallback: when PTC is active and the LLM wrote JS code as a
			// text/markdown response instead of using the execute_javascript
			// function-call, intercept it, execute it, and inject the result
			// back so the LLM can produce a grounded final answer.
			if r.svc.isPTCEnabled() {
				content := fullContent.String()
				if r.svc.ptcIntegration.IsCodeResponse(content) {
					code := r.svc.ptcIntegration.ExtractCode(content)
					if code != "" {
						tc := domain.ToolCall{
							ID:   domain.NormalizeToolCallID("ptc_fallback_" + uuid.New().String()[:8]),
							Type: "function",
							Function: domain.FunctionCall{
								Name:      "execute_javascript",
								Arguments: map[string]interface{}{"code": code},
							},
						}
						r.emitToolCall("execute_javascript", tc.Function.Arguments)
						execResult, execErr, _ := r.executeToolViaSubAgent(ctx, tc)
						r.emitToolResult("execute_javascript", execResult, execErr)

						// Append assistant's code message + execution result so
						// the LLM can synthesise a final answer in the next round.
						messages = append(messages, domain.Message{
							Role:      "assistant",
							Content:   content,
							ToolCalls: []domain.ToolCall{tc},
						})
						resultMsg := toolResultToString(execResult)
						if execErr != nil {
							resultMsg = fmt.Sprintf("execute_javascript error: %v", execErr)
						}
						messages = append(messages, domain.Message{
							Role:       "tool",
							ToolCallID: tc.ID,
							Content:    resultMsg,
						})
						continue // next round → LLM synthesises answer
					}
				}
			}

			// Final Answer - merge sources from runtime and service
			allSources := r.sources
			r.svc.ragSourcesMu.RLock()
			if len(r.svc.ragSources) > 0 {
				allSources = append(allSources, r.svc.ragSources...)
			}
			r.svc.ragSourcesMu.RUnlock()

			r.eventChan <- &Event{
				ID:        uuid.New().String(),
				Type:      EventTypeComplete,
				AgentName: r.currentAgent.Name(),
				AgentID:   r.currentAgent.ID(),
				Content:   fullContent.String(),
				Sources:   allSources, // Include all collected RAG sources
				Timestamp: time.Now(),
			}

			// Persist session history to agentgo.db
			if len(messages) > 0 {
				for _, msg := range messages {
					r.session.AddMessage(msg)
				}
			}
			if err := r.svc.store.SaveSession(r.session); err != nil {
				r.svc.logger.Warn("failed to save session history", slog.String("error", err.Error()))
			}

			// Clear service sources for next run
			r.svc.ragSourcesMu.Lock()
			r.svc.ragSources = nil
			r.svc.ragSourcesMu.Unlock()

			// Auto-save to memory ASYNC to prevent lag at the end
			go r.saveToMemory(context.Background(), goal, fullContent.String())
			return
		}
	}
}

// executeToolOrHandoff executes a tool call and handles agent switching
func (r *Runtime) executeToolOrHandoff(ctx context.Context, tc domain.ToolCall) (interface{}, error, bool) {
	toolName := tc.Function.Name
	resolvedToolName := r.resolveExecutableToolName(toolName)
	ctx = withEventSink(ctx, r.forwardSubAgentEvent)
	ctx = withRunDebug(ctx, r.debugEnabled())
	ctx = withCurrentSession(ctx, r.session)

	// === PRE-TOOL HOOK ===
	hookData := HookData{
		ToolName:  resolvedToolName,
		ToolArgs:  tc.Function.Arguments,
		SessionID: r.session.GetID(),
		AgentID:   r.currentAgent.ID(),
	}

	if r.svc.hooks != nil {
		modifiedData, err := r.svc.hooks.EmitWithResult(ctx, HookEventPreToolUse, hookData)
		if err != nil {
			// Hook blocked execution
			return nil, err, false
		}
		// Use modified args if hook changed them
		if modifiedData.ToolArgs != nil {
			tc.Function.Arguments = modifiedData.ToolArgs
		}
	}

	// Check for Handoff
	if strings.HasPrefix(tc.Function.Name, "transfer_to_") {
		for _, h := range r.currentAgent.Handoffs() {
			if h.ToolName() == tc.Function.Name {
				targetAgent := h.TargetAgent()
				reason := tc.Function.Arguments["reason"]

				r.emit(EventTypeHandoff, fmt.Sprintf("Transferring to %s: %v", targetAgent.Name(), reason))

				// SWITCH AGENT
				r.currentAgent = targetAgent
				return nil, nil, true
			}
		}
	}

	if err := r.svc.authorizeTool(ctx, PermissionRequest{
		ToolName:  resolvedToolName,
		ToolArgs:  tc.Function.Arguments,
		SessionID: r.session.GetID(),
		AgentID:   r.currentAgent.ID(),
	}); err != nil {
		return nil, err, false
	}

	// Normal Tool Execution
	var result interface{}
	var execErr error

	// 1. Special Case: task_complete
	if toolName == "task_complete" {
		res, _ := tc.Function.Arguments["result"].(string)
		result = res
		if result == "" {
			result = "Task complete"
		}
	} else if handler, ok := r.currentAgent.GetHandler(resolvedToolName); ok {
		// 2. Agent-Local Tools
		result, execErr = handler(ctx, tc.Function.Arguments)
	} else if r.svc.toolRegistry.Has(resolvedToolName) {
		// 3. Unified ToolRegistry — custom tools, RAG, Memory, SubAgent, search_available_tools.
		result, execErr = r.svc.toolRegistry.Call(ctx, resolvedToolName, tc.Function.Arguments)
	} else if r.svc.isMCPTool(resolvedToolName) {
		// 4. MCP Tools
		result, execErr = r.svc.mcpService.CallTool(ctx, resolvedToolName, tc.Function.Arguments)
	} else if r.svc.isSkill(ctx, resolvedToolName) && r.svc.skillsService != nil {
		// 5. Skills
		skillID := strings.TrimPrefix(resolvedToolName, "skill_")
		res, err := r.svc.skillsService.Execute(ctx, &skills.ExecutionRequest{
			SkillID:   skillID,
			Variables: tc.Function.Arguments,
		})
		if err != nil {
			execErr = err
		} else {
			result = res.Output
		}
	} else if toolName == "execute_javascript" && r.svc.ptcIntegration != nil {
		// 6. PTC: execute_javascript tool (JavaScript sandbox)
		result, execErr = r.svc.ptcIntegration.ExecuteJavascriptTool(ctx, tc.Function.Arguments)
	} else if domain.IsToolSearchTool(resolvedToolName) {
		// 7. Tool Search: search for deferred tools
		query, _ := tc.Function.Arguments["query"].(string)
		if query == "" {
			execErr = fmt.Errorf("tool search requires a 'query' argument")
		} else {
			// Determine search type
			searchType := "regex"
			if resolvedToolName == "tool_search_tool_bm25" {
				searchType = "bm25"
			}
			// Execute search
			matchedTools, err := r.svc.toolRegistry.ExecuteToolSearch(query, searchType)
			if err != nil {
				execErr = err
			} else {
				matchedTools = r.svc.filterToolDefinitionsForAgent(r.currentAgent, matchedTools)
				// Build tool_references result
				var refs []domain.ToolReference
				for _, t := range matchedTools {
					refs = append(refs, domain.ToolReference{ToolName: t.Function.Name})
					// Auto-activate the tool for this session
					r.svc.toolRegistry.ActivateForSession(r.session.GetID(), t.Function.Name)
				}
				result = domain.ToolSearchResult{ToolReferences: refs}
			}
		}
	} else {
		execErr = fmt.Errorf("unknown tool: %s", toolName)
	}

	// === POST-TOOL HOOK ===
	hookData.ToolResult = result
	hookData.ToolError = execErr
	if r.svc.hooks != nil {
		r.svc.hooks.Emit(HookEventPostToolUse, hookData)
	}

	return result, execErr, false
}

func (r *Runtime) resolveExecutableToolName(name string) string {
	if name == "" || name == "task_complete" || strings.HasPrefix(name, "transfer_to_") {
		return name
	}

	candidates := make([]string, 0, len(r.currentAgent.Tools())+32)
	for _, def := range r.currentAgent.Tools() {
		candidates = append(candidates, def.Function.Name)
	}
	for _, info := range r.svc.toolRegistry.ListForCallTool() {
		candidates = append(candidates, info.Name)
	}
	if r.svc.mcpService != nil {
		for _, def := range r.svc.mcpService.ListTools() {
			candidates = append(candidates, def.Function.Name)
		}
	}

	if resolved := resolveClosestToolName(name, candidates); resolved != "" {
		return resolved
	}
	return name
}

func (r *Runtime) prepareContext(ctx context.Context, goal string) (string, string) {
	var ragCtx, memCtx string

	g, groupCtx := errgroup.WithContext(ctx)

	// RAG Retrieval — skip when PTC is enabled: the LLM will call rag_query
	// explicitly via execute_javascript / callTool, so pre-injecting the
	// answer would short-circuit the tool and make it unreachable.
	if r.svc.ragProcessor != nil && !r.svc.isPTCEnabled() {
		g.Go(func() error {
			if res, err := r.svc.performRAGQuery(groupCtx, goal); err == nil {
				ragCtx = res
			}
			return nil
		})
	}

	// Memory Retrieval
	if r.svc.memoryService != nil {
		queryContext := r.svc.resolveMemoryQueryContext(r.session)
		r.svc.rememberMemoryQueryContext(r.session, queryContext)
		g.Go(func() error {
			var err error
			memCtx, _, err = r.svc.memoryService.RetrieveAndInjectWithContext(groupCtx, goal, queryContext)
			if err != nil {
				r.svc.logger.Warn("memory retrieval failed", slog.String("error", err.Error()))
			}
			return nil
		})
	}

	_ = g.Wait()
	return memCtx, ragCtx
}

func (r *Runtime) saveToMemory(ctx context.Context, goal, result string) {
	if r.svc.memoryService != nil {
		queryContext := r.svc.resolveMemoryQueryContext(r.session)
		if err := r.svc.memoryService.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
			SessionID:  r.session.GetID(),
			AgentID:    queryContext.AgentID,
			SquadID:    queryContext.SquadID,
			UserID:     queryContext.UserID,
			TaskGoal:   goal,
			TaskResult: result,
		}); err != nil {
			r.svc.logger.Warn("failed to store memory after run", slog.String("error", err.Error()))
		}
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

func (r *Runtime) emitToolCall(name string, args map[string]interface{}) {
	r.eventChan <- &Event{
		ID:        uuid.New().String(),
		Type:      EventTypeToolCall,
		AgentName: r.currentAgent.Name(),
		ToolName:  name,
		ToolArgs:  args,
		Timestamp: time.Now(),
	}
}

func (r *Runtime) emitToolResult(name string, res interface{}, err error) {
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
