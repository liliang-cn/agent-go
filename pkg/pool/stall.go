package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// Stall detection for model calls.
//
// A model call used to have one bound, a 600s total client timeout, so a
// stream that stopped sending bytes hung for ten minutes (a gemini behind a
// relay did), while a stream that was still progressing was cut at the same
// ten minutes. Now a streaming request is watched for bytes instead:
//
//   - FirstByteTimeout: no body byte within this long of the request going
//     out aborts the attempt. Default 180s; thinking models can be slow.
//   - StreamIdleTimeout: once bytes have come, none for this long aborts it.
//     Default 90s. SSE comment/keep-alive lines count as bytes.
//
// An aborted attempt emits a "stalled" wire event and returns an error that
// is ErrModelStalled (errors.Is). StreamWithTools retries it once, as long
// as nothing reached the callback; callers with a fallback model switch on
// the error after that. 0 disables a timeout.
//
// A stream that keeps sending is never cut: the 600s total timeout is gone
// for streams. Requests that are not streams (JSON replies, embeddings)
// keep the 600s total bound, since they have no bytes to watch before the
// whole answer is ready. If both stream timeouts are 0 a stream falls back
// to that total bound too.

// ErrModelStalled is domain.ErrModelStalled.
var ErrModelStalled = domain.ErrModelStalled

const (
	DefaultFirstByteTimeout  = 180 * time.Second
	DefaultStreamIdleTimeout = 90 * time.Second
	totalRequestTimeout      = 600 * time.Second
)

type stallConfig struct {
	mu        sync.Mutex
	set       bool
	firstByte time.Duration
	idle      time.Duration
}

var processStall = struct {
	firstByte, idle atomic.Int64
}{}

func init() {
	processStall.firstByte.Store(int64(DefaultFirstByteTimeout))
	processStall.idle.Store(int64(DefaultStreamIdleTimeout))
}

// SetStreamTimeouts sets the process-wide first-byte and stream-idle
// timeouts every client uses unless it has its own (Client.SetStreamTimeouts).
// 0 disables a timeout.
func SetStreamTimeouts(firstByte, idle time.Duration) {
	processStall.firstByte.Store(int64(firstByte))
	processStall.idle.Store(int64(idle))
}

// SetStreamTimeouts gives this client its own first-byte and stream-idle
// timeouts, overriding the process-wide ones. 0 disables a timeout.
func (c *Client) SetStreamTimeouts(firstByte, idle time.Duration) {
	if c.stall == nil {
		c.stall = &stallConfig{}
	}
	c.stall.mu.Lock()
	c.stall.set, c.stall.firstByte, c.stall.idle = true, firstByte, idle
	c.stall.mu.Unlock()
}

func (s *stallConfig) timeouts() (firstByte, idle time.Duration) {
	if s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.set {
			return s.firstByte, s.idle
		}
	}
	return time.Duration(processStall.firstByte.Load()), time.Duration(processStall.idle.Load())
}

// stallError is the error of an aborted attempt.
type stallError struct {
	phase string // "first byte" or "idle"
	after time.Duration
}

func (e *stallError) Error() string {
	if e.phase == "first byte" {
		return fmt.Sprintf("model stalled: no data from the provider within %s of the request", e.after)
	}
	return fmt.Sprintf("model stalled: stream idle for %s", e.after)
}

func (e *stallError) Is(target error) bool { return target == domain.ErrModelStalled }

// stallTransport applies the timeouts above to every request through it.
type stallTransport struct {
	base http.RoundTripper
	cfg  *stallConfig
}

func (t stallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	firstByte, idle := t.cfg.timeouts()
	stream := strings.Contains(req.Header.Get("Accept"), "text/event-stream")
	if !stream || (firstByte <= 0 && idle <= 0) {
		return t.plain(req)
	}

	ctx, cancel := context.WithCancelCause(req.Context())
	w := trace.WireOf(ctx)
	var timer *time.Timer
	var mu sync.Mutex
	stopped := false
	fire := func(phase string, d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return
		}
		err := &stallError{phase: phase, after: d}
		w.Stalled(err.Error())
		cancel(err)
	}
	arm := func(phase string, d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = nil
		if d > 0 && !stopped {
			timer = time.AfterFunc(d, func() { fire(phase, d) })
		}
	}
	stop := func() {
		mu.Lock()
		stopped = true
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
	}
	arm("first byte", firstByte)

	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		stop()
		cause := context.Cause(ctx)
		cancel(nil)
		var se *stallError
		if errors.As(cause, &se) && req.Context().Err() == nil {
			return nil, se
		}
		return nil, err
	}
	resp.Body = &stallBody{rc: resp.Body, ctx: ctx, cancel: cancel, stop: stop,
		// While a Read is blocked the idle timer runs; between Reads the
		// consumer has the bytes and the provider is not to blame.
		begin: func(seen bool) {
			if seen {
				arm("idle", idle)
			}
		},
		end: func(n int, seen bool) {
			if n > 0 || seen {
				arm("idle", 0) // stop the timer until the next Read
			}
		},
	}
	return resp, nil
}

// plain bounds a request that is not watched for bytes by the total timeout.
func (t stallTransport) plain(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), totalRequestTimeout)
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

type stallBody struct {
	rc     io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	stop   func()
	begin  func(seen bool)
	end    func(n int, seen bool)
	seen   bool
}

func (b *stallBody) Read(p []byte) (int, error) {
	b.begin(b.seen)
	n, err := b.rc.Read(p)
	if n > 0 {
		b.seen = true
	}
	b.end(n, b.seen)
	if err != nil && err != io.EOF {
		if cause := context.Cause(b.ctx); cause != nil {
			var se *stallError
			if errors.As(cause, &se) {
				return n, se
			}
		}
	}
	return n, err
}

func (b *stallBody) Close() error {
	b.stop()
	err := b.rc.Close()
	b.cancel(nil)
	return err
}
