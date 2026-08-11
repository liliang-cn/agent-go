package agent

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/mcp"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/poolsvc"
	"github.com/liliang-cn/agent-go/v3/pkg/rag/chunker"
	ragprocessor "github.com/liliang-cn/agent-go/v3/pkg/rag/processor"
	ragstore "github.com/liliang-cn/agent-go/v3/pkg/rag/store"
	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
	"github.com/liliang-cn/agent-go/v3/pkg/skills"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
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
	Skills *SkillsConfig `json:"skills,omitempty"`

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
	Enabled    bool   `json:"enabled"`
	MemoryPath string `json:"memory_path,omitempty"`
	// StoreType selects the backend: one of the built-ins ("file", "cortex",
	// "memoryflow", "graphflow") or any name passed to RegisterMemoryStore.
	StoreType        string   `json:"store_type,omitempty"`
	ReflectThreshold int      `json:"reflect_threshold,omitempty"` // auto-reflect after N new facts (0 = disabled)
	Mission          string   `json:"mission,omitempty"`           // MemoryBank mission statement
	Directives       []string `json:"directives,omitempty"`        // MemoryBank hard directives

	// RetrievalDisabled turns off memory retrieval (the read side) while the
	// write side keeps recording. Zero value = retrieval on.
	RetrievalDisabled bool `json:"retrieval_disabled,omitempty"`

	// AutoStoreDisabled turns off the post-run memory extraction pass (the
	// write side) while retrieval keeps reading. Zero value = auto-store on.
	AutoStoreDisabled bool `json:"auto_store_disabled,omitempty"`

	// DSN is the connection string handed to a registered store factory.
	DSN string `json:"dsn,omitempty"`
	// Options is free-form configuration handed to a registered store factory.
	Options map[string]string `json:"options,omitempty"`
	// Store, when set, is used verbatim: no factory, no registry lookup.
	Store domain.MemoryStore `json:"-"`
}

// SkillsConfig holds Skills configuration
type SkillsConfig struct {
	Enabled bool     `json:"enabled"`
	Paths   []string `json:"paths,omitempty"`
}

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
	observers         []Observer
	// Custom LLM service (optional - if not set, uses global pool)
	llmService domain.Generator
	// Custom Embedder service (optional - used with custom LLM for RAG/Memory)
	embedService domain.Embedder

	enableRAG         bool
	ragCfg            RAGConfig
	enableMCP         bool
	mcpCfgPaths       []string
	enableMemory      bool
	memoryCfg         MemoryConfig
	memoryService     domain.MemoryService
	registerGraphTool bool
	enableSkills      bool
	skillsPaths       []string
	requiredSkills    []string // Build() fails if any of these aren't installed
	toolPolicy        ToolExecutionPolicy

	tools        []*Tool // pre-registered via WithTool/WithTools
	extraModules []Module
	subagents    []SubagentSpec

	// Execution capabilities (all optional, zero-value = disabled)
	sandbox       sandbox.Sandbox
	enableDeliver bool
	autonomy      AutonomyProfile

	// cached result
	svc *Service
	err error
}

type modelIdentityProvider interface {
	GetModelName() string
	GetBaseURL() string
}

type fastModelProvider interface {
	IsFastModel() bool
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
	return &Builder{
		name: name,
	}
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
	cfg := MemoryConfig{} // defaults come from runtime config, not here
	for _, opt := range opts {
		opt(&cfg)
	}
	b.memoryCfg = cfg
	return b
}

// WithGraphMemory enables graph-aware mode: it uses the CortexDB GraphFlow
// memory store (entities + relations, not just vectors) AND registers the
// `graph_recall` tool so the agent loop can query the knowledge graph during
// reasoning. Extra MemoryOptions are applied on top of the graphflow default.
//
//	svc, _ := agent.New("assistant").WithGraphMemory().Build()
func (b *Builder) WithGraphMemory(opts ...MemoryOption) *Builder {
	merged := append([]MemoryOption{WithMemoryGraphFlow()}, opts...)
	b.WithMemory(merged...)
	b.registerGraphTool = true
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

// WithProgress is a concise alias for WithProgressCallback.
func (b *Builder) WithProgress(cb ProgressCallback) *Builder {
	b.progressCb = cb
	return b
}

// WithObserver registers one or more passive observability aspects on the
// service. Observers bracket model turns, tool calls, sub-agent runs, and
// terminal checkpoints with paired Start/End callbacks (see Observer). They
// cannot mutate or block a run — use hooks for that. Zero-overhead when unused.
func (b *Builder) WithObserver(obs ...Observer) *Builder {
	for _, o := range obs {
		if o != nil {
			b.observers = append(b.observers, o)
		}
	}
	return b
}

// WithConfig sets agentgo config
func (b *Builder) WithConfig(cfg *config.Config) *Builder {
	b.agentgoCfg = cfg
	return b
}

// WithLLM sets a custom LLM service for the agent.
// This overrides the default LLM from the global pool configured in agentgo.db.
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
// Tools registered here are available at Build() time,
// so they are reachable via callTool() in JS sandboxes as well.
//
//	svc, err := agent.New("bot").
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

func (b *Builder) build() (*Service, error) {
	if b.name == "" {
		return nil, fmt.Errorf("agent name is required")
	}

	agentgoCfg := b.agentgoCfg
	var err error
	if agentgoCfg == nil {
		agentgoCfg, err = config.Load()
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
		globalPool := poolsvc.Global()
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
		globalPool := poolsvc.Global()
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
	switch {
	case b.memoryService != nil:
		// Escape hatch: the embedder owns retrieval and injection too.
		memSvc = b.memoryService
		memoryStoreType = firstNonEmptyTaskString(b.memoryCfg.StoreType, "custom")
	case b.enableMemory:
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
	if b.toolPolicy.Default == "" && len(b.toolPolicy.Rules) == 0 {
		if db, dbErr := store.NewAgentGoDB(dbPath); dbErr == nil {
			if resources, resErr := db.ListResources(); resErr == nil {
				b.toolPolicy = ToolExecutionPolicyFromResources(resources)
			}
			_ = db.Close()
		}
	}

	// Create service
	svc, err := NewService(llmSvc, mcpAdapter, ragProcessor, dbPath, memSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to create service: %w", err)
	}
	svc.cfg = agentgoCfg
	svc.memoryStoreType = memoryStoreType
	if b.toolPolicy.Default != "" || len(b.toolPolicy.Rules) > 0 {
		svc.SetToolExecutionPolicy(b.toolPolicy)
	}

	// Apply debug config: either from WithDebug() builder call or global agentgoCfg.Debug (e.g. from DEBUG=1 env var)
	if agentgoCfg.Debug {
		b.debug = true
	}
	svc.SetDebug(b.debug)

	// Register module tools into the unified ToolRegistry.
	// Built-in modules (RAG, Memory) are registered first, then any extra
	// modules added via WithModule(). All registered tools are available to
	// collectAllAvailableTools().
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
			Description: "Search the catalog for available tools. If 'instruction' is provided, the tool will automatically execute the found tool. Use 'scope' to narrow the search to a specific MCP server (e.g. 'mcp_websearch') or skill name.",
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
						"description": "(Optional) Limit search to a specific MCP server prefix (e.g. 'mcp_websearch') or skill ID.",
					},
				},
				"required": []string{"query"},
			},
		},
	}
	svc.toolRegistry.RegisterWithMetadata(searchToolDef, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		queryStr, _ := args["query"].(string)
		instruction, _ := args["instruction"].(string)
		scope, _ := args["scope"].(string)
		// Enforced here rather than in the execution loop: the sandbox
		// calls this handler directly via callTool().
		if guidance, refused := admitToolDiscovery(ctx, queryStr); refused {
			return guidance, nil
		}
		return svc.SearchAndExecute(ctx, queryStr, instruction, scope)
	}, CategoryCustom, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel})

	// Register tools added inline via WithTool/WithTools. This runs after built-in
	// modules so all tools are reachable.
	for _, t := range b.tools {
		svc.Register(t)
	}

	if modelName, baseURL, isFastModel := resolveServiceModelInfo(llmSvc, agentgoCfg); modelName != "" || baseURL != "" {
		svc.SetModelInfo(modelName, baseURL, isFastModel)
	}

	svc.agent.SetName(b.name)
	svc.registry.Register(svc.agent)

	if b.systemPrompt != "" {
		svc.SetAgentInstructions(b.systemPrompt)
	}
	if b.progressCb != nil {
		svc.SetProgressCallback(b.progressCb)
	}
	if len(b.observers) > 0 {
		svc.RegisterObserver(b.observers...)
	}
	if b.permissionHandler != nil {
		svc.SetPermissionHandler(b.permissionHandler)
	}
	if b.permissionPolicy != nil {
		svc.SetPermissionPolicy(b.permissionPolicy)
	}

	if skillsSvc != nil {
		svc.SetSkillsService(skillsSvc)
	}
	// Fail fast if any explicitly-required skills are not installed.
	if len(b.requiredSkills) > 0 {
		if missing := svc.MissingSkills(b.requiredSkills...); len(missing) > 0 {
			return nil, fmt.Errorf("required skills not installed: %v (checked skills paths; install them under ~/.agentgo/skills or pass WithSkills paths)", missing)
		}
	}
	if mcpSvc != nil {
		svc.SetMCPService(mcpSvc)
	}
	svc.SetMemoryScope(b.name, "", "")

	// Baseline enforcement: every framework-built service gets the
	// agent-agnostic completion lints so the runtime rejects + re-prompts when
	// a model finishes on a plan ("now let me use the X skill...") or finishes
	// a file-producing task without ever writing the file. Both the UI's
	// agentService and every Manager agent route through here; agent-specific
	// lints are layered on top by applyBuiltInOutputLints. Registration is
	// idempotent.
	svc.RegisterOutputLint(NoPlanningOnlyFinish())
	svc.RegisterOutputLint(FileTaskMustWrite())
	// v3 acceptance gates (plan §4.5): no empty terminal answers, and a task
	// that asked for a delivery action cannot complete without evidence of it.
	svc.RegisterOutputLint(NonEmptyFinalAnswer())
	svc.RegisterOutputLint(TaskDeliveryContract())
	// ...and a task that asked the agent to set a reminder / add a schedule /
	// record a note cannot complete while the tool that does it sat unused.
	svc.RegisterOutputLint(RequestedActionContract())
	svc.RegisterOutputLint(NoToolScaffoldingAnswer())
	svc.RegisterOutputLint(DeliverableBlockMustCarryWork())

	// Graph-aware mode: expose the graph_recall tool so the loop can query the
	// knowledge graph. Registered when WithGraphMemory() opted in.
	if b.registerGraphTool {
		RegisterGraphRecallTool(svc)
	}

	// Wire the optional sandbox / autonomy / deliverable execution
	// capabilities. Each is opt-in via WithSandbox/WithAutonomy/etc.; tool sets
	// are registered on the unified registry so they're reachable by both the
	// LLM loop and PTC's callTool(). After registering, re-sync the PTC router
	// so the new tools are callable from sandboxed JS too.
	if b.sandbox != nil {
		svc.execSandbox = b.sandbox
		RegisterSandboxTools(svc, b.sandbox)
	}
	if b.enableDeliver && b.sandbox != nil {
		RegisterDeliverableTools(svc, b.sandbox)
	}
	if b.autonomy.MaxRounds > 0 || b.autonomy.LintRetryBudget > 0 || b.autonomy.Scratchpad {
		svc.defaultMaxTurns = b.autonomy.MaxRounds
		svc.lintRetryBudgetOverride = b.autonomy.LintRetryBudget
		if b.autonomy.Scratchpad {
			RegisterScratchpadTools(svc)
		}
	}
	// Sub-agents are tools, not orchestration: WithSubagents installs a single
	// `task(agent_name, prompt)` tool that runs the named sub-agent through
	// the same loop.
	if len(b.subagents) > 0 {
		RegisterSubagentTool(svc, b.subagents...)
	}
	return svc, nil
}

func resolveServiceModelInfo(llmSvc domain.Generator, cfg *config.Config) (string, string, bool) {
	if provider, ok := llmSvc.(modelIdentityProvider); ok {
		modelName := provider.GetModelName()
		baseURL := provider.GetBaseURL()
		if fastProvider, ok := llmSvc.(fastModelProvider); ok {
			return modelName, baseURL, fastProvider.IsFastModel()
		}
		return modelName, baseURL, domain.IsFastModelName(modelName)
	}

	if cfg != nil && len(cfg.LLM.Providers) > 0 {
		p := cfg.LLM.Providers[0]
		return p.ModelName, p.BaseURL, domain.IsFastModelName(p.ModelName)
	}

	return "", "", false
}

func (b *Builder) buildMemoryService(agentgoCfg *config.Config, embedSvc domain.Embedder, llmSvc domain.Generator) (domain.MemoryService, string, error) {
	var memStore domain.MemoryStore
	var err error

	storeType := config.NormalizeMemoryStoreType(b.memoryCfg.StoreType)
	if b.memoryCfg.StoreType == "" {
		storeType = agentgoCfg.GetMemoryStoreType()
	}

	memPath := b.memoryCfg.MemoryPath
	if memPath == "" {
		memPath = agentgoCfg.MemoryPrimaryPath()
	}

	memPath = normalizeFileMemoryPath(storeType, memPath, agentgoCfg)

	if agentgolog.IsDebug() {
		log.Printf("[DEBUG] Memory: storeType=%s, memPath=%s, embedSvc=%v", storeType, memPath, embedSvc != nil)
	}

	// Seam 1: an injected instance wins over every kind of lookup.
	if b.memoryCfg.Store != nil {
		memStore = b.memoryCfg.Store
		if storeType == "" {
			storeType = config.MemoryStoreType("custom")
		}
		return b.assembleMemoryService(memStore, llmSvc, embedSvc, storeType)
	}

	switch storeType {
	case config.MemoryStoreTypeFile:
		memStore, err = store.NewFileMemoryStore(memPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create file memory store: %w", err)
		}
	case config.MemoryStoreTypeCortex:
		memStore, err = store.NewCortexMemoryStore(memPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create cortex memory store: %w", err)
		}
		if err := memStore.InitSchema(context.Background()); err != nil {
			return nil, "", fmt.Errorf("failed to init cortex memory schema: %w", err)
		}
	case config.MemoryStoreTypeMemoryFlow:
		memStore, err = store.NewMemoryFlowStore(memPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create memoryflow memory store: %w", err)
		}
		if err := memStore.InitSchema(context.Background()); err != nil {
			return nil, "", fmt.Errorf("failed to init memoryflow memory schema: %w", err)
		}
	case config.MemoryStoreTypeGraphFlow:
		memStore, err = store.NewGraphFlowStore(memPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create graphflow memory store: %w", err)
		}
		if err := memStore.InitSchema(context.Background()); err != nil {
			return nil, "", fmt.Errorf("failed to init graphflow memory schema: %w", err)
		}
	default:
		// Seam 2: not a built-in — ask the plugin registry. The switch above
		// always wins, so a plugin can never shadow a built-in store type.
		factory, ok := domain.LookupMemoryStore(storeType.String())
		if !ok {
			return nil, "", fmt.Errorf(
				"unsupported memory store type: %s (built-in: file, cortex, memoryflow, graphflow; registered: %v)",
				storeType, domain.RegisteredMemoryStores())
		}
		memStore, err = factory(domain.MemoryStoreConfig{
			Name:      storeType.String(),
			Path:      memPath,
			DSN:       b.resolveMemoryDSN(agentgoCfg),
			Options:   b.resolveMemoryOptions(agentgoCfg),
			Embedder:  embedSvc,
			Generator: llmSvc,
		})
		if err != nil {
			return nil, "", fmt.Errorf("failed to create %q memory store: %w", storeType, err)
		}
		if memStore == nil {
			return nil, "", fmt.Errorf("memory store factory %q returned a nil store", storeType)
		}
		// A registered store owns its own bootstrap; an unsupported InitSchema
		// is a legitimate answer (a remote backend's schema is not ours).
		if err := memStore.InitSchema(context.Background()); err != nil && !domain.IsMemoryStoreUnsupported(err) {
			return nil, "", fmt.Errorf("failed to init %q memory schema: %w", storeType, err)
		}
	}

	return b.assembleMemoryService(memStore, llmSvc, embedSvc, storeType)
}

// resolveMemoryDSN prefers the builder option, then agentgo.toml.
func (b *Builder) resolveMemoryDSN(agentgoCfg *config.Config) string {
	if b.memoryCfg.DSN != "" {
		return b.memoryCfg.DSN
	}
	if agentgoCfg != nil {
		return agentgoCfg.Memory.DSN
	}
	return ""
}

// resolveMemoryOptions merges agentgo.toml `[memory.options]` with the builder
// options; the builder wins on conflict.
func (b *Builder) resolveMemoryOptions(agentgoCfg *config.Config) map[string]string {
	merged := map[string]string{}
	if agentgoCfg != nil {
		for k, v := range agentgoCfg.Memory.Options {
			merged[k] = v
		}
	}
	for k, v := range b.memoryCfg.Options {
		merged[k] = v
	}
	return merged
}

// assembleMemoryService wraps a resolved store in the standard memory service
// and seeds the MemoryBank directives. Every path through buildMemoryService
// ends here, so injected, registered and built-in stores get identical
// retrieval/injection behaviour.
func (b *Builder) assembleMemoryService(memStore domain.MemoryStore, llmSvc domain.Generator, embedSvc domain.Embedder, storeType config.MemoryStoreType) (domain.MemoryService, string, error) {
	memCfg := memory.DefaultConfig()
	if b.memoryCfg.ReflectThreshold > 0 {
		memCfg.ReflectThreshold = b.memoryCfg.ReflectThreshold
	}
	memCfg.DisableRetrieval = b.memoryCfg.RetrievalDisabled
	memCfg.DisableAutoStore = b.memoryCfg.AutoStoreDisabled

	memSvc := memory.NewService(memStore, llmSvc, embedSvc, memCfg)

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
	if storeType != config.MemoryStoreTypeFile {
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

// WithMemoryStoreType sets memory store type: "file", "cortex", "memoryflow", or "graphflow".
func WithMemoryStoreType(storeType string) MemoryOption {
	return func(c *MemoryConfig) { c.StoreType = storeType }
}

// WithMemoryReflect sets the auto-reflect threshold: after N new facts are stored,
// Reflect() is triggered asynchronously to consolidate them into observations.
// Set to 0 to disable auto-reflection.
func WithMemoryReflect(threshold int) MemoryOption {
	return func(c *MemoryConfig) { c.ReflectThreshold = threshold }
}

// WithMemoryRetrieval turns memory retrieval (the read side) on or off. It is
// on by default: every turn queries the memory store, and the store's own
// scoring, scope chain, noise filter and MaxMemories cap decide what survives.
//
// Pass false when an embedder wants memories recorded but never injected — for
// example to save prompt tokens. This is the only supported way to skip
// retrieval; the framework never infers it from the wording of a request.
//
//	svc, _ := agent.New("assistant").
//		WithMemory(agent.WithMemoryRetrieval(false)).
//		Build()
func WithMemoryRetrieval(enabled bool) MemoryOption {
	return func(c *MemoryConfig) { c.RetrievalDisabled = !enabled }
}

// WithMemoryAutoStore turns the automatic write side on or off. It is on by
// default: after every run one temperature-0.1 extraction call decides whether
// the interaction contained anything worth remembering, and the model's verdict
// is final.
//
// Pass false when an embedder wants retrieval without paying for one extraction
// call per turn — memories then only arrive through explicit MemoryService.Add
// or the memory tools. This is the only supported way to skip auto-store; the
// framework never infers it from the wording of a request.
//
//	svc, _ := agent.New("assistant").
//		WithMemory(agent.WithMemoryAutoStore(false)).
//		Build()
func WithMemoryAutoStore(enabled bool) MemoryOption {
	return func(c *MemoryConfig) { c.AutoStoreDisabled = !enabled }
}

// WithMemoryGraphFlow enables the CortexDB GraphFlow-enhanced memory store.
func WithMemoryGraphFlow() MemoryOption {
	return func(c *MemoryConfig) { c.StoreType = "graphflow" }
}

// WithMemoryDSN sets the connection string handed to a registered memory store
// factory (see RegisterMemoryStore). Built-in store types ignore it.
func WithMemoryDSN(dsn string) MemoryOption {
	return func(c *MemoryConfig) { c.DSN = dsn }
}

// WithMemoryOption sets one free-form option for a registered memory store
// factory. Repeatable.
func WithMemoryOption(key, value string) MemoryOption {
	return func(c *MemoryConfig) {
		if c.Options == nil {
			c.Options = map[string]string{}
		}
		c.Options[key] = value
	}
}

// WithMemoryOptions merges free-form options for a registered memory store
// factory.
func WithMemoryOptions(options map[string]string) MemoryOption {
	return func(c *MemoryConfig) {
		if c.Options == nil {
			c.Options = map[string]string{}
		}
		for k, v := range options {
			c.Options[k] = v
		}
	}
}

// WithMemoryStore injects an already-constructed domain.MemoryStore. It wins
// over StoreType: no registry lookup, no factory, no path resolution. Use it
// when the embedder owns the backend's lifecycle.
//
//	svc, _ := agent.New("assistant").WithMemory(agent.WithMemoryStore(myStore)).Build()
func WithMemoryStore(store domain.MemoryStore) MemoryOption {
	return func(c *MemoryConfig) { c.Store = store }
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

// SkillsOption modifies SkillsConfig
type SkillsOption func(*SkillsConfig)

// WithSkillsPaths sets skills paths
func WithSkillsPaths(paths ...string) SkillsOption {
	return func(c *SkillsConfig) { c.Paths = paths }
}

// ============================================================
