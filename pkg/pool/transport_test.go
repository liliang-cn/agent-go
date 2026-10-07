package pool

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Two bursts of concurrent requests to one provider: the second must ride the
// connections the first opened. On http.DefaultTransport only two idle
// connections per host survive, so the second burst dialled again.
func TestClientsReuseConnections(t *testing.T) {
	var dials int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond) // overlap, so a burst needs many connections at once
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			atomic.AddInt64(&dials, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	c, err := NewClient("test", srv.URL, "k", "m")
	if err != nil {
		t.Fatal(err)
	}
	burst := func() {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.Generate(context.Background(), "hi", nil); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	burst()
	first := atomic.LoadInt64(&dials)
	burst()
	if again := atomic.LoadInt64(&dials) - first; again > 0 {
		t.Fatalf("the second burst opened %d new connections; the first burst's %d should have been reused", again, first)
	}
}

// Warming opens the connection the first request then uses.
func TestWarmOpensTheConnectionTheFirstRequestUses(t *testing.T) {
	var dials int64
	var sawModels atomic.Bool
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			sawModels.Store(true)
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			atomic.AddInt64(&dials, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	p, err := NewPool(PoolConfig{Enabled: true, Strategy: StrategyRoundRobin, Providers: []Provider{{Name: "t", BaseURL: srv.URL, Key: "k", ModelName: "m"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawModels.Load() || atomic.LoadInt64(&dials) != 1 {
		t.Fatalf("warm: /models seen=%v, connections=%d; want one connection", sawModels.Load(), atomic.LoadInt64(&dials))
	}
	if _, err := p.Generate(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt64(&dials); n != 1 {
		t.Fatalf("the first request opened its own connection (%d total); the warm one should have been reused", n)
	}
}
