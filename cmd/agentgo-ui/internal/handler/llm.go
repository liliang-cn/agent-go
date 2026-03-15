package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/pkg/services"
	"github.com/liliang-cn/agent-go/pkg/store"
)

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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		JSONError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	poolService := services.GetGlobalPoolService()
	opts := services.ChatOptions{
		SessionID:    externalID,
		Provider:     provider,
		Model:        model,
		HistoryLimit: 20,
	}

	// Use streamAISDKChat style but with real LLM stream
	messageID := externalID
	if messageID == "" {
		messageID = uuid.New().String()
	}
	textPartID := "text-0"

	writeSSEChunk(w, flusher, map[string]any{
		"type":      "start",
		"messageId": messageID,
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
	})
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
