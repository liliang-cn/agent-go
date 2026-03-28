package agent

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	"github.com/liliang-cn/agent-go/v2/pkg/rag/chunker"
	ragprocessor "github.com/liliang-cn/agent-go/v2/pkg/rag/processor"
	ragstore "github.com/liliang-cn/agent-go/v2/pkg/rag/store"
	"github.com/liliang-cn/agent-go/v2/pkg/router"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

// ============================================================
// Config structures for JSON/YAML style initialization
// ============================================================

// Config holds all agent configuration
type Config struct {
	Name         string `json:"name"`
	DBPath       string `json:"db_path,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	Debug        bool   `json:"debug,omitempty"`

	RAG    *RAGConfig    `json:"rag,omitempty"`
	MCP    *MCPConfig    `json:"mcp,omitempty"`
	Memory *MemoryConfig `json:"memory,omitempty"`
	Router *RouterConfig `json:"router,omitempty"`
	Skills *SkillsConfig `json:"skills,omitempty"`
	PTC    *PTCConfig    `json:"ptc,omitempty"`

	ProgressCallback ProgressCallback `json:"-"`
}

// RAGConfig holds RAG configuration
type RAGConfig struct {
	Enabled        bool   `json:"enabled"`
	ChunkSize      int    `json:"chunk_size,omitempty"`
	Overlap        int    `json:"overlap,omitempty"`
	DBPath         string `json:"db_path,omitempty"`
	Collection     string `json:"collection,omitempty"`
	EmbeddingModel string `json:"embedding_model,omitempty"`
}

// MCPConfig holds MCP configuration
type MCPConfig struct {
	Enabled     bool     `json:"enabled"`
	ConfigPaths []string `json:"config_paths,omitempty"`
}

// MemoryConfig holds Memory configuration
type MemoryConfig struct {
	Enabled          bool     `json:"enabled"`
	MemoryPath       string   `json:"memory_path,omitempty"`
	StoreType        string   `json:"store_type,omitempty"`        // "file", "vector", "hybrid"
	ReflectThreshold int      `json:"reflect_threshold,omitempty"` // auto-reflect after N new facts (0 = disabled)
	Mission          string   `json:"mission,omitempty"`           // MemoryBank mission statement
	Directives       []string `json:"directives,omitempty"`        // MemoryBank hard directives
}

// RouterConfig holds Router configuration
type RouterConfig struct {
	Enabled   bool    `json:"enabled"`
	Threshold float64 `json:"threshold,omitempty"`
}

// SkillsConfig holds Skills configuration
type SkillsConfig struct {
	Enabled bool     `json:"enabled"`
	Paths   []string `json:"paths,omitempty"`
}

// PTCConfig is defined in ptc_integration.go

// ============================================================
// Builder - chainable configuration without explicit Build()
// ============================================================

// Builder allows chainable agent configuration.
// Assign to (*Service, error) to build - no explicit Build() needed!
type Builder struct {
	name              string
	agentgoCfg        *config.Config
	dbPath            string
	systemPrompt      string
	debug             bool
	progressCb        ProgressCallback
	permissionHandler PermissionHandler
	permissionPolicy  PermissionPolicy

	// Custom LLM service (optional - if not set, uses global pool)
	llmService domain.Generator
	// Custom Embedder service (optional - used with custom LLM for RAG/Memory)
	embedService domain.Embedder

	enableRAG       bool
	ragCfg          RAGConfig
	enableMCP       bool
	mcpCfgPaths     []string
	enableMemory    bool
	memoryCfg       MemoryConfig
	enableRouter    bool
	routerThreshold float64
	enableSkills    bool
	skillsPaths     []string
	enablePTC       bool
	ptcCfg          *PTCConfig

	tools        []*Tool // pre-registered via WithTool/WithTools
	extraModules []Module

	// cached result
	svc *Service
	err error
}

type modelInfoProvider interface {
	GetModelName() string
	GetBaseURL() string
}

// New creates a new agent builder for chainable configuration.
// No Build() needed - just assign to (*Service, error)!
//
// Example:
//
//	// Simple agent
//	svc, err := agent.New("my-agent")
//
//	// Chainable configuration
//	svc, err := agent.New("my-agent").WithRAG().WithMemory().WithMCP()
func New(name string) *Builder {
	return &Builder{name: name}
}

// WithRAG enables RAG processor
func (b *Builder) WithRAG(opts ...AgentGoption) *Builder {
	b.enableRAG = true
	cfg := RAGConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	b.ragCfg = cfg
	return b
}

// WithMCP enables MCP tools
func (b *Builder) WithMCP(opts ...MCPOption) *Builder {
	b.enableMCP = true
	cfg := MCPConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	b.mcpCfgPaths = cfg.ConfigPaths
	return b
}

// WithMemory enables memory service
func (b *Builder) WithMemory(opts ...MemoryOption) *Builder {
	b.enableMemory = true
	cfg := MemoryConfig{} // defaults come from config file, not here
	for _, opt := range opts {
		opt(&cfg)
	}
	b.memoryCfg = cfg
	return b
}

// WithRouter enables semantic router
func (b *Builder) WithRouter(opts ...RouterOption) *Builder {
	b.enableRouter = true
	cfg := RouterConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	b.routerThreshold = cfg.Threshold
	return b
}

// WithSkills enables skills service
func (b *Builder) WithSkills(opts ...SkillsOption) *Builder {
	b.enableSkills = true
	cfg := SkillsConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	b.skillsPaths = cfg.Paths
	return b
}

// WithPTC enables PTC
func (b *Builder) WithPTC(opts ...PTCOption) *Builder {
	b.enablePTC = true
	cfg := &PTCConfig{Enabled: true, MaxToolCalls: 20, Timeout: 600 * time.Second}
	for _, opt := range opts {
		opt(cfg)
	}
	b.ptcCfg = cfg
	return b
}

// WithModule registers an additional Module whose tools are self-registered
// into the ToolRegistry before PTC sync. Use this to add custom RAG, Memory,
// or domain-specific tool sets without modifying the builder internals.
//
//	agent.New("bot").
//	    WithModule(NewRAGModule(proc, nil)).
//	    WithModule(NewMemoryModule(memSvc, nil)).
//	    Build()
func (b *Builder) WithModule(mod Module) *Builder {
	b.extraModules = append(b.extraModules, mod)
	return b
}

// WithDBPath sets database path
func (b *Builder) WithDBPath(path string) *Builder {
	b.dbPath = path
	return b
}

// WithSystemPrompt sets the agent system prompt.
func (b *Builder) WithSystemPrompt(prompt string) *Builder {
	b.systemPrompt = prompt
	return b
}

// WithPrompt is a concise alias for WithSystemPrompt.
func (b *Builder) WithPrompt(prompt string) *Builder {
	b.systemPrompt = prompt
	return b
}

// WithDebug enables or disables debug mode.
// Called with no arguments (WithDebug()) it defaults to true.
//
//	agent.New("bot").WithDebug()           // enable
//	agent.New("bot").WithDebug(false)      // disable
//	agent.New("bot").WithDebug(os.Getenv("DEBUG") != "")
func (b *Builder) WithDebug(on ...bool) *Builder {
	if len(on) == 0 {
		b.debug = true
	} else {
		b.debug = on[0]
	}
	return b
}

// WithProgressCallback sets the progress callback.
func (b *Builder) WithProgressCallback(cb ProgressCallback) *Builder {
	b.progressCb = cb
	return b
}

// WithProgress is a concise alias for WithProgressCallback.
func (b *Builder) WithProgress(cb ProgressCallback) *Builder {
	b.progressCb = cb
	return b
}

// WithPermissionHandler installs a runtime permission handler for tool execution.
func (b *Builder) WithPermissionHandler(handler PermissionHandler) *Builder {
	b.permissionHandler = handler
	return b
}

// WithPermissionPolicy overrides the policy that decides which tools need approval.
func (b *Builder) WithPermissionPolicy(policy PermissionPolicy) *Builder {
	b.permissionPolicy = policy
	return b
}

// WithConfig sets agentgo config
func (b *Builder) WithConfig(cfg *config.Config) *Builder {
	b.agentgoCfg = cfg
	return b
}

// WithAgentName sets the global agent name used in built-in prompts and team names.
// This overrides the agent.name field from config file or environment.
// Must be called before Build() to take effect.
func (b *Builder) WithAgentName(name string) *Builder {
	if b.agentgoCfg == nil {
		b.agentgoCfg = &config.Config{}
	}
	b.agentgoCfg.Agent.Name = name
	return b
}

// WithLLM sets a custom LLM service for the agent.
// This overrides the default LLM from the global pool configured in agentgo.toml.
//
// The provided LLM must implement the domain.Generator interface.
//
// Example:
//
//	svc, err := agent.New("my-agent").
//	    WithLLM(myCustomLLM).
//	    Build()
func (b *Builder) WithLLM(llm domain.Generator) *Builder {
	b.llmService = llm
	return b
}

// WithEmbedder sets a custom embedding service for RAG and memory.
// This is optional - if not set, the global pool's embedder will be used.
// You typically need to provide this when using a custom LLM.
//
// Example:
//
//	svc, err := agent.New("my-agent").
//	    WithLLM(myCustomLLM).
//	    WithEmbedder(myCustomEmbedder).
//	    WithRAG().
//	    Build()
func (b *Builder) WithEmbedder(embedder domain.Embedder) *Builder {
	b.embedService = embedder
	return b
}

// WithTool adds a single tool to the agent inline in the builder chain.
// Tools registered here are available at Build() time, before PTC sync,
// so they are reachable via callTool() in JS sandboxes as well.
//
//	svc, err := agent.New("bot").
//	    WithPTC().
//	    WithTool(agent.NewTool("weather", "Get weather", handler)).
//	    WithTool(agent.BuildTool("search").Description("...").Handler(h).Build()).
//	    Build()
func (b *Builder) WithTool(tool *Tool) *Builder {
	if tool != nil {
		b.tools = append(b.tools, tool)
	}
	return b
}

// WithTools adds multiple tools inline in the builder chain.
//
//	svc, err := agent.New("bot").
//	    WithTools(tool1, tool2, tool3).
//	    Build()
func (b *Builder) WithTools(tools ...*Tool) *Builder {
	for _, t := range tools {
		if t != nil {
			b.tools = append(b.tools, t)
		}
	}
	return b
}

// Build constructs the Service. Called automatically on assignment.
func (b *Builder) Build() (*Service, error) {
	if b.svc != nil || b.err != nil {
		return b.svc, b.err
	}
	b.svc, b.err = b.build()
	return b.svc, b.err
}

// Unpack allows direct assignment to (*Service, error)
func (b *Builder) Unpack() (*Service, error) {
	return b.Build()
}

// Get builds and returns the Service (alias for Build)
func (b *Builder) Get() (*Service, error) {
	return b.Build()
}

func (b *Builder) build() (*Service, error) {
	if b.name == "" {
		return nil, fmt.Errorf("agent name is required")
	}

	agentgoCfg := b.agentgoCfg
	var err error
	if agentgoCfg == nil {
		agentgoCfg, err = config.Load("")
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	// Determine LLM service: use custom if provided, otherwise get from global pool
	var llmSvc domain.Generator
	if b.llmService != nil {
		llmSvc = b.llmService
	} else {
		// Initialize global pool for LLM
		globalPool := services.GetGlobalPoolService()
		if err := globalPool.Initialize(context.Background(), agentgoCfg); err != nil {
			return nil, fmt.Errorf("failed to initialize pool: %w", err)
		}
		llmSvc, err = globalPool.GetLLMService()
		if err != nil {
			return nil, fmt.Errorf("failed to get LLM: %w", err)
		}
	}

	// Determine Embedder service: use custom if provided, otherwise try global pool
	var embedSvc domain.Embedder
	if b.embedService != nil {
		embedSvc = b.embedService
	} else {
		// Try to get embedder from global pool (may not be available)
		globalPool := services.GetGlobalPoolService()
		// Only initialize if not already initialized (when custom LLM was provided)
		if err := globalPool.Initialize(context.Background(), agentgoCfg); err != nil {
			log.Printf("[INFO] Embedding service not available: %v", err)
		} else if emb, err := globalPool.GetEmbeddingService(context.Background()); err == nil {
			embedSvc = emb
		}
	}

	// Build MCP
	var mcpSvc *mcp.Service
	var mcpAdapter MCPToolExecutor
	if b.enableMCP {
		mcpCfg := &agentgoCfg.MCP
		if len(b.mcpCfgPaths) > 0 {
			loadedCfg, loadErr := config.LoadMCPConfig(b.mcpCfgPaths...)
			if loadErr != nil {
				return nil, fmt.Errorf("failed to load MCP config: %w", loadErr)
			}
			mcpCfg = loadedCfg
		}
		mcpSvc, err = mcp.NewService(mcpCfg, llmSvc)
		if err != nil {
			log.Printf("[WARN] Failed to create MCP service: %v", err)
		} else {
			if startErr := mcpSvc.StartServers(context.Background(), nil); startErr != nil {
				log.Printf("[WARN] Failed to start MCP servers: %v", startErr)
			}
			mcpAdapter = &mcpToolAdapter{service: mcpSvc}
		}
	}

	// Build Memory
	var memSvc domain.MemoryService
	var memoryStoreType string
	if b.enableMemory {
		memSvc, memoryStoreType, err = b.buildMemoryService(agentgoCfg, embedSvc, llmSvc)
		if err != nil {
			return nil, fmt.Errorf("failed to create memory service: %w", err)
		}
	}

	// Build RAG
	var ragProcessor domain.Processor
	if b.enableRAG {
		if embedSvc == nil {
			log.Printf("[WARN] RAG requires embedding model, but none available. RAG disabled.")
		} else {
			ragProcessor, err = b.buildRAGProcessor(agentgoCfg, embedSvc, llmSvc, memSvc)
			if err != nil {
				return nil, fmt.Errorf("failed to create RAG processor: %w", err)
			}
		}
	}

	// Build Router
	var routerSvc *router.Service
	if b.enableRouter {
		routerCfg := router.DefaultConfig()
		if b.routerThreshold > 0 {
			routerCfg.Threshold = b.routerThreshold
		}
		routerSvc, err = router.NewService(embedSvc, routerCfg)
		if err == nil {
			_ = routerSvc.RegisterDefaultIntents()
		}
	}

	// Build Skills
	var skillsSvc *skills.Service
	if b.enableSkills {
		skillsSvc, err = b.buildSkillsService(agentgoCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create skills service: %w", err)
		}
	}

	// DB Path
	dbPath := b.dbPath
	if dbPath == "" {
		dbPath = agentgoCfg.AgentDBPath()
	}

	// Create service
	svc, err := NewService(llmSvc, mcpAdapter, ragProcessor, dbPath, memSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to create service: %w", err)
	}
	svc.cfg = agentgoCfg
	svc.memoryStoreType = memoryStoreType

	// Apply debug config: either from WithDebug() builder call or global agentgoCfg.Debug (e.g. from DEBUG=1 env var)
	if agentgoCfg.Debug {
		b.debug = true
	}
	svc.SetDebug(b.debug)

	// Register module tools into the unified ToolRegistry.
	// Built-in modules (RAG, Memory) are registered first, then any extra
	// modules added via WithModule(). All registered tools are available to
	// both collectAllAvailableTools() and PTC's callTool().
	if ragProcessor != nil {
		ragMod := NewRAGModule(ragProcessor, svc.addRAGSources)
		if err := ragMod.RegisterTools(svc.toolRegistry); err != nil {
			return nil, fmt.Errorf("rag module registration failed: %w", err)
		}
	}
	if memSvc != nil && svc.shouldExposeMemoryTools() {
		memMod := NewMemoryModule(memSvc, svc.markRunMemorySaved, svc.resolveMemoryQueryContextFromContext)
		if err := memMod.RegisterTools(svc.toolRegistry); err != nil {
			return nil, fmt.Errorf("memory module registration failed: %w", err)
		}
	}
	for _, mod := range b.extraModules {
		if err := mod.RegisterTools(svc.toolRegistry); err != nil {
			return nil, fmt.Errorf("module %q registration failed: %w", mod.ID(), err)
		}
	}

	// Register search_available_tools built-in tool
	searchToolDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "search_available_tools",
			Description: "Search the catalog for available tools. If 'instruction' is provided, the tool will automatically execute the found tool. Use 'scope' to narrow the search to a specific MCP server (e.g. 'mcp_filesystem') or skill name.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Keywords to search for in tool name or description.",
					},
					"instruction": map[string]interface{}{
						"type":        "string",
						"description": "(Optional) A clear instruction of what action to perform. If provided, the system will select and execute the best matching tool.",
					},
					"scope": map[string]interface{}{
						"type":        "string",
						"description": "(Optional) Limit search to a specific MCP server prefix (e.g. 'mcp_filesystem', 'mcp_websearch') or skill ID.",
					},
				},
				"required": []string{"query"},
			},
		},
	}
	svc.toolRegistry.Register(searchToolDef, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		queryStr, _ := args["query"].(string)
		instruction, _ := args["instruction"].(string)
		scope, _ := args["scope"].(string)
		return svc.SearchAndExecute(ctx, queryStr, instruction, scope)
	}, CategoryCustom)

	// Register tools added inline via WithTool/WithTools. This runs after built-in
	// modules but before PTC sync so all tools are reachable via callTool() in JS.
	for _, t := range b.tools {
		svc.Register(t)
	}

	if modelName, baseURL := resolveServiceModelInfo(llmSvc, agentgoCfg); modelName != "" || baseURL != "" {
		svc.SetModelInfo(modelName, baseURL)
	}

	svc.agent.SetName(b.name)
	svc.registry.Register(svc.agent)

	if b.systemPrompt != "" {
		svc.SetAgentInstructions(b.systemPrompt)
	}
	if b.progressCb != nil {
		svc.SetProgressCallback(b.progressCb)
	}
	if b.permissionHandler != nil {
		svc.SetPermissionHandler(b.permissionHandler)
	}
	if b.permissionPolicy != nil {
		svc.SetPermissionPolicy(b.permissionPolicy)
	}

	// PTC
	if b.enablePTC && b.ptcCfg != nil {
		// Build the PTC router: MCP and Skills are dynamic providers; all static
		// tools (RAG, Memory, custom) are synced from the ToolRegistry below.
		routerOpts := buildPTCRouterOptions(mcpAdapter, skillsSvc)
		ptcRouter := ptc.NewAgentGoRouter(routerOpts...)
		// Sync registry tools into the ptcRouter so callTool() can reach them.
		svc.toolRegistry.SyncToPTCRouter(ptcRouter)

		// Register tool search tools in PTC router for deferred tool discovery
		for _, ts := range GetToolSearchTools() {
			ptcRouter.RegisterTool(ts.Function.Name, &ptc.ToolInfo{
				Name:        ts.Function.Name,
				Description: ts.Function.Description,
				Parameters:  ts.Function.Parameters,
			}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				query, _ := args["query"].(string)
				if query == "" {
					return nil, fmt.Errorf("tool search requires a 'query' argument")
				}
				searchType := "regex"
				if ts.Function.Name == "tool_search_tool_bm25" {
					searchType = "bm25"
				}
				results, err := svc.toolRegistry.ExecuteToolSearch(query, searchType)
				if err != nil {
					return nil, err
				}
				// Build tool_references result
				var refs []domain.ToolReference
				for _, t := range results {
					refs = append(refs, domain.ToolReference{ToolName: t.Function.Name})
					// Auto-activate the tool for this session (using empty session ID for PTC)
					svc.toolRegistry.ActivateForSession("", t.Function.Name)
				}
				return domain.ToolSearchResult{ToolReferences: refs}, nil
			})
		}

		ptcInteg, ptcErr := NewPTCIntegration(*b.ptcCfg, ptcRouter)
		if ptcErr != nil {
			return nil, fmt.Errorf("failed to create PTC integration: %w", ptcErr)
		}
		svc.SetPTC(ptcInteg)
	}

	if routerSvc != nil {
		svc.SetRouter(routerSvc)
	}
	if skillsSvc != nil {
		svc.SetSkillsService(skillsSvc)
	}
	if mcpSvc != nil {
		svc.SetMCPService(mcpSvc)
	}
	svc.SetMemoryScope(b.name, "", "")

	return svc, nil
}

func resolveServiceModelInfo(llmSvc domain.Generator, cfg *config.Config) (string, string) {
	if provider, ok := llmSvc.(modelInfoProvider); ok {
		return provider.GetModelName(), provider.GetBaseURL()
	}

	if cfg != nil && len(cfg.LLM.Providers) > 0 {
		p := cfg.LLM.Providers[0]
		return p.ModelName, p.BaseURL
	}

	return "", ""
}

func (b *Builder) buildMemoryService(agentgoCfg *config.Config, embedSvc domain.Embedder, llmSvc domain.Generator) (domain.MemoryService, string, error) {
	var memStore domain.MemoryStore
	var shadowStore domain.MemoryStore
	var err error

	storeType := config.NormalizeMemoryStoreType(b.memoryCfg.StoreType)
	if b.memoryCfg.StoreType == "" {
		storeType = agentgoCfg.GetMemoryStoreType()
	}

	memPath := b.memoryCfg.MemoryPath
	if memPath == "" {
		memPath = agentgoCfg.MemoryPrimaryPath()
	}

	// Warn if vector-only requested but no embedder (hybrid still works via text search)
	if storeType == config.MemoryStoreTypeVector && embedSvc == nil {
		log.Printf("[WARN] Memory store type 'vector' requires embedding model, but none available. Falling back to 'file'.")
		storeType = config.MemoryStoreTypeFile
	}

	memPath = normalizeFileMemoryPath(storeType, memPath, agentgoCfg)

	log.Printf("[DEBUG] Memory: storeType=%s, memPath=%s, embedSvc=%v", storeType, memPath, embedSvc != nil)

	switch storeType {
	case config.MemoryStoreTypeFile:
		memStore, err = store.NewFileMemoryStore(memPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create file memory store: %w", err)
		}
	case config.MemoryStoreTypeVector:
		memStore, err = store.NewMemoryStore(agentgoCfg.MemoryVectorDBPath())
		if err != nil {
			return nil, "", fmt.Errorf("failed to create vector memory store: %w", err)
		}
		if err := memStore.InitSchema(context.Background()); err != nil {
			return nil, "", fmt.Errorf("failed to init memory schema: %w", err)
		}
	case config.MemoryStoreTypeHybrid:
		fileStore, ferr := store.NewFileMemoryStore(memPath)
		if ferr != nil {
			return nil, "", fmt.Errorf("failed to create file memory store: %w", ferr)
		}
		// Wire LLM for Reflect() and IndexNavigator on the truth layer
		if llmSvc != nil {
			fileStore.WithLLM(llmSvc)
		}
		memStore = fileStore
		if sqliteStore, serr := store.NewMemoryStore(agentgoCfg.MemoryVectorDBPath()); serr == nil {
			_ = sqliteStore.InitSchema(context.Background())
			shadowStore = sqliteStore
		}
	default:
		return nil, "", fmt.Errorf("unsupported memory store type: %s", storeType)
	}

	memCfg := memory.DefaultConfig()
	if b.memoryCfg.ReflectThreshold > 0 {
		memCfg.ReflectThreshold = b.memoryCfg.ReflectThreshold
	}

	memSvc := memory.NewService(memStore, llmSvc, embedSvc, memCfg)
	if shadowStore != nil {
		memSvc.SetShadowIndex(shadowStore)
	}

	// Seed MemoryBank directives as high-priority preference memories
	if b.memoryCfg.Mission != "" || len(b.memoryCfg.Directives) > 0 {
		go func() {
			bCtx := context.Background()
			if b.memoryCfg.Mission != "" {
				_ = memSvc.Add(bCtx, &domain.Memory{
					Type:       domain.MemoryTypePreference,
					Content:    "Agent mission: " + b.memoryCfg.Mission,
					Importance: 1.0,
					SourceType: domain.MemorySourceUserInput,
					CreatedAt:  time.Now(),
				})
			}
			for _, d := range b.memoryCfg.Directives {
				_ = memSvc.Add(bCtx, &domain.Memory{
					Type:       domain.MemoryTypePreference,
					Content:    "Directive: " + d,
					Importance: 1.0,
					SourceType: domain.MemorySourceUserInput,
					CreatedAt:  time.Now(),
				})
			}
		}()
	}

	return memSvc, storeType.String(), nil
}

func normalizeFileMemoryPath(storeType config.MemoryStoreType, memPath string, agentgoCfg *config.Config) string {
	if storeType != config.MemoryStoreTypeFile && storeType != config.MemoryStoreTypeHybrid {
		return memPath
	}

	defaultPath := filepath.Join(agentgoCfg.DataDir(), "memories")
	if memPath == "" {
		return defaultPath
	}

	cleanPath := filepath.Clean(memPath)
	vectorPath := filepath.Clean(agentgoCfg.MemoryVectorDBPath())
	if cleanPath == vectorPath {
		return defaultPath
	}

	// File-backed memory store needs a directory. If the selected path still
	// looks like a DB file (for example after vector->file fallback), coerce it.
	if filepath.Ext(cleanPath) == ".db" {
		return defaultPath
	}

	return memPath
}

func (b *Builder) buildRAGProcessor(agentgoCfg *config.Config, embedSvc domain.Embedder, llmSvc domain.Generator, memSvc domain.MemoryService) (domain.Processor, error) {
	if !agentgoCfg.RAG.Enabled {
		return nil, nil
	}

	vectorStore, err := ragstore.NewVectorStore(ragstore.StoreConfig{
		Type:       "sqlite",
		Parameters: map[string]interface{}{"db_path": agentgoCfg.CortexDBPath()},
	})
	if err != nil {
		return nil, err
	}
	docStore := ragstore.NewDocumentStoreFor(vectorStore)
	return ragprocessor.New(embedSvc, llmSvc, chunker.New(), vectorStore, docStore, agentgoCfg, nil, memSvc), nil
}

func (b *Builder) buildSkillsService(agentgoCfg *config.Config) (*skills.Service, error) {
	skillsCfg := skills.DefaultConfig()
	paths := b.skillsPaths
	if len(paths) == 0 {
		paths = agentgoCfg.SkillsPaths()
	}
	skillsCfg.Paths = paths
	skillsCfg.DBPath = agentgoCfg.AgentDBPath()
	svc, err := skills.NewService(skillsCfg)
	if err != nil {
		return nil, err
	}
	_ = svc.LoadAll(context.Background())
	return svc, nil
}

// ============================================================
// Option types for nested configuration
// ============================================================

// AgentGoption modifies RAGConfig
type AgentGoption func(*RAGConfig)

// WithRAGEnabled sets RAG enabled status
func WithRAGEnabled(enabled bool) AgentGoption {
	return func(c *RAGConfig) { c.Enabled = enabled }
}

// WithRAGEmbeddingModel sets RAG embedding model
func WithRAGEmbeddingModel(model string) AgentGoption {
	return func(c *RAGConfig) { c.EmbeddingModel = model }
}

// MCPOption modifies MCPConfig
type MCPOption func(*MCPConfig)

// WithMCPConfigPaths sets MCP config file paths
func WithMCPConfigPaths(paths ...string) MCPOption {
	return func(c *MCPConfig) { c.ConfigPaths = paths }
}

// MemoryOption modifies MemoryConfig
type MemoryOption func(*MemoryConfig)

// WithMemoryPath sets memory storage path
func WithMemoryPath(path string) MemoryOption {
	return func(c *MemoryConfig) { c.MemoryPath = path }
}

// WithMemoryStoreType sets memory store type: "file", "vector", or "hybrid"
func WithMemoryStoreType(storeType string) MemoryOption {
	return func(c *MemoryConfig) { c.StoreType = storeType }
}

// WithMemoryReflect sets the auto-reflect threshold: after N new facts are stored,
// Reflect() is triggered asynchronously to consolidate them into observations.
// Set to 0 to disable auto-reflection.
func WithMemoryReflect(threshold int) MemoryOption {
	return func(c *MemoryConfig) { c.ReflectThreshold = threshold }
}

// WithMemoryHybrid enables hybrid store mode (FileMemoryStore as truth + RAG storage as shadow).
// This activates vector search acceleration alongside IndexNavigator logical retrieval.
func WithMemoryHybrid() MemoryOption {
	return func(c *MemoryConfig) { c.StoreType = "hybrid" }
}

// WithMemoryBank sets the agent's long-term mission statement and hard directives.
// Directives are stored as high-importance preference memories and injected into
// every prompt context with the highest priority.
func WithMemoryBank(mission string, directives []string) MemoryOption {
	return func(c *MemoryConfig) {
		c.Mission = mission
		c.Directives = directives
	}
}

// RouterOption modifies RouterConfig
type RouterOption func(*RouterConfig)

// WithRouterThreshold sets router threshold
func WithRouterThreshold(threshold float64) RouterOption {
	return func(c *RouterConfig) { c.Threshold = threshold }
}

// SkillsOption modifies SkillsConfig
type SkillsOption func(*SkillsConfig)

// WithSkillsPaths sets skills paths
func WithSkillsPaths(paths ...string) SkillsOption {
	return func(c *SkillsConfig) { c.Paths = paths }
}

// PTCOption modifies PTCConfig
type PTCOption func(*PTCConfig)

// WithPTCMaxToolCalls sets max tool calls
func WithPTCMaxToolCalls(n int) PTCOption { return func(c *PTCConfig) { c.MaxToolCalls = n } }

// WithPTCTimeout sets PTC timeout
func WithPTCTimeout(d time.Duration) PTCOption { return func(c *PTCConfig) { c.Timeout = d } }

// ============================================================
// TeamBuilder - chainable builder for TeamManager
// ============================================================

// TeamBuilder allows chainable TeamManager configuration.
type TeamBuilder struct {
	store       *Store
	dbPath      string
	agentName   string
	teamName    string
	cfg         *config.Config
	skipSeeding bool

	svc      *TeamManager
	buildErr error
}

// NewTeam creates a new team builder.
// Assign to (*TeamManager, error) to build - no explicit Build() needed!
//
// Example:
//
//	// Simple team manager
//	mgr, err := agent.NewTeam("~/.agentgo/data/agentgo.db")
//
//	// Chainable configuration
//	mgr, err := agent.NewTeam("~/.agentgo/data/agentgo.db").
//		WithAgentName("MyApp").
//		WithTeamName("My Team")
func NewTeam(dbPathOrStore interface{}) *TeamBuilder {
	b := &TeamBuilder{}
	switch v := dbPathOrStore.(type) {
	case string:
		b.dbPath = v
	case *Store:
		b.store = v
	default:
		b.buildErr = fmt.Errorf("dbPathOrStore must be a string path or *Store, got %T", v)
	}
	return b
}

// WithAgentName sets the global agent name used in built-in prompts and team names.
// This overrides the agent.name field from config file or environment.
// Must be called before Build() to take effect during initialization.
func (b *TeamBuilder) WithAgentName(name string) *TeamBuilder {
	b.agentName = name
	return b
}

// WithTeamName sets the default team name.
// If not set, defaults to "<AgentName> Team".
func (b *TeamBuilder) WithTeamName(name string) *TeamBuilder {
	b.teamName = name
	return b
}

// WithConfig sets the agentgo config.
func (b *TeamBuilder) WithConfig(cfg *config.Config) *TeamBuilder {
	b.cfg = cfg
	return b
}

// SkipSeeding skips seeding default built-in agents and team.
func (b *TeamBuilder) SkipSeeding() *TeamBuilder {
	b.skipSeeding = true
	return b
}

// Build creates the TeamManager.
func (b *TeamBuilder) Build() (*TeamManager, error) {
	if b.buildErr != nil {
		return nil, b.buildErr
	}
	if b.svc != nil {
		return b.svc, b.buildErr
	}

	if b.store == nil && b.dbPath == "" {
		b.buildErr = fmt.Errorf("dbPath or store is required")
		return nil, b.buildErr
	}

	if b.store == nil {
		var err error
		b.store, err = NewStore(b.dbPath)
		if err != nil {
			b.buildErr = fmt.Errorf("failed to create store: %w", err)
			return nil, b.buildErr
		}
	}

	mgr := NewTeamManager(b.store)

	// Apply config
	if b.cfg != nil {
		mgr.SetConfig(b.cfg)
	}

	// Apply agent name override
	if b.agentName != "" {
		mgr.SetAgentName(b.agentName)
	}

	// Apply team name override if set
	if b.teamName != "" {
		mgr.mu.Lock()
		if mgr.cfg == nil {
			mgr.cfg = &config.Config{}
		}
		mgr.cfg.Team.Name = b.teamName
		mgr.mu.Unlock()
	}

	// Seed default members
	if !b.skipSeeding {
		if err := mgr.SeedDefaultMembers(); err != nil {
			b.buildErr = err
			return nil, b.buildErr
		}
	}

	b.svc = mgr
	return b.svc, b.buildErr
}

// Unpack allows direct assignment to (*TeamManager, error)
func (b *TeamBuilder) Unpack() (*TeamManager, error) {
	return b.Build()
}

// Get builds and returns the TeamManager (alias for Build)
func (b *TeamBuilder) Get() (*TeamManager, error) {
	return b.Build()
}
