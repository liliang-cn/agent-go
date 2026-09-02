package agent

import (
	"context"
	"errors"
	"time"
)

// Closing a Service, and what a borrower gets afterwards.
//
// A Service owns exactly one resource whose lifetime outlives a run: the Store,
// and through it the *sql.DB behind agentgo.db. It opened that handle
// (NewService → NewStore), so it is the only thing allowed to close it.
// Everything that holds a *Service — a PromptExecutor on a timer, a Manager's
// service cache, a desktop window — is a borrower: it never closes the Service,
// and it must not keep driving turns through one that has been closed.
//
// The second half of that rule had no enforcement, and the failure was silent.
// A host that rebuilt its service (new settings, new UI rules) closed the old
// one while a PromptScheduler was still pointed at it. The schedule kept
// firing: the model answered, the events streamed, the answer reached the UI —
// and every write of the conversation failed with "sql: database is closed",
// logged as a warning and thrown away. The user saw a working scheduled task
// whose history was never saved.
//
// So Close now says so. It marks the Service closed before it releases
// anything, cancels whatever is still in flight, and every subsequent run is
// refused with ErrServiceClosed at startRun — the single entry point into the
// loop. A borrower that outlived its owner now finds out on the first run
// instead of losing data on every one.

// ErrServiceClosed is returned by every run entry point (Run, RunStream, Ask,
// Chat, structured output, and the prompt scheduler's executor) once Close has
// been called on the Service.
//
// It means the caller is holding a Service someone else has already released —
// typically a host that rebuilt its agent and left a scheduler, a cached
// handle or a goroutine pointed at the old one. The fix is to rebuild whatever
// holds it against the current Service, never to reopen the store underneath.
var ErrServiceClosed = errors.New("agent service is closed")

// closeDrainTimeout caps how long Close waits for runs it has cancelled to
// leave the loop. They are cancelled first, so the normal case is a few
// milliseconds; the cap exists so a run whose event stream nobody is draining
// cannot hang a host's shutdown for ever.
const closeDrainTimeout = 5 * time.Second

// Close releases the service: it refuses further runs, stops the ones in
// flight, drains the work a finished run left behind, and closes the store.
//
// It is idempotent — a second call is a no-op returning the first call's error
// — because "close the old service" is exactly the kind of thing a host ends
// up doing twice (a rebuild path and a shutdown path both reaching the same
// handle), and a double close should not be an error the host has to reason
// about.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() {
		// Refuse new runs before releasing anything: a scheduled prompt that
		// fires during shutdown must not slip into the loop behind us.
		s.closed.Store(true)
		s.stopRunsForClose()
		// Wait for work a run left running (memory extraction) before closing
		// the store underneath it.
		s.waitBackground()
		if stopErr := s.stopExtensions(context.Background()); stopErr != nil {
			err = stopErr
		}
		// The memory service owns a worker goroutine and a write queue; closing
		// it drains pending writes rather than leaving them to land after the
		// caller has torn its directory down.
		if closer, ok := s.memoryService.(interface{ Close() error }); ok && closer != nil {
			_ = closer.Close()
		}
		if closeErr := s.store.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	})
	return err
}

// Closed reports whether this Service has been closed. A host holding a
// Service it did not build (or one it may have replaced) can check before
// handing work to it, rather than discovering it from a failed run.
func (s *Service) Closed() bool {
	if s == nil {
		return true
	}
	return s.closed.Load()
}

// stopRunsForClose cancels every run still in flight and waits, briefly, for
// them to leave the loop.
//
// Cancelling is unconditional here, unlike Cancel(), which defers to a
// destructive tool mid-write: the store is going away either way, and a run
// left executing against it would produce exactly the silent write failures
// this file exists to prevent.
func (s *Service) stopRunsForClose() {
	s.cancelMu.Lock()
	pending := make([]context.CancelFunc, 0, len(s.runs))
	for _, h := range s.runs {
		pending = append(pending, h.cancel)
	}
	s.cancelMu.Unlock()
	if len(pending) == 0 {
		return
	}

	s.cancelLog("closing: cancelling every run in flight")
	for _, cancel := range pending {
		cancel()
	}

	deadline := time.Now().Add(closeDrainTimeout)
	for time.Now().Before(deadline) {
		s.cancelMu.RLock()
		left := len(s.runs)
		s.cancelMu.RUnlock()
		if left == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	s.cancelMu.RLock()
	left := len(s.runs)
	s.cancelMu.RUnlock()
	if left > 0 && s.logger != nil {
		// Not fatal, but worth saying out loud: those runs are about to find
		// the store closed, and their last writes will fail.
		s.logger.Warn("closing the agent service with runs still in flight",
			"runs", left, "waited", closeDrainTimeout.String())
	}
}
