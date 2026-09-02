package agent

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Cancelling a run.
//
// A Service can have several runs in flight at once — a chat turn, a scheduled
// prompt, a sub-agent, an async task — so "the current execution" is not a
// thing the Service can point at. What it can do is keep a registry: every run
// that enters the loop through startRun registers the CancelFunc of its own
// derived context and removes it again when its event stream closes. Cancelling
// is then a lookup, not a guess.
//
// This is what makes Cancel() real. Before, s.cancelFunc was only ever assigned
// by a test, so the production Cancel() always returned false — an API that
// looked like a stop button and was wired to nothing.
//
// Sub-agents are deliberately not registered here. A sub-agent runs under its
// parent's context, so cancelling the parent already stops it; registering it
// separately would only offer a way to kill half a run.

// ActiveRun describes one run currently executing on a Service. Returned by
// ActiveRuns so a host can show what is in flight and offer to stop it.
type ActiveRun struct {
	// RunID identifies this run for CancelRun. Set explicitly with
	// WithRunID; otherwise the runtime generates a UUID.
	RunID string `json:"run_id"`
	// SessionID is the conversation the run belongs to.
	SessionID string `json:"session_id,omitempty"`
	// TaskID is the task boundary inside that conversation.
	TaskID string `json:"task_id,omitempty"`
	// StartedAt is when the run entered the loop.
	StartedAt time.Time `json:"started_at"`
	// Tenant is the opaque owner label the caller attached with WithTenant,
	// empty when it attached none. It is what CancelTenant aims at and what
	// a per-customer limit is counted against; nothing in the loop reads it.
	Tenant string `json:"tenant,omitempty"`
}

// runHandle is an ActiveRun plus the means to stop it.
type runHandle struct {
	ActiveRun
	cancel context.CancelFunc
	seq    uint64
}

// registerRun derives a cancellable context for one run and records it so
// Cancel / CancelRun / CancelSession can reach it. The returned release func
// must be called exactly once when the run's event stream has closed: it
// cancels the derived context (releasing its resources) and forgets the run,
// so a stale ID can never answer "ok" to a stop.
//
// It also returns the id the run was actually registered under, which is not
// always the one that was asked for: a blank id is generated, and one that
// collides with a live run is made unique. Everything that names the run
// afterwards — its trace lines, its log lines — has to use the id the registry
// knows, or CancelRun cannot be reached from what the operator is reading.
func (s *Service) registerRun(ctx context.Context, runID, sessionID, taskID, tenant string) (context.Context, string, func(), error) {
	runCtx, cancel := context.WithCancel(ctx)
	if s == nil {
		return runCtx, runID, cancel, nil
	}

	runID = strings.TrimSpace(runID)
	if runID == "" {
		runID = uuid.NewString()
	}

	s.cancelMu.Lock()
	if s.runs == nil {
		s.runs = make(map[string]*runHandle)
	}
	// Admission is decided here, under the same lock that records the run:
	// a limit checked anywhere else is a limit two simultaneous callers can
	// both pass.
	if err := s.admitLocked(tenant); err != nil {
		s.cancelMu.Unlock()
		cancel()
		return runCtx, runID, func() {}, err
	}
	// A caller-supplied RunID that collides with a live run would otherwise
	// make the older run unstoppable. Give the newcomer a unique ID rather
	// than evicting the run that already owns the name.
	if _, taken := s.runs[runID]; taken {
		runID = runID + "#" + uuid.NewString()
	}
	s.runSeq++
	s.runs[runID] = &runHandle{
		ActiveRun: ActiveRun{
			RunID:     runID,
			SessionID: sessionID,
			TaskID:    taskID,
			StartedAt: time.Now(),
			Tenant:    tenant,
		},
		cancel: cancel,
		seq:    s.runSeq,
	}
	s.cancelMu.Unlock()

	var released bool
	return runCtx, runID, func() {
		s.cancelMu.Lock()
		if released {
			s.cancelMu.Unlock()
			return
		}
		released = true
		delete(s.runs, runID)
		s.cancelMu.Unlock()
		cancel()
	}, nil
}

// Cancel stops every run currently in flight on this service and reports
// whether anything was stopped.
//
// "Every run" rather than "the current one": a service can be driving a chat
// turn, a scheduled prompt and a sub-agent at the same time, so there is no
// single current run to point at, and a Cancel that silently picked one of them
// would be worse than one that says what it does. To stop exactly one run, give
// it a name with WithRunID and call CancelRun; to stop one conversation, call
// CancelSession.
//
// Cancellation is deferred — and false returned, having stopped nothing — while
// a tool whose InterruptBehavior is "block" is mid-execution. Yanking the
// context out from under a half-finished destructive write is worse than
// waiting for it; call again once it completes.
func (s *Service) Cancel() bool {
	if s == nil {
		return false
	}
	if s.hasBlockingToolInProgress() {
		s.cancelLog("cancellation deferred: a blocking tool is still in progress")
		return false
	}

	s.cancelMu.Lock()
	pending := make([]context.CancelFunc, 0, len(s.runs))
	for _, h := range s.runs {
		pending = append(pending, h.cancel)
	}
	s.cancelMu.Unlock()

	if len(pending) == 0 {
		return false
	}
	s.cancelLog("cancelling every run in flight")
	// Outside the lock: cancel runs the context's own callbacks, and nothing
	// is gained by making a second caller wait while they do.
	for _, cancel := range pending {
		cancel()
	}
	return true
}

// CancelRun stops one run by the ID it was registered under — the value passed
// to WithRunID, or the RunID reported by ActiveRuns. Returns false when no run
// with that ID is in flight (it already finished, or the ID is unknown), which
// a caller needs in order to tell "stopped it" from "there was nothing to
// stop". Subject to the same blocking-tool deferral as Cancel.
func (s *Service) CancelRun(runID string) bool {
	if s == nil {
		return false
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	if s.hasBlockingToolInProgress() {
		s.cancelLog("cancellation deferred: a blocking tool is still in progress")
		return false
	}

	s.cancelMu.Lock()
	h := s.runs[runID]
	s.cancelMu.Unlock()

	if h == nil {
		return false
	}
	s.cancelLog("cancelling run " + runID)
	h.cancel()
	return true
}

// CancelSession stops every run in flight for one session and reports whether
// anything was stopped. This is the shape a chat UI wants: it knows the
// conversation, not the run.
func (s *Service) CancelSession(sessionID string) bool {
	if s == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	if s.hasBlockingToolInProgress() {
		s.cancelLog("cancellation deferred: a blocking tool is still in progress")
		return false
	}

	s.cancelMu.Lock()
	pending := make([]context.CancelFunc, 0, 2)
	for _, h := range s.runs {
		if h.SessionID == sessionID {
			pending = append(pending, h.cancel)
		}
	}
	s.cancelMu.Unlock()

	if len(pending) == 0 {
		return false
	}
	s.cancelLog("cancelling session " + sessionID)
	for _, cancel := range pending {
		cancel()
	}
	return true
}

// ActiveRuns lists the runs currently executing on this service, oldest first.
// Hosts use it to show what is running and to hand a RunID to CancelRun.
func (s *Service) ActiveRuns() []ActiveRun {
	if s == nil {
		return nil
	}
	s.cancelMu.RLock()
	out := make([]ActiveRun, 0, len(s.runs))
	seqs := make(map[string]uint64, len(s.runs))
	for id, h := range s.runs {
		out = append(out, h.ActiveRun)
		seqs[id] = h.seq
	}
	s.cancelMu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return seqs[out[i].RunID] < seqs[out[j].RunID] })
	return out
}

func (s *Service) cancelLog(msg string) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Info("[agent] " + msg)
}
