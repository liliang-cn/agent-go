package log

import (
	"context"
	"log/slog"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// NewHandler wraps h so every record is redacted (Redact, IsSecretKey) and,
// when logged with a context carrying a span, carries trace_id, span_id and
// parent_span_id. A logger that already added them (With) is left alone.
func NewHandler(h slog.Handler) slog.Handler {
	if rh, ok := h.(*redactHandler); ok {
		return rh
	}
	return &redactHandler{inner: h}
}

type redactHandler struct {
	inner    slog.Handler
	hasTrace bool
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	if !h.hasTrace && ctx != nil && ctx.Value(traceAddedKey{}) == nil {
		if sc, ok := trace.SpanFromContext(ctx); ok {
			out.AddAttrs(slog.String("trace_id", sc.TraceID), slog.String("span_id", sc.SpanID))
			if sc.ParentSpanID != "" {
				out.AddAttrs(slog.String("parent_span_id", sc.ParentSpanID))
			}
		}
	}
	// A redacting handler further in (a host logger wrapped twice) must not
	// add the ids a second time.
	if ctx == nil {
		ctx = context.Background()
	}
	return h.inner.Handle(context.WithValue(ctx, traceAddedKey{}, true), out)
}

type traceAddedKey struct{}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	has := h.hasTrace
	for i, a := range attrs {
		red[i] = redactAttr(a)
		if a.Key == "trace_id" {
			has = true
		}
	}
	return &redactHandler{inner: h.inner.WithAttrs(red), hasTrace: has}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name), hasTrace: h.hasTrace}
}

func redactAttr(a slog.Attr) slog.Attr {
	if IsSecretKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(v.String()))
	case slog.KindGroup:
		group := v.Group()
		red := make([]any, 0, len(group))
		for _, g := range group {
			red = append(red, redactAttr(g))
		}
		return slog.Group(a.Key, red...)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, Redact(x.Error()))
		case map[string]any, map[string]string, []any, []string:
			return slog.Any(a.Key, RedactValue(x))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
