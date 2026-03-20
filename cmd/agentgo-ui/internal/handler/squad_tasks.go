package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func (h *Handler) HandleSquadTasks(w http.ResponseWriter, r *http.Request) {
	if h.squadManager == nil {
		JSONError(w, "Squad manager unavailable", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		squadID := strings.TrimSpace(r.URL.Query().Get("squad_id"))
		leadAgentName := strings.TrimSpace(r.URL.Query().Get("lead_agent_name"))
		if leadAgentName == "" {
			leadAgentName = strings.TrimSpace(r.URL.Query().Get("captain_name"))
		}
		afterRaw := strings.TrimSpace(r.URL.Query().Get("after"))
		limit := 20
		if limitRaw := strings.TrimSpace(r.URL.Query().Get("limit")); limitRaw != "" {
			if parsed, err := strconv.Atoi(limitRaw); err == nil && parsed > 0 && parsed <= 200 {
				limit = parsed
			}
		}

		var after time.Time
		if afterRaw != "" {
			if unixMillis, err := strconv.ParseInt(afterRaw, 10, 64); err == nil {
				after = time.UnixMilli(unixMillis)
			}
		}

		JSONResponse(w, map[string]any{
			"tasks": mapSharedTasksForAPI(listSquadTasks(h.squadManager, squadID, leadAgentName, after, limit)),
		})
	case http.MethodPost:
		var req struct {
			SquadID       string   `json:"squad_id"`
			LeadAgentName string   `json:"lead_agent_name"`
			CaptainName   string   `json:"captain_name"`
			Message       string   `json:"message"`
			AgentNames    []string `json:"agent_names"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			JSONError(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		leadAgentName := strings.TrimSpace(req.LeadAgentName)
		if leadAgentName == "" {
			leadAgentName = strings.TrimSpace(req.CaptainName)
		}
		task, err := h.squadManager.EnqueueSharedTaskForSquad(r.Context(), strings.TrimSpace(req.SquadID), leadAgentName, req.AgentNames, strings.TrimSpace(req.Message))
		if err != nil {
			JSONError(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusAccepted)
		JSONResponse(w, map[string]any{
			"task":        mapSharedTaskForAPI(task),
			"ack_message": task.AckMessage,
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func mapSharedTasksForAPI(tasks []*agent.SharedTask) []map[string]any {
	out := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, mapSharedTaskForAPI(task))
	}
	return out
}

func mapSharedTaskForAPI(task *agent.SharedTask) map[string]any {
	if task == nil {
		return nil
	}
	return map[string]any{
		"id":              task.ID,
		"squad_id":        task.SquadID,
		"captain_name":    task.CaptainName,
		"lead_agent_name": task.CaptainName,
		"agent_names":     task.AgentNames,
		"prompt":          task.Prompt,
		"ack_message":     task.AckMessage,
		"status":          task.Status,
		"queued_ahead":    task.QueuedAhead,
		"result_text":     task.ResultText,
		"results":         task.Results,
		"created_at":      task.CreatedAt,
		"started_at":      task.StartedAt,
		"finished_at":     task.FinishedAt,
	}
}

func listSquadTasks(manager *agent.SquadManager, squadID, leadAgentName string, after time.Time, limit int) []*agent.SharedTask {
	if strings.TrimSpace(squadID) != "" {
		return manager.ListSharedTasksForSquad(strings.TrimSpace(squadID), after, limit)
	}
	return manager.ListSharedTasks(strings.TrimSpace(leadAgentName), after, limit)
}
