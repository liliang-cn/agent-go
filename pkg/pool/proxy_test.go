package pool

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A proxy set while the process runs carries the next request, and setting
// it back to direct (or to the environment) takes effect as quickly. The
// environment alone could not: it is read once, at the first request.
func TestAProxySetWhileRunningCarriesTheNextRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)
	}))
	t.Cleanup(upstream.Close)
	var through atomic.Int32
	// A forward proxy: it is asked for the absolute URL and fetches it.
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		through.Add(1)
		req, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(px.Close)
	pu, _ := url.Parse(px.URL)
	t.Cleanup(func() { SetProxy(nil) })

	c, err := NewClient("test", upstream.URL, "k", "m")
	if err != nil {
		t.Fatal(err)
	}
	ask := func() {
		t.Helper()
		if err := c.StreamWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, nil, nil,
			func(*domain.GenerationResult) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	SetProxy(func(*http.Request) (*url.URL, error) { return pu, nil })
	ask()
	if through.Load() != 1 {
		t.Fatalf("%d requests through the proxy", through.Load())
	}
	SetProxy(func(*http.Request) (*url.URL, error) { return nil, nil })
	ask()
	if through.Load() != 1 {
		t.Fatal("a direct setting still went through the proxy")
	}
}
