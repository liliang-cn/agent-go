package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

type SquadResponse struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	LeadAgent   *agent.AgentModel   `json:"lead_agent,omitempty"`
	Captain     *agent.AgentModel   `json:"captain,omitempty"`
	Members     []*agent.AgentModel `json:"members"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

func (h *Handler) HandleSquads(w http.ResponseWriter, r *http.Request) {
	if h.squadManager == nil {
		JSONError(w, "Squad manager unavailable", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		squads, err := h.squadManager.ListSquads()
		if err != nil {
			JSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		out := make([]SquadResponse, 0, len(squads))
		for _, squad := range squads {
			members, err := h.squadManager.ListSquadAgentsForSquad(squad.ID)
			if err != nil {
				JSONError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			resp := SquadResponse{
				ID:          squad.ID,
				Name:        squad.Name,
				Description: squad.Description,
				CreatedAt:   squad.CreatedAt,
				UpdatedAt:   squad.UpdatedAt,
				Members:     make([]*agent.AgentModel, 0, len(members)),
			}
			for _, member := range members {
				resp.Members = append(resp.Members, member)
				if member.Kind == agent.AgentKindCaptain && resp.Captain == nil {
					resp.Captain = member
					resp.LeadAgent = member
				}
			}
			out = append(out, resp)
		}

		JSONResponse(w, map[string]any{"squads": out})
	case http.MethodPost:
		var req struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			JSONError(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		squad, err := h.squadManager.CreateSquad(r.Context(), &agent.Squad{
			ID:          uuid.New().String(),
			Name:        strings.TrimSpace(req.Name),
			Description: strings.TrimSpace(req.Description),
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		})
		if err != nil {
			JSONError(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusCreated)
		JSONResponse(w, squad)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
