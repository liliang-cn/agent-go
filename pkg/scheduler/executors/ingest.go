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

// IngestExecutor executes document ingestion tasks
type IngestExecutor struct {
	config    *config.Config
	processor *processor.Service
}

// NewIngestExecutor creates a new ingest executor
func NewIngestExecutor(cfg *config.Config) *IngestExecutor {
	return &IngestExecutor{
		config: cfg,
	}
}

// SetProcessor sets the processor service for this executor
func (e *IngestExecutor) SetProcessor(p *processor.Service) {
	e.processor = p
}

// Type returns the task type this executor handles
func (e *IngestExecutor) Type() scheduler.TaskType {
	return scheduler.TaskTypeIngest
}

// Validate checks if the parameters are valid for ingest execution
func (e *IngestExecutor) Validate(parameters map[string]string) error {
	path, exists := parameters["path"]
	if !exists || path == "" {
		return fmt.Errorf("path parameter is required")
	}

	if recursiveStr, exists := parameters["recursive"]; exists {
		if _, err := strconv.ParseBool(recursiveStr); err != nil {
			return fmt.Errorf("recursive must be a boolean: %s", recursiveStr)
		}
	}

	return nil
}

// Execute runs a document ingestion task
func (e *IngestExecutor) Execute(ctx context.Context, parameters map[string]string) (*scheduler.TaskResult, error) {
	path := parameters["path"]

	recursive := false
	if recursiveStr, exists := parameters["recursive"]; exists {
		if r, err := strconv.ParseBool(recursiveStr); err == nil {
			recursive = r
		}
	}

	// Check if processor is available
	if e.processor == nil {
		// Create output structure for unavailable service
		output := IngestTaskOutput{
			Path:      path,
			Recursive: recursive,
			Result:    "Ingest service is not available. To use ingestion functionality, please ensure the RAG environment is properly configured (embedders, vector store, document store, etc.).",
			Success:   false,
		}

		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true, // Task execution succeeded, even if service is unavailable
			Output:  string(outputJSON),
		}, nil
	}

	// Create ingest request
	ingestReq := domain.IngestRequest{
		FilePath: path,
		// Add other optional parameters
	}

	// Use the processor to perform real ingestion
	response, err := e.processor.Ingest(ctx, ingestReq)
	if err != nil {
		// Create output structure for failed ingestion
		output := IngestTaskOutput{
			Path:      path,
			Recursive: recursive,
			Result:    fmt.Sprintf("Failed to ingest documents: %v", err),
			Success:   false,
		}

		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true, // Task execution succeeded, even if ingestion failed
			Output:  string(outputJSON),
		}, nil
	}

	// Create output structure for successful ingestion
	output := IngestTaskOutput{
		Path:       path,
		Recursive:  recursive,
		DocumentID: response.DocumentID,
		ChunkCount: response.ChunkCount,
		Result:     response.Message,
		Success:    response.Success,
	}

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

// IngestTaskOutput represents the output of an ingest task
type IngestTaskOutput struct {
	Path       string `json:"path"`
	Recursive  bool   `json:"recursive"`
	DocumentID string `json:"document_id"`
	ChunkCount int    `json:"chunk_count"`
	Result     string `json:"result"`
	Success    bool   `json:"success"`
}
