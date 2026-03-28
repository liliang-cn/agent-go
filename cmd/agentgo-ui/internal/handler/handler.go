package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/rag"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

// Handler holds all services
type Handler struct {
	cfg            *config.Config
	ragClient      *rag.Client
	skillsService  *skills.Service
	mcpService     *mcp.Service
	memoryService  *memory.Service
	agentService   *agent.Service
	teamManager   *agent.TeamManager
	llm            domain.Generator
	embedder       domain.Embedder
	ConfigHandler  *ConfigHandler
	SetupHandler   *SetupHandler
	aiChatMu       sync.RWMutex
	aiChatSessions map[string]string
	opsLogMu       sync.RWMutex
	opsLogs        []OpsLogEntry
}

// New creates a new handler
func New(cfg *config.Config, ragClient *rag.Client, skillsService *skills.Service,
	mcpService *mcp.Service, memoryService *memory.Service,
	agentService *agent.Service, teamManager *agent.TeamManager, llm domain.Generator, embedder domain.Embedder) *Handler {

	configPath := resolveConfigPath(cfg)
	configHandler := NewConfigHandler(cfg, configPath)
	setupHandler := NewSetupHandler(cfg, configPath)

	return &Handler{
		cfg:            cfg,
		ConfigHandler:  configHandler,
		SetupHandler:   setupHandler,
		ragClient:      ragClient,
		skillsService:  skillsService,
		mcpService:     mcpService,
		memoryService:  memoryService,
		agentService:   agentService,
		teamManager:   teamManager,
		llm:            llm,
		embedder:       embedder,
		aiChatSessions: make(map[string]string),
		opsLogs:        make([]OpsLogEntry, 0, 64),
	}
}

// Helper functions
func JSONResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func JSONError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// Make sure Handler implements http.Handler
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Not found", http.StatusNotFound)
}

// HandleStatus returns system status including Agent, LLM, RAG, MCP info
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// LLM info
	var llmInfo map[string]interface{}
	if h.llm != nil {
		llmInfo = map[string]interface{}{
			"enabled": true,
		}
	} else {
		llmInfo = map[string]interface{}{"enabled": false}
	}

	// Embedder info
	var embedderInfo map[string]interface{}
	if h.embedder != nil {
		embedderInfo = map[string]interface{}{
			"enabled": true,
		}
	} else {
		embedderInfo = map[string]interface{}{"enabled": false}
	}

	// RAG info
	ragEnabled := h.cfg.RAG.Enabled
	var ragInfo map[string]interface{}
	if ragEnabled && h.ragClient != nil {
		stats, _ := h.ragClient.GetStats(r.Context())
		ragInfo = map[string]interface{}{
			"enabled":   true,
			"db_path":   h.cfg.CortexDBPath(),
			"documents": stats.TotalDocuments,
			"chunks":    stats.TotalChunks,
		}
	} else {
		ragInfo = map[string]interface{}{"enabled": false}
	}

	// MCP info
	mcpEnabled := h.cfg.MCP.Enabled
	var mcpInfo map[string]interface{}
	if mcpEnabled && h.mcpService != nil {
		servers := h.mcpService.ListServers()
		toolCount := 0
		for _, s := range servers {
			toolCount += s.ToolCount
		}
		mcpInfo = map[string]interface{}{
			"enabled":     true,
			"servers":     len(servers),
			"tools":       toolCount,
			"server_list": servers,
		}
	} else {
		mcpInfo = map[string]interface{}{"enabled": false}
	}

	// Skills info
	skillsEnabled := h.skillsService != nil
	var skillsInfo map[string]interface{}
	if skillsEnabled {
		list, _ := h.skillsService.ListSkills(r.Context(), skills.SkillFilter{})
		skillsInfo = map[string]interface{}{
			"enabled": true,
			"count":   len(list),
		}
	} else {
		skillsInfo = map[string]interface{}{"enabled": false}
	}

	// Memory info
	memoryEnabled := h.memoryService != nil
	var memoryInfo map[string]interface{}
	if memoryEnabled {
		memories, _, _ := h.memoryService.List(r.Context(), 100, 0)
		memoryInfo = map[string]interface{}{
			"enabled": true,
			"count":   len(memories),
		}
	} else {
		memoryInfo = map[string]interface{}{"enabled": false}
	}

	// Agent info
	agentEnabled := h.agentService != nil
	var agentInfo map[string]interface{}
	if agentEnabled {
		agentInfo = map[string]interface{}{
			"enabled": true,
		}
	} else {
		agentInfo = map[string]interface{}{"enabled": false}
	}

	// Get version from config
	version := "dev"
	if h.cfg != nil {
		// Try to get version from build info or config
		version = "1.0.0"
	}

	// Build providers list with detailed info from pool service
	poolService := services.GetGlobalPoolService()
	providers := []map[string]interface{}{}

	// Get LLM pool status
	llmStatus := poolService.GetLLMStatus()
	for name, st := range llmStatus {
		status := "disabled"
		if st.Healthy {
			status = "enabled"
		}
		providers = append(providers, map[string]interface{}{
			"name":            name,
			"status":          status,
			"type":            "llm",
			"model":           st.ModelName,
			"healthy":         st.Healthy,
			"active_requests": st.ActiveRequests,
			"max_concurrency": st.MaxConcurrency,
			"capability":      st.Capability,
		})
	}

	// Get Embedding pool status
	embedStatus := poolService.GetEmbeddingStatus()
	for name, st := range embedStatus {
		status := "disabled"
		if st.Healthy {
			status = "enabled"
		}
		providers = append(providers, map[string]interface{}{
			"name":            name,
			"status":          status,
			"type":            "embedding",
			"model":           st.ModelName,
			"healthy":         st.Healthy,
			"active_requests": st.ActiveRequests,
			"max_concurrency": st.MaxConcurrency,
			"capability":      st.Capability,
		})
	}

	// Add other services
	if enabled, ok := ragInfo["enabled"].(bool); ok && enabled {
		providers = append(providers, map[string]interface{}{
			"name":   "RAG",
			"status": "enabled",
			"type":   "rag",
		})
	}
	if enabled, ok := mcpInfo["enabled"].(bool); ok && enabled {
		providers = append(providers, map[string]interface{}{
			"name":   "MCP",
			"status": "enabled",
			"type":   "mcp",
		})
	}
	if enabled, ok := skillsInfo["enabled"].(bool); ok && enabled {
		providers = append(providers, map[string]interface{}{
			"name":   "Skills",
			"status": "enabled",
			"type":   "skills",
		})
	}
	if enabled, ok := memoryInfo["enabled"].(bool); ok && enabled {
		providers = append(providers, map[string]interface{}{
			"name":   "Memory",
			"status": "enabled",
			"type":   "memory",
		})
	}
	if enabled, ok := agentInfo["enabled"].(bool); ok && enabled {
		providers = append(providers, map[string]interface{}{
			"name":   "Agent",
			"status": "enabled",
			"type":   "agent",
		})
	}

	JSONResponse(w, map[string]interface{}{
		"status":    "running",
		"version":   version,
		"providers": providers,
		"llm":       llmInfo,
		"embedder":  embedderInfo,
		"rag":       ragInfo,
		"mcp":       mcpInfo,
		"skills":    skillsInfo,
		"memory":    memoryInfo,
		"agent":     agentInfo,
	})
}

func resolveConfigPath(cfg *config.Config) string {
	candidates := []string{
		filepath.Join(".", "agentgo.toml"),
		filepath.Join(cfg.Home, "agentgo.toml"),
		filepath.Join(cfg.Home, "config", "agentgo.toml"),
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			abs, absErr := filepath.Abs(candidate)
			if absErr == nil {
				return abs
			}
			return candidate
		}
	}

	fallback := filepath.Join(cfg.Home, "agentgo.toml")
	abs, err := filepath.Abs(fallback)
	if err == nil {
		return abs
	}
	return fallback
}
