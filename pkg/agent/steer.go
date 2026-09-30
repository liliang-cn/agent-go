package agent

import (
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Steering: a message into a run that is already going.
//
// A run reads its conversation once, at the start, and from then on the only
// things that reach the model are what its tools return. A host that learns
// something mid-run — another agent in a hive has a message for this one, the
// person typed a correction — could put it in an inbox for the model to read
// if it ever thought to, or wait for the run to end. Neither is "the message
// arrived". This is the third way: the host queues it, and the loop appends
// it to the conversation at the next round boundary, exactly as if it had been
// there when the round began.
//
// Two rules that keep it honest:
//
//   - Never mid-tool. A steer lands after the round's tool results are in the
//     conversation and before the next model call, so the model sees a
//     complete round and then the message.
//   - A message that lands during the final model call is not lost. When the
//     model has just answered without calling a tool and a steer arrived
//     while it was answering, the run does not end: the answer goes into the
//     conversation as the assistant's turn, the steer after it, and the model
//     gets one more round. Only a run that ends some other way — cancelled,
//     blocked, failed — drops what it could not apply, and it says so with
//     EventTypeSteerDropped so the host can start a turn instead.

const (
	// EventTypeSteer carries a steered message the moment the loop puts it
	// into the conversation, so a transcript can show it where it landed.
	EventTypeSteer EventType = "steer"
	// EventTypeSteerDropped carries a steered message the run ended without
	// applying. The host starts a new turn with it.
	EventTypeSteerDropped EventType = "steer_dropped"
)

// Steer queues msg for every run in flight on the session, to be applied at
// each run's next round boundary. It returns false when no run on that
// session is active, in which case the host starts a turn itself.
//
// An empty Role means "user". The content is the host's to shape — a hive
// host writes "[message from worker-1] …" — and it is stored in the session
// history like any user message, so later turns see it too.
func (s *Service) Steer(sessionID string, msg domain.Message) bool {
	if s == nil || strings.TrimSpace(msg.Content) == "" {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	queued := false
	for _, h := range s.runs {
		if h.SessionID == sessionID {
			h.queueSteer(msg)
			queued = true
		}
	}
	return queued
}

// SteerRun is Steer for one run, named by its id (WithRunID, or
// ActiveRuns()).
func (s *Service) SteerRun(runID string, msg domain.Message) bool {
	if s == nil || strings.TrimSpace(msg.Content) == "" {
		return false
	}
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	h, ok := s.runs[strings.TrimSpace(runID)]
	if !ok {
		return false
	}
	h.queueSteer(msg)
	return true
}

func (h *runHandle) queueSteer(msg domain.Message) {
	if strings.TrimSpace(msg.Role) == "" {
		msg.Role = "user"
	}
	h.steerMu.Lock()
	h.steers = append(h.steers, msg)
	h.steerMu.Unlock()
}

// takeSteers empties a run's queue, in arrival order.
func (s *Service) takeSteers(runID string) []domain.Message {
	if s == nil || runID == "" {
		return nil
	}
	s.cancelMu.Lock()
	h, ok := s.runs[runID]
	s.cancelMu.Unlock()
	if !ok {
		return nil
	}
	h.steerMu.Lock()
	out := h.steers
	h.steers = nil
	h.steerMu.Unlock()
	return out
}

// applySteers puts every queued message into the conversation, in order, and
// reports whether there was any. It runs at the round boundary — after the
// previous round's tool results, before the next model call — and is the one
// place a steer becomes part of the run.
func (r *Runtime) applySteers(messages *[]domain.Message, state *queryLoopState) bool {
	pending := r.svc.takeSteers(r.runID())
	if len(pending) == 0 {
		return false
	}
	taskID := currentTaskID(r.session)
	for _, msg := range pending {
		msg = withTaskID(msg, taskID)
		*messages = append(*messages, msg)
		// Into the history now, not at the end of the run: most completion
		// paths persist only the answer, and a message a later turn cannot
		// see was never really in the conversation. persistMessages dedupes
		// by content, so this cannot double-write.
		r.session.AddMessage(msg)
		r.emit(EventTypeSteer, msg.Content)
	}
	if err := r.svc.store.SaveSession(r.session); err != nil {
		r.reportHistoryPersistFailure("steered messages", err)
	}
	if state != nil {
		state.Messages = *messages
	}
	return true
}

// steerBeforeFinal is the check made when the model has just produced a
// final answer: if a steer landed while it was being produced, the answer
// becomes the assistant's turn, the steer follows it, and the caller
// continues into one more round instead of completing. It returns false, and
// changes nothing, when nothing is queued.
func (r *Runtime) steerBeforeFinal(answer string, messages *[]domain.Message, state *queryLoopState) bool {
	if r.svc == nil || !r.svc.hasSteers(r.runID()) {
		return false
	}
	if strings.TrimSpace(answer) != "" {
		msg := withTaskID(domain.Message{Role: "assistant", Content: answer}, currentTaskID(r.session))
		*messages = append(*messages, msg)
		r.session.AddMessage(msg)
	}
	r.applySteers(messages, state)
	if state != nil {
		state.setLoopTransition(queryLoopTransitionSteer, "a message arrived during the final answer")
		state.noteRoundCompleted()
	}
	return true
}

func (s *Service) hasSteers(runID string) bool {
	if s == nil || runID == "" {
		return false
	}
	s.cancelMu.Lock()
	h, ok := s.runs[runID]
	s.cancelMu.Unlock()
	if !ok {
		return false
	}
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	return len(h.steers) > 0
}

// dropSteers reports every message the run ended without applying. Called
// on the way out of the loop, before the event channel closes, so a host
// reading the stream sees the drop before it sees the end.
func (r *Runtime) dropSteers() {
	for _, msg := range r.svc.takeSteers(r.runID()) {
		r.emit(EventTypeSteerDropped, msg.Content)
	}
}
