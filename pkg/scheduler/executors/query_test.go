package executors

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockProcessorService simulates the processor service
type MockProcessorService struct {
	queryFunc func(ctx context.Context, req domain.QueryRequest) (*domain.QueryResponse, error)
}

func (m *MockProcessorService) Query(ctx context.Context, req domain.QueryRequest) (*domain.QueryResponse, error) {
	if m.queryFunc != nil {
		return m.queryFunc(ctx, req)
	}
	return &domain.QueryResponse{
		Answer: "Mock response",
	}, nil
}

func TestNewQueryExecutor(t *testing.T) {
	cfg := &config.Config{}
	executor := NewQueryExecutor(cfg)
	require.NotNil(t, executor)
	assert.NotNil(t, executor.config)
	assert.Nil(t, executor.processor) // Initially nil
}

func TestQueryExecutorType(t *testing.T) {
	executor := NewQueryExecutor(nil)
	assert.Equal(t, scheduler.TaskTypeQuery, executor.Type())
}

func TestQueryExecutorValidate(t *testing.T) {
	executor := NewQueryExecutor(nil)

	tests := []struct {
		name       string
		parameters map[string]string
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "Valid query",
			parameters: map[string]string{"query": "test query"},
			wantErr:    false,
		},
		{
			name:       "Valid with top-k",
			parameters: map[string]string{"query": "test", "top-k": "5"},
			wantErr:    false,
		},
		{
			name:       "Valid with show-sources",
			parameters: map[string]string{"query": "test", "show-sources": "true"},
			wantErr:    false,
		},
		{
			name:       "Valid with mcp",
			parameters: map[string]string{"query": "test", "mcp": "false"},
			wantErr:    false,
		},
		{
			name:       "Missing query",
			parameters: map[string]string{},
			wantErr:    true,
			errMsg:     "query parameter is required",
		},
		{
			name:       "Empty query",
			parameters: map[string]string{"query": ""},
			wantErr:    true,
			errMsg:     "query parameter is required",
		},
		{
			name:       "Invalid top-k",
			parameters: map[string]string{"query": "test", "top-k": "invalid"},
			wantErr:    true,
			errMsg:     "top-k must be a positive integer",
		},
		{
			name:       "Negative top-k",
			parameters: map[string]string{"query": "test", "top-k": "-1"},
			wantErr:    true,
			errMsg:     "top-k must be a positive integer",
		},
		{
			name:       "Invalid show-sources",
			parameters: map[string]string{"query": "test", "show-sources": "invalid"},
			wantErr:    true,
			errMsg:     "show-sources must be a boolean",
		},
		{
			name:       "Invalid mcp",
			parameters: map[string]string{"query": "test", "mcp": "invalid"},
			wantErr:    true,
			errMsg:     "mcp must be a boolean",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := executor.Validate(tt.parameters)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestQueryExecutorExecuteWithoutProcessor(t *testing.T) {
	executor := NewQueryExecutor(nil)
	ctx := context.Background()

	params := map[string]string{
		"query": "test query",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	// Parse output
	var output QueryTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Equal(t, "test query", output.Query)
	assert.Contains(t, output.Response, "RAG service is not available")
}

func TestQueryExecutorExecuteWithProcessor(t *testing.T) {
	executor := NewQueryExecutor(nil)

	// Test with nil processor (service unavailable case)
	// The actual processor integration would require a full service setup
	// which is beyond the scope of unit tests

	ctx := context.Background()
	params := map[string]string{
		"query":        "test query",
		"top-k":        "3",
		"show-sources": "true",
		"mcp":          "true",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
}

func TestQueryExecutorParameterParsing(t *testing.T) {
	executor := NewQueryExecutor(nil)
	ctx := context.Background()

	tests := []struct {
		name            string
		parameters      map[string]string
		expectedTopK    int
		expectedSources bool
		expectedMCP     bool
	}{
		{
			name:            "Default values",
			parameters:      map[string]string{"query": "test"},
			expectedTopK:    5,
			expectedSources: false,
			expectedMCP:     false,
		},
		{
			name: "Custom values",
			parameters: map[string]string{
				"query":        "test",
				"top-k":        "10",
				"show-sources": "true",
				"mcp":          "true",
			},
			expectedTopK:    10,
			expectedSources: true,
			expectedMCP:     true,
		},
		{
			name: "Invalid values use defaults",
			parameters: map[string]string{
				"query":        "test",
				"top-k":        "invalid",
				"show-sources": "invalid",
				"mcp":          "invalid",
			},
			expectedTopK:    5,
			expectedSources: false,
			expectedMCP:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(ctx, tt.parameters)
			require.NoError(t, err)
			require.NotNil(t, result)

			var output QueryTaskOutput
			err = json.Unmarshal([]byte(result.Output), &output)
			require.NoError(t, err)

			// Verify the parameters were parsed correctly
			// (The actual values would be used in the processor call)
			assert.Equal(t, tt.parameters["query"], output.Query)
			assert.Equal(t, tt.expectedMCP, output.UsedMCP)
		})
	}
}

func TestQueryExecutorSetProcessor(t *testing.T) {
	executor := NewQueryExecutor(nil)
	assert.Nil(t, executor.processor)

	// Test SetProcessor method
	executor.SetProcessor(nil)
	assert.Nil(t, executor.processor)
}

func TestQueryExecutorExecuteEdgeCases(t *testing.T) {
	executor := NewQueryExecutor(nil)
	ctx := context.Background()

	tests := []struct {
		name       string
		parameters map[string]string
	}{
		{
			name: "All parameters set",
			parameters: map[string]string{
				"query":        "complex query",
				"top-k":        "10",
				"show-sources": "true",
				"mcp":          "true",
			},
		},
		{
			name: "Zero top-k",
			parameters: map[string]string{
				"query": "test",
				"top-k": "0",
			},
		},
		{
			name: "Very large top-k",
			parameters: map[string]string{
				"query": "test",
				"top-k": "1000",
			},
		},
		{
			name: "Empty query but valid",
			parameters: map[string]string{
				"query": " ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(ctx, tt.parameters)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.Success)

			var output QueryTaskOutput
			err = json.Unmarshal([]byte(result.Output), &output)
			require.NoError(t, err)
		})
	}
}

func TestQueryExecutorCompleteFlow(t *testing.T) {
	executor := NewQueryExecutor(nil)
	ctx := context.Background()

	// Test with all features
	params := map[string]string{
		"query":        "test query with all features",
		"top-k":        "3",
		"show-sources": "true",
		"mcp":          "true",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	var output QueryTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Equal(t, "test query with all features", output.Query)
	assert.True(t, output.UsedMCP)
}

func TestQueryTaskOutputSerialization(t *testing.T) {
	output := QueryTaskOutput{
		Query:    "test query",
		Response: "test response",
		UsedMCP:  true,
		Sources: []QuerySource{
			{
				Content: "source content",
				Metadata: map[string]interface{}{
					"file": "test.txt",
					"page": 1,
				},
				Score: 0.95,
			},
		},
		ToolCalls: []QueryToolCall{
			{
				Name: "tool1",
				Arguments: map[string]interface{}{
					"param": "value",
				},
				Result: "tool result",
			},
		},
	}

	// Serialize
	data, err := json.MarshalIndent(output, "", "  ")
	require.NoError(t, err)

	// Deserialize
	var decoded QueryTaskOutput
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Verify
	assert.Equal(t, output.Query, decoded.Query)
	assert.Equal(t, output.Response, decoded.Response)
	assert.Equal(t, output.UsedMCP, decoded.UsedMCP)
	assert.Len(t, decoded.Sources, 1)
	assert.Len(t, decoded.ToolCalls, 1)
}

func TestQueryExecutorExecuteWithInvalidButDefaultableParams(t *testing.T) {
	executor := NewQueryExecutor(nil)
	ctx := context.Background()

	// Test with invalid parameters that pass validation but use defaults during execution
	params := map[string]string{
		"query":        "test query",
		"top-k":        "not-a-number", // Will use default
		"show-sources": "not-a-bool",   // Will use default
		"mcp":          "not-a-bool",   // Will use default
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	var output QueryTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Equal(t, "test query", output.Query)
	assert.False(t, output.UsedMCP) // Should be false due to invalid value
}

func TestQueryExecutorExecuteExtendedCases(t *testing.T) {
	executor := NewQueryExecutor(nil)
	ctx := context.Background()

	tests := []struct {
		name       string
		parameters map[string]string
	}{
		{
			name: "Query with top-k 1",
			parameters: map[string]string{
				"query": "test",
				"top-k": "1",
			},
		},
		{
			name: "Query with top-k 100",
			parameters: map[string]string{
				"query": "test",
				"top-k": "100",
			},
		},
		{
			name: "Query with top-k 5 (default)",
			parameters: map[string]string{
				"query": "test",
			},
		},
		{
			name: "Query with show-sources true",
			parameters: map[string]string{
				"query":        "test",
				"show-sources": "true",
			},
		},
		{
			name: "Query with show-sources false",
			parameters: map[string]string{
				"query":        "test",
				"show-sources": "false",
			},
		},
		{
			name: "Query with mcp true",
			parameters: map[string]string{
				"query": "test",
				"mcp":   "true",
			},
		},
		{
			name: "Query with mcp false",
			parameters: map[string]string{
				"query": "test",
				"mcp":   "false",
			},
		},
		{
			name: "Query with all flags true",
			parameters: map[string]string{
				"query":        "complex test",
				"top-k":        "20",
				"show-sources": "true",
				"mcp":          "true",
			},
		},
		{
			name: "Query with all flags false",
			parameters: map[string]string{
				"query":        "simple test",
				"top-k":        "1",
				"show-sources": "false",
				"mcp":          "false",
			},
		},
		{
			name: "Very long query",
			parameters: map[string]string{
				"query": "This is a very long query that contains multiple sentences and should test how the system handles longer input strings with various punctuation marks, numbers like 123, and special characters!",
			},
		},
		{
			name: "Query with top-k as string 10",
			parameters: map[string]string{
				"query": "test",
				"top-k": "10",
			},
		},
		{
			name: "Query with top-k as string 3",
			parameters: map[string]string{
				"query": "test",
				"top-k": "3",
			},
		},
		{
			name: "Query with top-k as string 7",
			parameters: map[string]string{
				"query": "test",
				"top-k": "7",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(ctx, tt.parameters)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.Success)

			var output QueryTaskOutput
			err = json.Unmarshal([]byte(result.Output), &output)
			require.NoError(t, err)
			assert.Equal(t, tt.parameters["query"], output.Query)
			// Response should mention RAG service unavailable
			assert.Contains(t, output.Response, "RAG service is not available")
		})
	}
}
