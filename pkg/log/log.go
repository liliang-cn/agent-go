package log

import (
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
)

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
	levelVar.Set(slog.LevelInfo)

	opts := &slog.HandlerOptions{
		Level: levelVar,
	}

	handler := slog.NewTextHandler(os.Stderr, opts)
	current.Store(slog.New(handler))
}

func defaultLogger() *slog.Logger { return current.Load() }

func SetLevel(level slog.Level) { levelVar.Set(level) }

func SetDebug(enabled bool) {
	if enabled {
		SetLevel(slog.LevelDebug)
	} else {
		SetLevel(slog.LevelInfo)
	}
}

func IsDebug() bool { return levelVar.Level() == slog.LevelDebug }

func Level() slog.Level { return levelVar.Level() }

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
// SetLevel still governs the built-in default; a logger installed here brings
// its own handler and therefore its own level.
func SetLogger(l *slog.Logger) {
	if l == nil {
		return
	}
	current.Store(l)
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
