package trace

import (
	"context"
	"encoding/json"
	"time"
)

// Decision is a choice the runtime made that changes what the model sees or
// what is sent for it: a tool hidden, a server tool dropped, a request sent
// again without a parameter. Each was correct when it was made and silent,
// which is what made a run that searched with one tool on Monday and another
// on Tuesday unexplainable from its trace.
type Decision struct {
	// What names the choice, a stable snake_case word: "hide_mcp_search",
	// "drop_server_tool", "retry_without_native_search", "defer_tools",
	// "eager_tools" and so on.
	What string `json:"what"`
	// Reason is why, for a person.
	Reason string `json:"reason"`
	// Detail is what the choice was about — the tools, the counts.
	Detail map[string]any `json:"detail,omitempty"`
}

// DecisionSink is a Sink that also hears decisions. The agent runtime's sink
// forwards them to its observers as "decision" events.
type DecisionSink interface {
	Sink
	Decision(ctx context.Context, d Decision)
}

// Decide reports a decision made under ctx: to the context's sink when it
// hears decisions, and to the trace lines and file when recording is on.
func Decide(ctx context.Context, what, reason string, detail map[string]any) {
	d := Decision{What: what, Reason: reason, Detail: detail}
	if ds, ok := sinkOf(ctx).(DecisionSink); ok {
		ds.Decision(ctx, d)
	}
	if !Enabled() {
		return
	}
	r := Record{Time: time.Now(), Kind: "decision", Name: what, Detail: reason}
	if len(detail) > 0 {
		if b, err := json.Marshal(detail); err == nil {
			r.Detail += " " + string(b)
		}
	}
	emit(ctx, r)
}
