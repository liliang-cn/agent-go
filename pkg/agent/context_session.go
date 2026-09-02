package agent

import "context"

const currentSessionKey contextKey = "current_session"

func withCurrentSession(ctx context.Context, session *Session) context.Context {
	if session == nil {
		return ctx
	}
	return context.WithValue(ctx, currentSessionKey, session)
}

func getCurrentSession(ctx context.Context) *Session {
	if ctx == nil {
		return nil
	}
	if session, ok := ctx.Value(currentSessionKey).(*Session); ok {
		return session
	}
	return nil
}

const currentPlanKeyKey contextKey = "current_plan_key"

// withCurrentPlanKey records the run's plan key for the scratchpad tools.
func withCurrentPlanKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, currentPlanKeyKey, key)
}

// currentPlanKey returns the run's plan key, or "".
func currentPlanKey(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	k, _ := ctx.Value(currentPlanKeyKey).(string)
	return k
}
