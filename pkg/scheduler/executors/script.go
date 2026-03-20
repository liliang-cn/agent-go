package executors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/scheduler"
)

// ScriptExecutor executes script tasks
type ScriptExecutor struct {
	config *config.Config
}

// NewScriptExecutor creates a new script executor
func NewScriptExecutor(cfg *config.Config) *ScriptExecutor {
	return &ScriptExecutor{
		config: cfg,
	}
}

// Type returns the task type this executor handles
func (e *ScriptExecutor) Type() scheduler.TaskType {
	return scheduler.TaskTypeScript
}

// Validate checks if the parameters are valid for script execution
func (e *ScriptExecutor) Validate(parameters map[string]string) error {
	script, exists := parameters["script"]
	if !exists || script == "" {
		return fmt.Errorf("script parameter is required")
	}

	// Check if script file exists (only if it looks like a file path and not a command)
	if !strings.Contains(script, " ") && !strings.Contains(script, "\n") &&
		!strings.HasPrefix(script, "#!/") && strings.Contains(script, "/") {
		// Looks like a file path (contains slash but no spaces or newlines)
		if !filepath.IsAbs(script) {
			// Make relative paths relative to working directory
			cwd, _ := os.Getwd()
			script = filepath.Join(cwd, script)
		}

		if _, err := os.Stat(script); os.IsNotExist(err) {
			return fmt.Errorf("script file does not exist: %s", script)
		}
	}
	// For commands like "echo hello" or inline scripts, no file validation needed

	return nil
}

// Execute runs a script task
func (e *ScriptExecutor) Execute(ctx context.Context, parameters map[string]string) (*scheduler.TaskResult, error) {
	script := parameters["script"]
	workDir := parameters["workdir"]
	shell := parameters["shell"]

	// Default shell
	if shell == "" {
		shell = "/bin/sh"
	}

	// Set working directory
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	start := time.Now()

	var cmd *exec.Cmd
	var output []byte
	var err error

	// Check if it's a script file or inline script
	if !strings.Contains(script, " ") && !strings.Contains(script, "\n") &&
		!strings.HasPrefix(script, "#!/") && strings.Contains(script, "/") &&
		filepath.Ext(script) != "" {
		// Treat as file path (has extension and slash, no spaces)
		if !filepath.IsAbs(script) {
			script = filepath.Join(workDir, script)
		}
		cmd = exec.CommandContext(ctx, shell, script)
	} else {
		// Treat as inline script/command
		cmd = exec.CommandContext(ctx, shell, "-c", script)
	}

	// Set working directory
	cmd.Dir = workDir

	// Set environment variables
	cmd.Env = os.Environ()
	if envVars, exists := parameters["env"]; exists {
		// Parse environment variables (format: KEY1=value1,KEY2=value2)
		for _, envVar := range strings.Split(envVars, ",") {
			if strings.Contains(envVar, "=") {
				cmd.Env = append(cmd.Env, strings.TrimSpace(envVar))
			}
		}
	}

	// Execute command
	output, err = cmd.CombinedOutput()
	duration := time.Since(start)

	// Create output
	scriptOutput := ScriptTaskOutput{
		Script:   script,
		Shell:    shell,
		WorkDir:  workDir,
		Output:   string(output),
		Duration: duration.String(),
		Success:  err == nil,
	}

	if err != nil {
		scriptOutput.Error = err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			scriptOutput.ExitCode = exitErr.ExitCode()
		}
	}

	// Marshal to JSON
	outputJSON, marshalErr := json.MarshalIndent(scriptOutput, "", "  ")
	if marshalErr != nil {
		return &scheduler.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to marshal output: %v", marshalErr),
		}, nil
	}

	return &scheduler.TaskResult{
		Success: err == nil,
		Output:  string(outputJSON),
		Error: func() string {
			if err != nil {
				return err.Error()
			} else {
				return ""
			}
		}(),
	}, nil
}

// ScriptTaskOutput represents the output of a script task
type ScriptTaskOutput struct {
	Script   string `json:"script"`
	Shell    string `json:"shell"`
	WorkDir  string `json:"workdir"`
	Output   string `json:"output"`
	Duration string `json:"duration"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}
