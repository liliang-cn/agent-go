package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/liliang-cn/agent-go/pkg/skills"
)

// ToolExecutor executes tool calls
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error)
}

// Executor executes agent plans step by step
type Executor struct {
	service       *Service
	llmService    domain.Generator
	toolExecutor  ToolExecutor
	mcpService    MCPToolExecutor
	ragProcessor  domain.Processor
	memoryService domain.MemoryService
	skillsService *skills.Service
}

// MCPToolExecutor defines the interface for MCP tool execution
type MCPToolExecutor interface {
	CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error)
	ListTools() []domain.ToolDefinition
	AddServer(ctx context.Context, name string, command string, args []string) error
}

// NewExecutor creates a new executor
func NewExecutor(
	service *Service,
	llmService domain.Generator,
	toolExecutor ToolExecutor,
	mcpService MCPToolExecutor,
	ragProcessor domain.Processor,
	memoryService domain.MemoryService,
) *Executor {
	return &Executor{
		service:       service,
		llmService:    llmService,
		toolExecutor:  toolExecutor,
		mcpService:    mcpService,
		ragProcessor:  ragProcessor,
		memoryService: memoryService,
		skillsService: nil,
	}
}

// SetSkillsService sets the skills service
func (e *Executor) SetSkillsService(skillsService *skills.Service) {
	e.skillsService = skillsService
}

// ExecutePlan executes a plan step by step
func (e *Executor) ExecutePlan(ctx context.Context, plan *Plan, session *Session) (*ExecutionResult, error) {
	startTime := time.Now()
	plan.Status = PlanStatusRunning

	stepsTotal := len(plan.Steps)
	stepsDone := 0
	stepsFailed := 0

	var finalResult interface{}
	var firstError string

	// Execute steps in order, respecting dependencies
	for i := range plan.Steps {
		step := &plan.Steps[i]

		// Check if dependencies are satisfied
		if !e.dependenciesSatisfied(step, plan.Steps) {
			step.Status = StepStatusFailed
			step.Error = "Dependencies not satisfied"
			stepsFailed++
			continue
		}

		// Execute the step
		result, err := e.ExecuteStep(ctx, step, plan, session)
		if err != nil {
			step.Status = StepStatusFailed
			step.Error = err.Error()
			stepsFailed++
			if firstError == "" {
				firstError = err.Error()
			}
		} else {
			step.Status = StepStatusCompleted
			step.Result = result
			stepsDone++
			// Last step's result becomes the final result
			if i == len(plan.Steps)-1 {
				finalResult = result
			}
		}

		step.CompletedAt = &[]time.Time{time.Now()}[0]
		plan.UpdatedAt = time.Now()
	}

	// Update plan status
	if stepsFailed == 0 {
		plan.Status = PlanStatusCompleted
	} else if stepsDone > 0 {
		plan.Status = PlanStatusCompleted // Partial success
	} else {
		plan.Status = PlanStatusFailed
	}

	duration := time.Since(startTime)

	result := &ExecutionResult{
		PlanID:          plan.ID,
		SessionID:       plan.SessionID,
		Success:         stepsFailed == 0,
		StepsTotal:      stepsTotal,
		StepsDone:       stepsDone,
		StepsFailed:     stepsFailed,
		StartedAt:       &startTime,
		CompletedAt:     &[]time.Time{startTime.Add(duration)}[0],
		ToolCalls:       stepsDone + stepsFailed,
		EstimatedTokens: e.service.estimateTextTokens(plan.Goal) + e.service.estimateTextTokens(formatResultForContent(finalResult)),
		FinalResult:     finalResult,
		Error:           firstError,
		Duration:        duration.String(),
	}

	// Store memories after successful task completion
	if e.memoryService != nil {
		log.Println("[Agent] Analyzing task for long-term memory storage...")
		req := &domain.MemoryStoreRequest{
			SessionID:    plan.SessionID,
			TaskGoal:     plan.Goal,
			TaskResult:   formatResultForContent(finalResult),
			ExecutionLog: e.buildExecutionLog(plan),
		}
		if session != nil {
			req.AgentID = session.AgentID
		}
		err := e.memoryService.StoreIfWorthwhile(context.Background(), req)
		if err != nil {
			log.Printf("[Agent] Warning: memory storage failed: %v", err)
		} else {
			log.Println("[Agent] Memory analysis completed.")
		}
	}

	return result, nil
}

// ExecuteStep executes a single step
func (e *Executor) ExecuteStep(ctx context.Context, step *Step, plan *Plan, session *Session) (interface{}, error) {
	step.Status = StepStatusRunning
	startTime := time.Now()
	step.StartedAt = &startTime

	// Preprocess arguments - handle placeholders like {{PREVIOUS_OUTPUT}}
	args := e.preprocessArguments(step, plan)

	// Route to appropriate executor based on tool name
	result, err := e.executeTool(ctx, step.Tool, args, plan, session)
	if err != nil {
		return nil, fmt.Errorf("tool execution failed: %w", err)
	}

	// Write result to file if OutputFile is specified
	if step.OutputFile != "" && result != nil {
		if err := e.writeResultToFile(step.OutputFile, result); err != nil {
			return nil, fmt.Errorf("failed to write to file %s: %w", step.OutputFile, err)
		}
	}

	return result, nil
}

// preprocessArguments handles special placeholders in arguments
func (e *Executor) preprocessArguments(step *Step, plan *Plan) map[string]interface{} {
	args := step.Arguments
	if args == nil {
		args = make(map[string]interface{})
	}

	// Find the current step index
	var stepIndex int = -1
	for i, s := range plan.Steps {
		if s.ID == step.ID {
			stepIndex = i
			break
		}
	}

	// Handle {{PREVIOUS_OUTPUT}} or similar placeholders
	for key, value := range args {
		if strValue, ok := value.(string); ok {
			if strings.Contains(strValue, "{{PREVIOUS_OUTPUT}}") || strings.Contains(strValue, "{{previous_output}}") {
				// Find the previous completed step
				if stepIndex > 0 {
					prevStep := &plan.Steps[stepIndex-1]
					if prevStep.Status == StepStatusCompleted && prevStep.Result != nil {
						// Convert previous result to string
						prevOutput := formatResultForContent(prevStep.Result)
						args[key] = strings.ReplaceAll(strValue, "{{PREVIOUS_OUTPUT}}", prevOutput)
						args[key] = strings.ReplaceAll(args[key].(string), "{{previous_output}}", prevOutput)
					}
				}
			}
		}
	}

	// Special handling for filesystem write_file: if content is missing or placeholder, use previous output
	if isFileWriteTool(step.Tool) {
		if _, hasContent := args["content"]; !hasContent {
			// Try to get content from previous step
			if stepIndex > 0 {
				prevStep := &plan.Steps[stepIndex-1]
				if prevStep.Status == StepStatusCompleted && prevStep.Result != nil {
					args["content"] = formatResultForContent(prevStep.Result)
				}
			}
		}
	}

	return args
}

// formatResultForContent formats a step result for use as file content
func formatResultForContent(result interface{}) string {
	if result == nil {
		return ""
	}

	switch v := result.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		// For complex types, try to format as JSON
		jsonBytes, err := json.Marshal(result)
		if err == nil {
			return string(jsonBytes)
		}
		return fmt.Sprintf("%v", result)
	}
}

// isFileWriteTool checks if the tool name indicates a file write operation
func isFileWriteTool(toolName string) bool {
	return strings.Contains(strings.ToLower(toolName), "write_file") ||
		strings.Contains(strings.ToLower(toolName), "create_file") ||
		strings.Contains(strings.ToLower(toolName), "filesystem") && strings.Contains(strings.ToLower(toolName), "write")
}

// executeTool routes the tool call to the appropriate executor
func (e *Executor) executeTool(ctx context.Context, toolName string, args map[string]interface{}, plan *Plan, session *Session) (interface{}, error) {
	// Normalize args to ensure we have a valid map
	if args == nil {
		args = make(map[string]interface{})
	}

	// Use SubAgent for tool execution to ensure it runs in a separate goroutine
	tc := domain.ToolCall{
		ID:   fmt.Sprintf("exec_%d", time.Now().UnixNano()),
		Type: "function",
		Function: domain.FunctionCall{
			Name:      toolName,
			Arguments: args,
		},
	}

	// Determine agent: use the one associated with the session if possible
	targetAgent := e.service.agent
	if session != nil && session.AgentID != "" && e.service.registry != nil {
		if a, ok := e.service.registry.GetAgent(session.AgentID); ok {
			targetAgent = a
		}
	}

	result, err, _ := e.service.executeToolViaSubAgent(ctx, targetAgent, session, tc)
	return result, err
}

// tryRAGTool attempts to execute a RAG-related tool
func (e *Executor) tryRAGTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	switch toolName {
	case "rag_query", "query":
		query, ok := args["query"].(string)
		if !ok {
			return nil, fmt.Errorf("missing 'query' argument")
		}
		req := domain.QueryRequest{
			Query:        query,
			TopK:         5,
			Temperature:  0.7,
			MaxTokens:    1000,
			Stream:       false,
			ShowThinking: false,
			ShowSources:  true,
		}
		if topK, ok := args["top_k"].(float64); ok {
			req.TopK = int(topK)
		}
		return e.ragProcessor.Query(ctx, req)

	case "rag_ingest", "ingest":
		content, _ := args["content"].(string)
		filePath, _ := args["file_path"].(string)
		if content == "" && filePath == "" {
			return nil, fmt.Errorf("missing 'content' or 'file_path' argument")
		}
		req := domain.IngestRequest{
			Content:   content,
			FilePath:  filePath,
			ChunkSize: 1000,
			Overlap:   200,
		}
		return e.ragProcessor.Ingest(ctx, req)

	default:
		return nil, fmt.Errorf("not a RAG tool: %s", toolName)
	}
}

// tryMCPTool attempts to execute an MCP tool

// executeLLM executes a general LLM call
func (e *Executor) executeLLM(ctx context.Context, args map[string]interface{}, plan *Plan, session *Session) (interface{}, error) {
	prompt, ok := args["prompt"].(string)
	if !ok || prompt == "" {
		// Fall back to using the goal or step description
		if plan != nil && plan.Goal != "" {
			prompt = plan.Goal
		}
		if prompt == "" {
			return nil, fmt.Errorf("missing 'prompt' argument")
		}
	}

	// Build prompt with session context
	fullPrompt := e.buildPromptWithContext(prompt, session)

	opts := &domain.GenerationOptions{
		Temperature: 0.7,
		MaxTokens:   1000,
	}
	if temp, ok := args["temperature"].(float64); ok {
		opts.Temperature = temp
	}
	if maxTokens, ok := args["max_tokens"].(float64); ok {
		opts.MaxTokens = int(maxTokens)
	}

	result, err := e.llmService.Generate(ctx, fullPrompt, opts)
	if err != nil {
		return nil, fmt.Errorf("LLM generation failed: %w", err)
	}

	return result, nil
}

// buildPromptWithContext builds a prompt including session context
func (e *Executor) buildPromptWithContext(prompt string, session *Session) string {
	if session == nil {
		return prompt
	}

	var sb strings.Builder

	// Add summary if available
	if session.Summary != "" {
		sb.WriteString("Conversation Summary:\n")
		sb.WriteString(session.Summary)
		sb.WriteString("\n\n")
	}

	messages := session.GetMessages()
	messageCount := len(messages)

	// If we have messages, add recent ones for context
	if messageCount > 0 {
		// Get last N messages for context (up to 10)
		historyLimit := 10
		startIdx := 0
		if messageCount > historyLimit {
			startIdx = messageCount - historyLimit
		}

		sb.WriteString("Recent conversation history:\n")
		// Exclude the last message if it's identical to the current prompt to avoid duplication
		endIdx := messageCount
		if messageCount > 0 && messages[messageCount-1].Content == prompt {
			endIdx = messageCount - 1
		}

		for i := startIdx; i < endIdx; i++ {
			msg := messages[i]
			sb.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, msg.Content))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Current Task:\n")
	sb.WriteString(prompt)

	return sb.String()
}

// dependenciesSatisfied checks if all dependencies for a step are satisfied
func (e *Executor) dependenciesSatisfied(step *Step, steps []Step) bool {
	for _, depID := range step.DependsOn {
		found := false
		for _, s := range steps {
			if s.ID == depID && s.Status == StepStatusCompleted {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// FormatResult formats a step result for display
func FormatResult(result interface{}) string {
	if result == nil {
		return "(empty)"
	}

	switch v := result.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case error:
		return v.Error()
	default:
		jsonBytes, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", result)
		}
		return string(jsonBytes)
	}
}

// tryMCPTool attempts to execute an MCP tool
func (e *Executor) tryMCPTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	// Call MCP service
	result, err := e.mcpService.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, err
	}

	// The result is *mcp.ToolResult with fields: Success (bool), Data (interface{}), Error (string)
	// Handle via type assertion since we can't import mcp package directly
	if result == nil {
		return nil, fmt.Errorf("MCP tool returned nil result")
	}

	// Try to convert to map for generic access
	if resultMap, ok := result.(map[string]interface{}); ok {
		if success, ok := resultMap["success"].(bool); ok && !success {
			errMsg, _ := resultMap["error"].(string)
			return nil, fmt.Errorf("MCP tool error: %s", errMsg)
		}
		data, ok := resultMap["data"]
		if ok {
			return data, nil
		}
		return result, nil
	}

	// If it's not a map, just return the result as-is
	return result, nil
}

// trySkillTool attempts to execute a skill tool
func (e *Executor) trySkillTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	if e.skillsService == nil {
		return nil, fmt.Errorf("skills service not available")
	}

	// Extract skill ID from tool name (remove "skill_" prefix)
	skillID := strings.TrimPrefix(toolName, "skill_")

	// Convert args to variables format for skills service
	variables := make(map[string]interface{})
	for k, v := range args {
		variables[k] = v
	}

	req := &skills.ExecutionRequest{
		SkillID:     skillID,
		Variables:   variables,
		Interactive: false,
		Context:     nil,
	}

	result, err := e.skillsService.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("skill execution failed: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("skill execution failed: %s", result.Error)
	}

	// Return the output as the result
	return result.Output, nil
}

// buildExecutionLog creates a log of the plan execution for memory extraction
func (e *Executor) buildExecutionLog(plan *Plan) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Goal: %s\n", plan.Goal))
	sb.WriteString(fmt.Sprintf("Steps: %d\n", len(plan.Steps)))

	for i, step := range plan.Steps {
		sb.WriteString(fmt.Sprintf("\n[Step %d] %s\n", i+1, step.Description))
		sb.WriteString(fmt.Sprintf("  Tool: %s\n", step.Tool))
		sb.WriteString(fmt.Sprintf("  Status: %s\n", step.Status))
		if step.Error != "" {
			sb.WriteString(fmt.Sprintf("  Error: %s\n", step.Error))
		}
		if step.Result != nil {
			resultPreview := formatResultForContent(step.Result)
			if len(resultPreview) > 200 {
				resultPreview = resultPreview[:200] + "..."
			}
			sb.WriteString(fmt.Sprintf("  Result: %s\n", resultPreview))
		}
	}

	return sb.String()
}

// writeResultToFile writes the result content to a file
func (e *Executor) writeResultToFile(filePath string, result interface{}) error {
	content := formatResultForContent(result)

	// Create directory if needed
	dir := filepath.Dir(filePath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}
	}

	// Write to file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	log.Printf("[Agent] ✅ Wrote result to %s (%d bytes)\n", filePath, len(content))
	return nil
}
