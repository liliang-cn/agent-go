package agent

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Surviving a provider that blinks.
//
// The loop used to treat every error from the model call the same way: emit
// one workflow_error and return. For a chat turn that is defensible — the
// user is sitting there and can press the button again. For a run that has
// been working for nineteen hours it is not: a single 502 from a gateway, or
// one connection reset, threw away everything and left no snapshot behind,
// because the bare return skipped the terminal path that writes one.
//
// Most of what goes wrong on a long run is not a failure at all, it is
// weather. Rate limits clear. Gateways restart. A stream drops mid-token.
// The fix is to tell the difference and wait it out.

const (
	// defaultLLMRetries is how many extra attempts a transient failure gets
	// before the run gives up. Four attempts spread over the backoff below
	// covers roughly a minute and a half of outage, which is longer than
	// most gateway restarts and rate-limit windows.
	defaultLLMRetries = 4

	// llmRetryBaseDelay is the first wait. It is deliberately not sub-second:
	// retrying a rate limit immediately is how a client earns a longer one.
	llmRetryBaseDelay = 2 * time.Second

	// llmRetryMaxDelay caps the exponential growth. Past a minute the wait
	// stops being a retry and starts being an outage the supervisor should
	// hear about.
	llmRetryMaxDelay = 60 * time.Second
)

// resolveLLMRetries picks how many retries this run gets: the run's own
// setting, then the framework default. Non-positive means "not set" at the
// run level, the same convention as the round budget; a caller who genuinely
// wants none says so with WithLLMRetries(-1), which resolves to zero.
func (r *Runtime) resolveLLMRetries() int {
	if r == nil || r.cfg == nil || r.cfg.MaxLLMRetries == 0 {
		return defaultLLMRetries
	}
	if r.cfg.MaxLLMRetries < 0 {
		return 0
	}
	return r.cfg.MaxLLMRetries
}

// llmRetryDelay returns the wait before attempt n (1-based), exponential and
// jittered. The jitter matters more than it looks: without it, every run that
// a shared gateway rate-limited at the same moment comes back at the same
// moment and limits itself again.
func llmRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := llmRetryBaseDelay
	for i := 1; i < attempt && delay < llmRetryMaxDelay; i++ {
		delay *= 2
	}
	if delay > llmRetryMaxDelay {
		delay = llmRetryMaxDelay
	}
	// Full jitter over the lower half: [delay/2, delay].
	half := delay / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// transientLLMError reports whether an error is weather rather than a wall —
// worth waiting out, as opposed to a request that will be rejected exactly
// the same way every time.
//
// This reads a provider's error, not a user's request. That is the side of
// the system where matching text is legitimate: the alternative is treating a
// gateway restart as a permanent failure, and no structured field exists to
// ask instead. A cancelled context is never transient — that is the caller's
// stop button, and retrying it would be ignoring them.
func transientLLMError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Typed first, wherever a type exists to check.
	if errors.Is(err, domain.ErrRateLimited) ||
		errors.Is(err, domain.ErrServiceUnavailable) ||
		errors.Is(err, domain.ErrNoHealthyProviders) {
		return true
	}
	// Our own per-turn timeout: the turn took too long, not "this request is
	// malformed". Worth one more go.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// A stream that ended early is the classic long-run failure: the request
	// was fine, the connection was not.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	msg := strings.ToLower(err.Error())

	// One 400 that is not about this request: Google refuses the *caller's
	// region*, and behind a gateway that rotates upstream accounts the caller
	// is whichever account the gateway picked this time. Measured against a
	// CLIProxyAPI pool: five identical requests, two refused, three served.
	// The request was fine; the routing was not. Asking again reaches a
	// different account. Against a single direct key this retries a hopeless
	// call a few times, which costs seconds — the trade is worth it.
	for _, routing := range []string{
		"user location is not supported",
		"failed_precondition",
	} {
		if strings.Contains(msg, routing) {
			return true
		}
	}

	// Permanent rejections win over everything below: a 400 that happens to
	// mention "timeout" in its body is still a 400, and retrying an expired
	// key four times just delays the real answer.
	for _, permanent := range []string{
		"400", "401", "403", "404", "422",
		"invalid api key", "incorrect api key", "authentication",
		"unauthorized", "forbidden", "permission denied",
		"model not found", "does not exist", "invalid request",
		"context length", "context_length_exceeded", "maximum context",
	} {
		if strings.Contains(msg, permanent) {
			return false
		}
	}

	for _, transient := range []string{
		"429", "500", "502", "503", "504",
		"rate limit", "rate_limit", "too many requests",
		"overloaded", "capacity", "server_error", "internal server error",
		"bad gateway", "service unavailable", "gateway timeout",
		"timeout", "timed out", "deadline exceeded",
		"connection reset", "connection refused", "broken pipe",
		"eof", "no such host", "temporarily", "try again",
	} {
		if strings.Contains(msg, transient) {
			return true
		}
	}
	return false
}

// waitBeforeLLMRetry sleeps for d unless the run is cancelled first, and
// reports whether the wait completed. A run stopped during a backoff must
// stop then, not sixty seconds later.
func waitBeforeLLMRetry(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// emitLLMRetry announces that a failed attempt is being waited out. It is an
// analytics event rather than an error because nothing has gone wrong yet —
// but it must be visible, or a run that pauses ninety seconds mid-task looks
// to a watching host exactly like one that hung.
func (r *Runtime) emitLLMRetry(round, attempt, maxAttempts int, delay time.Duration, err error) {
	if r == nil || r.eventChan == nil {
		return
	}
	r.eventChan <- NewAnalyticsEvent(AnalyticsLLMRetry, map[string]interface{}{
		"round":        round,
		"attempt":      attempt,
		"max_attempts": maxAttempts,
		"delay_ms":     delay.Milliseconds(),
		"error":        err.Error(),
	})
	r.emitModelRetryObserved(ModelRetryInfo{
		Round:   round,
		Kind:    "transient_error",
		Attempt: attempt,
		Reason:  err.Error(),
		Delay:   delay,
	})
}
