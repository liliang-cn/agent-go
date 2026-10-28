package log

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// A key must not reach a log line at any level, payload included, however it
// arrives: a header in an error, an argument named for it, a key pasted in
// text.
func TestSecretsNeverReachALogLine(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelPayload, "json")
	l.Log(context.Background(), LevelPayload, "request Authorization: Bearer sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"api_key", "sk-live-0123456789abcdef0123",
		"x-api-key", "whatever-it-is",
		"args", map[string]any{"query": "news", "token": "tok_abcdefgh", "nested": map[string]any{"password": "hunter22"}},
		"error", errors.New(`API error (status 401): {"x-api-key":"sk-ant-api03-zzzzzzzzzzzzzzzzzzzzzz"}`),
		"body", `{"headers":{"authorization":"Bearer abcdefghijklmnop"}}`,
	)
	out := buf.String()
	for _, leak := range []string{"sk-ant-api03", "sk-live-0123", "whatever-it-is", "tok_abcdefgh", "hunter22", "abcdefghijklmnop"} {
		if strings.Contains(out, leak) {
			t.Fatalf("secret %q in log line:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "news") || !strings.Contains(out, Redacted) || !strings.Contains(out, "PAYLOAD") {
		t.Fatalf("redaction ate the line: %s", out)
	}
}

func TestALineLoggedWithASpanCarriesItsTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo, "text")
	ctx, sc := trace.StartSpan(context.Background())
	ctx, child := trace.StartSpan(ctx)
	l.InfoContext(ctx, "model call", "purpose", "turn")
	out := buf.String()
	if !strings.Contains(out, "trace_id="+sc.TraceID) || !strings.Contains(out, "span_id="+child.SpanID) || !strings.Contains(out, "parent_span_id="+sc.SpanID) {
		t.Fatalf("line lacks trace ids: %s", out)
	}
	buf.Reset()
	l.Debug("hidden")
	if buf.Len() != 0 {
		t.Fatalf("debug written at info: %s", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"": slog.LevelWarn, "error": slog.LevelError, "INFO": slog.LevelInfo, "debug": slog.LevelDebug, "payload": LevelPayload} {
		if got, _ := ParseLevel(in); got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, ok := ParseLevel("loud"); ok {
		t.Fatal("unknown level accepted")
	}
}
