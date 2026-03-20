package handler

import (
	"net/http"

	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

func (h *Handler) HandleSkillsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.skillsService == nil {
		JSONResponse(w, []interface{}{})
		return
	}
	list, _ := h.skillsService.ListSkills(r.Context(), skills.SkillFilter{})
	result := make([]map[string]interface{}, len(list))
	for i, s := range list {
		result[i] = map[string]interface{}{
			"id":              s.ID,
			"name":            s.Name,
			"description":     s.Description,
			"collection":      s.Collection,
			"collection_path": s.CollectionPath,
			"enabled":         s.Enabled,
			"user_invocable":  s.UserInvocable,
			"path":            s.Path,
		}
	}
	JSONResponse(w, result)
}

func (h *Handler) HandleSkillsAdd(w http.ResponseWriter, r *http.Request) {
	JSONError(w, "Create SKILL.md in .skills/", http.StatusNotImplemented)
}

func (h *Handler) HandleSkillsOperation(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/skills/"):]
	if h.skillsService == nil {
		JSONError(w, "Skills service unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		skill, err := h.skillsService.GetSkill(r.Context(), id)
		if err != nil {
			JSONError(w, err.Error(), http.StatusNotFound)
			return
		}
		JSONResponse(w, map[string]interface{}{
			"id": skill.ID, "name": skill.Name, "description": skill.Description,
			"collection": skill.Collection, "collection_path": skill.CollectionPath,
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
