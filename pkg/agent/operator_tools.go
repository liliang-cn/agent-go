package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func registerOperatorTools(operator *Service) {
	if operator == nil {
		return
	}

	register := func(name, description string, parameters map[string]interface{}, metadata ToolMetadata, handler func(context.Context, map[string]interface{}) (interface{}, error)) {
		if operator.toolRegistry != nil && operator.toolRegistry.Has(name) {
			return
		}
		operator.AddToolWithMetadata(name, description, parameters, handler, metadata)
	}

	register("start_pty_session", "Start a generic PTY-backed command session. Use this for interactive CLI tools and command-driven agents.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{"type": "string", "description": "Executable command name or absolute path."},
			"args": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional command arguments.",
			},
			"workdir": map[string]interface{}{"type": "string", "description": "Optional working directory."},
			"env": map[string]interface{}{
				"type":        "object",
				"description": "Optional environment variables.",
			},
			"initial_input": map[string]interface{}{"type": "string", "description": "Optional initial text to send after startup."},
			"wait_ms": map[string]interface{}{
				"type":        "number",
				"description": "Optional delay before returning, default 800ms.",
			},
			"tail_chars": map[string]interface{}{
				"type":        "number",
				"description": "Optional output tail length in characters.",
			},
		},
		"required": []string{"command"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		command := getStringArg(args, "command")
		if command == "" {
			return nil, fmt.Errorf("command is required")
		}
		started, err := globalOperatorSessions.start("custom", command, getStringSliceArg(args, "args"), getStringArg(args, "workdir"), getStringMapArg(args, "env"), getStringArg(args, "initial_input"), time.Duration(getIntArg(args, "wait_ms", 800))*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(started, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("send_pty_input", "Send new input to an existing PTY session and return the latest output tail.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{"type": "string", "description": "PTY session id."},
			"input":      map[string]interface{}{"type": "string", "description": "Input text to send to the session."},
			"wait_ms":    map[string]interface{}{"type": "number", "description": "Optional delay before reading output, default 800ms."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"session_id", "input"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		sessionID := getStringArg(args, "session_id")
		input := getStringArg(args, "input")
		if sessionID == "" || input == "" {
			return nil, fmt.Errorf("session_id and input are required")
		}
		snapshot, err := globalOperatorSessions.sendPrompt(sessionID, input, time.Duration(getIntArg(args, "wait_ms", 800))*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(snapshot, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("get_pty_session", "Get the current status and output tail for one PTY session.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{"type": "string", "description": "PTY session id."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"session_id"},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		sessionID := getStringArg(args, "session_id")
		if sessionID == "" {
			return nil, fmt.Errorf("session_id is required")
		}
		snapshot, err := globalOperatorSessions.getSnapshot(sessionID, getIntArg(args, "tail_chars", operatorSessionTailDefault))
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(snapshot, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("list_pty_sessions", "List active and completed PTY sessions managed by Operator.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		tailChars := getIntArg(args, "tail_chars", operatorSessionTailDefault)
		sessions := globalOperatorSessions.list(tailChars)
		for _, session := range sessions {
			trimSessionOutput(session, tailChars)
		}
		return sessions, nil
	})

	register("interrupt_pty_session", "Send SIGINT to a PTY session, useful for interrupting Claude/Codex/Gemini/OpenCode or any interactive CLI.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{"type": "string", "description": "PTY session id."},
			"wait_ms":    map[string]interface{}{"type": "number", "description": "Optional delay before reading output, default 500ms."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"session_id"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		sessionID := getStringArg(args, "session_id")
		if sessionID == "" {
			return nil, fmt.Errorf("session_id is required")
		}
		snapshot, err := globalOperatorSessions.interrupt(sessionID, time.Duration(getIntArg(args, "wait_ms", 500))*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(snapshot, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("stop_pty_session", "Stop a PTY session with SIGTERM by default, or SIGKILL when force=true.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{"type": "string", "description": "PTY session id."},
			"force":      map[string]interface{}{"type": "boolean", "description": "Use SIGKILL instead of SIGTERM."},
			"wait_ms":    map[string]interface{}{"type": "number", "description": "Optional delay before reading output, default 500ms."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"session_id"},
	}, ToolMetadata{Destructive: true, InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		sessionID := getStringArg(args, "session_id")
		if sessionID == "" {
			return nil, fmt.Errorf("session_id is required")
		}
		force := false
		if raw, ok := args["force"].(bool); ok {
			force = raw
		}
		snapshot, err := globalOperatorSessions.stop(sessionID, force, time.Duration(getIntArg(args, "wait_ms", 500))*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(snapshot, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("start_coding_agent_session", "Start a provider-aware coding agent session for claude, gemini, codex, or opencode without making the model guess command names.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"provider": map[string]interface{}{
				"type":        "string",
				"description": "Coding agent provider: claude, gemini, codex, or opencode.",
			},
			"args": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional extra CLI arguments passed to the provider command.",
			},
			"workdir":        map[string]interface{}{"type": "string", "description": "Optional working directory."},
			"env":            map[string]interface{}{"type": "object", "description": "Optional environment variables."},
			"initial_prompt": map[string]interface{}{"type": "string", "description": "Optional prompt sent immediately after the session starts."},
			"wait_ms":        map[string]interface{}{"type": "number", "description": "Optional delay before returning, default 1200ms."},
			"tail_chars":     map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"provider"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		provider := getStringArg(args, "provider")
		command, resolvedArgs, err := resolveCodingAgentCommand(provider, "", getStringSliceArg(args, "args"))
		if err != nil {
			return nil, err
		}
		started, err := globalOperatorSessions.start(provider, command, resolvedArgs, getStringArg(args, "workdir"), getStringMapArg(args, "env"), getStringArg(args, "initial_prompt"), time.Duration(getIntArg(args, "wait_ms", 1200))*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(started, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("send_coding_agent_prompt", "Send a new prompt to an existing coding agent session and return the latest output tail.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{"type": "string", "description": "Coding agent session id."},
			"prompt":     map[string]interface{}{"type": "string", "description": "Prompt text to send."},
			"wait_ms":    map[string]interface{}{"type": "number", "description": "Optional delay before reading output, default 1200ms."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"session_id", "prompt"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		sessionID := getStringArg(args, "session_id")
		prompt := getStringArg(args, "prompt")
		if sessionID == "" || prompt == "" {
			return nil, fmt.Errorf("session_id and prompt are required")
		}
		snapshot, err := globalOperatorSessions.sendPrompt(sessionID, prompt, time.Duration(getIntArg(args, "wait_ms", 1200))*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(snapshot, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("get_coding_agent_session", "Get the current status and output tail for a coding agent session.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{"type": "string", "description": "Coding agent session id."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"session_id"},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		sessionID := getStringArg(args, "session_id")
		if sessionID == "" {
			return nil, fmt.Errorf("session_id is required")
		}
		snapshot, err := globalOperatorSessions.getSnapshot(sessionID, getIntArg(args, "tail_chars", operatorSessionTailDefault))
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(snapshot, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("list_coding_agent_sessions", "List coding agent sessions managed by Operator.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"provider":   map[string]interface{}{"type": "string", "description": "Optional provider filter."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		provider := strings.ToLower(strings.TrimSpace(getStringArg(args, "provider")))
		tailChars := getIntArg(args, "tail_chars", operatorSessionTailDefault)
		sessions := globalOperatorSessions.list(tailChars)
		if provider == "" {
			return sessions, nil
		}
		filtered := make([]map[string]interface{}, 0, len(sessions))
		for _, session := range sessions {
			if sessionProvider, ok := session["provider"].(string); ok && strings.EqualFold(sessionProvider, provider) {
				filtered = append(filtered, session)
			}
		}
		return filtered, nil
	})

	register("interrupt_coding_agent_session", "Interrupt a coding agent session with SIGINT.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{"type": "string", "description": "Coding agent session id."},
			"wait_ms":    map[string]interface{}{"type": "number", "description": "Optional delay before reading output, default 500ms."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"session_id"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		sessionID := getStringArg(args, "session_id")
		if sessionID == "" {
			return nil, fmt.Errorf("session_id is required")
		}
		snapshot, err := globalOperatorSessions.interrupt(sessionID, time.Duration(getIntArg(args, "wait_ms", 500))*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(snapshot, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("stop_coding_agent_session", "Stop a coding agent session with SIGTERM by default, or SIGKILL when force=true.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{"type": "string", "description": "Coding agent session id."},
			"force":      map[string]interface{}{"type": "boolean", "description": "Use SIGKILL instead of SIGTERM."},
			"wait_ms":    map[string]interface{}{"type": "number", "description": "Optional delay before reading output, default 500ms."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"session_id"},
	}, ToolMetadata{Destructive: true, InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		sessionID := getStringArg(args, "session_id")
		if sessionID == "" {
			return nil, fmt.Errorf("session_id is required")
		}
		force := false
		if raw, ok := args["force"].(bool); ok {
			force = raw
		}
		snapshot, err := globalOperatorSessions.stop(sessionID, force, time.Duration(getIntArg(args, "wait_ms", 500))*time.Millisecond)
		if err != nil {
			return nil, err
		}
		return trimSessionOutput(snapshot, getIntArg(args, "tail_chars", operatorSessionTailDefault)), nil
	})

	register("run_coding_agent_once", "Run a provider-aware coding agent command once and return its captured output. This avoids making the model guess command names. For codex, it uses non-interactive `codex exec -`.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"provider": map[string]interface{}{
				"type":        "string",
				"description": "Coding agent provider: claude, gemini, codex, or opencode.",
			},
			"prompt":     map[string]interface{}{"type": "string", "description": "Prompt text to execute once."},
			"workdir":    map[string]interface{}{"type": "string", "description": "Optional working directory."},
			"env":        map[string]interface{}{"type": "object", "description": "Optional environment variables."},
			"tail_chars": map[string]interface{}{"type": "number", "description": "Optional output tail length in characters."},
		},
		"required": []string{"provider", "prompt"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		provider := strings.ToLower(strings.TrimSpace(getStringArg(args, "provider")))
		prompt := getStringArg(args, "prompt")
		if provider == "" || prompt == "" {
			return nil, fmt.Errorf("provider and prompt are required")
		}
		workdir := getStringArg(args, "workdir")
		env := getStringMapArg(args, "env")

		var (
			result map[string]interface{}
			err    error
		)
		switch provider {
		case "codex":
			result, err = runCommandOnce("codex", []string{"exec", "-"}, workdir, env, prompt+"\n")
		default:
			command, resolvedArgs, resolveErr := resolveCodingAgentCommand(provider, "", nil)
			if resolveErr != nil {
				return nil, resolveErr
			}
			started, startErr := globalOperatorSessions.start(provider, command, resolvedArgs, workdir, env, prompt, 1200*time.Millisecond)
			if startErr != nil {
				return nil, startErr
			}
			result = trimSessionOutput(started, getIntArg(args, "tail_chars", operatorSessionTailDefault))
			return result, nil
		}
		if result != nil {
			if stdout, ok := result["stdout"].(string); ok {
				stderr, _ := result["stderr"].(string)
				result["output"] = stdout + stderr
				delete(result, "stdout")
				delete(result, "stderr")
			}
			trimSessionOutput(result, getIntArg(args, "tail_chars", operatorSessionTailDefault))
		}
		return result, err
	})
}

func trimSessionOutput(snapshot map[string]interface{}, tailChars int) map[string]interface{} {
	if snapshot == nil {
		return nil
	}
	if tailChars <= 0 {
		return snapshot
	}
	output, ok := snapshot["output"].(string)
	if !ok {
		return snapshot
	}
	runes := []rune(output)
	if len(runes) <= tailChars {
		return snapshot
	}
	snapshot["output"] = string(runes[len(runes)-tailChars:])
	return snapshot
}

func getStringMapArg(args map[string]interface{}, key string) map[string]string {
	if args == nil {
		return nil
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	out := map[string]string{}
	switch values := raw.(type) {
	case map[string]string:
		for key, value := range values {
			key = strings.TrimSpace(key)
			if key != "" {
				out[key] = value
			}
		}
	case map[string]interface{}:
		for key, value := range values {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if strValue, ok := value.(string); ok {
				out[key] = strValue
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
