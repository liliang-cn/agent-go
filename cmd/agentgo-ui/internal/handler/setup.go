package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/liliang-cn/agent-go/pkg/config"
	"github.com/liliang-cn/agent-go/pkg/pool"
	toml "github.com/pelletier/go-toml/v2"
)

type SetupHandler struct {
	cfg        *config.Config
	configPath string
}

type SetupProvider struct {
	Name           string `json:"name"`
	BaseURL        string `json:"baseUrl"`
	APIKey         string `json:"apiKey,omitempty"`
	ModelName      string `json:"modelName"`
	EmbeddingModel string `json:"embeddingModel,omitempty"`
	MaxConcurrency int    `json:"maxConcurrency"`
	Capability     int    `json:"capability"`
}

type SetupState struct {
	Initialized      bool            `json:"initialized"`
	ConfigPath       string          `json:"configPath"`
	Home             string          `json:"home"`
	WorkingDirectory string          `json:"workingDirectory"`
	ServerHost       string          `json:"serverHost"`
	ServerPort       int             `json:"serverPort"`
	MCPEnabled       bool            `json:"mcpEnabled"`
	SkillsPaths      []string        `json:"skillsPaths"`
	MemoryStoreType  string          `json:"memoryStoreType"`
	Providers        []SetupProvider `json:"providers"`
}

type ApplySetupRequest struct {
	Home            string        `json:"home"`
	ServerHost      string        `json:"serverHost"`
	ServerPort      int           `json:"serverPort"`
	MCPEnabled      bool          `json:"mcpEnabled"`
	MemoryStoreType string        `json:"memoryStoreType"`
	Provider        SetupProvider `json:"provider"`
}

func NewSetupHandler(cfg *config.Config, configPath string) *SetupHandler {
	return &SetupHandler{cfg: cfg, configPath: configPath}
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
	providers := make([]SetupProvider, 0, len(h.cfg.LLM.Providers))
	for _, p := range h.cfg.LLM.Providers {
		providers = append(providers, SetupProvider{
			Name:           p.Name,
			BaseURL:        p.BaseURL,
			ModelName:      p.ModelName,
			MaxConcurrency: p.MaxConcurrency,
			Capability:     p.Capability,
		})
	}

	return SetupState{
		Initialized:      h.isInitialized(),
		ConfigPath:       h.configPath,
		Home:             h.cfg.Home,
		WorkingDirectory: h.cfg.WorkspaceDir(),
		ServerHost:       h.cfg.Server.Host,
		ServerPort:       h.cfg.Server.Port,
		MCPEnabled:       h.cfg.MCP.Enabled,
		SkillsPaths:      h.cfg.SkillsPaths(),
		MemoryStoreType:  h.cfg.Memory.StoreType,
		Providers:        providers,
	}
}

func (h *SetupHandler) isInitialized() bool {
	content, err := os.ReadFile(h.configPath)
	if err != nil || len(content) == 0 {
		return false
	}
	var data map[string]interface{}
	if err := toml.Unmarshal(content, &data); err != nil {
		return false
	}
	setup, ok := data["setup"].(map[string]interface{})
	if !ok {
		return false
	}
	completed, _ := setup["completed"].(bool)
	return completed
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
	h.cfg.MCP.Enabled = req.MCPEnabled
	h.cfg.Memory.StoreType = req.MemoryStoreType
	
	// 极简 RAG 设置
	h.cfg.RAG.Enabled = req.Provider.EmbeddingModel != ""
	h.cfg.RAG.EmbeddingModel = req.Provider.EmbeddingModel

	h.cfg.ApplyHomeLayout()
	h.cfg.LLM.Enabled = true
	h.cfg.LLM.Strategy = pool.StrategyRoundRobin
	h.cfg.LLM.Providers = []pool.Provider{{
		Name:           req.Provider.Name,
		BaseURL:        req.Provider.BaseURL,
		Key:            req.Provider.APIKey,
		ModelName:      req.Provider.ModelName,
		MaxConcurrency: req.Provider.MaxConcurrency,
		Capability:     req.Provider.Capability,
	}}

	if err := h.saveSetupConfig(req); err != nil {
		JSONError(w, "Failed to save setup: "+err.Error(), http.StatusInternalServerError)
		return
	}

	JSONResponse(w, map[string]interface{}{
		"success":         true,
		"requiresRestart": true,
		"setup":           h.snapshot(),
	})
}

func (h *SetupHandler) saveSetupConfig(req ApplySetupRequest) error {
	dir := filepath.Dir(h.configPath)
	_ = os.MkdirAll(dir, 0755)

	data := map[string]interface{}{}
	if content, err := os.ReadFile(h.configPath); err == nil && len(content) > 0 {
		_ = toml.Unmarshal(content, &data)
	}

	data["home"] = h.cfg.Home
	setNested(data, []string{"server", "host"}, h.cfg.Server.Host)
	setNested(data, []string{"server", "port"}, h.cfg.Server.Port)
	setNested(data, []string{"mcp", "enabled"}, h.cfg.MCP.Enabled)
	setNested(data, []string{"memory", "store_type"}, h.cfg.Memory.StoreType)
	
	// 极简 RAG TOML
	setNested(data, []string{"rag", "enabled"}, h.cfg.RAG.Enabled)
	setNested(data, []string{"rag", "embedding_model"}, h.cfg.RAG.EmbeddingModel)

	// 清理旧路径（由系统自动推导）
	deleteNested(data, []string{"rag", "storage"})
	deleteNested(data, []string{"rag", "embedding"})
	deleteNested(data, []string{"memory", "memory_path"})
	deleteNested(data, []string{"cache", "path"})

	setNested(data, []string{"llm", "enabled"}, true)
	setNested(data, []string{"llm", "providers"}, []map[string]interface{}{{
		"name":            req.Provider.Name,
		"base_url":        req.Provider.BaseURL,
		"key":             req.Provider.APIKey,
		"model_name":      req.Provider.ModelName,
		"max_concurrency": req.Provider.MaxConcurrency,
		"capability":      req.Provider.Capability,
	}})
	
	setNested(data, []string{"setup", "completed"}, true)
	setNested(data, []string{"setup", "updated_at"}, time.Now().Format(time.RFC3339))

	content, err := toml.Marshal(data)
	if err != nil {
		return err
	}

	return os.WriteFile(h.configPath, content, 0644)
}

func setNested(m map[string]interface{}, keys []string, value interface{}) {
	for i := 0; i < len(keys)-1; i++ {
		key := keys[i]
		if _, ok := m[key]; !ok {
			m[key] = make(map[string]interface{})
		}
		m = m[key].(map[string]interface{})
	}
	m[keys[len(keys)-1]] = value
}

func deleteNested(m map[string]interface{}, keys []string) {
	for i := 0; i < len(keys)-1; i++ {
		key := keys[i]
		if next, ok := m[key].(map[string]interface{}); ok {
			m = next
		} else {
			return
		}
	}
	delete(m, keys[len(keys)-1])
}
