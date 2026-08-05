package agent

import "context"

// contextKey is the private key type for values the runtime threads through
// context.Context (session, agent, tool-use state, discovery budget).
type contextKey string

const currentAgentKey contextKey = "current_agent"

func withCurrentAgent(ctx context.Context, agent *Agent) context.Context {
	if agent == nil {
		return ctx
	}
	return context.WithValue(ctx, currentAgentKey, agent)
}

func getCurrentAgent(ctx context.Context) *Agent {
	if ctx == nil {
		return nil
	}
	if agent, ok := ctx.Value(currentAgentKey).(*Agent); ok {
		return agent
	}
	return nil
}
