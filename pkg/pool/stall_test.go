package pool

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

const sseHi = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"

// step is one thing the fake server does for a request: wait, send, or hang.
type step struct {
	wait time.Duration
	send string
	hang bool // block until the client goes away
	hdr  bool // flush headers first
}

func stallServer(t *testing.T, perAttempt ...[]step) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&calls, 1)) - 1
		if n >= len(perAttempt) {
			n = len(perAttempt) - 1
		}
		fl := w.(http.Flusher)
		for _, s := range perAttempt[n] {
			if s.hdr {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				fl.Flush()
			}
			select {
			case <-time.After(s.wait):
			case <-r.Context().Done():
				return
			}
			if s.send != "" {
				fmt.Fprint(w, s.send)
				fl.Flush()
			}
			if s.hang {
				<-r.Context().Done()
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func runStall(t *testing.T, srv *httptest.Server, fb, idle time.Duration) (error, string, []string) {
	t.Helper()
	c, err := NewClient("t", srv.URL, "", "m")
	if err != nil {
		t.Fatal(err)
	}
	c.SetStreamTimeouts(fb, idle)
	var mu sync.Mutex
	var kinds []string
	ctx := trace.WithWire(context.Background(), trace.NewWire(func(e trace.WireEvent) {
		mu.Lock()
		kinds = append(kinds, e.Kind)
		mu.Unlock()
	}))
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var got strings.Builder
	err = c.StreamWithTools(ctx, []domain.Message{{Role: "user", Content: "x"}}, nil, nil, func(r *domain.GenerationResult) error {
		got.WriteString(r.Content)
		return nil
	})
	mu.Lock()
	defer mu.Unlock()
	return err, got.String(), append([]string(nil), kinds...)
}

func count(kinds []string, k string) int {
	n := 0
	for _, x := range kinds {
		if x == k {
			n++
		}
	}
	return n
}

func TestStreamStallDetection(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		name         string
		attempts     [][]step
		fb, idle     time.Duration
		wantStalled  bool
		wantCalls    int32
		wantText     string
		wantStallEvs int
	}{
		{"headers then silence is cut by the first-byte timeout, retried once", [][]step{{{hdr: true, hang: true}}}, 150 * ms, time.Second, true, 2, "", 2},
		{"no first byte is cut by the first-byte timeout, retried once", [][]step{{{wait: 5 * time.Second}}}, 150 * ms, time.Second, true, 2, "", 2},
		{"first attempt stalls, the retry succeeds", [][]step{{{hdr: true, hang: true}}, {{hdr: true, send: sseHi + "data: [DONE]\n\n"}}}, 150 * ms, time.Second, false, 2, "hi", 1},
		{"stall after output is not retried (half an answer cannot be taken back)", [][]step{{{hdr: true, send: sseHi, hang: true}}}, time.Second, 150 * ms, true, 1, "hi", 1},
		{"slow but progressing stream is not cut", [][]step{{
			{hdr: true, wait: 80 * ms, send: sseHi}, {wait: 80 * ms, send: sseHi}, {wait: 80 * ms, send: sseHi},
			{wait: 80 * ms, send: sseHi}, {wait: 80 * ms, send: sseHi}, {wait: 80 * ms, send: "data: [DONE]\n\n"},
		}}, 200 * ms, 200 * ms, false, 1, "hihihihihi", 0},
		{"disabled timeouts never cut", [][]step{{{hdr: true, wait: 300 * ms, send: sseHi + "data: [DONE]\n\n"}}}, 0, 0, false, 1, "hi", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := stallServer(t, tc.attempts...)
			err, text, kinds := runStall(t, srv, tc.fb, tc.idle)
			if tc.wantStalled != errors.Is(err, ErrModelStalled) {
				t.Fatalf("err = %v, want stalled=%v", err, tc.wantStalled)
			}
			if !tc.wantStalled && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := atomic.LoadInt32(calls); got != tc.wantCalls {
				t.Errorf("server saw %d requests, want %d", got, tc.wantCalls)
			}
			if text != tc.wantText {
				t.Errorf("text = %q, want %q", text, tc.wantText)
			}
			if got := count(kinds, "stalled"); got != tc.wantStallEvs {
				t.Errorf("stalled wire events = %d, want %d (%v)", got, tc.wantStallEvs, kinds)
			}
		})
	}
}

func TestStallErrorIsRecognisable(t *testing.T) {
	srv, _ := stallServer(t, []step{{hdr: true, send: sseHi, hang: true}})
	err, _, _ := runStall(t, srv, time.Second, 100*time.Millisecond)
	if !errors.Is(err, domain.ErrModelStalled) || !errors.Is(err, ErrModelStalled) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "idle") {
		t.Errorf("message does not say why: %v", err)
	}
}

func TestProcessWideStreamTimeouts(t *testing.T) {
	fb, idle := time.Duration(processStall.firstByte.Load()), time.Duration(processStall.idle.Load())
	defer SetStreamTimeouts(fb, idle)
	if fb != DefaultFirstByteTimeout || idle != DefaultStreamIdleTimeout {
		t.Fatalf("defaults = %v, %v", fb, idle)
	}
	SetStreamTimeouts(100*time.Millisecond, time.Second)
	srv, calls := stallServer(t, []step{{hdr: true, hang: true}})
	c, _ := NewClient("t", srv.URL, "", "m") // no per-client override
	err := c.StreamWithTools(context.Background(), []domain.Message{{Role: "user", Content: "x"}}, nil, nil, func(*domain.GenerationResult) error { return nil })
	if !errors.Is(err, ErrModelStalled) || atomic.LoadInt32(calls) != 2 {
		t.Fatalf("err = %v, calls = %d", err, atomic.LoadInt32(calls))
	}
}
