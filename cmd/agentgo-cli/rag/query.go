package rag

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/rag/chunker"
	"github.com/liliang-cn/agent-go/v2/pkg/rag/processor"
	"github.com/liliang-cn/agent-go/v2/pkg/rag/store"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/spf13/cobra"
)

var (
	topK         int
	temperature  float64
	maxTokens    int
	stream       bool
	showThinking bool
	showSources  bool
	interactive  bool
	queryFile    string
	filterBy     []string
	enableTools  bool
	allowedTools []string
	maxToolCalls int
	useMCP       bool
)

var queryCmd = &cobra.Command{
	Use:   "query [question]",
	Short: "Query knowledge base with MCP tool integration",
	Long: `Perform semantic search and Q&A based on imported documents with MCP tool integration.
You can provide a question as an argument or use interactive mode.

Use --mcp to enable MCP tool integration for the query.
MCP tools provide enhanced functionality for file operations, database queries, and more.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Check if interactive mode (no arguments provided or --interactive flag)
		if interactive || len(args) == 0 {
			return fmt.Errorf("interactive mode not available - please provide a question")
		}

		// Handle MCP mode
		if useMCP {
			return processMCPQuery(cmd, args)
		}

		// Initialize stores based on configuration
		var vectorStore domain.VectorStore
		var docStore domain.DocumentStore
		var err error

		// Default to SQLite
		sqliteStore, err := store.NewSQLiteStore(Cfg.CortexDBPath(), Cfg.Internal.Storage.IndexType)
		if err != nil {
			return fmt.Errorf("failed to create vector store: %w", err)
		}
		vectorStore = sqliteStore
		docStore = store.NewDocumentStore(sqliteStore.GetCortexdbStore())
		defer func() {
			if err := sqliteStore.Close(); err != nil {
				fmt.Printf("failed to close vector store: %v\n", err)
			}
		}()

		// Close Qdrant connection if applicable
		if qdrantStore, ok := vectorStore.(*store.QdrantStore); ok {
			defer func() {
				if err := qdrantStore.Close(); err != nil {
					fmt.Printf("failed to close Qdrant store: %v\n", err)
				}
			}()
		}

		// If docStore is still nil (for SQLite vector store), create it
		if docStore == nil {
			if sqliteStore, ok := vectorStore.(*store.SQLiteStore); ok {
				docStore = store.NewDocumentStore(sqliteStore.GetCortexdbStore())
			}
		}

		// Initialize services using global LLM service
		ctx := context.Background()
		llmService, err := services.GetGlobalLLM()
		if err != nil {
			return fmt.Errorf("failed to get global LLM service: %w", err)
		}

		embedService, err := services.GetGlobalEmbeddingService(ctx)
		if err != nil {
			return fmt.Errorf("failed to get global embedder service: %w", err)
		}

		// Create metadata extractor from LLM service if it implements the interface
		var metadataExtractor domain.MetadataExtractor
		if extractor, ok := llmService.(domain.MetadataExtractor); ok {
			metadataExtractor = extractor
		} else {
			// Fallback: use embedder service
			if extractor, ok := embedService.(domain.MetadataExtractor); ok {
				metadataExtractor = extractor
			} else {
				return fmt.Errorf("neither LLM nor embedder service implements MetadataExtractor interface")
			}
		}

		chunkerService := chunker.New()

		processor := processor.New(
			embedService,
			llmService,
			chunkerService,
			vectorStore,
			docStore,
			Cfg,
			metadataExtractor,
			nil, // memoryService
		)

		// Tools have been removed - use MCP servers instead
		toolsEnabled := false

		if interactive || len(args) == 0 {
			return runInteractive(ctx, processor, toolsEnabled)
		}

		if queryFile != "" {
			return processQueryFile(ctx, processor, toolsEnabled)
		}

		query := strings.Join(args, " ")
		return processQuery(ctx, processor, query, toolsEnabled)
	},
}

func runInteractive(ctx context.Context, p *processor.Service, toolsEnabled bool) error {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("AgentGo Interactive Query Mode")
	fmt.Println("Type 'exit' or 'quit' to exit, 'help' for commands")
	fmt.Println()

	for {
		fmt.Print("agentgo> ")
		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		switch strings.ToLower(input) {
		case "exit", "quit", "q":
			fmt.Println("Goodbye!")
			return nil
		case "help", "h":
			printHelp()
			continue
		case "clear", "cls":
			fmt.Print("\033[2J\033[H")
			continue
		}

		if err := processQuery(ctx, p, input, toolsEnabled); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
		fmt.Println()
	}

	return scanner.Err()
}

func processQueryFile(ctx context.Context, p *processor.Service, toolsEnabled bool) error {
	file, err := os.Open(queryFile)
	if err != nil {
		return fmt.Errorf("failed to open query file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Printf("Warning: failed to close query file: %v\n", closeErr)
		}
	}()

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		query := strings.TrimSpace(scanner.Text())
		if query == "" || strings.HasPrefix(query, "#") {
			continue
		}

		fmt.Printf("Query %d: %s\n", lineNum, query)
		if err := processQuery(ctx, p, query, toolsEnabled); err != nil {
			fmt.Printf("Error processing query %d: %v\n", lineNum, err)
		}
		fmt.Println(strings.Repeat("-", 50))
	}

	return scanner.Err()
}

func processQuery(ctx context.Context, p *processor.Service, query string, toolsEnabled bool) error {
	// Parse filters from filterBy flag
	filters := make(map[string]interface{})
	for _, filter := range filterBy {
		parts := strings.SplitN(filter, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			filters[key] = value
		}
	}

	req := domain.QueryRequest{
		Query:        query,
		TopK:         topK,
		Temperature:  temperature,
		MaxTokens:    maxTokens,
		Stream:       stream, // Streaming now works with tools
		ShowThinking: showThinking,
		ShowSources:  showSources,
		Filters:      filters,
		ToolsEnabled: toolsEnabled,
		AllowedTools: allowedTools,
		MaxToolCalls: maxToolCalls,
	}

	if req.Stream {
		return processStreamQuery(ctx, p, req)
	}

	var resp domain.QueryResponse
	var err error

	// Use QueryWithTools if tools are enabled
	if toolsEnabled {
		resp, err = p.QueryWithTools(ctx, req)
	} else {
		resp, err = p.Query(ctx, req)
	}

	if err != nil {
		return err
	}

	fmt.Printf("Answer: %s\n", resp.Answer)

	// Show tool execution information if available
	if toolsEnabled && len(resp.ToolCalls) > 0 {
		fmt.Printf("\nTool Executions (%d):\n", len(resp.ToolCalls))
		for i, toolCall := range resp.ToolCalls {
			status := "Success"
			if !toolCall.Success {
				status = "Failed"
			}
			fmt.Printf("  [%d] %s - %s (%s)\n", i+1, toolCall.Function.Name, status, toolCall.Elapsed)
			if toolCall.Error != "" {
				fmt.Printf("      Error: %s\n", toolCall.Error)
			}
		}
	}

	// Show sources if requested
	if (showSources || Verbose) && len(resp.Sources) > 0 {
		fmt.Printf("\nSources (%d):\n", len(resp.Sources))
		for i, source := range resp.Sources {
			fmt.Printf("  [%d] Score: %.4f\n", i+1, source.Score)

			// Extract filename from metadata if available
			var sourceInfo string
			if source.Metadata != nil {
				if filename, ok := source.Metadata["filename"].(string); ok && filename != "" {
					sourceInfo = filename
				} else if source_path, ok := source.Metadata["source"].(string); ok && source_path != "" {
					sourceInfo = source_path
				} else {
					sourceInfo = "Unknown source"
				}
			} else {
				sourceInfo = "Unknown source"
			}

			fmt.Printf("      Source: %s\n", sourceInfo)
			if Verbose {
				fmt.Printf("      Content: %s...\n", truncateText(source.Content, 100))
				if len(source.Metadata) > 0 {
					fmt.Printf("      Metadata: %v\n", source.Metadata)
				}
			}
		}
	}

	if Verbose {
		fmt.Printf("\nElapsed: %s\n", resp.Elapsed)
	}

	return nil
}

func processStreamQuery(ctx context.Context, p *processor.Service, req domain.QueryRequest) error {
	fmt.Print("Answer: ")

	var sources []domain.Chunk
	var err error

	// If sources are requested, we need to get them first
	if req.ShowSources || Verbose {
		// First get sources by doing a quick non-streaming query
		tempReq := req
		tempReq.Stream = false
		tempReq.ShowSources = true

		var tempResp domain.QueryResponse
		if req.ToolsEnabled {
			tempResp, err = p.QueryWithTools(ctx, tempReq)
		} else {
			tempResp, err = p.Query(ctx, tempReq)
		}
		if err == nil {
			sources = tempResp.Sources
		}
	}

	// Now do the streaming
	if req.ToolsEnabled {
		err = p.StreamQueryWithTools(ctx, req, func(token string) {
			fmt.Print(token)
		})
	} else {
		err = p.StreamQuery(ctx, req, func(token string) {
			fmt.Print(token)
		})
	}

	fmt.Println()

	// Show sources after streaming is complete
	if (req.ShowSources || Verbose) && len(sources) > 0 {
		fmt.Printf("\nSources (%d):\n", len(sources))
		for i, source := range sources {
			fmt.Printf("  [%d] Score: %.4f\n", i+1, source.Score)

			// Extract filename from metadata if available
			var sourceInfo string
			if source.Metadata != nil {
				if filename, ok := source.Metadata["filename"].(string); ok && filename != "" {
					sourceInfo = filename
				} else if source_path, ok := source.Metadata["source"].(string); ok && source_path != "" {
					sourceInfo = source_path
				} else {
					sourceInfo = "Unknown source"
				}
			} else {
				sourceInfo = "Unknown source"
			}

			fmt.Printf("      Source: %s\n", sourceInfo)
			if Verbose {
				fmt.Printf("      Content: %s...\n", truncateText(source.Content, 100))
				if len(source.Metadata) > 0 {
					fmt.Printf("      Metadata: %v\n", source.Metadata)
				}
			}
		}
	}

	return err
}

func printHelp() {
	fmt.Println("Available commands:")
	fmt.Println("  help, h     - Show this help message")
	fmt.Println("  clear, cls  - Clear the screen")
	fmt.Println("  exit, quit  - Exit the program")
	fmt.Println("  <question>  - Ask a question to the knowledge base")
	fmt.Println()
	fmt.Println("Available flags:")
	fmt.Println("  --filter key=value    - Filter results by metadata")
	fmt.Println("  --mcp                 - Enable MCP tool integration")
	fmt.Println("  --allowed-tools tool1,tool2 - Specify allowed tools")
	fmt.Println("  --max-tool-calls N    - Maximum tool calls per query")
	fmt.Println()
	fmt.Println("Note: Use --mcp flag to enable MCP tool integration for enhanced functionality.")
}

func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}

func processMCPQuery(cmd *cobra.Command, args []string) error {
	if !Cfg.MCP.Enabled {
		return fmt.Errorf("MCP is disabled in configuration")
	}

	// Get the query
	var query string
	if len(args) == 0 {
		return fmt.Errorf("please provide a question when using --mcp flag")
	}
	query = strings.Join(args, " ")

	fmt.Printf("MCP Query: %s\n\n", query)

	// Initialize MCP service
	mcpService := mcp.NewMCPService(&Cfg.MCP)
	ctx := context.Background()

	if err := mcpService.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to start MCP service: %w", err)
	}
	defer func() {
		if err := mcpService.Close(); err != nil {
			fmt.Printf("failed to close mcp service: %v\n", err)
		}
	}()

	// Get available tools
	toolsMap := mcpService.GetAvailableTools()
	if len(toolsMap) == 0 {
		return fmt.Errorf("no MCP tools available")
	}

	// Initialize processors for LLM functionality
	vectorStore, err := store.NewSQLiteStore(Cfg.CortexDBPath(), Cfg.Internal.Storage.IndexType)
	if err != nil {
		return fmt.Errorf("failed to create vector store: %w", err)
	}
	defer func() {
		if err := vectorStore.Close(); err != nil {
			fmt.Printf("Warning: failed to close vector store: %v\n", err)
		}
	}()

	docStore := store.NewDocumentStore(vectorStore.GetCortexdbStore())

	// Initialize services using global LLM service
	llmService, err := services.GetGlobalLLM()
	if err != nil {
		return fmt.Errorf("failed to get global LLM service: %w", err)
	}

	embedService, err := services.GetGlobalEmbeddingService(ctx)
	if err != nil {
		return fmt.Errorf("failed to get global embedder service: %w", err)
	}

	// Create metadata extractor from LLM service if it implements the interface
	var metadataExtractor domain.MetadataExtractor
	if extractor, ok := llmService.(domain.MetadataExtractor); ok {
		metadataExtractor = extractor
	} else {
		// Fallback: use embedder service
		if extractor, ok := embedService.(domain.MetadataExtractor); ok {
			metadataExtractor = extractor
		} else {
			return fmt.Errorf("neither LLM nor embedder service implements MetadataExtractor interface")
		}
	}

	chunkerService := chunker.New()
	processor := processor.New(
		embedService,
		llmService,
		chunkerService,
		vectorStore,
		docStore,
		Cfg,
		metadataExtractor,
		nil, // memoryService
	)

	// Register MCP tools with the processor
	if err := processor.RegisterMCPTools(mcpService); err != nil {
		return fmt.Errorf("failed to register MCP tools: %w", err)
	}

	// Perform search to check relevance (use Query method to get chunks)
	searchReq := domain.QueryRequest{
		Query:        query,
		TopK:         3, // Small number for relevance check
		ShowSources:  true,
		ToolsEnabled: false, // Don't use tools for relevance check
	}

	searchResp, err := processor.Query(ctx, searchReq)
	var chunks []domain.Chunk
	if err == nil {
		chunks = searchResp.Sources
	}

	// Determine if RAG context is relevant (use default threshold)
	relevanceThreshold := 0.05
	hasRelevantContext := false
	var relevantChunks []domain.Chunk

	if len(chunks) > 0 {
		for _, chunk := range chunks {
			if chunk.Score >= relevanceThreshold {
				relevantChunks = append(relevantChunks, chunk)
				hasRelevantContext = true
			}
		}
	}

	// Check for tool-priority scenarios based on query-tool semantic matching
	shouldPrioritizeTools := shouldPrioritizeMCPTools(query, toolsMap)

	// Choose prompt strategy based on context relevance and tool priority
	var systemPrompt string
	var finalQuery string

	if shouldPrioritizeTools {
		// Tool-priority mode: Query strongly suggests MCP tool usage
		systemPrompt = fmt.Sprintf(`You are an AI assistant specialized in using MCP tools to answer user questions directly. The user's question strongly suggests the need for tool-based operations.

Available MCP Tools:
%s

User Question: %s

Analyze the question and immediately use the most appropriate MCP tools to answer it. Be proactive and direct in tool usage.`,
			formatMCPToolsForPrompt(toolsMap),
			query)
		finalQuery = systemPrompt
		fmt.Printf("🛠️ Query suggests tool usage - prioritizing MCP tools\n")
	} else if hasRelevantContext {
		// High relevance: Combine RAG context with MCP tools
		contextStr := buildContextString(relevantChunks)
		systemPrompt = fmt.Sprintf(`You are an AI assistant with access to both document context and MCP tools. 

Available MCP Tools:
%s

Document Context:
%s

User Question: %s

Use both the document context and MCP tools as needed to provide a comprehensive answer. Call MCP tools when you need live data or database operations.`,
			formatMCPToolsForPrompt(toolsMap),
			contextStr,
			query)
		finalQuery = systemPrompt
		fmt.Printf("📚 High relevance context found - combining RAG and MCP\n")
	} else {
		// Low relevance: MCP-only mode with direct instructions
		systemPrompt = fmt.Sprintf(`You are an AI assistant with access to MCP tools. The user's question doesn't match the available document context, so focus on using MCP tools to answer directly.

Available MCP Tools:
%s

User Question: %s

Analyze the question and use the appropriate MCP tools to answer it. Be proactive in calling tools when they can help answer the question.`,
			formatMCPToolsForPrompt(toolsMap),
			query)
		finalQuery = systemPrompt
		fmt.Printf("🔍 Low relevance search results (max score: %.2f) - using MCP-only mode\n", getMaxScore(chunks))
	}

	// Use regular query with tools enabled
	req := domain.QueryRequest{
		Query:        finalQuery,
		TopK:         5,
		Temperature:  0.7,
		MaxTokens:    30000,
		Stream:       false,
		ShowSources:  false,
		ToolsEnabled: true,
		MaxToolCalls: 5,
	}

	resp, err := processor.QueryWithTools(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to process MCP query: %w", err)
	}

	fmt.Printf("Answer: %s\n", resp.Answer)

	// Show tool execution information if available
	if len(resp.ToolCalls) > 0 {
		fmt.Printf("\nTool Executions (%d):\n", len(resp.ToolCalls))
		for i, toolCall := range resp.ToolCalls {
			status := "Success"
			if !toolCall.Success {
				status = "Failed"
			}
			fmt.Printf("  [%d] %s - %s (%s)\n", i+1, toolCall.Function.Name, status, toolCall.Elapsed)
			if toolCall.Error != "" {
				fmt.Printf("      Error: %s\n", toolCall.Error)
			}
		}
	}

	return nil
}

// Helper function to format MCP tools for prompt
func formatMCPToolsForPrompt(toolsMap map[string]*mcp.MCPToolWrapper) string {
	var tools []string
	for _, tool := range toolsMap {
		tools = append(tools, fmt.Sprintf("- %s: %s", tool.Name(), tool.Description()))
	}
	return strings.Join(tools, "\n")
}

// Helper function to build context string from relevant chunks
func buildContextString(chunks []domain.Chunk) string {
	if len(chunks) == 0 {
		return "No relevant context found."
	}

	var contexts []string
	for i, chunk := range chunks {
		contexts = append(contexts, fmt.Sprintf("[%d] %s (Score: %.2f)", i+1, truncateText(chunk.Content, 200), chunk.Score))
	}
	return strings.Join(contexts, "\n")
}

// Helper function to determine if query should prioritize MCP tools
func shouldPrioritizeMCPTools(query string, toolsMap map[string]*mcp.MCPToolWrapper) bool {
	queryLower := strings.ToLower(query)

	// Define tool priority keywords based on available tool capabilities
	toolPriorityKeywords := extractToolKeywords(toolsMap)

	// Check if query contains action verbs that suggest tool usage
	actionVerbs := []string{
		"list", "show", "display", "get", "fetch", "retrieve", "find",
		"create", "make", "generate", "build", "add", "insert",
		"update", "modify", "change", "edit", "set",
		"delete", "remove", "drop", "clear", "clean",
		"query", "search", "select", "execute", "run",
		"check", "verify", "validate", "test", "analyze",
		"count", "calculate", "compute", "measure",
		"export", "import", "backup", "restore",
	}

	// Score based on action verbs
	actionScore := 0
	for _, verb := range actionVerbs {
		if strings.Contains(queryLower, verb) {
			actionScore++
		}
	}

	// Score based on tool-specific keywords
	keywordScore := 0
	for _, keyword := range toolPriorityKeywords {
		if strings.Contains(queryLower, keyword) {
			keywordScore += 2 // Higher weight for tool-specific terms
		}
	}

	// Check for direct tool invocation patterns
	directInvocationScore := 0
	if strings.Contains(queryLower, "use") || strings.Contains(queryLower, "call") ||
		strings.Contains(queryLower, "run") || strings.Contains(queryLower, "execute") {
		directInvocationScore += 3
	}

	totalScore := actionScore + keywordScore + directInvocationScore

	// Prioritize tools if score is high enough
	return totalScore >= 2
}

// Helper function to extract relevant keywords from available tools
func extractToolKeywords(toolsMap map[string]*mcp.MCPToolWrapper) []string {
	keywords := make(map[string]bool)

	for _, tool := range toolsMap {
		toolName := strings.ToLower(tool.Name())
		description := strings.ToLower(tool.Description())

		// Extract keywords from tool names (remove mcp_ prefix)
		if strings.HasPrefix(toolName, "mcp_") {
			cleanName := toolName[4:] // Remove "mcp_"
			parts := strings.Split(cleanName, "_")
			for _, part := range parts {
				if len(part) > 2 { // Avoid short words
					keywords[part] = true
				}
			}
		}

		// Extract keywords from descriptions
		descriptionWords := strings.Fields(description)
		for _, word := range descriptionWords {
			// Clean word and add if significant
			cleaned := strings.Trim(word, ".,!?()[]{}\"'")
			if len(cleaned) > 3 && isSignificantWord(cleaned) {
				keywords[cleaned] = true
			}
		}
	}

	// Convert map to slice
	var result []string
	for keyword := range keywords {
		result = append(result, keyword)
	}

	return result
}

// Helper function to check if a word is significant for tool matching
func isSignificantWord(word string) bool {
	// Skip common English words
	commonWords := map[string]bool{
		"the": true, "and": true, "that": true, "have": true, "for": true,
		"not": true, "with": true, "you": true, "this": true, "but": true,
		"his": true, "from": true, "they": true, "she": true, "her": true,
		"been": true, "than": true, "its": true, "who": true, "oil": true,
		"use": true, "may": true, "these": true, "only": true, "other": true,
		"new": true, "some": true, "could": true, "time": true, "very": true,
		"when": true, "much": true, "can": true, "said": true, "each": true,
	}

	return !commonWords[strings.ToLower(word)]
}

// Helper function to get max score from chunks
func getMaxScore(chunks []domain.Chunk) float64 {
	if len(chunks) == 0 {
		return 0.0
	}

	maxScore := 0.0
	for _, chunk := range chunks {
		if chunk.Score > maxScore {
			maxScore = chunk.Score
		}
	}
	return maxScore
}

func init() {
	queryCmd.Flags().IntVar(&topK, "top-k", 5, "number of documents to retrieve")
	queryCmd.Flags().Float64Var(&temperature, "temperature", 0.7, "generation temperature")
	queryCmd.Flags().IntVar(&maxTokens, "max-tokens", 500, "maximum generation length")
	queryCmd.Flags().BoolVarP(&stream, "stream", "s", false, "stream the response")
	queryCmd.Flags().BoolVar(&showThinking, "show-thinking", true, "show AI thinking process")
	queryCmd.Flags().BoolVar(&showSources, "show-sources", false, "show source documents used for answering")
	queryCmd.Flags().BoolVar(&Verbose, "verbose", false, "show verbose output including sources")
	queryCmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "interactive mode")
	queryCmd.Flags().StringVar(&queryFile, "file", "", "batch query from file")
	queryCmd.Flags().StringSliceVar(&filterBy, "filter", []string{}, "filter by metadata (key=value format, can be used multiple times)")
	queryCmd.Flags().BoolVar(&enableTools, "tools", false, "enable tool calling capabilities (overrides config file setting)")
	queryCmd.Flags().StringSliceVar(&allowedTools, "allowed-tools", []string{}, "comma-separated list of allowed tools (empty means all enabled tools)")
	queryCmd.Flags().IntVar(&maxToolCalls, "max-tool-calls", 5, "maximum number of tool calls per query")
	queryCmd.Flags().BoolVar(&useMCP, "mcp", false, "use MCP tools for query processing")
}
