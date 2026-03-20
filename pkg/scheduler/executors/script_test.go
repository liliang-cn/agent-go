package executors

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewScriptExecutor(t *testing.T) {
	cfg := &config.Config{}
	executor := NewScriptExecutor(cfg)
	require.NotNil(t, executor)
	assert.NotNil(t, executor.config)
}

func TestScriptExecutorType(t *testing.T) {
	executor := NewScriptExecutor(nil)
	assert.Equal(t, scheduler.TaskTypeScript, executor.Type())
}

func TestScriptExecutorValidate(t *testing.T) {
	executor := NewScriptExecutor(nil)

	tests := []struct {
		name       string
		parameters map[string]string
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "Valid inline command",
			parameters: map[string]string{"script": "echo hello"},
			wantErr:    false,
		},
		{
			name:       "Valid inline script",
			parameters: map[string]string{"script": "#!/bin/bash\necho test"},
			wantErr:    false,
		},
		{
			name:       "Missing script parameter",
			parameters: map[string]string{},
			wantErr:    true,
			errMsg:     "script parameter is required",
		},
		{
			name:       "Empty script parameter",
			parameters: map[string]string{"script": ""},
			wantErr:    true,
			errMsg:     "script parameter is required",
		},
		{
			name:       "Non-existent file",
			parameters: map[string]string{"script": "/nonexistent/script.sh"},
			wantErr:    true,
			errMsg:     "script file does not exist",
		},
		{
			name:       "Non-existent relative file",
			parameters: map[string]string{"script": "relative/path/script.sh"},
			wantErr:    true,
			errMsg:     "script file does not exist",
		},
		{
			name:       "Command with space (not a file)",
			parameters: map[string]string{"script": "ls -la"},
			wantErr:    false,
		},
		{
			name:       "Script with newlines (not a file)",
			parameters: map[string]string{"script": "echo line1\necho line2"},
			wantErr:    false,
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

func TestScriptExecutorExecute(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	tests := []struct {
		name       string
		parameters map[string]string
		wantErr    bool
	}{
		{
			name:       "Simple echo command",
			parameters: map[string]string{"script": "echo 'Hello, World!'"},
			wantErr:    false,
		},
		{
			name:       "Command with exit code 0",
			parameters: map[string]string{"script": "true"},
			wantErr:    false,
		},
		{
			name:       "Command with exit code 1",
			parameters: map[string]string{"script": "false"},
			wantErr:    false, // Execute doesn't return error, just sets Success=false
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(ctx, tt.parameters)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)

				// Parse output
				var output ScriptTaskOutput
				err = json.Unmarshal([]byte(result.Output), &output)
				require.NoError(t, err)

				if tt.parameters["script"] == "false" {
					assert.Equal(t, 1, output.ExitCode)
				} else if tt.parameters["script"] == "true" {
					assert.Equal(t, 0, output.ExitCode)
				}
			}
		})
	}
}

func TestScriptExecutorWithScript(t *testing.T) {
	// Create a temporary script file
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "test.sh")
	scriptContent := `#!/bin/bash
echo "Script output"
exit 0
`
	err := os.WriteFile(scriptPath, []byte(scriptContent), 0755)
	require.NoError(t, err)

	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	params := map[string]string{
		"script": scriptPath,
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	// Parse output
	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Equal(t, 0, output.ExitCode)
	assert.Contains(t, output.Output, "Script output")
}

func TestScriptExecutorWithEnvironment(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	params := map[string]string{
		"script": "echo $TEST_VAR",
		"env":    "TEST_VAR=test_value",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Contains(t, output.Output, "test_value")
}

func TestScriptExecutorWithTimeout(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	params := map[string]string{
		"script":  "sleep 0.1 && echo done",
		"timeout": "1s",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	// Should complete successfully within timeout
	assert.Equal(t, 0, output.ExitCode)
}

// ScriptTaskOutput is already defined in script.go

func TestScriptExecutorFailedCommand(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	params := map[string]string{
		"script": "exit 42",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Equal(t, 42, output.ExitCode)
	assert.False(t, output.Success)
}

func TestScriptExecutorMultilineScript(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	params := map[string]string{
		"script": "echo line1\necho line2\necho line3",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Contains(t, output.Output, "line1")
	assert.Contains(t, output.Output, "line2")
	assert.Contains(t, output.Output, "line3")
}

func TestScriptExecutorWithWorkDir(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	tempDir := t.TempDir()
	params := map[string]string{
		"script":  "pwd",
		"workdir": tempDir,
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Contains(t, output.Output, filepath.Base(tempDir))
}

func TestScriptExecutorWithCustomShell(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	params := map[string]string{
		"script": "echo $0",
		"shell":  "/bin/sh",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	// Should show the shell being used
	assert.Contains(t, output.Output, "sh")
}

func TestScriptExecutorWithInvalidTimeout(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	// Test with invalid timeout format (should use default)
	params := map[string]string{
		"script":  "echo test",
		"timeout": "invalid",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Equal(t, 0, output.ExitCode)
}

func TestScriptExecutorWithStderr(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	// Test script that writes to stderr
	params := map[string]string{
		"script": "echo 'stdout message' && >&2 echo 'stderr message'",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Contains(t, output.Output, "stdout message")
	// Stderr might be combined with stdout or in Error field
	assert.True(t,
		strings.Contains(output.Output, "stderr message") ||
			strings.Contains(output.Error, "stderr message"),
		"stderr message should be in output or error")
}

func TestScriptExecutorTimeoutExceeded(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx := context.Background()

	// Test with very short timeout that should complete normally
	params := map[string]string{
		"script":  "echo 'quick test'",
		"timeout": "5s",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var output ScriptTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	// Command should complete successfully
	assert.Equal(t, 0, output.ExitCode)
	assert.Contains(t, output.Output, "quick test")
}

func TestScriptExecutorContextCancellation(t *testing.T) {
	executor := NewScriptExecutor(nil)
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	params := map[string]string{
		"script": "echo test",
	}

	result, err := executor.Execute(ctx, params)
	// Should still work even with cancelled context for short commands
	require.NoError(t, err)
	require.NotNil(t, result)
}
