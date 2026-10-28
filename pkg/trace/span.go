package trace

// One id for one flow.
//
// A run carried task_id, run_id and session_id, and each says something true:
// which task, which call to Run, which conversation. None of them says "these
// six runs, in three processes, are one thing a person asked for". A sub-agent
// has its own session and no run id, a resumed task starts a new run, a worker
// on another machine has its own everything, and the trace files of all of
// them had to be joined by hand on timestamps.
//
// So every run is a span in a W3C Trace Context trace: a 32-hex trace id that
// the whole flow shares, a 16-hex span id of its own, and the span id of
// whatever started it. The shape is the W3C one on purpose — it is what
// OpenTelemetry speaks, so pkg/otelobserver reuses these ids instead of minting
// its own, and it is what a `traceparent` header carries, so the flow crosses
// HTTP, MCP and a host's own gRPC link without a translation table.
//
// The ids live on the context. A host starts a flow with a known id
// (WithTraceparent, from an incoming header) or lets the first run mint one;
// every run, sub-agent, tool call and model call started from that context
// joins it as a child.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// TraceparentHeader is the W3C Trace Context header (and MCP _meta key, and
// gRPC metadata key) the flow travels in. TracestateHeader is its companion,
// vendor state passed through untouched.
const (
	TraceparentHeader = "traceparent"
	TracestateHeader  = "tracestate"
)

// SpanContext is one span's place in a flow.
type SpanContext struct {
	// TraceID is 32 lowercase hex digits, shared by every span of the flow.
	TraceID string `json:"trace_id"`
	// SpanID is 16 lowercase hex digits, this span's own.
	SpanID string `json:"span_id"`
	// ParentSpanID is the span that started this one; empty for a flow's
	// root, or when the parent is remote and its own parent unknown.
	ParentSpanID string `json:"parent_span_id,omitempty"`
	// TraceState is the W3C tracestate that arrived with the flow, passed on
	// unchanged to every call made under it.
	TraceState string `json:"tracestate,omitempty"`
}

// Valid reports whether sc has a well-formed trace id and span id.
func (sc SpanContext) Valid() bool {
	return isHex(sc.TraceID, 32) && isHex(sc.SpanID, 16)
}

// Traceparent renders sc as a W3C traceparent value ("00-<trace>-<span>-01"),
// or "" when sc is not valid. The span named is sc itself: whoever receives it
// becomes sc's child.
func (sc SpanContext) Traceparent() string {
	if !sc.Valid() {
		return ""
	}
	return "00-" + sc.TraceID + "-" + sc.SpanID + "-01"
}

// ParseTraceparent reads a W3C traceparent value. The result's SpanID is the
// sender's span — the parent of whatever runs here.
func ParseTraceparent(v string) (SpanContext, error) {
	parts := strings.Split(strings.TrimSpace(strings.ToLower(v)), "-")
	if len(parts) < 4 || !isHex(parts[0], 2) || parts[0] == "ff" {
		return SpanContext{}, errors.New("trace: malformed traceparent")
	}
	sc := SpanContext{TraceID: parts[1], SpanID: parts[2]}
	if !sc.Valid() || allZero(sc.TraceID) || allZero(sc.SpanID) {
		return SpanContext{}, errors.New("trace: invalid traceparent ids")
	}
	return sc, nil
}

// NewTraceID returns a random 32-hex trace id.
func NewTraceID() string { return randomHex(16) }

// NewSpanID returns a random 16-hex span id.
func NewSpanID() string { return randomHex(8) }

// ChildSpanID derives a stable span id for a child of parent named by key —
// a tool call's id, say — so the two places that need it (the observer line
// and the context the tool runs under) agree without passing it around.
func ChildSpanID(parent SpanContext, key string) string {
	sum := sha256.Sum256([]byte(parent.TraceID + "/" + parent.SpanID + "/" + key))
	return hex.EncodeToString(sum[:8])
}

type spanKey struct{}

// ContextWithSpan returns ctx carrying sc as the current span. An invalid sc
// leaves ctx as it was.
func ContextWithSpan(ctx context.Context, sc SpanContext) context.Context {
	if ctx == nil || !sc.Valid() {
		return ctx
	}
	return context.WithValue(ctx, spanKey{}, sc)
}

// SpanFromContext is the current span of ctx.
func SpanFromContext(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	sc, ok := ctx.Value(spanKey{}).(SpanContext)
	return sc, ok && sc.Valid()
}

// TraceIDFromContext is the flow ctx belongs to, or "".
func TraceIDFromContext(ctx context.Context) string {
	sc, _ := SpanFromContext(ctx)
	return sc.TraceID
}

// StartSpan opens a child of ctx's current span — or, when ctx has none, the
// root of a new flow — and returns ctx carrying it.
func StartSpan(ctx context.Context) (context.Context, SpanContext) {
	if ctx == nil {
		ctx = context.Background()
	}
	sc := SpanContext{SpanID: NewSpanID()}
	if parent, ok := SpanFromContext(ctx); ok {
		sc.TraceID, sc.ParentSpanID, sc.TraceState = parent.TraceID, parent.SpanID, parent.TraceState
	} else {
		sc.TraceID = NewTraceID()
	}
	return context.WithValue(ctx, spanKey{}, sc), sc
}

// WithTraceparent continues a flow begun elsewhere: what runs under the
// returned context is a child of the span the header names. A missing or
// malformed header leaves ctx as it was, so the first run starts a flow of
// its own.
func WithTraceparent(ctx context.Context, traceparent string) context.Context {
	sc, err := ParseTraceparent(traceparent)
	if err != nil {
		return ctx
	}
	return ContextWithSpan(ctx, sc)
}

// TraceparentFromContext is the traceparent to send on a call made from ctx,
// or "" when ctx is in no flow.
func TraceparentFromContext(ctx context.Context) string {
	sc, ok := SpanFromContext(ctx)
	if !ok {
		return ""
	}
	return sc.Traceparent()
}

// Inject writes ctx's traceparent (and tracestate, when one came with the
// flow) through set — a gRPC metadata.MD's Set, a map, anything keyed by
// string. Nothing is written when ctx is in no flow.
func Inject(ctx context.Context, set func(key, value string)) {
	sc, ok := SpanFromContext(ctx)
	if !ok || set == nil {
		return
	}
	set(TraceparentHeader, sc.Traceparent())
	if sc.TraceState != "" {
		set(TracestateHeader, sc.TraceState)
	}
}

// Extract reads a traceparent (and tracestate) through get and returns ctx
// continuing that flow; see WithTraceparent.
func Extract(ctx context.Context, get func(key string) string) context.Context {
	if get == nil {
		return ctx
	}
	sc, err := ParseTraceparent(get(TraceparentHeader))
	if err != nil {
		return ctx
	}
	sc.TraceState = strings.TrimSpace(get(TracestateHeader))
	return ContextWithSpan(ctx, sc)
}

// InjectHTTP sets the traceparent header on h.
func InjectHTTP(ctx context.Context, h http.Header) {
	if h == nil {
		return
	}
	Inject(ctx, h.Set)
}

// ExtractHTTP continues the flow an incoming request's traceparent names.
func ExtractHTTP(ctx context.Context, h http.Header) context.Context {
	if h == nil {
		return ctx
	}
	return Extract(ctx, h.Get)
}

// Links gathers the spans other processes report back for calls made under
// one context — a server that answers with its own traceparent (a response
// header, an MCP result's _meta) is saying "the work you asked for is this
// span of mine". The tool call that made the request records them, so a flow's
// tree reaches into the other process instead of stopping at the call.
type Links struct {
	mu   sync.Mutex
	seen map[string]bool
	list []string
}

type linksKey struct{}

// WithLinks returns ctx collecting child traceparents into a new Links.
func WithLinks(ctx context.Context) (context.Context, *Links) {
	l := &Links{}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, linksKey{}, l), l
}

// NoteChild records traceparent — a remote span answering a call made with
// ctx — on the Links ctx collects into, if any. Malformed values are dropped.
func NoteChild(ctx context.Context, traceparent string) {
	if ctx == nil {
		return
	}
	l, _ := ctx.Value(linksKey{}).(*Links)
	if l == nil {
		return
	}
	sc, err := ParseTraceparent(traceparent)
	if err != nil {
		return
	}
	tp := sc.Traceparent()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	if !l.seen[tp] {
		l.seen[tp] = true
		l.list = append(l.list, tp)
	}
}

// List is the traceparents noted so far, in arrival order.
func (l *Links) List() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.list...)
}

// HTTPTransport wraps base so every request carries the traceparent of the
// context it was made with, and a traceparent the server answers with is
// noted as a child (NoteChild). base nil means http.DefaultTransport.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if _, ok := base.(traceTransport); ok {
		return base
	}
	return traceTransport{base: base}
}

type traceTransport struct{ base http.RoundTripper }

func (t traceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if _, ok := SpanFromContext(ctx); ok && req.Header.Get(TraceparentHeader) == "" {
		req = req.Clone(ctx)
		InjectHTTP(ctx, req.Header)
	}
	resp, err := t.base.RoundTrip(req)
	if resp != nil {
		if tp := resp.Header.Get(TraceparentHeader); tp != "" {
			NoteChild(ctx, tp)
		}
	}
	return resp, err
}

func randomHex(n int) string {
	b := make([]byte, n)
	for {
		if _, err := rand.Read(b); err != nil {
			panic("trace: crypto/rand failed: " + err.Error())
		}
		s := hex.EncodeToString(b)
		if !allZero(s) {
			return s
		}
	}
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func allZero(s string) bool { return strings.Trim(s, "0") == "" }
