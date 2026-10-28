package trace

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A Wire follows one model call on its way to the provider and back: waiting
// for a free slot in the pool, each HTTP attempt and what it answered, a
// request sent again without a parameter the upstream refused, the first byte
// of the answer and the bytes after it.
//
// A model call used to be two points, its start and its end. Between them a
// person saw "Thinking…" and a log saw nothing, whether the call was queued
// behind four others, retrying a 429, waiting on a provider that had not
// answered, or reading a long answer. The Wire says which, as it happens.
//
// trace.Model gives every call one and keeps it on the context its Context
// method returns; the HTTP clients (pkg/pool, pkg/providers) find it there
// with WireOf and report to it. Every method is safe on a nil Wire, so a
// client reports without checking whether anyone listens.
type Wire struct {
	mu       sync.Mutex
	start    time.Time
	phase    string
	phaseAt  time.Time
	attempts int
	status   int
	bytes    int64
	lastByte time.Time
	detail   string
	said     time.Time // the last "waiting" line, so one is said per quiet stretch
	on       func(WireEvent)
	// firstAt is when the first byte of an answer arrived, after start; zero
	// until one has. usage adds up what the provider said each answer cost.
	// A call that hands its record no usage or first-output time of its own
	// is given these (Call.End): the provider reported them either way.
	firstAt  time.Duration
	usage    domain.TokenUsage
	hasUsage bool
}

// WireEvent is one thing that happened to a model call on the wire.
type WireEvent struct {
	// Kind is queued, slot, request, response, error, retry, restart,
	// first_byte or waiting.
	Kind string `json:"kind"`
	// Attempt is the HTTP attempt this is about, from 1.
	Attempt int `json:"attempt,omitempty"`
	// Status is the HTTP status of a response.
	Status int `json:"status,omitempty"`
	// ElapsedMs is how long after the call started this happened.
	ElapsedMs int64 `json:"elapsed_ms"`
	// WaitMs is how long the thing named waited: a slot, a retry's delay,
	// the quiet stretch of a waiting line.
	WaitMs int64 `json:"wait_ms,omitempty"`
	// Bytes is how much of the answer had arrived.
	Bytes int64 `json:"bytes,omitempty"`
	// Detail is what the client said: the provider, an error, a reason.
	Detail string `json:"detail,omitempty"`
	// Text is the event as a person reads it.
	Text string `json:"text"`
}

// Wire phases, as State reports them.
const (
	WireQueued    = "queued"
	WireRequested = "awaiting_response"
	WireAnswered  = "awaiting_output"
	WireStreaming = "streaming"
	WireRetrying  = "retrying"
)

type wireKey struct{}

// NewWire starts following a call; on hears each event.
func NewWire(on func(WireEvent)) *Wire {
	now := time.Now()
	return &Wire{start: now, phaseAt: now, on: on}
}

// WithWire puts w on ctx for the clients the call goes through.
func WithWire(ctx context.Context, w *Wire) context.Context {
	if w == nil || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, wireKey{}, w)
}

// WireOf is the Wire on ctx, or nil.
func WireOf(ctx context.Context) *Wire {
	if ctx == nil {
		return nil
	}
	w, _ := ctx.Value(wireKey{}).(*Wire)
	return w
}

func (w *Wire) setPhase(p string, now time.Time) {
	w.phase, w.phaseAt, w.said = p, now, time.Time{}
}

// send hands e to the listener, outside the lock.
func (w *Wire) send(e WireEvent) {
	if w.on != nil {
		w.on(e)
	}
}

func (w *Wire) event(kind string, now time.Time) WireEvent {
	return WireEvent{Kind: kind, Attempt: w.attempts, ElapsedMs: now.Sub(w.start).Milliseconds(), Bytes: w.bytes}
}

// Queued says the call is waiting for a free slot; detail says whose.
func (w *Wire) Queued(detail string) {
	if w == nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	w.setPhase(WireQueued, now)
	w.detail = detail
	e := w.event("queued", now)
	w.mu.Unlock()
	e.Detail = detail
	e.Text = "queued: " + detail
	w.send(e)
}

// Slot says the call has its slot, after waiting as long as it did.
func (w *Wire) Slot() {
	if w == nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	if w.phase != WireQueued {
		w.mu.Unlock()
		return
	}
	waited := now.Sub(w.phaseAt)
	w.setPhase("", now)
	e := w.event("slot", now)
	w.mu.Unlock()
	e.WaitMs = waited.Milliseconds()
	e.Text = "got a slot after " + dur(waited)
	w.send(e)
}

// Request says an HTTP attempt went out; detail names it (model, size).
func (w *Wire) Request(detail string) {
	if w == nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	w.attempts++
	w.status, w.detail = 0, detail
	w.setPhase(WireRequested, now)
	e := w.event("request", now)
	w.mu.Unlock()
	e.Detail = detail
	e.Text = fmt.Sprintf("request sent (attempt %d)", e.Attempt)
	if detail != "" {
		e.Text += ": " + detail
	}
	w.send(e)
}

// Response says the attempt's headers came back with status.
func (w *Wire) Response(status int, detail string) {
	if w == nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	took := now.Sub(w.phaseAt)
	w.status = status
	if status < 400 {
		w.setPhase(WireAnswered, now)
	}
	e := w.event("response", now)
	w.mu.Unlock()
	e.Status, e.Detail, e.WaitMs = status, detail, took.Milliseconds()
	e.Text = fmt.Sprintf("HTTP %d after %s (attempt %d)", status, dur(took), e.Attempt)
	if detail != "" {
		e.Text += ": " + detail
	}
	w.send(e)
}

// Failed says the attempt ended in err without an answer.
func (w *Wire) Failed(err error) {
	if w == nil || err == nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	e := w.event("error", now)
	e.Status = w.status
	w.mu.Unlock()
	e.Detail = clipWire(err.Error())
	e.Text = fmt.Sprintf("attempt %d failed after %s: %s", e.Attempt, dur(now.Sub(w.start)), e.Detail)
	w.send(e)
}

// Retry says the call goes again after wait, and why.
func (w *Wire) Retry(reason string, wait time.Duration) {
	if w == nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	w.setPhase(WireRetrying, now)
	e := w.event("retry", now)
	w.mu.Unlock()
	e.Detail, e.WaitMs = clipWire(reason), wait.Milliseconds()
	e.Text = "retrying"
	if wait > 0 {
		e.Text += " in " + dur(wait)
	}
	if e.Detail != "" {
		e.Text += ": " + e.Detail
	}
	w.send(e)
}

// Restart says the answer so far was dropped and asked for again, and why.
func (w *Wire) Restart(reason string) {
	if w == nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	w.bytes = 0
	w.setPhase(WireRequested, now)
	e := w.event("restart", now)
	w.mu.Unlock()
	e.Detail = clipWire(reason)
	e.Text = "asked again: " + e.Detail
	w.send(e)
}

// Read notes n bytes of the answer arriving.
func (w *Wire) Read(n int) {
	if w == nil || n <= 0 {
		return
	}
	now := time.Now()
	w.mu.Lock()
	first := w.bytes == 0
	w.bytes += int64(n)
	w.lastByte, w.said = now, time.Time{}
	var e WireEvent
	if first {
		took := now.Sub(w.phaseAt)
		if w.firstAt == 0 {
			w.firstAt = now.Sub(w.start)
		}
		w.setPhase(WireStreaming, now)
		e = w.event("first_byte", now)
		e.WaitMs = took.Milliseconds()
		e.Text = "first bytes of the answer after " + dur(now.Sub(w.start))
	}
	w.mu.Unlock()
	if first {
		w.send(e)
	}
}

// Usage adds what one answer cost, as the provider reported it. A call can
// take more than one answer — a structured call asked again with a larger
// budget — and pays for each.
func (w *Wire) Usage(u *domain.TokenUsage) {
	if w == nil || u == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.usage.PromptTokens += u.PromptTokens
	w.usage.CompletionTokens += u.CompletionTokens
	w.usage.CachedPromptTokens += u.CachedPromptTokens
	w.usage.CacheWriteTokens += u.CacheWriteTokens
	w.usage.ReasoningTokens += u.ReasoningTokens
	w.hasUsage = true
}

// reported is what the wire saw of the call's cost and its first byte: the
// usage, nil when no answer reported any, and the time to the first byte,
// zero when none arrived.
func (w *Wire) reported() (*domain.TokenUsage, time.Duration) {
	if w == nil {
		return nil, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.hasUsage {
		return nil, w.firstAt
	}
	u := w.usage
	return &u, w.firstAt
}

// Body wraps an answer's body so each read is noted.
func (w *Wire) Body(rc io.ReadCloser) io.ReadCloser {
	if w == nil || rc == nil {
		return rc
	}
	return &wireBody{ReadCloser: rc, w: w}
}

type wireBody struct {
	io.ReadCloser
	w *Wire
}

func (b *wireBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.w.Read(n)
	return n, err
}

// WireState is where a call is now.
type WireState struct {
	Phase    string `json:"phase"`
	PhaseMs  int64  `json:"phase_ms"`
	Attempts int    `json:"attempts"`
	Status   int    `json:"status,omitempty"`
	Bytes    int64  `json:"bytes"`
	// QuietMs is how long since the last byte, once bytes have come.
	QuietMs int64  `json:"quiet_ms,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// State is where the call is now.
func (w *Wire) State() WireState {
	if w == nil {
		return WireState{}
	}
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	s := WireState{Phase: w.phase, PhaseMs: now.Sub(w.phaseAt).Milliseconds(), Attempts: w.attempts,
		Status: w.status, Bytes: w.bytes, Detail: w.detail}
	if !w.lastByte.IsZero() {
		s.QuietMs = now.Sub(w.lastByte).Milliseconds()
	}
	return s
}

// Waiting says what the call is waiting on, when it has gone quiet for at
// least quiet: once per quiet stretch, repeated each further quiet. It is
// what a heartbeat calls; a call that is moving says nothing.
func (w *Wire) Waiting(quiet time.Duration) {
	if w == nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	since := w.phaseAt
	if w.phase == WireStreaming {
		since = w.lastByte
	}
	if !w.said.IsZero() {
		since = w.said
	}
	if w.phase == "" || now.Sub(since) < quiet {
		w.mu.Unlock()
		return
	}
	w.said = now
	e := w.event("waiting", now)
	e.Status, e.Detail = w.status, w.detail
	var stuck time.Duration
	switch w.phase {
	case WireStreaming:
		stuck = now.Sub(w.lastByte)
		e.Text = fmt.Sprintf("answer stalled: no bytes for %s (%s so far)", dur(stuck), size(w.bytes))
	case WireQueued:
		stuck = now.Sub(w.phaseAt)
		e.Text = fmt.Sprintf("still queued for a slot after %s: %s", dur(stuck), w.detail)
	case WireRequested:
		stuck = now.Sub(w.phaseAt)
		e.Text = fmt.Sprintf("no response from the provider %s after the request (attempt %d)", dur(stuck), w.attempts)
	case WireAnswered:
		stuck = now.Sub(w.phaseAt)
		e.Text = fmt.Sprintf("provider answered HTTP %d %s ago and has sent nothing since", w.status, dur(stuck))
	case WireRetrying:
		stuck = now.Sub(w.phaseAt)
		e.Text = fmt.Sprintf("waiting %s to retry (attempt %d, last HTTP %d)", dur(stuck), w.attempts, w.status)
	}
	w.mu.Unlock()
	e.WaitMs = stuck.Milliseconds()
	w.send(e)
}

// Heartbeat says Waiting every tick until stop is closed or ctx is done.
func (w *Wire) Heartbeat(ctx context.Context, tick, quiet time.Duration, stop <-chan struct{}) {
	if w == nil {
		return
	}
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-done:
			return
		case <-t.C:
			w.Waiting(quiet)
		}
	}
}

func dur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

func size(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

func clipWire(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
