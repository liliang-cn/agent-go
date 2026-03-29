package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v2/cmd/agentgo-ui/internal/handler"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v2/pkg/log"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/rag"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
	"github.com/spf13/cobra"
)

//go:embed dist
var staticFS embed.FS

var (
	uiPort    int
	uiHost    string
	uiVersion string = "dev"
)

func main() {
	if err := Execute(); err != nil {
		fmt.Println("Error:", err)
	}
}

func Execute() error {
	var rootCmd = &cobra.Command{
		Use:   "agentgo-ui",
		Short: "AgentGo Web UI Server",
		Long:  `AgentGo Web UI provides a web interface for interacting with AgentGo's RAG and Agent capabilities.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("failed to load configuration: %w", err)
			}

			// Initialize global pool service
			globalPoolService := services.GetGlobalPoolService()
			ctx := context.Background()
			if err := globalPoolService.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("failed to initialize global pool service: %w", err)
			}

			return nil
		},
		RunE: runServer,
	}

	rootCmd.PersistentFlags().IntVarP(&uiPort, "port", "p", 7127, "port to run the UI server on")
	rootCmd.PersistentFlags().StringVar(&uiHost, "host", "0.0.0.0", "host to bind the UI server to")
	rootCmd.Version = uiVersion

	return rootCmd.Execute()
}

func runServer(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Get pool service
	poolService := services.GetGlobalPoolService()

	// Get LLM and Embedder from pool
	llm, err := poolService.GetLLMService()
	if err != nil {
		return fmt.Errorf("failed to get LLM service: %w", err)
	}

	embedder, err := poolService.GetEmbeddingService(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get embedding service: %w", err)
	}

	// Create RAG client
	ragClient, err := rag.NewClient(cfg, embedder, llm, nil)
	if err != nil {
		return fmt.Errorf("failed to create RAG client: %w", err)
	}

	// Create Skills service
	skillsService, err := skills.NewService(&skills.Config{
		Enabled: true,
		Paths:   cfg.SkillsPaths(),
	})
	if err != nil {
		agentgolog.Warn("Failed to create skills service: %v", err)
	}
	if skillsService != nil {
		if loadErr := skillsService.LoadAll(context.Background()); loadErr != nil {
			agentgolog.Warn("Failed to load skills: %v", loadErr)
		}
	}

	// Create MCP service
	if err := os.MkdirAll(cfg.WorkspaceDir(), 0755); err != nil {
		return fmt.Errorf("failed to create workspace directory: %w", err)
	}

	mcpConfig := &mcp.Config{
		Enabled:           true,
		Servers:           cfg.MCP.Servers,
		ServersConfigPath: cfg.MCPServersPath(), // Keep for potential writing
		FilesystemDirs:    cfg.MCP.FilesystemDirs,
		LoadedServers:     mcp.GetBuiltInServers(cfg.MCP.FilesystemDirs),
	}

	// Merge configurations from all paths
	for _, path := range cfg.MCPServersPaths() {
		if _, err := os.Stat(path); err == nil {
			agentgolog.Infof("Loading MCP servers from %s", path)
			tempCfg := mcp.DefaultConfig()
			tempCfg.ServersConfigPath = path
			if loadErr := tempCfg.LoadServersFromJSON(); loadErr == nil {
				for name, srv := range tempCfg.LoadedServers {
					mcpConfig.LoadedServers[name] = srv
				}
			}
		}
	}

	var mcpService *mcp.Service
	mcpService, err = mcp.NewService(mcpConfig, llm)
	if err != nil {
		agentgolog.Warn("Failed to create MCP service: %v", err)
	} else {
		if startErr := mcpService.StartServers(context.Background(), nil); startErr != nil {
			agentgolog.Warn("Failed to start MCP servers: %v", startErr)
		}
	}

	// Create Memory service
	var memoryStore domain.MemoryStore
	switch cfg.GetMemoryStoreType() {
	case config.MemoryStoreTypeCortex:
		memoryStore, err = store.NewCortexMemoryStore(cfg.MemoryPrimaryPath())
	default:
		memoryStore, err = store.NewFileMemoryStore(cfg.Memory.MemoryPath)
	}
	if err != nil {
		agentgolog.Warn("Failed to create memory store: %v", err)
	}
	if memoryStore != nil {
		if initErr := memoryStore.InitSchema(context.Background()); initErr != nil {
			agentgolog.Warn("Failed to init memory store schema: %v", initErr)
		}
	}
	var memoryService *memory.Service
	if memoryStore != nil {
		memoryService = memory.NewService(memoryStore, llm, embedder, memory.DefaultConfig())
	}

	var teamManager *agent.TeamManager

	// Create Agent service using Builder
	agentgolog.Infof("Creating agent service with Builder...")
	b := agent.New("AgentGo Frontdesk").
		WithSystemPrompt("You are the system Frontdesk and captain agent. You can interact with users, and delegate tasks to specialized agents using the tools provided.").
		WithDebug().
		WithPTC().
		WithMCP().
		WithMemory().
		WithSkills().
		WithConfig(cfg)

	// Only enable RAG if enabled in config
	if cfg.RAG.Enabled {
		b = b.WithRAG()
	}

	agentService, err := b.Build()
	if err != nil {
		agentgolog.Warn("Failed to create agent service: %v", err)
	} else {
		agentgolog.Infof("Agent service created successfully")

		// Initialize TeamManager
		agentDBPath := cfg.AgentDBPath()
		agentStore, storeErr := agent.NewStore(agentDBPath)
		if storeErr != nil {
			agentgolog.Warn("Failed to create agent store: %v", storeErr)
		} else {
			teamManager = agent.NewTeamManager(agentStore)
			teamManager.SetConfig(cfg)
			if err := teamManager.SeedDefaultMembers(); err != nil {
				agentgolog.Warn("Failed to seed default team members: %v", err)
			}
			teamManager.RegisterCaptainTools(agentService)
			agentgolog.Infof("Team manager and captain-agent tools initialized")
		}
	}

	// Create handler
	h := handler.New(cfg, ragClient, skillsService, mcpService, memoryService, agentService, teamManager, llm, embedder)

	// Create API router
	mux := http.NewServeMux()

	// RAG endpoints
	mux.HandleFunc("/api/query", h.HandleQuery)
	mux.HandleFunc("/api/documents", h.HandleDocuments)
	mux.HandleFunc("/api/documents/", h.HandleDocumentOperation)
	mux.HandleFunc("/api/collections", h.HandleCollections)
	mux.HandleFunc("/api/status", h.HandleStatus)
	mux.HandleFunc("/api/chat", h.HandleChat)
	mux.HandleFunc("/api/chat/sessions", h.HandleChatSessions)
	mux.HandleFunc("/api/chat/session/", h.HandleChatSessionMessages)
	mux.HandleFunc("/api/chat/multi", h.HandleMultiAgentChat)
	mux.HandleFunc("/api/teams/tasks", h.HandleTeamTasks)
	mux.HandleFunc("/api/teams", h.HandleTeams)
	mux.HandleFunc("/api/ingest", h.HandleIngest)

	// Skills endpoints
	mux.HandleFunc("/api/skills", h.HandleSkillsList)
	mux.HandleFunc("/api/skills/add", h.HandleSkillsAdd)
	mux.HandleFunc("/api/skills/", h.HandleSkillsOperation)

	// MCP endpoints
	mux.HandleFunc("/api/mcp/servers", h.HandleMCPServers)
	mux.HandleFunc("/api/mcp/tools", h.HandleMCPTools)
	mux.HandleFunc("/api/mcp/add", h.HandleMCPAddServer)
	mux.HandleFunc("/api/mcp/call", h.HandleMCPCallTool)

	// Memory endpoints
	mux.HandleFunc("/api/memories", h.HandleMemories)
	mux.HandleFunc("/api/memories/add", h.HandleMemoryAdd)
	mux.HandleFunc("/api/memories/search", h.HandleMemorySearch)
	mux.HandleFunc("/api/memories/", h.HandleMemoryOperation)

	// Agent endpoints
	mux.HandleFunc("/api/agent/run", h.HandleAgentRun)
	mux.HandleFunc("/api/agent/stream", h.HandleAgentStream)

	mux.HandleFunc("/api/agents", h.HandleAgents)
	mux.HandleFunc("/api/agents/", h.HandleAgentOperation)
	mux.HandleFunc("/api/ops/logs", h.HandleOpsLogs)

	mux.HandleFunc("/api/config", h.ConfigHandler.HandleConfig)
	mux.HandleFunc("/api/setup", h.SetupHandler.HandleSetup)

	// Serve static files
	distFS, err := fs.Sub(staticFS, "dist")
	if err != nil {
		return fmt.Errorf("failed to load static files: %w", err)
	}

	// SPA fallback - serve index.html for unmatched routes
	fileServer := http.FileServer(http.FS(distFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Try to serve static file first
		if r.URL.Path != "/" {
			if _, err := distFS.Open(r.URL.Path[1:]); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// Serve index.html for SPA routes
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})

	addr := fmt.Sprintf("%s:%d", uiHost, uiPort)
	agentgolog.Infof("Starting AgentGo UI server on %s", addr)

	server := &http.Server{
		Addr:         addr,
		Handler:      corsMiddleware(mux),
		ReadTimeout:  300 * time.Second,
		WriteTimeout: 600 * time.Second,
		IdleTimeout:  600 * time.Second,
	}

	return server.ListenAndServe()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
