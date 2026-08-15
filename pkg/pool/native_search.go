package pool

import (
	"bytes"
	"encoding/json"
	"sync/atomic"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// nativeSearchState is what a client has learned about its upstream's native
// web-search support. It implements the evidence rules documented on
// domain.NativeWebSearchReporter:
//
//   - a rejection of the web-search parameters proves unsupported, and always
//     wins — a pool of heterogeneous upstreams behind one URL that sometimes
//     rejects is not an upstream auto mode can rely on;
//   - grounding evidence in a response proves supported, but only upgrades
//     from unknown — it never overrides a recorded rejection;
//   - a provider that merely accepts the parameters proves nothing: most
//     OpenAI-compatible servers silently ignore fields they do not know, and
//     treating acceptance as support would hide the MCP search tools behind a
//     capability that does not exist.
//
// The state lives behind a pointer shared by every client derived from the
// same provider (model overrides included), so one verdict serves them all.
type nativeSearchState struct {
	v atomic.Int32 // 0 unknown, 1 supported, 2 unsupported
}

const (
	nativeSearchUnknown     = int32(0)
	nativeSearchSupported   = int32(1)
	nativeSearchUnsupported = int32(2)
)

func (s *nativeSearchState) markUnsupported() {
	if s == nil {
		return
	}
	s.v.Store(nativeSearchUnsupported)
}

func (s *nativeSearchState) markSupported() {
	if s == nil {
		return
	}
	s.v.CompareAndSwap(nativeSearchUnknown, nativeSearchSupported)
}

func (s *nativeSearchState) verdict() (supported, known bool) {
	if s == nil {
		return false, false
	}
	switch s.v.Load() {
	case nativeSearchSupported:
		return true, true
	case nativeSearchUnsupported:
		return false, true
	default:
		return false, false
	}
}

// NativeWebSearchVerdict implements domain.NativeWebSearchReporter.
func (c *Client) NativeWebSearchVerdict() (supported, known bool) {
	if c == nil {
		return false, false
	}
	return c.nativeSearch.verdict()
}

// NativeWebSearchVerdict implements domain.NativeWebSearchReporter for the
// pool: unsupported if any client has proven unsupported (a pool that only
// sometimes searches natively cannot be relied on), supported if at least one
// has proven supported and none has proven otherwise.
func (p *Pool) NativeWebSearchVerdict() (supported, known bool) {
	if p == nil {
		return false, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	anySupported := false
	for _, w := range p.clients {
		if w == nil || w.client == nil {
			continue
		}
		s, k := w.client.NativeWebSearchVerdict()
		if !k {
			continue
		}
		if !s {
			return false, true
		}
		anySupported = true
	}
	return anySupported, anySupported
}

// recordNativeWebSearch turns one completed request into verdict evidence.
// requested/final are the options as sent first and as last retried; resp is
// the successful response body.
func (c *Client) recordNativeWebSearch(requested, final *domain.GenerationOptions, resp []byte) {
	if c == nil || requested == nil || final == nil {
		return
	}
	wanted := domain.UsesNativeWebSearch(requested.WebSearchMode)
	kept := domain.UsesNativeWebSearch(final.WebSearchMode)
	switch {
	case wanted && !kept:
		// The compatibility fallback stripped the parameters and the retry
		// succeeded: the upstream rejected native web search.
		c.nativeSearch.markUnsupported()
	case kept && responseShowsWebGrounding(resp):
		c.nativeSearch.markSupported()
	}
}

// responseShowsWebGrounding reports whether a chat-completions response body
// carries evidence that the model actually searched: OpenAI-style
// url_citation annotations, or Gemini-style grounding metadata as some
// gateways pass it through. This reads the provider's output, never the
// user's wording — absence of evidence keeps the verdict unknown rather than
// proving anything.
func responseShowsWebGrounding(resp []byte) bool {
	if len(resp) == 0 {
		return false
	}
	var probe struct {
		Choices []struct {
			Message struct {
				Annotations []struct {
					Type string `json:"type"`
				} `json:"annotations"`
			} `json:"message"`
			GroundingSnake json.RawMessage `json:"grounding_metadata"`
			GroundingCamel json.RawMessage `json:"groundingMetadata"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp, &probe); err != nil {
		return false
	}
	nonNull := func(raw json.RawMessage) bool {
		trimmed := bytes.TrimSpace(raw)
		return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) &&
			!bytes.Equal(trimmed, []byte("{}")) && !bytes.Equal(trimmed, []byte("[]"))
	}
	for _, ch := range probe.Choices {
		for _, a := range ch.Message.Annotations {
			if a.Type == "url_citation" {
				return true
			}
		}
		if nonNull(ch.GroundingSnake) || nonNull(ch.GroundingCamel) {
			return true
		}
	}
	return false
}
