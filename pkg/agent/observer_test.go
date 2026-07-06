package agent

import (
	"context"
	"sync"
	"testing"
)

// recordingObserver embeds BaseObserver and records which callbacks fired,
// keyed by the correlation IDs so tests can assert Start/End pairing.
type recordingObserver struct {
	BaseObserver
	mu          sync.Mutex
	modelStarts []string
	modelEnds   []string
	modelDeltas []ModelDelta
	toolStarts  []string
	toolEnds    []string
	subStarts   []string
	subEnds     []string
	checkpoints []CheckpointInfo
}

func (o *recordingObserver) OnModelStart(_ context.Context, info ModelInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.modelStarts = append(o.modelStarts, info.SpanID)
}
func (o *recordingObserver) OnModelDelta(_ context.Context, d ModelDelta) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.modelDeltas = append(o.modelDeltas, d)
}
func (o *recordingObserver) OnModelEnd(_ context.Context, info ModelInfo, _ *ModelResult, _ error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.modelEnds = append(o.modelEnds, info.SpanID)
}
func (o *recordingObserver) OnToolStart(_ context.Context, info ToolInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.toolStarts = append(o.toolStarts, info.CallID)
}
func (o *recordingObserver) OnToolEnd(_ context.Context, info ToolInfo, _ any, _ error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.toolEnds = append(o.toolEnds, info.CallID)
}
func (o *recordingObserver) OnSubAgentStart(_ context.Context, info SubAgentInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.subStarts = append(o.subStarts, info.SubAgentID)
}
func (o *recordingObserver) OnSubAgentEnd(_ context.Context, info SubAgentInfo, _ any, _ error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.subEnds = append(o.subEnds, info.SubAgentID)
}
func (o *recordingObserver) OnCheckpoint(_ context.Context, info CheckpointInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.checkpoints = append(o.checkpoints, info)
}

// panicObserver panics on its first callback to exercise recovery.
type panicObserver struct{ BaseObserver }

func (panicObserver) OnModelStart(context.Context, ModelInfo) { panic("boom") }

func TestBaseObserverIsNoOp(t *testing.T) {
	// A bare BaseObserver must satisfy the interface and do nothing harmful.
	var o Observer = BaseObserver{}
	o.OnModelStart(context.Background(), ModelInfo{})
	o.OnModelDelta(context.Background(), ModelDelta{})
	o.OnModelEnd(context.Background(), ModelInfo{}, nil, nil)
	o.OnToolStart(context.Background(), ToolInfo{})
	o.OnToolEnd(context.Background(), ToolInfo{}, nil, nil)
	o.OnSubAgentStart(context.Background(), SubAgentInfo{})
	o.OnSubAgentEnd(context.Background(), SubAgentInfo{}, nil, nil)
	o.OnCheckpoint(context.Background(), CheckpointInfo{})
}

func TestEmitObserverFanOut(t *testing.T) {
	svc := &Service{}
	a := &recordingObserver{}
	b := &recordingObserver{}
	svc.RegisterObserver(a, b)

	svc.emitObserver(func(o Observer) { o.OnModelStart(context.Background(), ModelInfo{SpanID: "s1"}) })

	if len(a.modelStarts) != 1 || len(b.modelStarts) != 1 {
		t.Fatalf("expected both observers to receive the event, got a=%d b=%d", len(a.modelStarts), len(b.modelStarts))
	}
	if a.modelStarts[0] != "s1" || b.modelStarts[0] != "s1" {
		t.Fatalf("expected SpanID s1 delivered, got a=%v b=%v", a.modelStarts, b.modelStarts)
	}
}

func TestEmitObserverRecoversPanic(t *testing.T) {
	svc := &Service{}
	rec := &recordingObserver{}
	// Register the panicking observer first so we prove the later one still runs.
	svc.RegisterObserver(panicObserver{}, rec)

	// Must not panic.
	svc.emitObserver(func(o Observer) { o.OnModelStart(context.Background(), ModelInfo{SpanID: "ok"}) })

	if len(rec.modelStarts) != 1 || rec.modelStarts[0] != "ok" {
		t.Fatalf("expected recording observer to still fire after a panicking one, got %v", rec.modelStarts)
	}
}

func TestEmitObserverNilSafe(t *testing.T) {
	var svc *Service
	// nil service, nil fn: must not panic.
	svc.emitObserver(func(Observer) {})
	svc.RegisterObserver(&recordingObserver{})

	svc2 := &Service{}
	svc2.emitObserver(nil)               // nil fn
	svc2.emitObserver(func(Observer) {}) // no observers registered — zero overhead
}
