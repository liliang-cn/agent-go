// Package ptc provides CLI commands for PTC (Programmatic Tool Calling)
package ptc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	ptcgrpc "github.com/liliang-cn/agent-go/v2/pkg/ptc/grpc"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc/runtime/goja"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc/runtime/wazero"
	ptcstore "github.com/liliang-cn/agent-go/v2/pkg/ptc/store"
	"github.com/liliang-cn/agent-go/v2/pkg/search"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
	"github.com/spf13/cobra"
)

var (
	Cfg     *config.Config
	Verbose bool
)

// SetSharedVariables sets shared variables from root command
func SetSharedVariables(cfg *config.Config, verbose bool) {
	Cfg = cfg
	Verbose = verbose
}

// createRouterWithServices creates a PTC router with MCP and Skills services injected
func createRouterWithServices(ctx context.Context) *ptc.AgentGoRouter {
	if Cfg == nil {
		return ptc.NewAgentGoRouter()
	}

	var opts []ptc.RouterOption

	// Inject MCP service.
	mcpManager := mcp.NewMCPToolManager(&Cfg.MCP)
	succeeded, _ := mcpManager.StartWithFailures(ctx)
	if len(succeeded) > 0 {
		var mcpToolInfos []ptc.ToolInfo
		for name, tool := range mcpManager.ListTools() {
			mcpToolInfos = append(mcpToolInfos, ptc.ToolInfo{
				Name:        name,
				Description: tool.Description(),
				Parameters:  tool.Schema(),
				Category:    "mcp",
			})
		}
		opts = append(opts, ptc.WithMCPService(&mcpExecutorAdapter{manager: mcpManager}), ptc.WithMCPToolInfos(mcpToolInfos))
	}

	// Inject Skills service.
	skillCfg := &skills.Config{
		Enabled:      true,
		Paths:        Cfg.SkillsPaths(),
		AutoLoad:     true,
		CacheEnabled: true,
	}
	skillsService, err := skills.NewService(skillCfg)
	if err == nil {
		skillsService.LoadAll(ctx)

		var skillToolInfos []ptc.ToolInfo
		for _, s := range mustListSkills(ctx, skillsService) {
			skillToolInfos = append(skillToolInfos, ptc.ToolInfo{
				Name:        "skill_" + s.ID,
				Description: s.Description,
				Category:    "skill",
			})
		}
		opts = append(opts, ptc.WithSkillsService(&skillsExecutorAdapter{service: skillsService}), ptc.WithSkillToolInfos(skillToolInfos))
	}

	router := ptc.NewAgentGoRouter(opts...)
	registerCLIToolSearchTools(router)
	return router
}

func registerCLIToolSearchTools(router *ptc.AgentGoRouter) {
	if router == nil {
		return
	}

	register := func(name, description string, handler ptc.ToolHandler) {
		_ = router.RegisterTool(name, &ptc.ToolInfo{
			Name:        name,
			Description: description,
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search query",
					},
				},
				"required": []string{"query"},
			},
			Category: "search",
		}, handler)
	}

	register("search_available_tools", "Search the available tool catalog using keywords and return tool references.", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		query, _ := args["query"].(string)
		return cliSearchTools(ctx, router, query, "keyword")
	})

	register("tool_search_tool_regex", "Search for tools by regex over tool names and descriptions and return tool references.", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		query, _ := args["query"].(string)
		return cliSearchTools(ctx, router, query, "regex")
	})

	register("tool_search_tool_bm25", "Search for tools using natural language and return tool references.", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		query, _ := args["query"].(string)
		return cliSearchTools(ctx, router, query, "bm25")
	})
}

func cliSearchTools(ctx context.Context, router *ptc.AgentGoRouter, query string, searchType string) (interface{}, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("tool search requires a non-empty query")
	}

	tools, err := router.ListAvailableTools(ctx)
	if err != nil {
		return nil, err
	}

	filtered := make([]ptc.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "search_available_tools" || domain.IsToolSearchTool(tool.Name) {
			continue
		}
		filtered = append(filtered, tool)
	}

	matches := make([]ptc.ToolInfo, 0)
	keywords := cliToolSearchKeywords(query)
	switch searchType {
	case "bm25":
		candidates := filterToolInfosByKeywords(filtered, keywords)
		if len(candidates) == 0 {
			candidates = filtered
		}
		documents := make([]search.Document, 0, len(candidates))
		toolByName := make(map[string]ptc.ToolInfo, len(candidates))
		for _, tool := range candidates {
			toolByName[tool.Name] = tool
			documents = append(documents, search.Document{
				ID: tool.Name,
				Text: strings.Join([]string{
					tool.Name,
					strings.ReplaceAll(tool.Name, "_", " "),
					tool.Description,
				}, " "),
			})
		}
		for _, item := range search.Rank(query, documents, 5, nil) {
			if tool, ok := toolByName[item.ID]; ok {
				matches = append(matches, tool)
			}
		}
		matches = rerankToolInfosByKeywordAffinity(matches, keywords)
	case "regex":
		var re *regexp.Regexp
		if compiled, err := regexp.Compile("(?i)" + query); err == nil {
			re = compiled
		}
		queryLower := strings.ToLower(query)
		for _, tool := range filtered {
			name := tool.Name
			desc := tool.Description
			matched := false
			if re != nil {
				matched = re.MatchString(name) || re.MatchString(desc)
			} else {
				nameLower := strings.ToLower(name)
				descLower := strings.ToLower(desc)
				matched = strings.Contains(nameLower, queryLower) || strings.Contains(descLower, queryLower)
			}
			if matched {
				matches = append(matches, tool)
			}
			if len(matches) >= 5 {
				break
			}
		}
		matches = rerankToolInfosByKeywordAffinity(matches, keywords)
	default:
		for _, tool := range filtered {
			if matchesToolInfoKeywords(tool, keywords) {
				matches = append(matches, tool)
			}
			if len(matches) >= 5 {
				break
			}
		}
		matches = rerankToolInfosByKeywordAffinity(matches, keywords)
	}

	refs := make([]domain.ToolReference, 0, len(matches))
	for _, tool := range matches {
		refs = append(refs, domain.ToolReference{ToolName: tool.Name})
	}
	return domain.ToolSearchResult{ToolReferences: refs}, nil
}

func cliToolSearchKeywords(text string) []string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return nil
	}
	stopwords := map[string]struct{}{
		"get": {}, "show": {}, "tell": {}, "find": {}, "search": {}, "lookup": {},
		"tool": {}, "tools": {}, "please": {}, "help": {}, "with": {}, "for": {},
		"the": {}, "a": {}, "an": {}, "my": {}, "me": {}, "need": {},
	}
	raw := strings.Fields(normalized)
	keywords := make([]string, 0, len(raw))
	for _, token := range raw {
		token = strings.Trim(token, ".,!?;:()[]{}\"'")
		if len(token) < 3 {
			continue
		}
		if _, skip := stopwords[token]; skip {
			continue
		}
		keywords = append(keywords, token)
	}
	return keywords
}

func matchesToolInfoKeywords(tool ptc.ToolInfo, keywords []string) bool {
	if len(keywords) == 0 {
		return false
	}
	nameLower := strings.ToLower(tool.Name)
	descLower := strings.ToLower(tool.Description)
	for _, kw := range keywords {
		if strings.Contains(nameLower, kw) || strings.Contains(descLower, kw) {
			return true
		}
	}
	return false
}

func filterToolInfosByKeywords(tools []ptc.ToolInfo, keywords []string) []ptc.ToolInfo {
	if len(keywords) == 0 {
		return nil
	}
	filtered := make([]ptc.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if matchesToolInfoKeywords(tool, keywords) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func rerankToolInfosByKeywordAffinity(tools []ptc.ToolInfo, keywords []string) []ptc.ToolInfo {
	if len(tools) <= 1 || len(keywords) == 0 {
		return tools
	}
	type scored struct {
		tool  ptc.ToolInfo
		score int
	}
	scoredTools := make([]scored, 0, len(tools))
	for _, tool := range tools {
		nameLower := strings.ToLower(tool.Name)
		descLower := strings.ToLower(tool.Description)
		score := 0
		for _, kw := range keywords {
			if strings.Contains(nameLower, kw) {
				score += 30
			}
			if strings.Contains(descLower, kw) {
				score += 10
			}
		}
		if strings.HasPrefix(nameLower, "mcp_") {
			score += 5
		}
		scoredTools = append(scoredTools, scored{tool: tool, score: score})
	}
	slices.SortStableFunc(scoredTools, func(a, b scored) int {
		if a.score == b.score {
			return strings.Compare(a.tool.Name, b.tool.Name)
		}
		if a.score > b.score {
			return -1
		}
		return 1
	})
	result := make([]ptc.ToolInfo, 0, len(scoredTools))
	for _, item := range scoredTools {
		result = append(result, item.tool)
	}
	return result
}

// mcpExecutorAdapter adapts mcp.Service to ptc.MCPProvider interface
type mcpExecutorAdapter struct {
	manager *mcp.MCPToolManager
}

func (a *mcpExecutorAdapter) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	result, err := a.manager.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

// skillsExecutorAdapter adapts skills.Service to ptc skillsProvider interface
type skillsExecutorAdapter struct {
	service *skills.Service
}

func (a *skillsExecutorAdapter) RunSkill(ctx context.Context, id string, vars map[string]interface{}) (string, error) {
	return a.service.RunSkill(ctx, id, vars)
}

func (a *skillsExecutorAdapter) ListSkillInfos(ctx context.Context) []ptc.ToolInfo {
	skillList, _ := a.service.ListSkills(ctx, skills.SkillFilter{})
	var infos []ptc.ToolInfo
	for _, s := range skillList {
		infos = append(infos, ptc.ToolInfo{
			Name:        "skill_" + s.ID,
			Description: s.Description,
			Category:    "skill",
		})
	}
	return infos
}

func mustListSkills(ctx context.Context, svc *skills.Service) []*skills.Skill {
	skillList, _ := svc.ListSkills(ctx, skills.SkillFilter{})
	return skillList
}

// RAGProcessorAdapter adapts domain.Processor to ptc router
type RAGProcessorAdapter struct {
	processor domain.Processor
}

func (a *RAGProcessorAdapter) QueryRaw(ctx context.Context, query string, topK int) (string, error) {
	resp, err := a.processor.Query(ctx, domain.QueryRequest{
		Query: query,
		TopK:  topK,
	})
	if err != nil {
		return "", err
	}
	return resp.Answer, nil
}

// Cmd is the parent command for PTC operations
var Cmd = &cobra.Command{
	Use:   "ptc",
	Short: "PTC (Programmatic Tool Calling) - Execute LLM-generated code safely",
	Long: `PTC allows LLMs to generate code instead of JSON parameters for tool calls.
The code is executed in a secure sandbox environment.

Examples:
  # Execute JavaScript code
  agentgo ptc execute --code "return callTool('rag_query', {query: 'test'})"

  # Execute code from file
  agentgo ptc execute --file script.js

  # List available tools
  agentgo ptc tools

  # Start gRPC server
  agentgo ptc serve`,
}

var executeCmd = &cobra.Command{
	Use:   "execute",
	Short: "Execute code in the PTC sandbox",
	Long: `Execute JavaScript code in a secure sandbox environment.

The code can call registered tools using the callTool() function.

Examples:
  agentgo ptc execute --code "console.log('Hello, World!')"
  agentgo ptc execute --code "return callTool('rag_query', {query: 'test'})"
  agentgo ptc execute --file myscript.js --timeout 60s`,
	RunE: runExecute,
}

var toolsCmd = &cobra.Command{
	Use:   "tools",
	Short: "List available tools for PTC",
	RunE:  runTools,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the PTC gRPC server",
	Long: `Start a gRPC server for PTC execution.
This allows external services to call the PTC service.`,
	RunE: runServe,
}

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "Show execution history",
	RunE:  runHistory,
}

var (
	// Execute flags
	codeString    string
	codeFile      string
	execTimeout   string
	execLanguage  string
	execContext   string
	execTools     []string
	execMaxMemory int
	execRuntime   string
	outputJSON    bool
)

func init() {
	Cmd.AddCommand(executeCmd)
	Cmd.AddCommand(toolsCmd)
	Cmd.AddCommand(serveCmd)
	Cmd.AddCommand(historyCmd)

	// Execute command flags
	executeCmd.Flags().StringVarP(&codeString, "code", "c", "", "Code to execute")
	executeCmd.Flags().StringVarP(&codeFile, "file", "f", "", "File containing code to execute")
	executeCmd.Flags().StringVarP(&execTimeout, "timeout", "t", "30s", "Execution timeout")
	executeCmd.Flags().StringVarP(&execLanguage, "language", "l", "javascript", "Code language (javascript)")
	executeCmd.Flags().StringVarP(&execContext, "context", "x", "", "JSON context variables to inject")
	executeCmd.Flags().StringSliceVarP(&execTools, "tools", "T", []string{}, "Allowed tools (comma-separated)")
	executeCmd.Flags().IntVarP(&execMaxMemory, "memory", "m", 64, "Maximum memory in MB")
	executeCmd.Flags().StringVarP(&execRuntime, "runtime", "r", "goja", "Runtime to use (goja or wazero)")
	executeCmd.Flags().BoolVarP(&outputJSON, "json", "j", false, "Output result as JSON")

	// Tools command flags
	toolsCmd.Flags().BoolVarP(&outputJSON, "json", "j", false, "Output as JSON")

	// Serve command flags
	serveCmd.Flags().String("address", "unix:///tmp/ptc.sock", "Server address (unix://path or host:port)")
	serveCmd.Flags().String("runtime", "goja", "Runtime to use (goja or wazero)")
}

func runExecute(cmd *cobra.Command, args []string) error {
	// Get code from flag or file
	code := codeString
	if code == "" && codeFile != "" {
		data, err := os.ReadFile(codeFile)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}
		code = string(data)
	}

	if code == "" {
		// Try reading from stdin
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("failed to read stdin: %w", err)
			}
			code = string(data)
		}
	}

	if code == "" {
		return fmt.Errorf("no code provided. Use --code, --file, or pipe via stdin")
	}

	// Parse timeout
	timeout, err := time.ParseDuration(execTimeout)
	if err != nil {
		return fmt.Errorf("invalid timeout: %w", err)
	}

	// Parse context
	contextVars := make(map[string]interface{})
	if execContext != "" {
		if err := json.Unmarshal([]byte(execContext), &contextVars); err != nil {
			return fmt.Errorf("invalid context JSON: %w", err)
		}
	}

	// Create PTC service with MCP and Skills integration
	ctx := context.Background()
	router := createRouterWithServices(ctx)

	ptcConfig := ptc.DefaultConfig()
	ptcConfig.Enabled = true
	ptcConfig.DefaultTimeout = timeout
	ptcConfig.MaxMemoryMB = execMaxMemory

	store := ptcstore.NewMemoryStore(100)

	service, err := ptc.NewService(&ptcConfig, router, store)
	if err != nil {
		return fmt.Errorf("failed to create PTC service: %w", err)
	}

	// Create and set runtime based on selection
	var runtime ptc.SandboxRuntime
	switch execRuntime {
	case "wazero", "wasm":
		runtime = wazero.NewRuntimeWithConfig(&ptcConfig)
	case "goja", "js":
		runtime = goja.NewRuntimeWithConfig(&ptcConfig)
	default:
		runtime = goja.NewRuntimeWithConfig(&ptcConfig)
	}
	service.SetRuntime(runtime)

	// Build execution request
	req := &ptc.ExecutionRequest{
		Code:        code,
		Language:    ptc.LanguageType(execLanguage),
		Context:     contextVars,
		Tools:       execTools,
		Timeout:     timeout,
		MaxMemoryMB: execMaxMemory,
	}

	// Execute
	start := time.Now()
	result, err := service.Execute(ctx, req)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	if outputJSON {
		output, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}
		fmt.Println(string(output))
		return nil
	}

	// Human-readable output
	fmt.Printf("Execution ID: %s\n", result.ID)
	fmt.Printf("Duration: %v\n", result.Duration)
	fmt.Printf("Success: %v\n", result.Success)

	if result.ReturnValue != nil {
		fmt.Printf("\nReturn Value:\n")
		printValue(result.ReturnValue)
	}

	if result.Output != nil {
		fmt.Printf("\nOutput:\n")
		printValue(result.Output)
	}

	if len(result.Logs) > 0 {
		fmt.Printf("\nLogs:\n")
		for _, log := range result.Logs {
			fmt.Printf("  %s\n", log)
		}
	}

	if len(result.ToolCalls) > 0 {
		fmt.Printf("\nTool Calls (%d):\n", len(result.ToolCalls))
		for _, tc := range result.ToolCalls {
			fmt.Printf("  - %s (%v)\n", tc.ToolName, tc.Duration)
			if tc.Error != "" {
				fmt.Printf("    Error: %s\n", tc.Error)
			} else if tc.Result != nil {
				fmt.Printf("    Result: %v\n", tc.Result)
			}
		}
	}

	if result.Error != "" {
		fmt.Printf("\nError: %s\n", result.Error)
	}

	_ = start // used for tracking
	return nil
}

func runTools(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	router := createRouterWithServices(ctx)

	tools, err := router.ListAvailableTools(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tools: %w", err)
	}

	if outputJSON {
		output, err := json.MarshalIndent(tools, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal tools: %w", err)
		}
		fmt.Println(string(output))
		return nil
	}

	// Group tools by category
	categories := make(map[string][]ptc.ToolInfo)
	for _, tool := range tools {
		cat := tool.Category
		if cat == "" {
			cat = "other"
		}
		categories[cat] = append(categories[cat], tool)
	}

	fmt.Printf("Available Tools (%d total):\n\n", len(tools))

	// Print tools by category
	for cat, catTools := range categories {
		fmt.Printf("📦 %s (%d):\n", cat, len(catTools))
		for _, tool := range catTools {
			fmt.Printf("  - %s\n", tool.Name)
			if tool.Description != "" {
				fmt.Printf("    %s\n", tool.Description)
			}
		}
		fmt.Println()
	}

	return nil
}

func runServe(cmd *cobra.Command, args []string) error {
	address, _ := cmd.Flags().GetString("address")
	runtimeType, _ := cmd.Flags().GetString("runtime")

	// Create PTC service
	ptcConfig := ptc.DefaultConfig()
	ptcConfig.Enabled = true
	ptcConfig.GRPC.Enabled = true
	ptcConfig.GRPC.Address = address

	router := ptc.NewAgentGoRouter()
	store := ptcstore.NewMemoryStore(1000)

	service, err := ptc.NewService(&ptcConfig, router, store)
	if err != nil {
		return fmt.Errorf("failed to create PTC service: %w", err)
	}

	// Create and set runtime based on selection
	var runtime ptc.SandboxRuntime
	switch runtimeType {
	case "wazero", "wasm":
		runtime = wazero.NewRuntimeWithConfig(&ptcConfig)
	default:
		runtime = goja.NewRuntimeWithConfig(&ptcConfig)
	}
	service.SetRuntime(runtime)

	// Create and start gRPC server
	grpcServer := ptcgrpc.NewGRPCServer(service, &ptcConfig.GRPC)

	fmt.Printf("Starting PTC gRPC server on %s (runtime: %s)\n", address, runtimeType)
	if err := grpcServer.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	fmt.Println("Server started. Press Ctrl+C to stop.")

	// Wait for interrupt
	select {}
}

func runHistory(cmd *cobra.Command, args []string) error {
	// For now, history is not persisted between commands
	fmt.Println("History is only available during a session.")
	fmt.Println("Use --json flag with execute command to capture results.")
	return nil
}

func printValue(v interface{}) {
	switch val := v.(type) {
	case string:
		fmt.Println(val)
	default:
		b, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			fmt.Printf("%v\n", val)
		} else {
			fmt.Println(string(b))
		}
	}
}
