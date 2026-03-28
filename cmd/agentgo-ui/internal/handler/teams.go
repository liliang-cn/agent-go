package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

type TeamResponse struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	LeadAgent   *agent.AgentModel   `json:"lead_agent,omitempty"`
	Captain     *agent.AgentModel   `json:"captain,omitempty"`
	Members     []*agent.AgentModel `json:"members"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

func (h *Handler) HandleTeams(w http.ResponseWriter, r *http.Request) {
	if h.teamManager == nil {
		JSONError(w, "Team manager unavailable", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		teams, err := h.teamManager.ListTeams()
		if err != nil {
			JSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		out := make([]TeamResponse, 0, len(teams))
		for _, t := range teams {
			members, err := h.teamManager.ListTeamAgentsByTeam(t.ID)
			if err != nil {
				JSONError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			resp := TeamResponse{
				ID:          t.ID,
				Name:        t.Name,
				Description: t.Description,
				CreatedAt:   t.CreatedAt,
				UpdatedAt:   t.UpdatedAt,
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

		JSONResponse(w, map[string]any{"teams": out})
	case http.MethodPost:
		var req struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			JSONError(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		team, err := h.teamManager.CreateTeam(r.Context(), &agent.Team{
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
		JSONResponse(w, team)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
