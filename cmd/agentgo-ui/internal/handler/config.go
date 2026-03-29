package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

type ConfigHandler struct {
	cfg *config.Config
	mu  sync.Mutex
}

func NewConfigHandler(cfg *config.Config) *ConfigHandler {
	return &ConfigHandler{cfg: cfg}
}

// ConfigSnapshot 提供给 UI 的精简配置视图
type ConfigSnapshot struct {
	Home              string   `json:"home"`
	Debug             bool     `json:"debug"`
	ServerHost        string   `json:"serverHost"`
	ServerPort        int      `json:"serverPort"`
	RAGEnabled        bool     `json:"ragEnabled"`
	RAGEmbeddingModel string   `json:"ragEmbeddingModel"`
	MemoryStoreType   string   `json:"memoryStoreType"`
	CacheStoreType    string   `json:"cacheStoreType"`
	AgentDBPath       string   `json:"agentDbPath"`
	RAGDBPath         string   `json:"ragDbPath"`
	MemoryPath        string   `json:"memoryPath"`
	DataDir           string   `json:"dataDir"`
	WorkspaceDir      string   `json:"workspaceDir"`
	MCPAllowedDirs    []string `json:"mcpAllowedDirs"`
	MCPServersPath    string   `json:"mcpServersPath"`
	SkillsPaths       []string `json:"skillsPaths"`
}

type UpdateConfigRequest struct {
	Home              *string `json:"home,omitempty"`
	Debug             *bool   `json:"debug,omitempty"`
	ServerHost        *string `json:"serverHost,omitempty"`
	ServerPort        *int    `json:"serverPort,omitempty"`
	RAGEmbeddingModel *string `json:"ragEmbeddingModel,omitempty"`
	MemoryStoreType   *string `json:"memoryStoreType,omitempty"`
}

func (h *ConfigHandler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		JSONResponse(w, h.snapshot())
	case http.MethodPut:
		h.updateConfig(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *ConfigHandler) updateConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	pendingEmbeddingModel := ""
	hasEmbeddingModelUpdate := req.RAGEmbeddingModel != nil

	if req.Home != nil {
		h.cfg.Home = *req.Home
	}
	if req.Debug != nil {
		h.cfg.Debug = *req.Debug
	}
	if req.ServerHost != nil {
		h.cfg.Server.Host = *req.ServerHost
	}
	if req.ServerPort != nil {
		h.cfg.Server.Port = *req.ServerPort
	}
	if req.RAGEmbeddingModel != nil {
		pendingEmbeddingModel = *req.RAGEmbeddingModel
	}
	if req.MemoryStoreType != nil {
		if err := h.cfg.SetMemoryStoreTypeString(*req.MemoryStoreType); err != nil {
			JSONError(w, "Invalid memory store type: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// 核心：重新推导所有路径
	h.cfg.ApplyHomeLayout()
	if req.Debug != nil {
		if err := saveDBConfigValue(h.cfg, "debug", strconv.FormatBool(h.cfg.Debug)); err != nil {
			JSONError(w, "Failed to save debug: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.ServerHost != nil {
		if err := saveDBConfigValue(h.cfg, "server.host", h.cfg.Server.Host); err != nil {
			JSONError(w, "Failed to save server host: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.ServerPort != nil {
		if err := saveDBConfigValue(h.cfg, "server.port", strconv.Itoa(h.cfg.Server.Port)); err != nil {
			JSONError(w, "Failed to save server port: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if hasEmbeddingModelUpdate {
		if err := saveDBConfigValue(h.cfg, "rag.embedding_model", pendingEmbeddingModel); err != nil {
			JSONError(w, "Failed to save embedding model: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.MemoryStoreType != nil {
		if err := saveDBConfigValue(h.cfg, "memory.store_type", h.cfg.GetMemoryStoreType().String()); err != nil {
			JSONError(w, "Failed to save memory store type: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := h.cfg.LoadDBBackedRuntime(); err != nil {
		JSONError(w, "Failed to reload runtime config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	JSONResponse(w, map[string]interface{}{
		"success": true,
		"config":  h.snapshot(),
	})
}

func (h *ConfigHandler) snapshot() ConfigSnapshot {
	_ = h.cfg.LoadDBBackedRuntime()
	return ConfigSnapshot{
		Home:              h.cfg.Home,
		Debug:             h.cfg.Debug,
		ServerHost:        h.cfg.Server.Host,
		ServerPort:        h.cfg.Server.Port,
		RAGEnabled:        h.cfg.RAG.Enabled,
		RAGEmbeddingModel: h.cfg.RAG.EmbeddingModel,
		MemoryStoreType:   h.cfg.GetMemoryStoreType().String(),
		CacheStoreType:    h.cfg.Cache.StoreType,
		AgentDBPath:       h.cfg.AgentDBPath(),
		RAGDBPath:         h.cfg.CortexDBPath(),
		MemoryPath:        h.cfg.MemoryPrimaryPath(),
		DataDir:           h.cfg.DataDir(),
		WorkspaceDir:      h.cfg.WorkspaceDir(),
		MCPAllowedDirs:    append([]string(nil), h.cfg.MCP.FilesystemDirs...),
		MCPServersPath:    h.cfg.MCPServersPath(),
		SkillsPaths:       h.cfg.SkillsPaths(),
	}
}
