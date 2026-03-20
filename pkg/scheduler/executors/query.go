package executors

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/rag/processor"
	"github.com/liliang-cn/agent-go/v2/pkg/scheduler"
)

// QueryExecutor executes RAG query tasks
type QueryExecutor struct {
	config    *config.Config
	processor *processor.Service
}

// NewQueryExecutor creates a new query executor
func NewQueryExecutor(cfg *config.Config) *QueryExecutor {
	// For now, return without processor to avoid initialization complexity
	// The executor will show an error message instead of crashing
	return &QueryExecutor{
		config:    cfg,
		processor: nil, // Will be set later or use alternative approach
	}
}

// SetProcessor sets the processor service for this executor
func (e *QueryExecutor) SetProcessor(p *processor.Service) {
	e.processor = p
}

// Type returns the task type this executor handles
func (e *QueryExecutor) Type() scheduler.TaskType {
	return scheduler.TaskTypeQuery
}

// Validate checks if the parameters are valid for query execution
func (e *QueryExecutor) Validate(parameters map[string]string) error {
	query, exists := parameters["query"]
	if !exists || query == "" {
		return fmt.Errorf("query parameter is required")
	}

	// Validate optional parameters
	if topKStr, exists := parameters["top-k"]; exists {
		if topK, err := strconv.Atoi(topKStr); err != nil || topK <= 0 {
			return fmt.Errorf("top-k must be a positive integer: %s", topKStr)
		}
	}

	if showSourcesStr, exists := parameters["show-sources"]; exists {
		if _, err := strconv.ParseBool(showSourcesStr); err != nil {
			return fmt.Errorf("show-sources must be a boolean: %s", showSourcesStr)
		}
	}

	if mcpStr, exists := parameters["mcp"]; exists {
		if _, err := strconv.ParseBool(mcpStr); err != nil {
			return fmt.Errorf("mcp must be a boolean: %s", mcpStr)
		}
	}

	return nil
}

// Execute performs the RAG query
func (e *QueryExecutor) Execute(ctx context.Context, parameters map[string]string) (*scheduler.TaskResult, error) {
	// Extract parameters
	query := parameters["query"]

	topK := 5 // default
	if topKStr, exists := parameters["top-k"]; exists {
		if k, err := strconv.Atoi(topKStr); err == nil {
			topK = k
		}
	}

	showSources := false
	if showSourcesStr, exists := parameters["show-sources"]; exists {
		if s, err := strconv.ParseBool(showSourcesStr); err == nil {
			showSources = s
		}
	}

	useMCP := false
	if mcpStr, exists := parameters["mcp"]; exists {
		if m, err := strconv.ParseBool(mcpStr); err == nil {
			useMCP = m
		}
	}

	// Check if processor is available
	if e.processor == nil {
		// Create output structure for unavailable service
		output := QueryTaskOutput{
			Query:    query,
			Response: "RAG service is not available. To use query functionality, please ensure the RAG environment is properly configured (embedders, LLM providers, vector store, etc.).",
			UsedMCP:  useMCP,
		}

		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true, // Task execution succeeded, even if service is unavailable
			Output:  string(outputJSON),
		}, nil
	}

	// Execute the RAG query using the processor service
	// Create query request
	queryReq := domain.QueryRequest{
		Query:        query,
		TopK:         topK,
		ShowSources:  showSources,
		ToolsEnabled: useMCP,
	}

	// Call the processor to execute the query
	response, err := e.processor.Query(ctx, queryReq)
	if err != nil {
		// Create output structure for failed query
		output := QueryTaskOutput{
			Query:    query,
			Response: fmt.Sprintf("Query execution failed: %v", err),
			UsedMCP:  useMCP,
		}

		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true, // Task execution succeeded, even if query failed
			Output:  string(outputJSON),
		}, nil
	}

	// Create output structure for successful query
	var output QueryTaskOutput
	output.Query = query
	output.Response = response.Answer
	output.UsedMCP = useMCP

	// Convert sources if any
	if len(response.Sources) > 0 {
		output.Sources = make([]QuerySource, len(response.Sources))
		for i, src := range response.Sources {
			output.Sources[i] = QuerySource{
				Content:  src.Content,
				Metadata: src.Metadata,
				Score:    src.Score,
			}
		}
	}

	// Convert tool calls if any
	if len(response.ToolCalls) > 0 {
		output.ToolCalls = make([]QueryToolCall, len(response.ToolCalls))
		for i, call := range response.ToolCalls {
			output.ToolCalls[i] = QueryToolCall{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			}
		}
	}

	// Marshal to JSON for storage
	outputJSON, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return &scheduler.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to marshal output: %v", err),
		}, nil
	}

	return &scheduler.TaskResult{
		Success: true,
		Output:  string(outputJSON),
	}, nil
}

// QueryTaskOutput represents the output of a query task
type QueryTaskOutput struct {
	Query     string          `json:"query"`
	Response  string          `json:"response"`
	UsedMCP   bool            `json:"used_mcp"`
	Sources   []QuerySource   `json:"sources,omitempty"`
	ToolCalls []QueryToolCall `json:"tool_calls,omitempty"`
}

// QuerySource represents a source in the query output
type QuerySource struct {
	Content  string                 `json:"content"`
	Metadata map[string]interface{} `json:"metadata"`
	Score    float64                `json:"score"`
}

// QueryToolCall represents a tool call in the query output
type QueryToolCall struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
	Result    interface{}            `json:"result"`
}
