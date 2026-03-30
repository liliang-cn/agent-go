package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

type SetupHandler struct {
	cfg *config.Config
}

type SetupProvider struct {
	Name           string   `json:"name"`
	BaseURL        string   `json:"baseUrl"`
	APIKey         string   `json:"apiKey,omitempty"`
	ModelName      string   `json:"modelName"`
	Models         []string `json:"models,omitempty"`
	EmbeddingModel string   `json:"embeddingModel,omitempty"`
	MaxConcurrency int      `json:"maxConcurrency"`
	Capability     int      `json:"capability"`
}

type SetupState struct {
	Initialized      bool            `json:"initialized"`
	Home             string          `json:"home"`
	WorkingDirectory string          `json:"workingDirectory"`
	ServerHost       string          `json:"serverHost"`
	ServerPort       int             `json:"serverPort"`
	SkillsPaths      []string        `json:"skillsPaths"`
	MemoryStoreType  string          `json:"memoryStoreType"`
	Providers        []SetupProvider `json:"providers"`
}

type ApplySetupRequest struct {
	Home            string        `json:"home"`
	ServerHost      string        `json:"serverHost"`
	ServerPort      int           `json:"serverPort"`
	MemoryStoreType string        `json:"memoryStoreType"`
	Provider        SetupProvider `json:"provider"`
}

func NewSetupHandler(cfg *config.Config) *SetupHandler {
	return &SetupHandler{cfg: cfg}
}

func (h *SetupHandler) HandleSetup(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		JSONResponse(w, h.snapshot())
	case http.MethodPut:
		h.apply(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *SetupHandler) snapshot() SetupState {
	_ = h.cfg.LoadDBBackedRuntime()
	providers := make([]SetupProvider, 0, len(h.cfg.LLM.Providers))
	for _, p := range h.cfg.LLM.Providers {
		providers = append(providers, SetupProvider{
			Name:           p.Name,
			BaseURL:        p.BaseURL,
			ModelName:      p.ModelName,
			Models:         append([]string(nil), p.Models...),
			EmbeddingModel: h.cfg.RAG.EmbeddingModel,
			MaxConcurrency: p.MaxConcurrency,
			Capability:     p.Capability,
		})
	}

	return SetupState{
		Initialized:      h.isInitialized(),
		Home:             h.cfg.Home,
		WorkingDirectory: h.cfg.WorkspaceDir(),
		ServerHost:       h.cfg.Server.Host,
		ServerPort:       h.cfg.Server.Port,
		SkillsPaths:      h.cfg.SkillsPaths(),
		MemoryStoreType:  h.cfg.GetMemoryStoreType().String(),
		Providers:        providers,
	}
}

func (h *SetupHandler) isInitialized() bool {
	return runtimeInitialized(h.cfg)
}

func (h *SetupHandler) apply(w http.ResponseWriter, r *http.Request) {
	var req ApplySetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	h.cfg.Home = req.Home
	h.cfg.Server.Host = req.ServerHost
	h.cfg.Server.Port = req.ServerPort
	if err := h.cfg.SetMemoryStoreTypeString(req.MemoryStoreType); err != nil {
		JSONError(w, "Invalid memory store type: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.cfg.ApplyHomeLayout()
	if err := saveDBConfigValue(h.cfg, "server.host", h.cfg.Server.Host); err != nil {
		JSONError(w, "Failed to save server host: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := saveDBConfigValue(h.cfg, "server.port", strconv.Itoa(h.cfg.Server.Port)); err != nil {
		JSONError(w, "Failed to save server port: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := saveDBConfigValue(h.cfg, "memory.store_type", h.cfg.GetMemoryStoreType().String()); err != nil {
		JSONError(w, "Failed to save memory store type: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := saveSetupProviderState(h.cfg, req.Provider); err != nil {
		JSONError(w, "Failed to save setup providers: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.cfg.LoadDBBackedRuntime(); err != nil {
		JSONError(w, "Failed to reload runtime config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	JSONResponse(w, map[string]interface{}{
		"success":         true,
		"requiresRestart": true,
		"setup":           h.snapshot(),
	})
}
