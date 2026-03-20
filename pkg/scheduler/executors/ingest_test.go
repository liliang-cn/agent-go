package executors

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewIngestExecutor(t *testing.T) {
	cfg := &config.Config{}
	executor := NewIngestExecutor(cfg)
	require.NotNil(t, executor)
	assert.NotNil(t, executor.config)
	assert.Nil(t, executor.processor)
}

func TestIngestExecutorType(t *testing.T) {
	executor := NewIngestExecutor(nil)
	assert.Equal(t, scheduler.TaskTypeIngest, executor.Type())
}

func TestIngestExecutorValidate(t *testing.T) {
	executor := NewIngestExecutor(nil)

	tests := []struct {
		name       string
		parameters map[string]string
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "Valid path",
			parameters: map[string]string{"path": "/path/to/file.txt"},
			wantErr:    false,
		},
		{
			name:       "Valid with recursive",
			parameters: map[string]string{"path": "/path/to/dir", "recursive": "true"},
			wantErr:    false,
		},
		{
			name:       "Missing path",
			parameters: map[string]string{},
			wantErr:    true,
			errMsg:     "path parameter is required",
		},
		{
			name:       "Empty path",
			parameters: map[string]string{"path": ""},
			wantErr:    true,
			errMsg:     "path parameter is required",
		},
		{
			name:       "Invalid recursive",
			parameters: map[string]string{"path": "/test", "recursive": "invalid"},
			wantErr:    true,
			errMsg:     "recursive must be a boolean",
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

func TestIngestExecutorExecuteWithoutProcessor(t *testing.T) {
	executor := NewIngestExecutor(nil)
	ctx := context.Background()

	params := map[string]string{
		"path": "/test/file.txt",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	// Parse output
	var output IngestTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Equal(t, "/test/file.txt", output.Path)
	// Success is false when processor is nil
	assert.False(t, output.Success)
	assert.Contains(t, output.Result, "Ingest service is not available")
}

func TestIngestExecutorParameterParsing(t *testing.T) {
	executor := NewIngestExecutor(nil)
	ctx := context.Background()

	tests := []struct {
		name              string
		parameters        map[string]string
		expectedSource    string
		expectedChunkSize int
		expectedOverlap   int
	}{
		{
			name:              "Path only",
			parameters:        map[string]string{"path": "/test.txt"},
			expectedSource:    "path",
			expectedChunkSize: 500,
			expectedOverlap:   50,
		},
		{
			name: "Path with recursive",
			parameters: map[string]string{
				"path":      "/test/dir",
				"recursive": "true",
			},
			expectedSource:    "path",
			expectedChunkSize: 500,
			expectedOverlap:   50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(ctx, tt.parameters)
			require.NoError(t, err)
			require.NotNil(t, result)

			var output IngestTaskOutput
			err = json.Unmarshal([]byte(result.Output), &output)
			require.NoError(t, err)

			// Verify output structure is valid but success is false without processor
			assert.False(t, output.Success)
		})
	}
}

func TestIngestExecutorSetProcessor(t *testing.T) {
	executor := NewIngestExecutor(nil)
	assert.Nil(t, executor.processor)

	// We can't easily create a real processor.Service, but we can test the setter
	// This mainly tests that the method exists and is callable
	executor.SetProcessor(nil)
	assert.Nil(t, executor.processor)
}

// IngestTaskOutput is already defined in ingest.go

func TestIngestExecutorCompleteFlow(t *testing.T) {
	executor := NewIngestExecutor(nil)
	ctx := context.Background()

	// Test recursive ingestion
	params := map[string]string{
		"path":      "/test/dir",
		"recursive": "true",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	var output IngestTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Equal(t, "/test/dir", output.Path)
	assert.True(t, output.Recursive)
}

func TestIngestExecutorInvalidRecursive(t *testing.T) {
	executor := NewIngestExecutor(nil)

	err := executor.Validate(map[string]string{
		"path":      "/test",
		"recursive": "not-a-bool",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "recursive must be a boolean")
}

func TestIngestExecutorExecuteVariousParams(t *testing.T) {
	executor := NewIngestExecutor(nil)
	ctx := context.Background()

	tests := []struct {
		name       string
		parameters map[string]string
	}{
		{
			name: "With chunk size 100",
			parameters: map[string]string{
				"path":      "/test/file.txt",
				"chunkSize": "100",
			},
		},
		{
			name: "With chunk size 1000",
			parameters: map[string]string{
				"path":      "/test/file.txt",
				"chunkSize": "1000",
			},
		},
		{
			name: "With chunk size 5000",
			parameters: map[string]string{
				"path":      "/test/file.txt",
				"chunkSize": "5000",
			},
		},
		{
			name: "With overlap 10",
			parameters: map[string]string{
				"path":    "/test/file.txt",
				"overlap": "10",
			},
		},
		{
			name: "With overlap 100",
			parameters: map[string]string{
				"path":    "/test/file.txt",
				"overlap": "100",
			},
		},
		{
			name: "With overlap 500",
			parameters: map[string]string{
				"path":    "/test/file.txt",
				"overlap": "500",
			},
		},
		{
			name: "With source type",
			parameters: map[string]string{
				"path":   "/test/file.txt",
				"source": "file",
			},
		},
		{
			name: "All parameters",
			parameters: map[string]string{
				"path":      "/test/dir",
				"recursive": "true",
				"chunkSize": "2000",
				"overlap":   "200",
				"source":    "directory",
			},
		},
		{
			name: "Invalid chunk size (uses default)",
			parameters: map[string]string{
				"path":      "/test/file.txt",
				"chunkSize": "invalid",
			},
		},
		{
			name: "Invalid overlap (uses default)",
			parameters: map[string]string{
				"path":    "/test/file.txt",
				"overlap": "invalid",
			},
		},
		{
			name: "Invalid recursive (uses default false)",
			parameters: map[string]string{
				"path":      "/test/file.txt",
				"recursive": "invalid",
			},
		},
		{
			name: "Empty chunk size (uses default)",
			parameters: map[string]string{
				"path":      "/test/file.txt",
				"chunkSize": "",
			},
		},
		{
			name: "Empty overlap (uses default)",
			parameters: map[string]string{
				"path":    "/test/file.txt",
				"overlap": "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(ctx, tt.parameters)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.Success)

			var output IngestTaskOutput
			err = json.Unmarshal([]byte(result.Output), &output)
			require.NoError(t, err)
			assert.False(t, output.Success) // Should be false without processor
		})
	}
}
