package pool

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"
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
//
// A declared format (Provider.NativeWebSearch) sets the verdict at
// construction. Absence of evidence never moves it — that is the point of
// declaring, since a gateway that strips grounding metadata (cpa does) can
// never prove support by evidence. A rejection still does: it is the upstream
// refusing the very field the declaration names, and honouring the
// declaration past that would hide the MCP search tools behind a search that
// cannot run. So a rejected declaration falls back to unsupported, and says so
// at ERROR, because it is a configuration mistake somebody has to fix.
type nativeSearchState struct {
	v        atomic.Int32 // 0 unknown, 1 supported, 2 unsupported
	format   domain.NativeWebSearchFormat
	options  map[string]interface{}
	declared bool
	// proven is set when a response carried grounding evidence. A declared
	// verdict says the field works; only proof says the model actually
	// searches with it, and only proof may hide the MCP search tools.
	proven   atomic.Bool
	provider string
	rejected sync.Once
}

// newNativeSearchState builds the state for one provider, applying its
// declaration when there is one.
func newNativeSearchState(provider string, format domain.NativeWebSearchFormat, options map[string]interface{}) *nativeSearchState {
	s := &nativeSearchState{format: format, options: cloneSearchOptions(options), provider: provider}
	switch format {
	case domain.NativeWebSearchUndeclared:
	case domain.NativeWebSearchNone:
		s.declared = true
		s.v.Store(nativeSearchUnsupported)
	default:
		s.declared = true
		s.v.Store(nativeSearchSupported)
	}
	return s
}

// sendFormat is the request shape to use when native search is requested:
// the declared one, or undeclared (send every known field and learn).
func (s *nativeSearchState) sendFormat() domain.NativeWebSearchFormat {
	if s == nil {
		return domain.NativeWebSearchUndeclared
	}
	return s.format
}

// sendOptions are the declared extra parameters for the declared field.
func (s *nativeSearchState) sendOptions() map[string]interface{} {
	if s == nil {
		return nil
	}
	return s.options
}

func cloneSearchOptions(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
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
	if s.declared && s.format != domain.NativeWebSearchNone {
		s.rejected.Do(func() {
			slog.Error("provider rejected the native web-search field it was declared to support; falling back to MCP search — fix native_web_search or the gateway",
				"module", "pool", "provider", s.provider, "declared", string(s.format))
		})
	}
	s.v.Store(nativeSearchUnsupported)
}

func (s *nativeSearchState) markSupported() {
	if s == nil {
		return
	}
	if s.declared {
		// Evidence never moves a declared verdict, but it does prove that a
		// declared "supported" is real.
		if s.format != domain.NativeWebSearchNone {
			s.proven.Store(true)
		}
		return
	}
	if s.v.CompareAndSwap(nativeSearchUnknown, nativeSearchSupported) || s.v.Load() == nativeSearchSupported {
		s.proven.Store(true)
	}
}

func (s *nativeSearchState) isProven() bool {
	if s == nil {
		return false
	}
	supported, known := s.verdict()
	return supported && known && s.proven.Load()
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

// NativeWebSearchProven implements domain.NativeWebSearchEvidence: whether a
// response has shown this client's upstream actually searching, as opposed to
// support that was only declared.
func (c *Client) NativeWebSearchProven() bool {
	if c == nil {
		return false
	}
	return c.nativeSearch.isProven()
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

// NativeWebSearchProven implements domain.NativeWebSearchEvidence for the
// pool: true only when every client that reports support has proven it, since
// any of them may serve the next request.
func (p *Pool) NativeWebSearchProven() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	anyProven := false
	for _, w := range p.clients {
		if w == nil || w.client == nil {
			continue
		}
		if s, k := w.client.NativeWebSearchVerdict(); !(s && k) {
			continue
		}
		if !w.client.NativeWebSearchProven() {
			return false
		}
		anyProven = true
	}
	return anyProven
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
