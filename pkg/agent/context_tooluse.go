package agent

import "context"

// toolUseSinkKey carries a per-run callback that records a tool name as "used".
// The runtime installs it at loop start pointing at its toolNamesUsed set, so
// tools that invoke OTHER tools internally (notably execute_javascript / PTC,
// which calls fs_write, web_search, ... inside the Goja sandbox) can report
// those inner tool names. Without this, goal-aware output lints only ever see
// "execute_javascript" and are blind to what PTC actually did.
const toolUseSinkKey contextKey = "tool_use_sink"

func withToolUseSink(ctx context.Context, sink func(name string)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, toolUseSinkKey, sink)
}

// recordToolUse reports an (inner) tool name to the run's tool-use sink, if one
// is installed. Safe to call with any ctx; a no-op when no sink is present.
func recordToolUse(ctx context.Context, name string) {
	if ctx == nil || name == "" {
		return
	}
	if sink, ok := ctx.Value(toolUseSinkKey).(func(string)); ok && sink != nil {
		sink(name)
	}
}
