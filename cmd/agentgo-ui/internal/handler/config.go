package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/liliang-cn/agent-go/pkg/config"
	toml "github.com/pelletier/go-toml/v2"
)

type ConfigHandler struct {
	cfg        *config.Config
	configPath string
	mu         sync.Mutex
}

func NewConfigHandler(cfg *config.Config, configPath string) *ConfigHandler {
	return &ConfigHandler{cfg: cfg, configPath: configPath}
}

// ConfigSnapshot 提供给 UI 的精简配置视图
type ConfigSnapshot struct {
	Home              string `json:"home"`
	Debug             bool   `json:"debug"`
	ServerHost        string `json:"serverHost"`
	ServerPort        int    `json:"serverPort"`
	RAGEnabled        bool   `json:"ragEnabled"`
	RAGEmbeddingModel string `json:"ragEmbeddingModel"`
	MemoryStoreType   string `json:"memoryStoreType"`
	CacheStoreType    string `json:"cacheStoreType"`
	DataDir           string `json:"dataDir"`
	WorkspaceDir      string `json:"workspaceDir"`
	MCPServersPath    string `json:"mcpServersPath"`
}

type UpdateConfigRequest struct {
	Home              *string `json:"home,omitempty"`
	Debug             *bool   `json:"debug,omitempty"`
	ServerHost        *string `json:"serverHost,omitempty"`
	ServerPort        *int    `json:"serverPort,omitempty"`
	RAGEnabled        *bool   `json:"ragEnabled,omitempty"`
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
	if req.RAGEnabled != nil {
		h.cfg.RAG.Enabled = *req.RAGEnabled
	}
	if req.RAGEmbeddingModel != nil {
		h.cfg.RAG.EmbeddingModel = *req.RAGEmbeddingModel
	}
	if req.MemoryStoreType != nil {
		if err := h.cfg.SetMemoryStoreTypeString(*req.MemoryStoreType); err != nil {
			JSONError(w, "Invalid memory store type: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// 核心：重新推导所有路径
	h.cfg.ApplyHomeLayout()

	if err := h.saveConfig(); err != nil {
		JSONError(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	JSONResponse(w, map[string]interface{}{
		"success": true,
		"config":  h.snapshot(),
	})
}

func (h *ConfigHandler) saveConfig() error {
	dir := filepath.Dir(h.configPath)
	_ = os.MkdirAll(dir, 0755)

	// 只保存核心配置，保持 TOML 简洁
	data := map[string]interface{}{
		"home":  h.cfg.Home,
		"debug": h.cfg.Debug,
		"server": map[string]interface{}{
			"host": h.cfg.Server.Host,
			"port": h.cfg.Server.Port,
		},
		"llm": map[string]interface{}{
			"enabled":   h.cfg.LLM.Enabled,
			"strategy":  h.cfg.LLM.Strategy,
			"providers": h.cfg.LLM.Providers,
		},
		"rag": map[string]interface{}{
			"enabled":         h.cfg.RAG.Enabled,
			"embedding_model": h.cfg.RAG.EmbeddingModel,
		},
		"memory": map[string]interface{}{
			"store_type": h.cfg.GetMemoryStoreType().String(),
		},
		"cache": map[string]interface{}{
			"store_type": h.cfg.Cache.StoreType,
		},
	}

	content, err := toml.Marshal(data)
	if err != nil {
		return err
	}

	return os.WriteFile(h.configPath, content, 0644)
}

func (h *ConfigHandler) snapshot() ConfigSnapshot {
	return ConfigSnapshot{
		Home:              h.cfg.Home,
		Debug:             h.cfg.Debug,
		ServerHost:        h.cfg.Server.Host,
		ServerPort:        h.cfg.Server.Port,
		RAGEnabled:        h.cfg.RAG.Enabled,
		RAGEmbeddingModel: h.cfg.RAG.EmbeddingModel,
		MemoryStoreType:   h.cfg.GetMemoryStoreType().String(),
		CacheStoreType:    h.cfg.Cache.StoreType,
		DataDir:           h.cfg.DataDir(),
		WorkspaceDir:      h.cfg.WorkspaceDir(),
		MCPServersPath:    h.cfg.MCPServersPath(),
	}
}
