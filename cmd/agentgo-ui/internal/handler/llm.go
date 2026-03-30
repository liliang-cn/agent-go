package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

type directChatToolLLM interface {
	GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error)
}

type directChatToolExecution struct {
	ToolCallID string
	ToolName   string
	Arguments  map[string]interface{}
	Result     interface{}
	Success    bool
	Error      string
}

// HandleChat is the unified entry point for chat, but dispatches to specific handlers
func (h *Handler) HandleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		JSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	mode := strings.TrimSpace(strings.ToLower(stringValue(raw["mode"])))
	prompt := extractLastUserMessage(raw["messages"])

	// 1. Dispatch based on mode
	switch mode {
	case "agent":
		h.handleAISDKAgentChat(w, r, raw, prompt)
	case "llm":
		h.handleDirectLLMChat(w, r, raw, prompt)
	case "rag":
		h.handleRAGChat(w, r, raw, prompt)
	default:
		// Default to direct LLM if no mode or unknown mode
		h.handleDirectLLMChat(w, r, raw, prompt)
	}
}

// handleDirectLLMChat handles pure LLM chat without RAG or Agent involvement
func (h *Handler) handleDirectLLMChat(w http.ResponseWriter, r *http.Request, raw map[string]any, prompt string) {
	if strings.TrimSpace(prompt) == "" {
		JSONError(w, "Message required", http.StatusBadRequest)
		return
	}

	externalID, _ := raw["id"].(string)
	provider := stringValue(raw["provider"])
	model := stringValue(raw["model"])
	selectedToolNames := stringSliceValue(raw["tool_names"])
	maxToolCalls := intValue(raw["max_tool_calls"], 6)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		JSONError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	poolService := services.GetGlobalPoolService()
	messageID := externalID
	if messageID == "" {
		messageID = uuid.New().String()
	}
	sessionID := externalID
	if sessionID == "" {
		sessionID = messageID
	}

	if len(selectedToolNames) > 0 {
		if h.mcpService == nil {
			JSONError(w, "MCP service not available", http.StatusServiceUnavailable)
			return
		}

		availableTools := h.mcpService.GetAvailableTools(r.Context())
		toolDefs, armedToolNames := buildDirectChatToolDefinitions(availableTools, selectedToolNames)
		if len(toolDefs) == 0 {
			JSONError(w, "Selected tools are not available", http.StatusBadRequest)
			return
		}

		llmClient, releaseLLM, err := acquireDirectChatLLM(poolService, provider, model)
		if err != nil {
			JSONError(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer releaseLLM()

		db := poolService.GetAgentGoDB()
		messages := loadDirectChatHistory(db, sessionID, 20)
		messages = append(messages, domain.Message{
			Role:    "user",
			Content: prompt,
		})

		writeSSEChunk(w, flusher, map[string]any{
			"type":      "start",
			"messageId": messageID,
			"messageMetadata": map[string]any{
				"mode":            "llm",
				"provider":        provider,
				"model":           model,
				"session_id":      sessionID,
				"tools_enabled":   true,
				"armed_tools":     armedToolNames,
				"tool_call_count": 0,
			},
		})

		usedToolNames := make([]string, 0, len(armedToolNames))
		executedToolCount := 0
		finalResult, executions, err := runDirectLLMToolLoop(
			r.Context(),
			llmClient,
			messages,
			toolDefs,
			&domain.GenerationOptions{MaxTokens: 2000},
			maxToolCalls,
			func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
				return h.mcpService.CallTool(ctx, toolName, args)
			},
			func(round int, reasoning string) {
				if strings.TrimSpace(reasoning) == "" {
					return
				}
				reasoningID := fmt.Sprintf("reasoning-%d", round)
				writeSSEChunk(w, flusher, map[string]any{
					"type": "reasoning-start",
					"id":   reasoningID,
				})
				for _, chunk := range chunkText(reasoning, 96) {
					writeSSEChunk(w, flusher, map[string]any{
						"type":  "reasoning-delta",
						"id":    reasoningID,
						"delta": chunk,
					})
				}
				writeSSEChunk(w, flusher, map[string]any{
					"type": "reasoning-end",
					"id":   reasoningID,
				})
			},
			func(execution directChatToolExecution) {
				usedToolNames = appendUniqueString(usedToolNames, execution.ToolName)
				executedToolCount++

				writeSSEChunk(w, flusher, map[string]any{
					"type":       "tool-input-available",
					"toolCallId": execution.ToolCallID,
					"toolName":   execution.ToolName,
					"input":      execution.Arguments,
					"dynamic":    true,
				})
				if execution.Error != "" {
					writeSSEChunk(w, flusher, map[string]any{
						"type":       "tool-output-error",
						"toolCallId": execution.ToolCallID,
						"errorText":  execution.Error,
						"dynamic":    true,
					})
				} else {
					writeSSEChunk(w, flusher, map[string]any{
						"type":       "tool-output-available",
						"toolCallId": execution.ToolCallID,
						"output":     execution.Result,
						"dynamic":    true,
					})
				}
				writeSSEChunk(w, flusher, map[string]any{
					"type": "message-metadata",
					"messageMetadata": map[string]any{
						"mode":            "llm",
						"provider":        provider,
						"model":           model,
						"session_id":      sessionID,
						"tools_enabled":   true,
						"armed_tools":     armedToolNames,
						"used_tools":      usedToolNames,
						"tool_call_count": executedToolCount,
					},
				})
			},
		)
		if err != nil {
			writeSSEChunk(w, flusher, map[string]any{
				"type":      "error",
				"errorText": err.Error(),
			})
			return
		}

		if strings.TrimSpace(finalResult.Content) != "" {
			textPartID := "text-0"
			writeSSEChunk(w, flusher, map[string]any{
				"type": "text-start",
				"id":   textPartID,
			})
			for _, chunk := range chunkText(finalResult.Content, 96) {
				writeSSEChunk(w, flusher, map[string]any{
					"type":  "text-delta",
					"id":    textPartID,
					"delta": chunk,
				})
			}
			writeSSEChunk(w, flusher, map[string]any{
				"type": "text-end",
				"id":   textPartID,
			})
		}

		if db != nil && strings.TrimSpace(finalResult.Content) != "" {
			_ = db.AddMessage(sessionID, "user", prompt, map[string]interface{}{
				"type": "llm",
			})
			_ = db.AddMessage(sessionID, "assistant", finalResult.Content, map[string]interface{}{
				"type":             "llm",
				"provider":         provider,
				"model":            model,
				"tool_calls_count": len(executions),
				"used_tools":       usedToolNames,
			})
		}

		writeSSEChunk(w, flusher, map[string]any{
			"type":         "finish",
			"finishReason": "stop",
			"messageMetadata": map[string]any{
				"mode":            "llm",
				"provider":        provider,
				"model":           model,
				"session_id":      sessionID,
				"tools_enabled":   true,
				"armed_tools":     armedToolNames,
				"used_tools":      usedToolNames,
				"tool_call_count": len(executions),
			},
		})
		return
	}

	opts := services.ChatOptions{
		SessionID:    sessionID,
		Provider:     provider,
		Model:        model,
		HistoryLimit: 20,
	}

	// Use streamAISDKChat style but with real LLM stream
	textPartID := "text-0"

	writeSSEChunk(w, flusher, map[string]any{
		"type":      "start",
		"messageId": messageID,
		"messageMetadata": map[string]any{
			"mode":            "llm",
			"provider":        provider,
			"model":           model,
			"session_id":      sessionID,
			"tools_enabled":   false,
			"tool_call_count": 0,
		},
	})
	writeSSEChunk(w, flusher, map[string]any{
		"type": "text-start",
		"id":   textPartID,
	})

	err := poolService.StreamChat(r.Context(), prompt, opts, func(token string) {
		writeSSEChunk(w, flusher, map[string]any{
			"type":  "text-delta",
			"id":    textPartID,
			"delta": token,
		})
	})

	if err != nil {
		writeSSEChunk(w, flusher, map[string]any{
			"type":      "error",
			"errorText": err.Error(),
		})
		return
	}

	writeSSEChunk(w, flusher, map[string]any{
		"type": "text-end",
		"id":   textPartID,
	})
	writeSSEChunk(w, flusher, map[string]any{
		"type":         "finish",
		"finishReason": "stop",
		"messageMetadata": map[string]any{
			"mode":            "llm",
			"provider":        provider,
			"model":           model,
			"session_id":      sessionID,
			"tools_enabled":   false,
			"tool_call_count": 0,
		},
	})
}

func acquireDirectChatLLM(poolService *services.GlobalPoolService, provider, model string) (directChatToolLLM, func(), error) {
	var (
		llmClient *pool.Client
		err       error
	)

	switch {
	case provider != "" && model != "":
		llmClient, err = poolService.GetLLMByProviderAndModel(provider, model)
	case provider != "":
		llmClient, err = poolService.GetLLMByProvider(provider)
	case model != "":
		llmClient, err = poolService.GetLLMByModel(model)
	default:
		llmClient, err = poolService.GetLLM()
	}
	if err != nil {
		return nil, nil, err
	}

	return llmClient, func() {
		poolService.ReleaseLLM(llmClient)
	}, nil
}

func buildDirectChatToolDefinitions(available []mcp.AgentToolInfo, selected []string) ([]domain.ToolDefinition, []string) {
	if len(selected) == 0 {
		return nil, nil
	}

	allowed := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}

	toolDefs := make([]domain.ToolDefinition, 0, len(allowed))
	armedToolNames := make([]string, 0, len(allowed))
	for _, tool := range available {
		if _, ok := allowed[tool.Name]; !ok {
			continue
		}
		parameters := tool.InputSchema
		if len(parameters) == 0 {
			parameters = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
		toolDefs = append(toolDefs, domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
		armedToolNames = append(armedToolNames, tool.Name)
	}
	return toolDefs, armedToolNames
}

func loadDirectChatHistory(db *store.AgentGoDB, sessionID string, limit int) []domain.Message {
	if db == nil || sessionID == "" {
		return nil
	}
	history, err := db.GetMessages(sessionID, limit)
	if err != nil {
		return nil
	}
	messages := make([]domain.Message, 0, len(history))
	for _, msg := range history {
		messages = append(messages, domain.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	return messages
}

func runDirectLLMToolLoop(
	ctx context.Context,
	llm directChatToolLLM,
	baseMessages []domain.Message,
	toolDefs []domain.ToolDefinition,
	opts *domain.GenerationOptions,
	maxToolCalls int,
	executeTool func(context.Context, string, map[string]interface{}) (interface{}, error),
	onReasoning func(round int, reasoning string),
	onTool func(execution directChatToolExecution),
) (*domain.GenerationResult, []directChatToolExecution, error) {
	if maxToolCalls <= 0 {
		maxToolCalls = 6
	}
	if opts == nil {
		opts = &domain.GenerationOptions{}
	}

	messages := append([]domain.Message{}, baseMessages...)
	executions := make([]directChatToolExecution, 0, maxToolCalls)
	toolBudgetReached := false
	const maxRounds = 8

	for round := 0; round < maxRounds; round++ {
		currentTools := toolDefs
		if toolBudgetReached {
			currentTools = nil
		}

		result, err := llm.GenerateWithTools(ctx, messages, currentTools, opts)
		if err != nil {
			return nil, executions, err
		}
		if strings.TrimSpace(result.ReasoningContent) != "" && onReasoning != nil {
			onReasoning(round, result.ReasoningContent)
		}

		for i := range result.ToolCalls {
			if result.ToolCalls[i].ID == "" {
				result.ToolCalls[i].ID = domain.NormalizeToolCallID(fmt.Sprintf("%s_%d_%d", result.ToolCalls[i].Function.Name, round, i))
			} else {
				result.ToolCalls[i].ID = domain.NormalizeToolCallID(result.ToolCalls[i].ID)
			}
		}

		if len(currentTools) == 0 || len(result.ToolCalls) == 0 {
			return result, executions, nil
		}

		messages = append(messages, domain.Message{
			Role:             "assistant",
			Content:          result.Content,
			ReasoningContent: result.ReasoningContent,
			ToolCalls:        result.ToolCalls,
			ResponseID:       result.ID,
		})

		for _, call := range result.ToolCalls {
			if len(executions) >= maxToolCalls {
				toolBudgetReached = true
				messages = append(messages, domain.Message{
					Role:    "user",
					Content: "Tool call budget reached. Based on the collected tool results, provide the best possible final answer without calling more tools.",
				})
				break
			}

			resultPayload, err := executeTool(ctx, call.Function.Name, call.Function.Arguments)
			payload, success, errorText := normalizeDirectToolResult(resultPayload, err)
			execution := directChatToolExecution{
				ToolCallID: call.ID,
				ToolName:   call.Function.Name,
				Arguments:  call.Function.Arguments,
				Result:     payload,
				Success:    success,
				Error:      errorText,
			}
			if onTool != nil {
				onTool(execution)
			}
			messages = append(messages, domain.Message{
				Role:       "tool",
				Content:    toolResultContent(payload),
				ToolCallID: call.ID,
			})
			executions = append(executions, execution)
		}
	}

	return nil, executions, fmt.Errorf("tool loop exceeded %d rounds", maxRounds)
}

func normalizeDirectToolResult(result interface{}, err error) (interface{}, bool, string) {
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}, false, err.Error()
	}

	switch value := result.(type) {
	case *mcp.ToolResult:
		if value == nil {
			return map[string]interface{}{
				"success": false,
				"error":   "tool returned no result",
			}, false, "tool returned no result"
		}
		if value.Success {
			return value, true, ""
		}
		return value, false, value.Error
	default:
		return result, true, ""
	}
}

func toolResultContent(result interface{}) string {
	switch value := result.(type) {
	case string:
		return value
	default:
		return stringifyJSON(result)
	}
}

func stringSliceValue(v any) []string {
	rawItems, ok := v.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(rawItems))
	for _, item := range rawItems {
		if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
			values = append(values, strings.TrimSpace(str))
		}
	}
	return values
}

func intValue(v any, fallback int) int {
	switch value := v.(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return fallback
	}
}

func appendUniqueString(values []string, next string) []string {
	for _, value := range values {
		if value == next {
			return values
		}
	}
	return append(values, next)
}

// handleRAGChat handles chat with RAG retrieval
func (h *Handler) handleRAGChat(w http.ResponseWriter, r *http.Request, raw map[string]any, prompt string) {
	externalID, _ := raw["id"].(string)
	sessionID, err := h.getOrCreateAISDKSession(r.Context(), externalID)
	if err != nil {
		JSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Use existing RAG client chat (which will eventually be unified)
	resp, err := h.ragClient.Chat(r.Context(), sessionID, prompt, nil)
	if err != nil {
		JSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	streamAISDKChat(w, r, externalID, resp.Answer)
}

// HandleChatSessions returns a list of chat sessions from unified DB
func (h *Handler) HandleChatSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil {
			limit = val
		}
	}

	sessionType := r.URL.Query().Get("type")

	poolService := services.GetGlobalPoolService()
	db := poolService.GetAgentGoDB()
	if db == nil {
		JSONError(w, "Unified database unavailable", http.StatusServiceUnavailable)
		return
	}

	sessions, err := db.ListSessions(sessionType, limit)
	if err != nil {
		JSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	JSONResponse(w, map[string]interface{}{
		"sessions": sessionsToUI(db, sessions),
	})
}

func sessionsToUI(db *store.AgentGoDB, sessions []*store.ChatSession) []map[string]interface{} {
	uiSessions := make([]map[string]interface{}, 0, len(sessions))
	for _, s := range sessions {
		messageCount := len(s.Messages)
		if db != nil {
			if count, err := db.CountMessages(s.ID); err == nil && count > 0 {
				messageCount = count
			}
		}
		uiSessions = append(uiSessions, map[string]interface{}{
			"id":       s.ID,
			"type":     s.Type,
			"title":    s.Title,
			"messages": messageCount,
			"created":  s.CreatedAt.Format(time.RFC3339),
			"updated":  s.UpdatedAt.Format(time.RFC3339),
		})
	}
	return uiSessions
}

// HandleChatSessionMessages returns messages for a specific session
func (h *Handler) HandleChatSessionMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Support both /api/chat/sessions/{id} and /api/chat/session/{id}
	sessionID := strings.TrimPrefix(r.URL.Path, "/api/chat/sessions/")
	sessionID = strings.TrimPrefix(sessionID, "/api/chat/session/")

	if sessionID == "" || sessionID == "sessions" || sessionID == "session" {
		JSONError(w, "Session ID required", http.StatusBadRequest)
		return
	}

	poolService := services.GetGlobalPoolService()
	db := poolService.GetAgentGoDB()
	if db == nil {
		JSONError(w, "Unified database unavailable", http.StatusServiceUnavailable)
		return
	}

	session, err := db.GetSession(sessionID)
	if err != nil {
		JSONError(w, err.Error(), http.StatusNotFound)
		return
	}

	// Convert messages to AI SDK format for frontend rendering
	type AISDKPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type AISDKMessage struct {
		ID    string      `json:"id"`
		Role  string      `json:"role"`
		Parts []AISDKPart `json:"parts"`
	}

	aiSDKMessages := make([]AISDKMessage, 0, len(session.Messages))
	for i, m := range session.Messages {
		msgID := fmt.Sprintf("%s-%d", session.ID, i)
		aiSDKMessages = append(aiSDKMessages, AISDKMessage{
			ID:   msgID,
			Role: m.Role,
			Parts: []AISDKPart{
				{Type: "text", Text: m.Content},
			},
		})
	}

	JSONResponse(w, map[string]interface{}{
		"id":       session.ID,
		"title":    session.Title,
		"created":  session.CreatedAt.Format(time.RFC3339),
		"updated":  session.UpdatedAt.Format(time.RFC3339),
		"messages": aiSDKMessages,
		"metadata": session.Metadata,
	})
}
