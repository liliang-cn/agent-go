package agent

import "context"

type eventSinkContextKey struct{}
type runDebugContextKey struct{}
type runIDContextKey struct{}

func withEventSink(ctx context.Context, sink func(*Event)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, eventSinkContextKey{}, sink)
}

func eventSinkFromContext(ctx context.Context) func(*Event) {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(eventSinkContextKey{}).(func(*Event))
	return sink
}

// withCurrentRunID carries the run's registry id down the call tree.
//
// The run id is minted in startRun, and the places that most need it — a tool
// dispatch several frames below the loop, a log line written by service-level
// code while a run is in flight — have no other route to it. Session and task
// ids do not stand in: one session can have several runs in flight at once, so
// grouping a trace or a log by session is grouping two runs together.
func withCurrentRunID(ctx context.Context, runID string) context.Context {
	if runID == "" {
		return ctx
	}
	return context.WithValue(ctx, runIDContextKey{}, runID)
}

// currentRunID returns the run id on the context, or "" outside a run.
func currentRunID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(runIDContextKey{}).(string)
	return id
}

func withRunDebug(ctx context.Context, debug bool) context.Context {
	return context.WithValue(ctx, runDebugContextKey{}, debug)
}

func runDebugFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	debug, _ := ctx.Value(runDebugContextKey{}).(bool)
	return debug
}

// The run's tenant and session on the context.
//
// Both exist for the same reason the run id does: code several frames below
// the loop — a tool handler starting background work — has no other route to
// them, and a background task started without them loses whose work it is
// and which conversation asked for it.
type tenantContextKey struct{}
type sessionContextKeyType struct{}

func withCurrentTenant(ctx context.Context, tenant string) context.Context {
	if tenant == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantContextKey{}, tenant)
}

// currentRunTenant reads the tenant of the run that is calling, so work
// started on somebody's behalf stays theirs.
func currentRunTenant(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	tenant, _ := ctx.Value(tenantContextKey{}).(string)
	return tenant
}

func withCurrentSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionContextKeyType{}, sessionID)
}

// currentRunSessionID reads the conversation a background task was started
// from, so a host can show a person the work their own chat kicked off.
func currentRunSessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(sessionContextKeyType{}).(string)
	return id
}
