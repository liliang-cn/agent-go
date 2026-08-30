package agent

// Options collects the low-frequency knobs that used to have a builder method
// each. v3 keeps roughly twenty `WithX` methods for the things people actually
// reach for (model, prompt, memory, MCP, skills, PTC, sandbox, tools,
// sub-agents) and routes everything else through one WithOptions call:
//
//	svc, _ := agent.New("assistant").
//		WithMemory().
//		WithOptions(agent.Options{
//			RequiredSkills:      []string{"pdf"},
//			PermissionPolicy:    myPolicy,
//			ToolExecutionPolicy: myToolPolicy,
//		}).
//		Build()
//
// The zero value changes nothing, so partial structs are fine and fields can
// be added without breaking callers.
type Options struct {
	// RequiredSkills makes Build() fail unless every named skill is installed.
	// Implies skills support.
	RequiredSkills []string

	// Modules are extra capability modules whose tools self-register on the
	// service (see module.go).
	Modules []Module

	// PermissionHandler is consulted before each tool call.
	PermissionHandler PermissionHandler

	// PermissionPolicy is the declarative allow/deny/ask policy applied when
	// no handler answers.
	PermissionPolicy PermissionPolicy

	// ToolExecutionPolicy controls which tools are exposed to the model
	// versus deferred behind tool search.
	ToolExecutionPolicy ToolExecutionPolicy

	// Observers are passive observability aspects fanned out at the model /
	// tool / sub-agent seams.
	Observers []Observer

	// Deliverables registers the deliverable-scanning tools. Requires a
	// sandbox (WithSandbox); ignored otherwise.
	Deliverables bool
}

// WithOptions applies a bundle of low-frequency settings. Calling it more than
// once merges: later non-zero fields win, and slices append.
func (b *Builder) WithOptions(o Options) *Builder {
	if len(o.RequiredSkills) > 0 {
		b.enableSkills = true
		b.requiredSkills = append(b.requiredSkills, o.RequiredSkills...)
	}
	if len(o.Modules) > 0 {
		b.extraModules = append(b.extraModules, o.Modules...)
	}
	if o.PermissionHandler != nil {
		b.permissionHandler = o.PermissionHandler
	}
	if o.PermissionPolicy != nil {
		b.permissionPolicy = o.PermissionPolicy
	}
	if o.ToolExecutionPolicy.Default != "" || len(o.ToolExecutionPolicy.Rules) > 0 {
		b.toolPolicy = o.ToolExecutionPolicy
	}
	if len(o.Observers) > 0 {
		b.observers = append(b.observers, o.Observers...)
	}
	if o.Deliverables {
		b.enableDeliver = true
	}
	return b
}

// WithSubagents registers named sub-agents and exposes them through a single
// `task(agent_name, prompt)` tool. This is v3's only composition primitive:
// there is no team, no router and no handoff — a sub-agent is a tool call
// inside the same loop, so events, checkpoints and lints all still apply.
//
//	svc, _ := agent.New("lead").
//		WithSubagents(
//			agent.SubagentSpec{Name: "researcher", Description: "Searches and summarises.",
//				Instructions: "You research topics and return a concise brief."},
//		).
//		Build()
func (b *Builder) WithSubagents(specs ...SubagentSpec) *Builder {
	b.subagents = append(b.subagents, specs...)
	return b
}

// WithDelegation decides whether the three built-in delegation tools —
// delegate_to_subagent, delegate_async and subagent_send_message — go into the
// schema the model sees.
//
// The default is derived, not fixed: they are offered when named sub-agents
// exist (WithSubagents) and withheld when they do not. That is a change from
// the old behaviour, where all three were registered at construction and
// offered on every request whatever the caller had configured — a four-tool
// agent with no sub-agents was billed for nine tool schemas per turn, and the
// model could call tools that only re-run a clone of itself.
//
// Pass true to get them without configuring sub-agents: running a sub-goal in
// an isolated context and getting only its result back is a real capability,
// and the generic tool is the only way to reach it. Pass false to withhold them
// even from an agent that has sub-agents, when the named `task` tool is the
// only route you want the model to take.
//
// Either way the handlers stay in the registry and remain callable by name;
// this governs exposure to the model, not registration.
func (b *Builder) WithDelegation(enabled bool) *Builder {
	b.delegation = &enabled
	return b
}

// WithLengthLimits decides whether the system prompt carries the numeric length
// anchors: "keep text between tool calls to ≤25 words. Keep final responses to
// ≤100 words unless the task requires more detail."
//
// The default is on, and stays on — agents in production are tuned against
// those numbers and silently lifting the cap would change everyone's output
// length. But it is an opinion about the answer, not a rule about the runtime,
// and it can contradict the caller's own instructions: an agent told to cite a
// source for every statement cannot also stay under 100 words, and the caller
// never opted into the cap. Pass false when your own prompt owns the length.
func (b *Builder) WithLengthLimits(enabled bool) *Builder {
	b.omitLengthLimits = !enabled
	return b
}
