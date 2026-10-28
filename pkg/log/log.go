// Package log is the framework's one logger: log/slog, leveled, structured,
// redacted, and joined to the trace.
//
// Everything agent-go logs of its own accord goes through here. A host hands
// in its own *slog.Logger (SetLogger, or agent.New(...).WithLogger) and gets
// the framework's lines in its own handler; without one, the default writes
// to stderr at warn, so an embedded library says nothing until something is
// wrong.
//
// A deployed binary is turned up without a rebuild:
//
//	AGENTGO_LOG_LEVEL   error | warn (default) | info | debug | payload
//	AGENTGO_LOG_FORMAT  text (default) | json
//	AGENTGO_LOG_FILE    append to this file instead of stderr
//
// What each level carries, for a run:
//
//	info     run start and end; one line per model call (purpose, model,
//	         duration, tokens, server tools) and per tool call (name, ok/err,
//	         duration)
//	debug    + the runtime's decisions, model retries, wire steps (HTTP
//	         status, bytes, resume, fallback), lint verdicts with reasons,
//	         tool arguments and results clipped
//	payload  + full request and response bodies, tool arguments and results
//	         whole
//
// Every line logged with a context (the *Context methods, which is how the
// runtime logs) carries the trace_id, span_id and parent_span_id of the span
// on it, so a log line and a trace line of the same run join on trace_id.
//
// Secrets are redacted at every level, payload included: Authorization and
// x-api-key values, bearer tokens, provider key shapes, and any attribute
// whose name says it is a secret. See Redact.
package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
)

// LevelPayload is below debug: whole request and response bodies.
const LevelPayload = slog.Level(-8)

var (
	// current holds the logger every helper in this package writes through.
	// It is an atomic pointer because SetLogger can be called while runs are
	// in flight — a host that installs its own handler after starting work
	// must not race the goroutines already logging.
	current  atomic.Pointer[slog.Logger]
	levelVar *slog.LevelVar
)

func init() {
	levelVar = &slog.LevelVar{}
	level, _ := ParseLevel(os.Getenv("AGENTGO_LOG_LEVEL"))
	levelVar.Set(level)

	var w io.Writer = os.Stderr
	if p := strings.TrimSpace(os.Getenv("AGENTGO_LOG_FILE")); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			w = f
		} else {
			fmt.Fprintf(os.Stderr, "agentgo-log: cannot open %s: %v\n", p, err)
		}
	}
	current.Store(slog.New(NewHandler(baseHandler(w, os.Getenv("AGENTGO_LOG_FORMAT"), levelVar))))
}

// ParseLevel reads a level name: error, warn, info, debug, payload (or
// trace). Empty or unknown is warn, the default; ok is false for unknown.
func ParseLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error":
		return slog.LevelError, true
	case "warn", "warning", "":
		return slog.LevelWarn, true
	case "info":
		return slog.LevelInfo, true
	case "debug":
		return slog.LevelDebug, true
	case "payload", "trace", "all":
		return LevelPayload, true
	}
	return slog.LevelWarn, false
}

func baseHandler(w io.Writer, format string, level slog.Leveler) slog.Handler {
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.LevelKey {
			if l, ok := a.Value.Any().(slog.Level); ok && l <= LevelPayload {
				a.Value = slog.StringValue("PAYLOAD")
			}
		}
		return a
	}}
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// New returns a logger writing to w at level in format ("text" or "json"),
// redacted and trace-joined like the default.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	return slog.New(NewHandler(baseHandler(w, format, level)))
}

func defaultLogger() *slog.Logger { return current.Load() }

// SetLevel sets the built-in default logger's level.
func SetLevel(level slog.Level) { levelVar.Set(level) }

func SetDebug(enabled bool) {
	if enabled {
		SetLevel(slog.LevelDebug)
	} else {
		SetLevel(slog.LevelWarn)
	}
}

func IsDebug() bool { return Enabled(context.Background(), slog.LevelDebug) }

func Level() slog.Level { return levelVar.Level() }

// Enabled reports whether the framework's logger writes level.
func Enabled(ctx context.Context, level slog.Level) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	return defaultLogger().Enabled(ctx, level)
}

func GetLogger() *slog.Logger { return defaultLogger() }

// SetLogger routes everything this package logs — which is everything the
// framework logs of its own accord — through l.
//
// The default writes text to stderr, which is a rude default for a library:
// a host with its own JSON handler, its own destination, or its own sampling
// had no way to say so, and no way to collect the framework's own lines
// alongside its application's. Passing nil is ignored rather than silencing
// the framework by accident.
//
// The host's handler brings its own level; the lines still pass through
// redaction and gain the trace ids on the way to it.
func SetLogger(l *slog.Logger) {
	if l == nil {
		return
	}
	current.Store(slog.New(NewHandler(l.Handler())))
}

func WithModule(module string) *slog.Logger {
	return defaultLogger().With(slog.String("module", module))
}

// Structured Logging
func Debug(msg string, args ...any) { defaultLogger().Debug(msg, args...) }
func Info(msg string, args ...any)  { defaultLogger().Info(msg, args...) }
func Warn(msg string, args ...any)  { defaultLogger().Warn(msg, args...) }
func Error(msg string, args ...any) { defaultLogger().Error(msg, args...) }

// Format-style Logging (Compatibility)
func Debugf(format string, args ...any) {
	defaultLogger().Debug(fmt.Sprintf(format, args...))
}
func Infof(format string, args ...any) {
	defaultLogger().Info(fmt.Sprintf(format, args...))
}
func Warnf(format string, args ...any) {
	defaultLogger().Warn(fmt.Sprintf(format, args...))
}
func Errf(format string, args ...any) {
	defaultLogger().Error(fmt.Sprintf(format, args...))
}
