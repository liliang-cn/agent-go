package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
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

// HandleTaskOperation dispatches /api/tasks/:id and its sub-resources.
//
//	GET  /api/tasks/:id                  → task struct (status, stop_reason via events, cost via stats)
//	GET  /api/tasks/:id/checkpoints      → list of TaskCheckpoint
//	POST /api/tasks/:id/replay           → ResumeFromCheckpoint, returns refreshed task
//	GET  /api/tasks/:id/trace            → events + frames (raw task.Task)
func (h *Handler) HandleTaskOperation(w http.ResponseWriter, r *http.Request) {
	if h.teamManager == nil {
		JSONError(w, "Team manager unavailable", http.StatusServiceUnavailable)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if rest == "" {
		JSONError(w, "task id is required", http.StatusBadRequest)
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	taskID := strings.TrimSpace(parts[0])
	if taskID == "" {
		JSONError(w, "task id is required", http.StatusBadRequest)
		return
	}
	sub := ""
	if len(parts) == 2 {
		sub = strings.TrimSpace(parts[1])
	}

	switch sub {
	case "":
		h.handleTaskGet(w, r, taskID)
	case "checkpoints":
		h.handleTaskCheckpoints(w, r, taskID)
	case "replay":
		h.handleTaskReplay(w, r, taskID)
	case "trace":
		h.handleTaskTrace(w, r, taskID)
	default:
		JSONError(w, "unknown task sub-resource: "+sub, http.StatusNotFound)
	}
}

func (h *Handler) handleTaskGet(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.teamManager.GetUnifiedTask(taskID)
	if err != nil {
		JSONError(w, err.Error(), http.StatusNotFound)
		return
	}
	JSONResponse(w, map[string]any{"task": task})
}

// handleTaskCheckpoints lists the most-recent N checkpoints for a task
// (descending Seq). The UI uses this to render a timeline with replay
// buttons.
func (h *Handler) handleTaskCheckpoints(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	cps, err := h.teamManager.Tasks().ListCheckpoints(r.Context(), taskID, limit)
	if err != nil {
		JSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Compact the payload — strip the full message slice and just keep
	// summary fields so the list view stays small. The UI fetches a
	// specific checkpoint via the replay POST when it wants to act on it.
	type checkpointSummary struct {
		ID           string `json:"id"`
		TaskID       string `json:"task_id"`
		Seq          int    `json:"seq"`
		Round        int    `json:"round"`
		AfterTool    string `json:"after_tool,omitempty"`
		SessionID    string `json:"session_id,omitempty"`
		AgentName    string `json:"agent_name,omitempty"`
		MessageCount int    `json:"message_count"`
		FinalText    string `json:"final_text,omitempty"`
		CreatedAt    string `json:"created_at,omitempty"`
	}
	out := make([]checkpointSummary, 0, len(cps))
	for _, cp := range cps {
		out = append(out, checkpointSummary{
			ID:           cp.ID,
			TaskID:       cp.TaskID,
			Seq:          cp.Seq,
			Round:        cp.Round,
			AfterTool:    cp.AfterTool,
			SessionID:    cp.SessionID,
			AgentName:    cp.AgentName,
			MessageCount: len(cp.Messages),
			FinalText:    cp.FinalText,
			CreatedAt:    cp.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	JSONResponse(w, map[string]any{"checkpoints": out})
}

// handleTaskReplay re-runs a task from a checkpoint. Optional body:
//
//	{ "checkpoint_id": "abc", "follow_up": "and also do X" }
//
// Both fields are optional. With nothing supplied, replays from the
// latest checkpoint.
func (h *Handler) handleTaskReplay(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		CheckpointID string `json:"checkpoint_id"`
		FollowUp     string `json:"follow_up"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			JSONError(w, "invalid replay request: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	task, err := h.teamManager.Tasks().ResumeFromCheckpoint(r.Context(), taskID, agent.CheckpointResumeOptions{
		CheckpointID: strings.TrimSpace(body.CheckpointID),
		FollowUp:     strings.TrimSpace(body.FollowUp),
	})
	if err != nil {
		JSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	JSONResponse(w, map[string]any{"task": task})
}

// handleTaskTrace returns the raw task with full Events + Frames so the
// UI can render an event timeline / debug view. This is the heavy
// counterpart to the compact `GET /api/tasks/:id` (which is fine for the
// list / overview).
func (h *Handler) handleTaskTrace(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.teamManager.GetUnifiedTask(taskID)
	if err != nil {
		JSONError(w, err.Error(), http.StatusNotFound)
		return
	}
	JSONResponse(w, map[string]any{"task": task})
}
