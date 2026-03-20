package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Memory handlers

func (h *Handler) HandleMemories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.memoryService == nil {
		JSONResponse(w, []interface{}{})
		return
	}
	list, _, _ := h.memoryService.List(r.Context(), 100, 0)
	JSONResponse(w, list)
}

func (h *Handler) HandleMemoryAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.memoryService == nil {
		JSONError(w, "Memory service unavailable", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Content    string  `json:"content"`
		Type       string  `json:"type"`
		Importance float64 `json:"importance"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	mem := &domain.Memory{
		ID:         uuid.New().String(),
		Type:       domain.MemoryType(req.Type),
		Content:    req.Content,
		Importance: req.Importance,
		CreatedAt:  time.Now(),
	}
	h.memoryService.Add(r.Context(), mem)
	JSONResponse(w, map[string]interface{}{"success": true, "id": mem.ID})
}

func (h *Handler) HandleMemorySearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.memoryService == nil {
		JSONResponse(w, []interface{}{})
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		JSONResponse(w, []interface{}{})
		return
	}
	list, _ := h.memoryService.Search(r.Context(), q, 10)
	JSONResponse(w, list)
}

func (h *Handler) HandleMemoryOperation(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/memories/"):]
	if h.memoryService == nil {
		JSONError(w, "Memory service unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		mem, err := h.memoryService.Get(r.Context(), id)
		if err != nil {
			JSONError(w, err.Error(), http.StatusNotFound)
			return
		}
		JSONResponse(w, mem)
	case http.MethodDelete:
		h.memoryService.Delete(r.Context(), id)
		JSONResponse(w, map[string]bool{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
