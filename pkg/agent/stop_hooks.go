package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// StopHookConfig configures a shell-command stop hook. Stop hooks run at the
// end of each turn (after tool execution, before the continuation decision)
// and can block the loop by setting prevent_continuation in their JSON output.
type StopHookConfig struct {
	// Command is the shell command to execute
	Command string
	// Description explains what this hook does
	Description string
	// Blocking if true, a non-zero exit blocks continuation with the stderr as
	// the BlockingError message.
	Blocking bool
	// Timeout is the max execution time (0 = no limit)
	Timeout time.Duration
	// EnvVars additional environment variables for the hook
	EnvVars map[string]string
}

// runStopShellHook executes a single shell stop-hook configuration and
// returns the resulting HookData with the stop fields populated.
//
// Shell hook contract: the command may write JSON to stdout in the form
//
//	{"prevent_continuation": true, "stop_reason": "why"}
//
// to ask the runtime to stop the loop. Non-JSON stdout is propagated as
// HookOutput for logging only.
func runStopShellHook(ctx context.Context, cfg StopHookConfig, data HookData) HookData {
	start := time.Now()

	cmdCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		cmdCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(cmdCtx, "/bin/sh", "-c", cfg.Command)
	env := []string{
		fmt.Sprintf("AGENTGO_SESSION_ID=%s", data.SessionID),
		fmt.Sprintf("AGENTGO_AGENT_ID=%s", data.AgentID),
		fmt.Sprintf("AGENTGO_GOAL=%s", data.Goal),
	}
	for k, v := range cfg.EnvVars {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	data.DurationMs = time.Since(start).Milliseconds()
	data.HookOutput = stdout.String()
	data.HookError = stderr.String()

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			data.BlockingError = fmt.Sprintf("Stop hook timed out after %v: %s", cfg.Timeout, cfg.Command)
			data.PreventContinuation = true
		} else {
			data.HookExitCode = -1
			if exitErr, ok := err.(*exec.ExitError); ok {
				data.HookExitCode = exitErr.ExitCode()
			}
			if cfg.Blocking {
				data.BlockingError = fmt.Sprintf("Stop hook failed: %s (exit %d)", stderr.String(), data.HookExitCode)
				data.PreventContinuation = true
			}
		}
	}

	applyStopHookControl(stdout.String(), &data)
	return data
}

// applyStopHookControl parses a JSON directive emitted by a shell hook on
// stdout and applies the prevent_continuation / stop_reason fields to data.
func applyStopHookControl(output string, data *HookData) {
	if data == nil {
		return
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}

	var payload struct {
		PreventContinuation bool   `json:"prevent_continuation"`
		StopReason          string `json:"stop_reason"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err == nil {
		if payload.PreventContinuation {
			data.PreventContinuation = true
		}
		if strings.TrimSpace(payload.StopReason) != "" {
			data.StopReason = strings.TrimSpace(payload.StopReason)
		}
	}
}

// FormatToolResultsForHook formats tool results for stop hook context.
func FormatToolResultsForHook(results []ToolExecutionResult) string {
	if len(results) == 0 {
		return ""
	}
	var lines []string
	for _, r := range results {
		resultStr := fmt.Sprintf("%v", r.Result)
		if r.ToolType == "error" {
			lines = append(lines, fmt.Sprintf("[%s] ERROR: %s", r.ToolName, resultStr))
		} else {
			lines = append(lines, fmt.Sprintf("[%s] OK: %s", r.ToolName, truncateForHook(resultStr)))
		}
	}
	return strings.Join(lines, "\n")
}

func truncateForHook(s string) string {
	const maxLen = 500
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}

// StopHookResult is the legacy shape returned to internal call sites that
// still want the old result type. It is now a thin view over the Stop-hook
// fields on HookData. New code should read those fields directly.
type StopHookResult struct {
	PreventContinuation bool
	StopReason          string
	BlockingError       string
	HookOutput          string
	HookError           string
	ExitCode            int
	DurationMs          int64
}

func stopHookResultFromData(data HookData) *StopHookResult {
	if !data.PreventContinuation && data.BlockingError == "" && data.HookOutput == "" {
		return nil
	}
	return &StopHookResult{
		PreventContinuation: data.PreventContinuation,
		StopReason:          data.StopReason,
		BlockingError:       data.BlockingError,
		HookOutput:          data.HookOutput,
		HookError:           data.HookError,
		ExitCode:            data.HookExitCode,
		DurationMs:          data.DurationMs,
	}
}
