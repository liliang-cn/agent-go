package pool

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// sharedTransport is the one connection pool every LLM client draws from.
//
// Each client used to sit on http.DefaultTransport, which keeps two idle
// connections per host. A run talks to the same provider from several
// goroutines at once — the constraint check beside memory retrieval, the
// answer, the memory extraction after it — so all but two of those opened a
// fresh TCP and TLS connection every time, and a handshake to a provider
// abroad is a few hundred milliseconds before the first byte. One tuned pool,
// shared by every client in the process, keeps them warm.
var sharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          256,
	MaxIdleConnsPerHost:   64,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: time.Second,
}

// drainAndClose finishes reading a response body before closing it: a body
// closed with bytes still unread takes its connection with it instead of
// handing it back to the pool. A stream that stops at "[DONE]" leaves the
// end of the chunked body behind; this reads it, up to a bound.
func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}

// Warm opens this client's connection to its provider ahead of the first
// request: one GET of the OpenAI-compatible /models, which costs no tokens.
// The connection then waits in the shared pool, and the first real request
// skips the TCP and TLS handshake. A provider without /models still answers
// something; any answer is enough, and an error is returned only when the
// provider could not be reached at all.
func (c *Client) Warm(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+"/models", nil)
	if err != nil {
		return err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	drainAndClose(resp.Body)
	return nil
}

// Warm opens a connection to every provider in the pool, side by side.
func (p *Pool) Warm(ctx context.Context) error {
	p.mu.RLock()
	clients := make([]*Client, 0, len(p.clients))
	for _, w := range p.clients {
		if w != nil && w.client != nil {
			clients = append(clients, w.client)
		}
	}
	p.mu.RUnlock()

	errs := make(chan error, len(clients))
	for _, c := range clients {
		go func(c *Client) { errs <- c.Warm(ctx) }(c)
	}
	var first error
	for range clients {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	return first
}
