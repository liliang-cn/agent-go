package trace

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type wireLog struct {
	mu sync.Mutex
	ev []WireEvent
}

func (l *wireLog) on(e WireEvent) { l.mu.Lock(); l.ev = append(l.ev, e); l.mu.Unlock() }

func (l *wireLog) kinds() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var k []string
	for _, e := range l.ev {
		k = append(k, e.Kind)
	}
	return strings.Join(k, " ")
}

func (l *wireLog) last() WireEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ev[len(l.ev)-1]
}

// A call's way through the wire, step by step, each step saying what it was.
func TestAWireSaysEachStepOfACall(t *testing.T) {
	var l wireLog
	w := NewWire(l.on)
	w.Queued("brain 5/5")
	w.Slot()
	w.Request("POST /chat/completions")
	w.Response(429, "Retry-After 2")
	w.Retry("HTTP 429", 2*time.Second)
	w.Request("POST /chat/completions")
	w.Response(200, "")
	w.Read(0)
	w.Read(10)
	w.Read(10)
	w.Failed(errors.New("stream reset"))
	if got := l.kinds(); got != "queued slot request response retry request response first_byte error" {
		t.Fatal(got)
	}
	if e := l.ev[3]; e.Status != 429 || e.Attempt != 1 || !strings.Contains(e.Text, "HTTP 429") || !strings.Contains(e.Text, "Retry-After 2") {
		t.Fatalf("%+v", e)
	}
	if s := w.State(); s.Phase != WireStreaming || s.Attempts != 2 || s.Bytes != 20 || s.Status != 200 {
		t.Fatalf("%+v", s)
	}
}

// A call that has stopped moving says what it waits on, once per quiet
// stretch; a call that is moving says nothing.
func TestAQuietWireSaysWhatItWaitsOn(t *testing.T) {
	var l wireLog
	w := NewWire(l.on)
	w.Request("POST /chat/completions")
	w.Waiting(time.Hour)
	if len(l.ev) != 1 {
		t.Fatalf("a moving call spoke: %s", l.kinds())
	}
	time.Sleep(20 * time.Millisecond)
	w.Waiting(10 * time.Millisecond)
	if e := l.last(); e.Kind != "waiting" || !strings.Contains(e.Text, "no response from the provider") {
		t.Fatalf("%+v", e)
	}
	w.Waiting(10 * time.Millisecond)
	if n := len(l.ev); n != 2 {
		t.Fatalf("said again at once: %s", l.kinds())
	}
	w.Response(200, "")
	time.Sleep(20 * time.Millisecond)
	w.Waiting(10 * time.Millisecond)
	if e := l.last(); !strings.Contains(e.Text, "provider answered HTTP 200") {
		t.Fatalf("%+v", e)
	}
	w.Read(100)
	time.Sleep(20 * time.Millisecond)
	w.Waiting(10 * time.Millisecond)
	if e := l.last(); !strings.Contains(e.Text, "answer stalled") || !strings.Contains(e.Text, "100 B") {
		t.Fatalf("%+v", e)
	}
}

// Every method is a no-op on a nil Wire, so a client reports without asking
// whether anyone listens.
func TestANilWireIsQuiet(t *testing.T) {
	var w *Wire
	w.Queued("x")
	w.Slot()
	w.Request("x")
	w.Response(200, "")
	w.Failed(errors.New("x"))
	w.Retry("x", time.Second)
	w.Restart("x")
	w.Read(1)
	w.Waiting(0)
	if WireOf(context.Background()) != nil || WireOf(WithWire(context.Background(), nil)) != nil {
		t.Fatal("a wire from nowhere")
	}
	if (*Call)(nil).Context(context.Background()) == nil {
		t.Fatal("nil call lost the context")
	}
}

type wireSink struct {
	wireLog
}

func (*wireSink) ModelStart(context.Context, string, string) {}
func (*wireSink) ModelEnd(context.Context, string, Record)   {}
func (*wireSink) Step(context.Context, Record)               {}
func (s *wireSink) Wire(_ context.Context, _, purpose string, e WireEvent) {
	e.Detail = purpose
	s.on(e)
}

// A model call made with a WireSink carries a Wire, its steps reach the sink,
// and its heartbeat stops when the call ends.
func TestAModelCallCarriesItsWireToItsSink(t *testing.T) {
	tick, quiet := WireTick, WireQuiet
	WireTick, WireQuiet = 5*time.Millisecond, 10*time.Millisecond
	defer func() { WireTick, WireQuiet = tick, quiet }()
	s := &wireSink{}
	ctx := WithSink(context.Background(), s)
	call := Model(ctx, "turn")
	w := WireOf(call.Context(ctx))
	if w == nil || w != call.Wire() {
		t.Fatal("no wire on the call's context")
	}
	w.Request("POST /chat/completions")
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(s.kinds(), "waiting") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(s.kinds(), "request waiting") || s.last().Detail != "turn" {
		t.Fatalf("%s %+v", s.kinds(), s.last())
	}
	call.End(nil, nil)
	n := len(s.kinds())
	time.Sleep(50 * time.Millisecond)
	if len(s.kinds()) != n {
		t.Fatal("the heartbeat went on after the call ended")
	}
}
