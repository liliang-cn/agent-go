package agent

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// EventTypeModelWire is a model call's progress on the wire (trace.Wire): a
// queue for a slot, an HTTP attempt and its status, a retry and why, the
// first bytes of the answer, and, while it waits, what it waits on. Content
// is the line a person reads; StateDelta carries the fields.
const EventTypeModelWire EventType = "model_wire"

// WireObserver is an Observer that also hears each model call's progress on
// the wire. It is optional, like StepObserver: an observer without it is not
// asked.
type WireObserver interface {
	OnModelWire(ctx context.Context, info ModelWireInfo)
}

// ModelWireInfo is one step of a model call on the wire.
type ModelWireInfo struct {
	TaskID    string
	RunID     string
	SessionID string
	AgentName string
	Round     int
	// SpanID is the call's id in pkg/trace, the same for every step of it.
	SpanID string
	// Purpose is what the call was for: turn, constraints, memory-pick …
	Purpose string
	Event   trace.WireEvent
}

// Wire hands a model call's progress to the observers and to the run's
// events (trace.WireSink).
func (s runtimeSink) Wire(ctx context.Context, id, purpose string, e trace.WireEvent) {
	r := s.r
	info := ModelWireInfo{TaskID: currentTaskID(r.session), RunID: r.runID(), SessionID: r.sessionID(),
		AgentName: r.currentAgentName(), Round: int(r.currentRound.Load()), SpanID: id, Purpose: purpose, Event: e}
	r.svc.emitObserver(func(o Observer) {
		if wo, ok := o.(WireObserver); ok {
			wo.OnModelWire(ctx, info)
		}
	})
	r.emitWire(purpose, e)
}

// emitWire puts a wire step on the run's events. It may come from a call's
// heartbeat or a call made after the run, so it never sends once the run's
// channel is closing, and gives up rather than hold a caller when no one is
// reading.
func (r *Runtime) emitWire(purpose string, e trace.WireEvent) {
	if r == nil || r.eventChan == nil || r.eventsClosed.Load() {
		return
	}
	text := e.Text
	if purpose != "" && purpose != "turn" {
		text = purpose + ": " + text
	}
	ev := &Event{
		ID:        uuid.New().String(),
		Type:      EventTypeModelWire,
		AgentName: r.currentAgentName(),
		Content:   text,
		StateDelta: map[string]interface{}{
			"purpose": purpose, "kind": e.Kind, "attempt": e.Attempt, "status": e.Status,
			"elapsed_ms": e.ElapsedMs, "wait_ms": e.WaitMs, "bytes": e.Bytes, "detail": e.Detail,
		},
		Timestamp: time.Now(),
	}
	defer func() { _ = recover() }() // closed between the check and the send
	select {
	case r.eventChan <- ev:
	case <-time.After(2 * time.Second):
	}
}
