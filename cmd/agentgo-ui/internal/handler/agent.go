package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/pkg/agent"
	agentgolog "github.com/liliang-cn/agent-go/pkg/log"
)

// Agent handlers

func (h *Handler) HandleAgentRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Message string `json:"message"`
		Debug   bool   `json:"debug"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	if req.Message == "" {
		JSONError(w, "Message required", http.StatusBadRequest)
		return
	}
	if h.agentService == nil {
		JSONError(w, "Agent service unavailable", http.StatusServiceUnavailable)
		return
	}

	start := time.Now()
	result, err := h.agentService.Run(r.Context(), req.Message)
	dur := time.Since(start).Milliseconds()

	if err != nil {
		JSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	JSONResponse(w, map[string]interface{}{
		"response":    result.Text(),
		"duration_ms": dur,
	})
}

func (h *Handler) HandleAgentStream(w http.ResponseWriter, r *http.Request) {
	agentgolog.Infof("Agent stream request from %s", r.RemoteAddr)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Message   string `json:"message"`
		Debug     bool   `json:"debug"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		JSONError(w, "Message required", http.StatusBadRequest)
		return
	}

	svc := h.agentService
	if svc == nil {
		JSONError(w, "Agent service unavailable", http.StatusServiceUnavailable)
		return
	}

	if req.Debug {
		svc.SetDebug(true)
		defer svc.SetDebug(false)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		JSONError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	events, err := svc.RunStream(r.Context(), req.Message)
	if err != nil {
		data, _ := json.Marshal(map[string]string{"type": "error", "content": err.Error()})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		return
	}

	for evt := range events {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		payload := map[string]interface{}{
			"type":       string(evt.Type),
			"content":    evt.Content,
			"agent_name": evt.AgentName,
		}
		if evt.ToolName != "" {
			payload["tool_name"] = evt.ToolName
		}
		if evt.ToolArgs != nil {
			payload["tool_args"] = evt.ToolArgs
		}
		if evt.ToolResult != nil {
			payload["tool_result"] = evt.ToolResult
		}
		if evt.Round > 0 {
			payload["round"] = evt.Round
			payload["debug_type"] = evt.DebugType
		}

		data, _ := json.Marshal(payload)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return
		}
		flusher.Flush()
	}

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (h *Handler) HandleAgents(w http.ResponseWriter, r *http.Request) {
	if h.squadManager == nil {
		JSONError(w, "Agent manager unavailable", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		agents, err := h.squadManager.ListAgents()
		if err != nil {
			JSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		JSONResponse(w, map[string]interface{}{"agents": agents})
	case http.MethodPost:
		var req struct {
			SquadID               string   `json:"squad_id"`
			Name                  string   `json:"name"`
			Kind                  string   `json:"kind"`
			Description           string   `json:"description"`
			Instructions          string   `json:"instructions"`
			Model                 string   `json:"model"`
			PreferredProvider     string   `json:"preferred_provider"`
			PreferredModel        string   `json:"preferred_model"`
			RequiredLLMCapability int      `json:"required_llm_capability"`
			MCPTools              []string `json:"mcp_tools"`
			Skills                []string `json:"skills"`
			EnableRAG             bool     `json:"enable_rag"`
			EnableMemory          bool     `json:"enable_memory"`
			EnablePTC             bool     `json:"enable_ptc"`
			EnableMCP             bool     `json:"enable_mcp"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			JSONError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			JSONError(w, "Agent name required", http.StatusBadRequest)
			return
		}

		agentModel, err := h.squadManager.CreateAgent(r.Context(), &agent.AgentModel{
			ID:                    uuid.New().String(),
			TeamID:                strings.TrimSpace(req.SquadID),
			Name:                  strings.TrimSpace(req.Name),
			Kind:                  agent.AgentKind(strings.TrimSpace(req.Kind)),
			Description:           strings.TrimSpace(req.Description),
			Instructions:          strings.TrimSpace(req.Instructions),
			Model:                 strings.TrimSpace(req.Model),
			PreferredProvider:     strings.TrimSpace(req.PreferredProvider),
			PreferredModel:        strings.TrimSpace(req.PreferredModel),
			RequiredLLMCapability: req.RequiredLLMCapability,
			MCPTools:              req.MCPTools,
			Skills:                req.Skills,
			EnableRAG:             req.EnableRAG,
			EnableMemory:          req.EnableMemory,
			EnablePTC:             req.EnablePTC,
			EnableMCP:             req.EnableMCP,
			CreatedAt:             time.Now(),
			UpdatedAt:             time.Now(),
		})
		if err != nil {
			h.appendOpsLog(OpsLogEntry{
				AgentName: strings.TrimSpace(req.Name),
				Kind:      "create",
				Status:    "error",
				Title:     "Create agent failed",
				Detail:    err.Error(),
			})
			JSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.appendOpsLog(OpsLogEntry{
			AgentName: agentModel.Name,
			Kind:      "create",
			Status:    "success",
			Title:     "Created agent",
			Detail:    agentModel.Description,
			Metadata: map[string]interface{}{
				"model":                   agentModel.Model,
				"preferred_provider":      agentModel.PreferredProvider,
				"preferred_model":         agentModel.PreferredModel,
				"required_llm_capability": agentModel.RequiredLLMCapability,
			},
		})
		w.WriteHeader(http.StatusCreated)
		JSONResponse(w, agentModel)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) HandleAgentOperation(w http.ResponseWriter, r *http.Request) {
	if h.squadManager == nil {
		JSONError(w, "Agent manager unavailable", http.StatusServiceUnavailable)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(path, "/")
	name, err := url.PathUnescape(parts[0])
	if err != nil {
		JSONError(w, "Invalid agent name", http.StatusBadRequest)
		return
	}

	switch {
	case len(parts) == 3 && parts[1] == "dispatch" && parts[2] == "stream" && r.Method == http.MethodPost:
		var req struct {
			Instruction string `json:"instruction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			JSONError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Instruction) == "" {
			JSONError(w, "Instruction required", http.StatusBadRequest)
			return
		}

		events, err := h.squadManager.DispatchTaskStream(r.Context(), name, req.Instruction)
		if err != nil {
			JSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			JSONError(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		for evt := range events {
			select {
			case <-r.Context().Done():
				return
			default:
			}

			payload := map[string]interface{}{
				"type":       string(evt.Type),
				"content":    evt.Content,
				"agent_name": evt.AgentName,
			}
			if evt.ToolName != "" {
				payload["tool_name"] = evt.ToolName
			}
			if evt.ToolArgs != nil {
				payload["tool_args"] = evt.ToolArgs
			}
			if evt.ToolResult != nil {
				payload["tool_result"] = evt.ToolResult
			}
			if len(evt.Sources) > 0 {
				payload["sources"] = evt.Sources
			}
			if evt.Round > 0 {
				payload["round"] = evt.Round
				payload["debug_type"] = evt.DebugType
			}

			data, _ := json.Marshal(payload)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	case len(parts) == 2 && parts[1] == "dispatch" && r.Method == http.MethodPost:
		var req struct {
			Instruction string `json:"instruction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			JSONError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Instruction) == "" {
			JSONError(w, "Instruction required", http.StatusBadRequest)
			return
		}

		start := time.Now()
		result, err := h.squadManager.DispatchTask(r.Context(), name, req.Instruction)
		if err != nil {
			h.appendOpsLog(OpsLogEntry{
				AgentName: name,
				Kind:      "dispatch",
				Status:    "error",
				Title:     "Dispatch failed",
				Detail:    err.Error(),
			})
			JSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		duration := time.Since(start).Milliseconds()
		h.appendOpsLog(OpsLogEntry{
			AgentName:  name,
			Kind:       "dispatch",
			Status:     "success",
			Title:      "Dispatch completed",
			Detail:     result,
			DurationMS: &duration,
			Metadata: map[string]interface{}{
				"instruction": req.Instruction,
			},
		})
		agentModel, _ := h.squadManager.GetAgentByName(name)
		JSONResponse(w, map[string]interface{}{
			"success":     true,
			"agent":       agentModel,
			"response":    result,
			"duration_ms": duration,
		})
	case len(parts) == 1 && r.Method == http.MethodGet:
		agentModel, err := h.squadManager.GetAgentByName(name)
		if err != nil {
			JSONError(w, err.Error(), http.StatusNotFound)
			return
		}
		JSONResponse(w, agentModel)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
