package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v2/cmd/agentgo-cli/internal/cliui"
	"github.com/liliang-cn/agent-go/v2/cmd/agentgo-cli/internal/lineinput"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/spf13/cobra"
)

func delegatedResultLooksFailed(result string) bool {
	normalized := strings.ToLower(strings.TrimSpace(result))
	return strings.HasPrefix(normalized, "code execution failed:") ||
		strings.HasPrefix(normalized, "execute_javascript failed:") ||
		strings.HasPrefix(normalized, "task failed:") ||
		strings.Contains(normalized, "\n**status:** failed")
}

var (
	chatSessionID  string
	chatStream     bool
	chatWithPTC    bool
	chatNoMemory   bool
	chatShowMemory bool
)

type delegatedTask struct {
	AgentName   string
	Instruction string
}

type chatRequest struct {
	Input string
	Done  chan struct{}
}

var agentMentionPattern = regexp.MustCompile(`^@([^\s@]+)$`)
var chatThinkBlockRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

var chatCmd = &cobra.Command{
	Use:   "chat [message]",
	Short: "Chat with AgentGo Agent (conversational AI with memory)",
	Long: `Interactive chat with the AgentGo Agent that maintains conversation context across messages.

The agent has access to:
- MCP Tools (external integrations)
- Skills (domain-specific capabilities)
- Memory (long-term factual memory)
- RAG (knowledge base retrieval)
- PTC (Programmatic Tool Calling - JS sandbox)

Examples:
  agentgo chat "你好"
  agentgo chat --with-ptc "比较三个城市的旅行预算"
  agentgo chat --show-memory "我之前说过我喜欢什么颜色？"
  agentgo chat --no-memory "临时不要记得这次对话内容"
  agentgo chat  # Interactive mode`,
	RunE: runChat,
}

func init() {
	RootCmd.AddCommand(chatCmd)
	chatCmd.Flags().StringVarP(&chatSessionID, "session", "s", "", "Session ID for conversation (default: auto-generated)")
	chatCmd.Flags().BoolVarP(&chatStream, "stream", "", false, "Stream the response")
	chatCmd.Flags().BoolVar(&chatWithPTC, "with-ptc", false, "Enable Programmatic Tool Calling (JS sandbox)")
	chatCmd.Flags().BoolVar(&chatNoMemory, "no-memory", false, "Disable long-term memory for this chat")
	chatCmd.Flags().BoolVar(&chatShowMemory, "show-memory", false, "Show retrieved memories in output")
}

func runChat(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	chatCfg := Cfg
	if chatCfg == nil {
		var err error
		chatCfg, err = config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	agentDBPath := chatCfg.AgentDBPath()

	var agentManager *agent.TeamManager
	agentStore, storeErr := agent.NewStore(agentDBPath)
	if storeErr == nil {
		agentManager = agent.NewTeamManager(agentStore)
		agentManager.SetConfig(chatCfg)
		if err := agentManager.SeedDefaultMembers(); err != nil {
			agentManager = nil
		}
	}

	svc, err := buildChatConciergeService(chatCfg, agentDBPath, agentManager)
	if err != nil {
		return fmt.Errorf("failed to build concierge service: %w", err)
	}
	defer svc.Close()

	// Set session if specified
	if chatSessionID != "" {
		svc.SetSessionID(chatSessionID)
	}

	// Get current session ID
	currentSessionID := svc.CurrentSessionID()
	if currentSessionID != "" {
		fmt.Printf("%s Session: %s\n", cliui.Session, currentSessionID)
	}

	// Interactive mode (no arguments)
	if len(args) == 0 {
		return runInteractiveChat(ctx, svc, agentManager)
	}

	// Single message mode
	message := strings.Join(args, " ")
	fmt.Printf("\n%s%s\n", cliui.UserPrompt, message)

	if agentManager != nil {
		tasks, parseErr := parseDelegatedTasks(message, func(name string) bool {
			_, err := agentManager.GetAgentByName(name)
			return err == nil
		})
		if parseErr != nil {
			return parseErr
		}
		if len(tasks) > 0 {
			return runDelegatedTaskChainAsync(context.Background(), agentManager, svc.CurrentSessionID(), tasks, nil, false)
		}
	}

	result, err := svc.Chat(ctx, message)
	if err != nil {
		return fmt.Errorf("chat failed: %w", err)
	}

	displayResult(result)
	if follower := newChatTaskFollower(agentManager); follower != nil {
		follower.StartTaskIDs(ctx, executionResultAsyncTaskIDs(result))
	}

	// Show session ID after first message
	if currentSessionID == "" {
		newSessionID := svc.CurrentSessionID()
		fmt.Printf("\n%s Session: %s (use --session %s to continue)\n", cliui.Tip, newSessionID, newSessionID)
	}

	return nil
}

func buildChatConciergeService(chatCfg *config.Config, agentDBPath string, manager *agent.TeamManager) (*agent.Service, error) {
	if manager != nil && !chatNoMemory && !chatWithPTC && !chatCfg.GetMemoryStoreType().UsesVector() {
		if svc, err := manager.GetAgentService(agent.BuiltInConciergeAgentName); err == nil {
			svc.SetDebug(debug)
			svc.SetProgressCallback(progressCallback)
			manager.RegisterConciergeTools(svc)
			return svc, nil
		}
	}

	systemPrompt := "You are Concierge, the always-on dispatch agent for AgentGo. Your only job is intake, routing, status inspection, and task dispatch. Do not do substantive work yourself unless the user is asking for dispatch metadata, agent or team status, or task status. For almost every substantive user request, call route_builtin_request with the user's request. That tool runs PromptOptimizer and IntentRouter in parallel, then dispatches to the correct specialist. Do not manually claim that something was saved, recalled, verified, or executed unless a routing or status tool has already confirmed it. Keep replies concise, acknowledge queued work clearly, and never pretend background work is already finished."
	if manager != nil {
		if model, err := manager.GetAgentByName(agent.BuiltInConciergeAgentName); err == nil && strings.TrimSpace(model.Instructions) != "" {
			systemPrompt = strings.TrimSpace(model.Instructions)
		}
	}

	builder := agent.New(agent.BuiltInConciergeAgentName).
		WithConfig(chatCfg).
		WithSystemPrompt(systemPrompt).
		WithDBPath(agentDBPath).
		WithDebug(debug).
		WithProgress(progressCallback)

	if chatWithPTC {
		builder.WithPTC()
	}

	globalPool := services.GetGlobalPoolService()
	if err := globalPool.Initialize(context.Background(), chatCfg); err == nil {
		if llmSvc, llmErr := globalPool.GetLLMService(); llmErr == nil {
			builder.WithLLM(llmSvc)
		}
		if embedSvc, embedErr := globalPool.GetEmbeddingService(context.Background()); embedErr == nil {
			builder.WithEmbedder(embedSvc)
		}
	}

	svc, err := builder.Build()
	if err != nil {
		return nil, err
	}
	if manager != nil {
		manager.RegisterConciergeTools(svc)
	}
	return svc, nil
}

func displayResult(result *agent.ExecutionResult) {
	// Show memories if requested
	if (chatShowMemory || verbose) && len(result.Memories) > 0 {
		fmt.Printf("\n%s Retrieved Memories (%d):\n", cliui.Memory, len(result.Memories))
		for i, mem := range result.Memories {
			sourceTag := ""
			if mem.SourceType != "" {
				sourceTag = fmt.Sprintf(" src:%s", mem.SourceType)
			}
			confTag := ""
			if mem.Confidence > 0 {
				confTag = fmt.Sprintf(" conf:%.2f", mem.Confidence)
			}
			evTag := ""
			if len(mem.EvidenceIDs) > 0 {
				evTag = fmt.Sprintf(" evidence:%d", len(mem.EvidenceIDs))
			}
			scopeTag := ""
			if scope := formatMemoryScope(mem.ScopeType, mem.ScopeID, mem.SessionID); scope != "" {
				scopeTag = fmt.Sprintf(" scope:%s", scope)
			}
			fmt.Printf("  %d. [%s%s%s%s%s] %s (score: %.2f)\n",
				i+1, mem.Type, sourceTag, confTag, evTag,
				scopeTag,
				truncateString(mem.Content, 100), mem.Score)
			if chatShowMemory || debug {
				if promotion := formatMemoryPromotion(mem.Metadata); promotion != "" {
					fmt.Printf("     promotion: %s\n", promotion)
				}
				if reason := formatMemoryReason(mem.Metadata, "scope_reason"); reason != "" {
					fmt.Printf("     scope_reason: %s\n", truncateString(reason, 120))
				}
				if reason := formatMemoryReason(mem.Metadata, "promotion_reason"); reason != "" {
					fmt.Printf("     promotion_reason: %s\n", truncateString(reason, 120))
				}
			}
		}
		if chain := formatScopeChain(result.Metadata); chain != "" {
			fmt.Printf("  %s Scope chain: %s\n", cliui.Tip, chain)
		}
		if result.MemoryLogic != "" {
			fmt.Printf("  %s Navigator reasoning: %s\n", cliui.Tip, truncateString(result.MemoryLogic, 200))
		}
	}

	if result.PTCResult != nil && result.PTCResult.Type != agent.PTCResultTypeText {
		fmt.Printf("\n%s (PTC Mode):\n%s\n", cliui.Concierge, result.PTCResult.FormatForLLM())
	} else if result.FinalResult != nil {
		fmt.Printf("\n%s: %s\n", cliui.Concierge, sanitizeChatDisplayText(fmt.Sprint(result.FinalResult)))
	} else {
		fmt.Printf("\n%s: (empty response)\n", cliui.Concierge)
	}

	if result != nil {
		if result.StartedAt != nil {
			fmt.Printf("Started: %s\n", result.StartedAt.Format("2006-01-02 15:04:05"))
		}
		if result.CompletedAt != nil {
			fmt.Printf("Completed: %s\n", result.CompletedAt.Format("2006-01-02 15:04:05"))
		}
		if result.EstimatedTokens > 0 {
			fmt.Printf("Estimated tokens: %d\n", result.EstimatedTokens)
		}
		if result.ToolCalls > 0 {
			fmt.Printf("Tool calls: %d\n", result.ToolCalls)
		}
	}
}

// truncateString truncates a string to a maximum length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func sanitizeChatDisplayText(text string) string {
	text = chatThinkBlockRe.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func formatMemoryScope(scopeType domain.MemoryScopeType, scopeID, sessionID string) string {
	scopeTypeStr := strings.TrimSpace(string(scopeType))
	scopeID = strings.TrimSpace(scopeID)
	sessionID = strings.TrimSpace(sessionID)
	if scopeTypeStr == "" {
		switch {
		case sessionID == "", sessionID == "global", sessionID == "default":
			scopeTypeStr = "global"
		case strings.Contains(sessionID, ":"):
			parts := strings.SplitN(sessionID, ":", 2)
			scopeTypeStr = strings.TrimSpace(parts[0])
			if scopeID == "" && len(parts) == 2 {
				scopeID = strings.TrimSpace(parts[1])
			}
		default:
			scopeTypeStr = "session"
			if scopeID == "" {
				scopeID = sessionID
			}
		}
	}
	if scopeTypeStr == "" {
		return ""
	}
	if scopeTypeStr == "global" || scopeID == "" {
		return scopeTypeStr
	}
	return scopeTypeStr + ":" + scopeID
}

func formatMemoryPromotion(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	promoted, _ := metadata["promoted"].(bool)
	if !promoted {
		return ""
	}
	from, _ := metadata["promoted_from_scope"].(string)
	to, _ := metadata["promoted_to_scope"].(string)
	switch {
	case strings.TrimSpace(from) != "" && strings.TrimSpace(to) != "":
		return from + " -> " + to
	case strings.TrimSpace(to) != "":
		return "-> " + to
	default:
		return "true"
	}
}

func formatMemoryReason(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func formatScopeChain(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata["memory_scope_chain"]
	items := make([]string, 0)
	if ok {
		if typed, ok := raw.([]string); ok {
			items = append(items, typed...)
		} else if ifaceItems, ok := raw.([]interface{}); ok {
			for _, item := range ifaceItems {
				if s, ok := item.(string); ok {
					items = append(items, s)
				}
			}
		}
	}
	if len(items) == 0 {
		if rawCtx, ok := metadata["memory_scope_context"].(map[string]interface{}); ok {
			appendIf := func(prefix string, key string) {
				if value, ok := rawCtx[key].(string); ok && strings.TrimSpace(value) != "" {
					items = append(items, prefix+":"+strings.TrimSpace(value))
				}
			}
			appendIf("session", "session_id")
			appendIf("agent", "agent_id")
			appendIf("team", "team_id")
			appendIf("user", "user_id")
			items = append(items, "global")
		}
	}
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || strings.HasSuffix(item, ":") {
			continue
		}
		filtered = append(filtered, item)
	}
	return strings.Join(filtered, " > ")
}

// progressCallback displays agent progress
func progressCallback(event agent.ProgressEvent) {
	switch event.Type {
	case "thinking":
		fmt.Printf("  %s %s\n", cliui.Thinking, event.Message)
	case "tool_call":
		if event.Tool != "" {
			fmt.Printf("  %s %s\n", cliui.Tool, event.Message)
		}
	case "tool_result":
		fmt.Printf("  ✓ %s\n", event.Message)
	}
}

func runInteractiveChat(ctx context.Context, svc *agent.Service, manager *agent.TeamManager) error {
	chatCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	fmt.Printf("%s AgentGo Concierge Chat\n", cliui.Concierge)
	if chatWithPTC {
		fmt.Printf("%s PTC Mode: Enabled (JS sandbox for complex logic)\n", cliui.PTC)
	}
	if chatNoMemory {
		fmt.Println("🔇 Memory Mode: Disabled")
	} else if chatShowMemory || verbose {
		fmt.Printf("%s Memory Mode: Enabled (Showing retrievals)\n", cliui.Memory)
	}
	fmt.Printf("%s This chat talks to Concierge, the always-on intake agent for AgentGo.\n", cliui.Tip)
	fmt.Printf("%s Type 'quit' or 'exit' to end, 'clear' to reset session\n", cliui.Tip)
	fmt.Printf("%s Tip: Use '@AgentName <instruction>' to run a saved agent in the background\n", cliui.Tip)
	fmt.Println()

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	quitChan := make(chan struct{})
	var quitOnce sync.Once
	closeQuit := func() {
		quitOnce.Do(func() {
			close(quitChan)
		})
	}
	requests := make(chan chatRequest)
	taskFollower := newChatTaskFollower(manager)

	go func() {
		for {
			select {
			case <-quitChan:
				return
			case req, ok := <-requests:
				if !ok {
					return
				}
				fmt.Printf("\n%s\n", cliui.Thinking)
				result, err := svc.Chat(ctx, req.Input)
				if err != nil {
					fmt.Printf("%s Error: %v\n\n", cliui.Error, err)
				} else {
					displayResult(result)
					if taskFollower != nil {
						taskFollower.StartTaskIDs(chatCtx, executionResultAsyncTaskIDs(result))
					}
					if taskFollower != nil {
						taskFollower.StartSessionTasks(chatCtx, svc.CurrentSessionID())
					}
					fmt.Println()
				}
				if req.Done != nil {
					close(req.Done)
				}
			}
		}
	}()

	// Input goroutine
	go func() {
		for {
			input, err := lineinput.ReadInteractiveLine(cliui.UserPrompt)
			if err != nil {
				switch {
				case errors.Is(err, lineinput.ErrInputCanceled):
					fmt.Print("^C\r\n")
					continue
				case errors.Is(err, io.EOF):
					closeQuit()
					cancel()
					fmt.Printf("\n%s Goodbye!\n", cliui.Goodbye)
					return
				default:
					fmt.Printf("\n%s Input error: %v\n", cliui.Error, err)
					continue
				}
			}
			input = strings.TrimSpace(input)

			if input == "" {
				continue
			}

			// Exit commands
			if input == "quit" || input == "exit" || input == "q" {
				closeQuit()
				cancel()
				fmt.Printf("\n%s Goodbye!\n", cliui.Goodbye)
				return
			}

			// Clear session
			if input == "clear" || input == "reset" {
				svc.ResetSession()
				fmt.Printf("✓ Session reset (new: %s)\n", svc.CurrentSessionID())
				continue
			}

			if manager != nil {
				tasks, parseErr := parseDelegatedTasks(input, func(name string) bool {
					_, err := manager.GetAgentByName(name)
					return err == nil
				})
				if parseErr != nil {
					fmt.Printf("%s %v\n\n", cliui.Error, parseErr)
					continue
				}
				if len(tasks) > 0 {
					fmt.Printf("\n🚀 Delegating %d task(s) in background...\n", len(tasks))
					if err := runDelegatedTaskChainAsync(chatCtx, manager, svc.CurrentSessionID(), tasks, taskFollower, true); err != nil {
						fmt.Printf("\n%s %v\n\n", cliui.Error, err)
					}

					continue
				}
			}

			// Process message normally (Concierge handling)
			done := make(chan struct{})
			requests <- chatRequest{Input: input, Done: done}
			<-done
		}
	}()

	// Wait for quit or interrupt
	select {
	case <-quitChan:
		// Normal exit
	case <-sigChan:
		// User pressed Ctrl+C
		closeQuit()
		cancel()
		fmt.Printf("\n\n%s Interrupted. Goodbye!\n", cliui.Goodbye)
	}

	return nil
}

func parseDelegatedTasks(input string, isKnownAgent func(name string) bool) ([]delegatedTask, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}

	words := strings.Fields(trimmed)
	if len(words) == 0 {
		return nil, nil
	}

	firstName, ok := parseMentionedAgent(words[0])
	if !ok {
		return nil, nil
	}
	if isKnownAgent != nil && !isKnownAgent(firstName) {
		return nil, fmt.Errorf("unknown agent: %s", firstName)
	}

	tasks := make([]delegatedTask, 0, 2)
	current := delegatedTask{AgentName: firstName}

	for _, word := range words[1:] {
		if nextName, isMention := parseMentionedAgent(word); isMention {
			if isKnownAgent != nil && isKnownAgent(nextName) {
				current.Instruction = strings.TrimSpace(current.Instruction)
				if current.Instruction == "" {
					return nil, fmt.Errorf("please provide an instruction for %s", current.AgentName)
				}
				tasks = append(tasks, current)
				current = delegatedTask{AgentName: nextName}
				continue
			}
		}

		if current.Instruction == "" {
			current.Instruction = word
		} else {
			current.Instruction += " " + word
		}
	}

	current.Instruction = strings.TrimSpace(current.Instruction)
	if current.Instruction == "" {
		return nil, fmt.Errorf("please provide an instruction for %s", current.AgentName)
	}
	tasks = append(tasks, current)

	return tasks, nil
}

func parseMentionedAgent(word string) (string, bool) {
	matches := agentMentionPattern.FindStringSubmatch(word)
	if len(matches) != 2 {
		return "", false
	}
	return matches[1], true
}
