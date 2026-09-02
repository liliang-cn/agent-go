package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func TestTransientLLMError(t *testing.T) {
	t.Parallel()
	transient := []error{
		domain.ErrRateLimited,
		domain.ErrServiceUnavailable,
		domain.ErrNoHealthyProviders,
		context.DeadlineExceeded,
		io.EOF,
		io.ErrUnexpectedEOF,
		fmt.Errorf("wrapped: %w", domain.ErrRateLimited),
		errors.New("request failed (model=x): 502 Bad Gateway"),
		errors.New("upstream returned 503 service unavailable"),
		errors.New("Error 429: Too Many Requests"),
		errors.New("the model is currently overloaded, please try again"),
		errors.New("read tcp 1.2.3.4:443: connection reset by peer"),
		errors.New("Post \"https://api/v1/chat\": EOF"),
		errors.New("dial tcp: lookup api.example.com: no such host"),
	}
	for _, err := range transient {
		if !transientLLMError(err) {
			t.Errorf("expected transient: %v", err)
		}
	}

	permanent := []error{
		nil,
		context.Canceled,
		errors.New("401 Unauthorized: invalid api key"),
		errors.New("403 Forbidden"),
		errors.New("404 model not found: gpt-9"),
		errors.New("400 Bad Request: invalid request body"),
		errors.New("This model's maximum context length is 8192 tokens"),
		errors.New("something nobody has ever seen"),
	}
	for _, err := range permanent {
		if transientLLMError(err) {
			t.Errorf("expected permanent: %v", err)
		}
	}
}

// A 400 whose body happens to say "timeout" is still a 400. Retrying it four
// times only delays the real answer.
func TestTransientLLMErrorPrefersThePermanentVerdict(t *testing.T) {
	t.Parallel()
	err := errors.New("400 Bad Request: invalid request, timeout must be a number")
	if transientLLMError(err) {
		t.Error("a permanent rejection that mentions a transient word is still permanent")
	}
}

func TestTransientLLMErrorRecognisesNetErrors(t *testing.T) {
	t.Parallel()
	_, err := net.Dial("tcp", "127.0.0.1:1")
	if err == nil {
		t.Skip("nothing refused the connection; no net.Error to test with")
	}
	if !transientLLMError(err) {
		t.Errorf("a network error should be transient: %v", err)
	}
}

func TestLLMRetryDelayGrowsAndIsCapped(t *testing.T) {
	t.Parallel()
	var prev time.Duration
	for attempt := 1; attempt <= 10; attempt++ {
		d := llmRetryDelay(attempt)
		if d <= 0 {
			t.Fatalf("attempt %d: non-positive delay %v", attempt, d)
		}
		if d > llmRetryMaxDelay {
			t.Fatalf("attempt %d: delay %v exceeds the cap %v", attempt, d, llmRetryMaxDelay)
		}
		if attempt > 1 && attempt < 6 && d < prev/4 {
			t.Errorf("attempt %d: delay %v collapsed relative to %v", attempt, d, prev)
		}
		prev = d
	}
}

// The jitter has to actually jitter, or every run a shared gateway limited at
// the same moment comes back at the same moment and limits itself again.
func TestLLMRetryDelayIsJittered(t *testing.T) {
	t.Parallel()
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[llmRetryDelay(4)] = true
	}
	if len(seen) < 2 {
		t.Error("expected jittered delays, got a constant")
	}
}

func TestWaitBeforeLLMRetryStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitBeforeLLMRetry(ctx, time.Hour) {
		t.Error("a cancelled run must not sit out the backoff")
	}
}

func TestResolveLLMRetries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  int
		want int
	}{
		{"unset falls back to the default", 0, defaultLLMRetries},
		{"a run's own budget is used", 9, 9},
		{"negative means none at all", -1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runtime{cfg: &RunConfig{MaxLLMRetries: tc.cfg}}
			if got := r.resolveLLMRetries(); got != tc.want {
				t.Errorf("resolveLLMRetries() = %d, want %d", got, tc.want)
			}
		})
	}
}

// flakyLLM fails the first failures calls with err, then answers normally.
type flakyLLM struct {
	mu       sync.Mutex
	failures int
	err      error
	calls    int32
}

func (l *flakyLLM) next() (*domain.GenerationResult, error) {
	atomic.AddInt32(&l.calls, 1)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures > 0 {
		l.failures--
		return nil, l.err
	}
	return &domain.GenerationResult{Content: "Recovered.", FinishReason: "stop"}, nil
}

func (l *flakyLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *flakyLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *flakyLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.next()
}

func (l *flakyLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	res, err := l.next()
	if err != nil {
		return err
	}
	return cb(res)
}

func (l *flakyLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *flakyLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func runFlaky(t *testing.T, name string, llm *flakyLLM, opts ...RunOption) (*ExecutionResult, error) {
	t.Helper()
	svc, err := New(name).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	return svc.Run(context.Background(), "Say something.", opts...)
}

// The whole point: one 502 used to end the run.
func TestRunSurvivesATransientProviderFailure(t *testing.T) {
	llm := &flakyLLM{failures: 1, err: errors.New("502 Bad Gateway")}
	result, err := runFlaky(t, "retry-survives", llm)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Success {
		t.Fatalf("run failed after a retryable error: %s", result.Error)
	}
	if result.Text() != "Recovered." {
		t.Errorf("final text = %q, want the answer from the successful attempt", result.Text())
	}
	if n := atomic.LoadInt32(&llm.calls); n < 2 {
		t.Errorf("the provider was called %d times; the failure was never retried", n)
	}
}

// A permanent rejection must fail immediately — retrying an expired key four
// times with backoff just makes the user wait for the same answer.
func TestRunDoesNotRetryAPermanentFailure(t *testing.T) {
	llm := &flakyLLM{failures: 99, err: errors.New("401 Unauthorized: invalid api key")}
	result, _ := runFlaky(t, "retry-permanent", llm)
	if result != nil && result.Success {
		t.Fatal("expected the run to fail")
	}
	if n := atomic.LoadInt32(&llm.calls); n != 1 {
		t.Errorf("the provider was called %d times, want exactly 1", n)
	}
}

// WithLLMRetries(-1) opts out, and the run then behaves like it used to.
func TestWithLLMRetriesCanDisableRetrying(t *testing.T) {
	llm := &flakyLLM{failures: 99, err: errors.New("502 Bad Gateway")}
	result, _ := runFlaky(t, "retry-disabled", llm, WithLLMRetries(-1))
	if result != nil && result.Success {
		t.Fatal("expected the run to fail")
	}
	if n := atomic.LoadInt32(&llm.calls); n != 1 {
		t.Errorf("the provider was called %d times, want exactly 1", n)
	}
}

// A 400 that refuses the caller's region is a routing failure, not a bad
// request: behind a gateway rotating upstream accounts, the same call
// succeeds on the next account. It must be retried, and it must not drag
// every other 400 along with it.
func TestRegionRefusalIsRetried(t *testing.T) {
	refused := errors.New(`API error (status 400): {"error":{"code":400,"message":"User location is not supported for the API use.","status":"FAILED_PRECONDITION"}}`)
	if !transientLLMError(refused) {
		t.Fatal("a region refusal from a rotating gateway must be retried")
	}
	bad := errors.New(`API error (status 400): {"error":{"message":"invalid request: messages must not be empty"}}`)
	if transientLLMError(bad) {
		t.Fatal("an ordinary 400 must still be permanent")
	}
}
