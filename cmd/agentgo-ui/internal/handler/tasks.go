package handler

import (
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) HandleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.teamManager == nil {
		JSONResponse(w, map[string]any{"tasks": []any{}})
		return
	}

	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	JSONResponse(w, map[string]any{
		"tasks": h.teamManager.ListUnifiedTasks(limit),
	})
}

func (h *Handler) HandleTaskOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.teamManager == nil {
		JSONError(w, "Team manager unavailable", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/tasks/"))
	if id == "" {
		JSONError(w, "task id is required", http.StatusBadRequest)
		return
	}
	task, err := h.teamManager.GetUnifiedTask(id)
	if err != nil {
		JSONError(w, err.Error(), http.StatusNotFound)
		return
	}
	JSONResponse(w, map[string]any{"task": task})
}
