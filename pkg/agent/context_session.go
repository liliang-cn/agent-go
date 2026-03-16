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
