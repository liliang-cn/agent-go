package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// executeSubAgentDelegation runs one sub-agent synchronously inside the parent
// loop. It is the single implementation behind both the generic `delegate`
// tool and the named `task(agent_name, prompt)` tool registered by
// WithSubagents — a sub-agent is just a tool, not a routing decision.
func (s *Service) executeSubAgentDelegation(ctx context.Context, currentAgent *Agent, args map[string]interface{}) (interface{}, error) {
	goal, _ := args["goal"].(string)
	if strings.TrimSpace(goal) == "" {
		goal, _ = args["prompt"].(string)
	}
	if strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("delegate: 'goal' argument is required")
	}

	// Nesting is bounded here, the one place both `task` and
	// delegate_to_subagent pass through. Beyond the bound nothing runs; the
	// caller gets told to do the work itself.
	depth := subagentDepth(ctx)
	maxDepth := s.SubagentMaxDepth()
	subName, _ := args["_subagent_name"].(string)
	if depth >= maxDepth {
		return subagentDepthRefusal(depth, maxDepth, strings.TrimSpace(subName)), nil
	}
	childDepth := depth + 1
	ctx = withSubagentDepth(ctx, childDepth)

	maxTurns := 5
	if mt, ok := args["max_turns"].(float64); ok {
		maxTurns = int(mt)
	} else if mt, ok := args["max_turns"].(int); ok {
		maxTurns = mt
	}

	timeoutSeconds := 60
	if ts, ok := args["timeout_seconds"].(float64); ok {
		timeoutSeconds = int(ts)
	} else if ts, ok := args["timeout_seconds"].(int); ok {
		timeoutSeconds = ts
	}

	allowlist := stringSliceArg(args["tools_allowlist"])
	denylist := stringSliceArg(args["tools_denylist"])
	// A child at the bound is not offered a way to go deeper. The refusal
	// above still answers a call it makes anyway; withholding the tools is
	// what keeps it from spending a turn finding that out.
	if childDepth >= maxDepth {
		denylist = append(append([]string(nil), denylist...), delegationToolNames...)
	}

	var contextData map[string]interface{}
	if ctxData, ok := args["context"].(map[string]interface{}); ok {
		contextData = ctxData
	}

	s.emitProgress("tool_call", fmt.Sprintf("→ Delegating to sub-agent: %s", truncateGoal(goal, 50)), 0, "delegate")

	subAgent := s.CreateSubAgent(currentAgent, goal,
		WithSubAgentMaxTurns(maxTurns),
		WithSubAgentTimeout(time.Duration(timeoutSeconds)*time.Second),
		WithSubAgentToolAllowlist(allowlist),
		WithSubAgentToolDenylist(denylist),
		WithSubAgentContext(contextData),
	)
	subAgent.config.Debug = runDebugFromContext(ctx)
	subInstructions, _ := args["_subagent_instructions"].(string)
	if strings.TrimSpace(subName) != "" && strings.TrimSpace(subInstructions) != "" {
		subAgent.config.Agent = NewAgentWithConfig(strings.TrimSpace(subName), subInstructions, currentAgent.Tools())
	}
	// The spec's model rides the run's own model field — the same one
	// WithModel sets — so routing, precedence and status reporting are the
	// ones every run already has.
	subAgent.config.Model, _ = args["_subagent_model"].(string)
	subAgent.config.Provider, _ = args["_subagent_provider"].(string)

	sink := eventSinkFromContext(ctx)
	var (
		result interface{}
		err    error
	)
	if sink == nil {
		result, err = subAgent.Run(ctx)
	} else {
		// Stamp each event with the child that produced it. Siblings running
		// side by side share one event channel and, when they come from one
		// spec, one agent name and id; without this a host could not tell
		// whose stream a line belongs to.
		subID := subAgent.ID()
		for evt := range subAgent.RunAsync(ctx) {
			if evt != nil && evt.SubAgentID == "" {
				stamped := *evt
				stamped.SubAgentID = subID
				stamped.SubAgentDepth = childDepth
				evt = &stamped
			}
			sink(evt)
		}
		result, err = subAgent.GetResult()
	}
	if err != nil {
		return nil, fmt.Errorf("sub-agent execution failed: %w", err)
	}

	s.emitProgress("tool_result", "✓ Sub-agent completed", 0, "delegate")

	return map[string]interface{}{
		"subagent_id":   subAgent.ID(),
		"subagent_name": subAgent.Name(),
		"state":         string(subAgent.GetState()),
		"turns_used":    subAgent.GetCurrentTurn(),
		"duration_ms":   subAgent.GetDuration().Milliseconds(),
		"result":        result,
	}, nil
}

func stringSliceArg(v interface{}) []string {
	items, ok := v.([]interface{})
	if !ok {
		if strs, ok := v.([]string); ok {
			return strs
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// SubagentSpec describes one named sub-agent reachable through the `task` tool.
type SubagentSpec struct {
	Name         string
	Description  string
	Instructions string
	// Tools, when non-empty, restricts the sub-agent to these tool names.
	Tools []string
	// MaxTurns caps the sub-agent's tool-round budget (default 10).
	MaxTurns int

	// Parallel declares that several calls to this sub-agent — and calls to
	// other concurrency-safe tools — may run at the same time within one
	// turn. The default is false, and it is the right default: a child that
	// writes the workspace its siblings read or write is not safe beside
	// them. Set it for children that only read, or that each work in their
	// own worktree. At most SubagentMaxParallel of them run at once
	// (WithSubagentMaxParallel).
	Parallel bool

	// Model and Provider point this sub-agent's runs at a named model, exactly
	// as WithModel does for a top-level run: a preference the pool honours
	// when it has a matching client and falls back from when it does not.
	// Empty leaves the choice to the service's router or the pool.
	Model    string
	Provider string
}

// RegisterSubagentTool installs the `task` tool on svc, giving the model one
// deterministic way to hand work to a named sub-agent. This replaces v2's
// team routing, handoffs and built-in role delegation: composition is a tool
// call inside the same loop, so events, checkpoints and lints all still apply.
func RegisterSubagentTool(svc *Service, specs ...SubagentSpec) {
	if svc == nil || len(specs) == 0 {
		return
	}
	// Named sub-agents are the thing the built-in delegation tools delegate TO,
	// so installing them is also what earns those tools their place in the
	// schema. See Service.offersDelegationTools.
	svc.subagentsConfigured = true
	byName := make(map[string]SubagentSpec, len(specs))
	names := make([]string, 0, len(specs))
	var catalog strings.Builder
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		byName[strings.ToLower(name)] = spec
		svc.subagentLimits.setParallel(name, spec.Parallel)
		names = append(names, name)
		fmt.Fprintf(&catalog, "\n- %s: %s", name, strings.TrimSpace(spec.Description))
	}
	if len(names) == 0 {
		return
	}

	def := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name: "task",
			Description: "Hand a self-contained piece of work to a specialised sub-agent and wait for its answer. " +
				"The sub-agent runs its own tool loop and returns only its final result. Available agents:" + catalog.String(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_name": map[string]interface{}{
						"type":        "string",
						"enum":        names,
						"description": "Which sub-agent to run.",
					},
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "The complete task for the sub-agent. It cannot see your conversation, so include everything it needs.",
					},
				},
				"required": []string{"agent_name", "prompt"},
			},
		},
	}

	svc.toolRegistry.RegisterWithMetadata(def, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agentName, _ := args["agent_name"].(string)
		spec, ok := byName[strings.ToLower(strings.TrimSpace(agentName))]
		if !ok {
			return nil, fmt.Errorf("task: unknown agent %q (available: %s)", agentName, strings.Join(names, ", "))
		}
		prompt, _ := args["prompt"].(string)
		if strings.TrimSpace(prompt) == "" {
			return nil, fmt.Errorf("task: 'prompt' is required")
		}
		maxTurns := spec.MaxTurns
		if maxTurns <= 0 {
			maxTurns = 10
		}
		delegateArgs := map[string]interface{}{
			"goal":                   prompt,
			"max_turns":              maxTurns,
			"_subagent_name":         spec.Name,
			"_subagent_instructions": spec.Instructions,
			"_subagent_model":        strings.TrimSpace(spec.Model),
			"_subagent_provider":     strings.TrimSpace(spec.Provider),
		}
		if len(spec.Tools) > 0 {
			allow := make([]interface{}, 0, len(spec.Tools))
			for _, t := range spec.Tools {
				allow = append(allow, t)
			}
			delegateArgs["tools_allowlist"] = allow
		}
		out, err := svc.executeSubAgentDelegation(ctx, svc.agent, delegateArgs)
		if err != nil {
			return nil, err
		}
		if m, ok := out.(map[string]interface{}); ok {
			if refused, _ := m["refused"].(bool); refused {
				return m, nil
			}
			return map[string]interface{}{
				"ok":         true,
				"agent_name": spec.Name,
				"result":     m["result"],
				"turns_used": m["turns_used"],
			}, nil
		}
		return out, nil
	}, CategoryCustom, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock})
}

// formatResultForContent renders an arbitrary tool/step result as text.
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
		if jsonBytes, err := json.Marshal(result); err == nil {
			return string(jsonBytes)
		}
		return fmt.Sprintf("%v", result)
	}
}

// buildUserContextMetaMessage renders the ambient user context (cwd, platform,
// date …) as a <system-reminder> user message.
func buildUserContextMetaMessage(userCtx *UserContext) *domain.Message {
	if userCtx == nil {
		return nil
	}
	content := strings.TrimSpace(userCtx.FormatForMetaMessage())
	if content == "" {
		return nil
	}
	return &domain.Message{
		Role:    "user",
		Content: "<system-reminder>\n" + content + "\n</system-reminder>",
	}
}
