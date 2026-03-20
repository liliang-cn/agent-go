package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/rag"
)

// RAG handlers

func (h *Handler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Query      string `json:"query"`
		Collection string `json:"collection"`
		TopK       int    `json:"top_k"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.TopK == 0 {
		req.TopK = 5
	}

	result, err := h.ragClient.Query(r.Context(), req.Query, &rag.QueryOptions{
		TopK:        req.TopK,
		Temperature: 0.7,
		ShowSources: true,
	})
	if err != nil {
		JSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	JSONResponse(w, result)
}

func (h *Handler) HandleDocuments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	docs, err := h.ragClient.ListDocuments(r.Context())
	if err != nil {
		JSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	JSONResponse(w, docs)
}

func (h *Handler) HandleDocumentOperation(w http.ResponseWriter, r *http.Request) {
	docID := r.URL.Path[len("/api/documents/"):]
	if docID == "" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		doc, err := h.ragClient.GetDocument(r.Context(), docID)
		if err != nil {
			JSONError(w, err.Error(), http.StatusNotFound)
			return
		}
		JSONResponse(w, doc)
	case http.MethodDelete:
		if err := h.ragClient.DeleteDocument(r.Context(), docID); err != nil {
			JSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		JSONResponse(w, map[string]bool{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) HandleCollections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats, _ := h.ragClient.GetStats(r.Context())
	JSONResponse(w, []map[string]interface{}{
		{"name": "default", "count": stats.TotalDocuments},
	})
}

func (h *Handler) handleAISDKAgentChat(w http.ResponseWriter, r *http.Request, raw map[string]any, prompt string) {
	if h.squadManager == nil {
		JSONError(w, "Agent manager unavailable", http.StatusServiceUnavailable)
		return
	}

	agentName := strings.TrimSpace(stringValue(raw["agent_name"]))
	if agentName == "" {
		agentName = "Captain"
	}

	events, err := h.squadManager.DispatchTaskStream(r.Context(), agentName, prompt)
	if err != nil {
		JSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	chatID := strings.TrimSpace(stringValue(raw["id"]))
	streamAISDKAgentChat(w, r, chatID, agentName, events)
}

func (h *Handler) getOrCreateAISDKSession(ctx context.Context, externalID string) (string, error) {
	if externalID == "" {
		session, err := h.ragClient.StartChat(ctx, map[string]interface{}{
			"created_via": "ai-sdk",
		})
		if err != nil {
			return "", err
		}
		return session.ID, nil
	}

	h.aiChatMu.RLock()
	if sessionID := h.aiChatSessions[externalID]; sessionID != "" {
		h.aiChatMu.RUnlock()
		return sessionID, nil
	}
	h.aiChatMu.RUnlock()

	session, err := h.ragClient.StartChat(ctx, map[string]interface{}{
		"external_chat_id": externalID,
		"created_via":      "ai-sdk",
	})
	if err != nil {
		return "", err
	}

	h.aiChatMu.Lock()
	h.aiChatSessions[externalID] = session.ID
	h.aiChatMu.Unlock()
	return session.ID, nil
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func (h *Handler) HandleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		JSONError(w, "Failed to parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		JSONError(w, "No file uploaded: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Create temp file
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, "agentgo-upload-"+filepath.Base(header.Filename))
	out, err := os.Create(tempFile)
	if err != nil {
		JSONError(w, "Failed to create temp file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()

	// Copy uploaded file to temp
	if _, err := io.Copy(out, file); err != nil {
		os.Remove(tempFile)
		JSONError(w, "Failed to save file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out.Close()

	// Ingest the file
	resp, err := h.ragClient.IngestFile(r.Context(), tempFile, &rag.IngestOptions{
		ChunkSize: 1000,
		Overlap:   200,
	})

	// Clean up temp file
	os.Remove(tempFile)

	if err != nil {
		JSONError(w, "Failed to ingest file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	JSONResponse(w, map[string]interface{}{
		"success":    resp.Success,
		"document":   header.Filename,
		"documentId": resp.DocumentID,
		"chunks":     resp.ChunkCount,
		"message":    resp.Message,
	})
}
