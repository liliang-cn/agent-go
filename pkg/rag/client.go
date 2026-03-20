package rag

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/rag/chunker"
	"github.com/liliang-cn/agent-go/v2/pkg/rag/processor"
	"github.com/liliang-cn/agent-go/v2/pkg/rag/store"
)

// Client provides high-level RAG operations
type Client struct {
	processor    *processor.Service
	vectorStore  domain.VectorStore
	docStore     *store.DocumentStore
	embedder     domain.Embedder
	llm          domain.Generator
	config       *config.Config
	mcpService   *mcp.Service
	mcpConfig    *mcp.Config
	agentService *agent.Service
	agentDBPath  string
}

// NewClient creates a new RAG client
func NewClient(cfg *config.Config, embedder domain.Embedder, llm domain.Generator, metadataExtractor domain.MetadataExtractor) (*Client, error) {
	// Initialize vector store based on configuration
	var vectorStore domain.VectorStore
	var docStore *store.DocumentStore
	var err error

	// Default to SQLite
	sqliteStore, err := store.NewSQLiteStore(cfg.CortexDBPath(), cfg.Internal.Storage.IndexType)
	if err != nil {
		return nil, fmt.Errorf("failed to create vector store: %w", err)
	}
	vectorStore = sqliteStore
	docStore = store.NewDocumentStore(sqliteStore.GetCortexdbStore())

	// If docStore is still nil (for SQLite vector store), create it
	if docStore == nil {
		if sqliteStore, ok := vectorStore.(*store.SQLiteStore); ok {
			docStore = store.NewDocumentStore(sqliteStore.GetCortexdbStore())
		}
	}

	// Initialize chunker
	chunkerService := chunker.New()

	// Initialize memory service (optional, can be nil)
	var memoryService domain.MemoryService

	// Initialize processor
	proc := processor.New(
		embedder,
		llm,
		chunkerService,
		vectorStore,
		docStore,
		cfg,
		metadataExtractor,
		memoryService,
	)

	// Initialize MCP service
	// MCP service configuration
	mcpConfig := &mcp.Config{
		ServersConfigPath: cfg.MCP.ServersConfigPath,
		Enabled:           cfg.MCP.Enabled,
	}

	var mcpService *mcp.Service
	// MCP service will be initialized lazily with LLM when needed

	return &Client{
		processor:   proc,
		vectorStore: vectorStore,
		docStore:    docStore,
		embedder:    embedder,
		llm:         llm,
		config:      cfg,
		mcpService:  mcpService,
		mcpConfig:   mcpConfig,
	}, nil
}

// IngestOptions configures how content is ingested
type IngestOptions struct {
	ChunkSize          int                    // Size of text chunks
	Overlap            int                    // Overlap between chunks
	EnhancedExtraction bool                   // Enable enhanced metadata extraction
	Metadata           map[string]interface{} // Additional metadata
}

// DefaultIngestOptions returns default ingest options
func DefaultIngestOptions() *IngestOptions {
	return &IngestOptions{
		ChunkSize: 1000,
		Overlap:   200,
	}
}

// IngestFile ingests a file from the local filesystem
func (c *Client) IngestFile(ctx context.Context, filePath string, opts *IngestOptions) (*domain.IngestResponse, error) {
	if opts == nil {
		opts = DefaultIngestOptions()
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute path: %w", err)
	}

	req := domain.IngestRequest{
		FilePath:  absPath,
		ChunkSize: opts.ChunkSize,
		Overlap:   opts.Overlap,
		Metadata:  opts.Metadata,
	}

	// Handle enhanced extraction
	/*
		if opts.EnhancedExtraction {
			origConfig := c.config.Ingest.MetadataExtraction.Enable
			c.config.Ingest.MetadataExtraction.Enable = true
			defer func() {
				c.config.Ingest.MetadataExtraction.Enable = origConfig
			}()
		}
	*/

	resp, err := c.processor.Ingest(ctx, req)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

// IngestText ingests text content directly
func (c *Client) IngestText(ctx context.Context, text, source string, opts *IngestOptions) (*domain.IngestResponse, error) {
	if opts == nil {
		opts = DefaultIngestOptions()
	}

	metadata := make(map[string]interface{})
	if opts.Metadata != nil {
		metadata = opts.Metadata
	}
	metadata["source"] = source
	metadata["type"] = "text"
	metadata["ingested_at"] = time.Now().Format(time.RFC3339)

	req := domain.IngestRequest{
		Content:   text,
		ChunkSize: opts.ChunkSize,
		Overlap:   opts.Overlap,
		Metadata:  metadata,
	}

	resp, err := c.processor.Ingest(ctx, req)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

// IngestURL ingests content from a URL
func (c *Client) IngestURL(ctx context.Context, url string, opts *IngestOptions) (*domain.IngestResponse, error) {
	if opts == nil {
		opts = DefaultIngestOptions()
	}

	req := domain.IngestRequest{
		URL:       url,
		ChunkSize: opts.ChunkSize,
		Overlap:   opts.Overlap,
		Metadata:  opts.Metadata,
	}

	resp, err := c.processor.Ingest(ctx, req)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

// IngestBatch ingests multiple files or contents
func (c *Client) IngestBatch(ctx context.Context, requests []domain.IngestRequest) ([]domain.IngestResponse, error) {
	return c.processor.IngestBatch(ctx, requests)
}

// QueryOptions configures how queries are executed
type QueryOptions struct {
	TopK         int                    // Number of documents to retrieve
	Temperature  float64                // LLM temperature
	MaxTokens    int                    // Maximum tokens in response
	ShowSources  bool                   // Include source documents in response
	ShowThinking bool                   // Show reasoning process
	Stream       bool                   // Enable streaming response
	ToolsEnabled bool                   // Enable tool calling
	AllowedTools []string               // Specific tools to allow
	MaxToolCalls int                    // Maximum tool calls
	Filters      map[string]interface{} // Metadata filters for document retrieval
	// Advanced Search Options
	RerankStrategy  string  // "keyword", "rrf", "diversity"
	RerankBoost     float64 // For keyword reranker
	DiversityLambda float32 // For MMR (0-1)
	EnableACL       bool
	ACLIDs          []string
}

// DefaultQueryOptions returns default query options
func DefaultQueryOptions() *QueryOptions {
	return &QueryOptions{
		TopK:        5,
		Temperature: 0.7,
		MaxTokens:   2000,
		ShowSources: true,
	}
}

// Query performs a RAG query
func (c *Client) Query(ctx context.Context, query string, opts *QueryOptions) (*domain.QueryResponse, error) {
	if opts == nil {
		opts = DefaultQueryOptions()
	}

	req := domain.QueryRequest{
		Query:           query,
		TopK:            opts.TopK,
		Temperature:     opts.Temperature,
		MaxTokens:       opts.MaxTokens,
		ShowSources:     opts.ShowSources,
		ShowThinking:    opts.ShowThinking,
		Stream:          opts.Stream,
		ToolsEnabled:    opts.ToolsEnabled,
		AllowedTools:    opts.AllowedTools,
		MaxToolCalls:    opts.MaxToolCalls,
		Filters:         opts.Filters,
		RerankStrategy:  opts.RerankStrategy,
		RerankBoost:     opts.RerankBoost,
		DiversityLambda: opts.DiversityLambda,
		EnableACL:       opts.EnableACL,
		ACLIDs:          opts.ACLIDs,
	}

	resp, err := c.processor.Query(ctx, req)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

// ListDocuments lists all documents in the store
func (c *Client) ListDocuments(ctx context.Context) ([]domain.Document, error) {
	if c.docStore != nil {
		return c.docStore.List(ctx)
	}
	// Fallback to vector store if it supports listing (like SQLite)
	return c.vectorStore.List(ctx)
}

// DeleteDocument deletes a document by ID
func (c *Client) DeleteDocument(ctx context.Context, documentID string) error {
	return c.vectorStore.Delete(ctx, documentID)
}

// GetDocument retrieves a document by ID
func (c *Client) GetDocument(ctx context.Context, documentID string) (domain.Document, error) {
	if c.docStore != nil {
		return c.docStore.Get(ctx, documentID)
	}
	return domain.Document{}, fmt.Errorf("document store not available")
}

// Reset clears all documents from the store
func (c *Client) Reset(ctx context.Context) error {
	return c.vectorStore.Reset(ctx)
}

// GetStats returns statistics about the RAG store
func (c *Client) GetStats(ctx context.Context) (*domain.Stats, error) {
	var docs []domain.Document
	var err error

	if c.docStore != nil {
		docs, err = c.docStore.List(ctx)
	} else {
		docs, err = c.vectorStore.List(ctx)
	}

	if err != nil {
		return nil, err
	}

	// Count chunks (approximate based on documents)
	totalChunks := 0
	for range docs {
		// Estimate chunks per document
		totalChunks += 5 // This is an approximation
	}

	return &domain.Stats{
		TotalDocuments: len(docs),
		TotalChunks:    totalChunks,
	}, nil
}

// Close closes the RAG client and releases resources
func (c *Client) Close() error {
	var errs []error

	// Close agent service
	if c.agentService != nil {
		if err := c.agentService.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if c.vectorStore != nil {
		if closer, ok := c.vectorStore.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if c.docStore != nil {
		// Document store typically uses same connection as SQLite vector store
		// so it's already closed above
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// GetProcessor returns the underlying processor for advanced operations
func (c *Client) GetProcessor() *processor.Service {
	return c.processor
}

// GetVectorStore returns the underlying vector store for advanced operations
func (c *Client) GetVectorStore() domain.VectorStore {
	return c.vectorStore
}

// GetSQLiteStore returns the underlying SQLite store for chat session storage
func (c *Client) GetSQLiteStore() *store.SQLiteStore {
	if sqliteStore, ok := c.vectorStore.(*store.SQLiteStore); ok {
		return sqliteStore
	}
	return nil
}

// initMCPService initializes the MCP service if not already done
func (c *Client) initMCPService(ctx context.Context) error {
	if c.mcpService != nil {
		return nil // Already initialized
	}

	if c.mcpConfig == nil || !c.mcpConfig.Enabled {
		return fmt.Errorf("MCP not enabled in configuration")
	}

	// Create MCP service with LLM
	service, err := mcp.NewService(c.mcpConfig, c.llm)
	if err != nil {
		return fmt.Errorf("failed to create MCP service: %w", err)
	}

	c.mcpService = service
	return nil
}

// MCP/Tools integration methods

// ListTools lists available MCP tools
func (c *Client) ListTools(ctx context.Context) ([]interface{}, error) {
	// Initialize MCP service if needed
	if err := c.initMCPService(ctx); err != nil {
		return []interface{}{}, fmt.Errorf("MCP service unavailable: %w", err)
	}

	// Get available tools from all running servers
	tools := c.mcpService.GetAvailableTools(ctx)

	// Convert to generic interface slice
	result := make([]interface{}, len(tools))
	for i, tool := range tools {
		result[i] = map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"server":      tool.ServerName,
		}
	}

	return result, nil
}

// CallTool executes an MCP tool
func (c *Client) CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (interface{}, error) {
	// Initialize MCP service if needed
	if err := c.initMCPService(ctx); err != nil {
		return nil, fmt.Errorf("MCP service unavailable: %w", err)
	}

	// Call the tool
	result, err := c.mcpService.CallTool(ctx, toolName, arguments)
	if err != nil {
		return nil, fmt.Errorf("tool execution failed: %w", err)
	}

	// Convert result to generic interface
	return map[string]interface{}{
		"success": result.Success,
		"data":    result.Data,
		"error":   result.Error,
	}, nil
}

// GetMCPStatus returns MCP service status
func (c *Client) GetMCPStatus(ctx context.Context) (interface{}, error) {
	if c.mcpConfig == nil || !c.mcpConfig.Enabled {
		return map[string]interface{}{
			"enabled": false,
			"message": "MCP not enabled in configuration",
		}, nil
	}

	// Initialize MCP service if needed
	if err := c.initMCPService(ctx); err != nil {
		return map[string]interface{}{
			"enabled": false,
			"message": fmt.Sprintf("MCP service initialization failed: %v", err),
		}, nil
	}

	// Get server status
	servers := c.mcpService.ListServers()

	serverStatus := make([]interface{}, len(servers))
	for i, server := range servers {
		serverStatus[i] = map[string]interface{}{
			"name":        server.Name,
			"description": server.Description,
			"running":     server.Running,
			"tool_count":  server.ToolCount,
		}
	}

	return map[string]interface{}{
		"enabled": true,
		"message": "MCP service operational",
		"servers": serverStatus,
	}, nil
}

// IngestTextWithMetadata ingests text with custom metadata
func (c *Client) IngestTextWithMetadata(ctx context.Context, text, source string, metadata map[string]interface{}, opts *IngestOptions) (*domain.IngestResponse, error) {
	if opts == nil {
		opts = DefaultIngestOptions()
	}

	// Merge custom metadata with default metadata
	mergedMetadata := make(map[string]interface{})
	if opts.Metadata != nil {
		for k, v := range opts.Metadata {
			mergedMetadata[k] = v
		}
	}
	for k, v := range metadata {
		mergedMetadata[k] = v
	}

	// Create modified options
	modifiedOpts := *opts
	modifiedOpts.Metadata = mergedMetadata

	req := domain.IngestRequest{
		Content:   text,
		ChunkSize: opts.ChunkSize,
		Overlap:   opts.Overlap,
		Metadata:  modifiedOpts.Metadata,
	}

	resp, err := c.processor.Ingest(ctx, req)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

// Enhanced generation methods with profiles

// GenerateWithContext generates text with context awareness
func (c *Client) GenerateWithContext(ctx context.Context, prompt string, contextDocs []domain.Chunk, opts *domain.GenerationOptions) (string, error) {
	if c.llm == nil {
		return "", fmt.Errorf("LLM not available")
	}

	// Build context string
	var contextStr string
	if len(contextDocs) > 0 {
		var contexts []string
		for _, chunk := range contextDocs {
			contexts = append(contexts, fmt.Sprintf("[Context: %s]\n%s", chunk.Metadata["source"], chunk.Content))
		}
		contextStr = strings.Join(contexts, "\n\n---\n\n")
	}

	// Combine context and prompt
	fullPrompt := prompt
	if contextStr != "" {
		fullPrompt = fmt.Sprintf("Context:\n%s\n\nQuestion:\n%s", contextStr, prompt)
	}

	return c.llm.Generate(ctx, fullPrompt, opts)
}

// StreamWithContext streams generation with context awareness
func (c *Client) StreamWithContext(ctx context.Context, prompt string, contextDocs []domain.Chunk, opts *domain.GenerationOptions, callback func(string)) error {
	if c.llm == nil {
		return fmt.Errorf("LLM not available")
	}

	// Build context string
	var contextStr string
	if len(contextDocs) > 0 {
		var contexts []string
		for _, chunk := range contextDocs {
			contexts = append(contexts, fmt.Sprintf("[Context: %s]\n%s", chunk.Metadata["source"], chunk.Content))
		}
		contextStr = strings.Join(contexts, "\n\n---\n\n")
	}

	// Combine context and prompt
	fullPrompt := prompt
	if contextStr != "" {
		fullPrompt = fmt.Sprintf("Context:\n%s\n\nQuestion:\n%s", contextStr, prompt)
	}

	return c.llm.Stream(ctx, fullPrompt, opts, callback)
}

// Chat methods

// StartChat starts a new chat session
func (c *Client) StartChat(ctx context.Context, metadata map[string]interface{}) (*domain.ChatSession, error) {
	return c.processor.StartChat(ctx, metadata)
}

// Chat performs a chat interaction with history
func (c *Client) Chat(ctx context.Context, sessionID string, message string, opts *QueryOptions) (*domain.QueryResponse, error) {
	req := &domain.QueryRequest{
		Query:           message,
		TopK:            opts.TopK,
		Temperature:     opts.Temperature,
		MaxTokens:       opts.MaxTokens,
		ShowSources:     opts.ShowSources,
		ShowThinking:    opts.ShowThinking,
		Stream:          opts.Stream,
		ToolsEnabled:    opts.ToolsEnabled,
		AllowedTools:    opts.AllowedTools,
		MaxToolCalls:    opts.MaxToolCalls,
		Filters:         opts.Filters,
		RerankStrategy:  opts.RerankStrategy,
		RerankBoost:     opts.RerankBoost,
		DiversityLambda: opts.DiversityLambda,
		EnableACL:       opts.EnableACL,
		ACLIDs:          opts.ACLIDs,
	}
	return c.processor.Chat(ctx, sessionID, message, req)
}

// ============================================================================
// Agent Methods - Autonomous AI Agent with Handoffs, Guardrails, Tracing
// ============================================================================

// AgentOptions configures agent behavior
type AgentOptions struct {
	EnableHandoffs   bool                  // Enable agent handoffs
	EnableGuardrails bool                  // Enable input/output guardrails
	EnableTracing    bool                  // Enable execution tracing
	Guardrails       []*agent.Guardrail    // Custom guardrails
	Handoffs         []agent.HandoffOption // Handoff configurations
	SessionID        string                // Resume existing session
}

// DefaultAgentOptions returns default agent options
func DefaultAgentOptions() *AgentOptions {
	return &AgentOptions{
		EnableHandoffs:   false,
		EnableGuardrails: true,
		EnableTracing:    false,
		Guardrails: []*agent.Guardrail{
			agent.ContentModerationGuardrail(),
			agent.PromptInjectionGuardrail(),
		},
	}
}

// initAgentService initializes the agent service if not already done
func (c *Client) initAgentService(ctx context.Context) error {
	if c.agentService != nil {
		return nil
	}

	// Use the same unified DB path for the agent
	if c.agentDBPath == "" {
		c.agentDBPath = c.config.AgentDBPath()
	}

	// Initialize MCP service for agent
	if err := c.initMCPService(ctx); err != nil {
		return fmt.Errorf("failed to initialize MCP service for agent: %w", err)
	}

	// Create MCP tool executor adapter
	mcpAdapter := &mcpToolAdapter{service: c.mcpService}

	// Create agent service (memoryService can be nil initially)
	svc, err := agent.NewService(c.llm, mcpAdapter, c.processor, c.agentDBPath, nil)
	if err != nil {
		return fmt.Errorf("failed to create agent service: %w", err)
	}

	// Set MCP service for full public access
	if c.mcpService != nil {
		svc.SetMCPService(c.mcpService)
	}

	c.agentService = svc
	return nil
}

// mcpToolAdapter adapts mcp.Service to agent.MCPToolExecutor
type mcpToolAdapter struct {
	service *mcp.Service
}

func (a *mcpToolAdapter) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	result, err := a.service.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return nil, fmt.Errorf("MCP tool error: %s", result.Error)
	}
	return result.Data, nil
}

func (a *mcpToolAdapter) ListTools() []domain.ToolDefinition {
	tools := a.service.GetAvailableTools(context.Background())
	result := make([]domain.ToolDefinition, len(tools))
	for i, t := range tools {
		result[i] = domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"arguments": map[string]interface{}{
							"type":        "object",
							"description": "Tool arguments",
						},
					},
				},
			},
		}
	}
	return result
}

func (a *mcpToolAdapter) AddServer(ctx context.Context, name string, command string, args []string) error {
	return a.service.AddDynamicServer(ctx, name, command, args)
}

// RunAgent executes an agent with the given goal
func (c *Client) RunAgent(ctx context.Context, goal string, opts *AgentOptions) (*agent.ExecutionResult, error) {
	if err := c.initAgentService(ctx); err != nil {
		return nil, err
	}

	if opts == nil {
		opts = DefaultAgentOptions()
	}

	// Run with session if provided
	if opts.SessionID != "" {
		return c.agentService.Run(ctx, goal, agent.WithSessionID(opts.SessionID))
	}

	return c.agentService.Run(ctx, goal)
}

// PlanAgent creates a plan for the given goal without executing
func (c *Client) PlanAgent(ctx context.Context, goal string) (*agent.Plan, error) {
	if err := c.initAgentService(ctx); err != nil {
		return nil, err
	}

	return c.agentService.Plan(ctx, goal)
}

// ExecuteAgentPlan executes an existing plan
func (c *Client) ExecuteAgentPlan(ctx context.Context, plan *agent.Plan) error {
	if err := c.initAgentService(ctx); err != nil {
		return err
	}

	_, err := c.agentService.ExecutePlan(ctx, plan)
	return err
}

// GetAgentSession retrieves an agent session by ID
func (c *Client) GetAgentSession(sessionID string) (*agent.Session, error) {
	if c.agentService == nil {
		return nil, fmt.Errorf("agent service not initialized")
	}

	return c.agentService.GetSession(sessionID)
}

// ListAgentSessions returns all agent sessions
func (c *Client) ListAgentSessions(limit int) ([]*agent.Session, error) {
	if c.agentService == nil {
		return nil, fmt.Errorf("agent service not initialized")
	}

	return c.agentService.ListSessions(limit)
}

// GetAgentPlan retrieves a plan by ID
func (c *Client) GetAgentPlan(planID string) (*agent.Plan, error) {
	if c.agentService == nil {
		return nil, fmt.Errorf("agent service not initialized")
	}

	return c.agentService.GetPlan(planID)
}

// GetAgentTracer returns the agent tracer for exporting traces
func (c *Client) GetAgentTracer() *agent.Tracer {
	if c.agentService == nil {
		return nil
	}

	// This would require exposing tracer from agent.Service
	// For now, return a new tracer that can read traces
	return agent.NewTracer()
}

// CreateAgent creates a custom agent with specific configuration
func (c *Client) CreateAgent(name, instructions string) (*agent.Agent, error) {
	if err := c.initAgentService(context.Background()); err != nil {
		return nil, err
	}

	// Create agent with custom configuration
	a := agent.NewAgentWithConfig(name, instructions, nil)
	return a, nil
}

// AgentWithHandoffs creates an agent with handoff capabilities
func (c *Client) AgentWithHandoffs(name, instructions string, handoffs []*agent.Agent) (*agent.Agent, []*agent.Handoff) {
	primaryAgent := agent.NewAgentWithConfig(name, instructions, nil)

	// Create handoffs from other agents
	agentHandoffs := make([]*agent.Handoff, len(handoffs))
	for i, target := range handoffs {
		agentHandoffs[i] = agent.NewHandoff(target)
	}

	return primaryAgent, agentHandoffs
}

// ============================================================================
// Agent Convenience Methods
// ============================================================================

// QuickAgent runs a quick agent task with default settings
func (c *Client) QuickAgent(ctx context.Context, goal string) (string, error) {
	result, err := c.RunAgent(ctx, goal, DefaultAgentOptions())
	if err != nil {
		return "", err
	}

	if result.FinalResult != nil {
		return fmt.Sprintf("%v", result.FinalResult), nil
	}

	return fmt.Sprintf("Completed %d steps in %s", result.StepsDone, result.Duration), nil
}

// AgentChat runs an agent in chat mode with conversation history
func (c *Client) AgentChat(ctx context.Context, sessionID, message string) (*agent.ExecutionResult, error) {
	if err := c.initAgentService(ctx); err != nil {
		return nil, err
	}

	return c.agentService.Run(ctx, message, agent.WithSessionID(sessionID))
}
