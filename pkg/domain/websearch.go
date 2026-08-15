package domain

import "strings"

type WebSearchMode string

const (
	WebSearchModeAuto   WebSearchMode = "auto"
	WebSearchModeNative WebSearchMode = "native"
	WebSearchModeMCP    WebSearchMode = "mcp"
	WebSearchModeOff    WebSearchMode = "off"
)

// NormalizeWebSearchMode maps a mode string onto a mode. Empty and
// unrecognized values fall back to mcp: this function also normalizes
// per-request GenerationOptions, whose zero value must keep meaning "no
// native search parameters" — internal single-shot calls construct bare
// options all the time. The auto-by-default behaviour lives one level up, in
// the service-level configuration (Service.webSearchMode), where it only
// affects agent tool rounds.
func NormalizeWebSearchMode(mode WebSearchMode) WebSearchMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(WebSearchModeAuto):
		return WebSearchModeAuto
	case string(WebSearchModeNative):
		return WebSearchModeNative
	case string(WebSearchModeOff):
		return WebSearchModeOff
	default:
		return WebSearchModeMCP
	}
}

func UsesNativeWebSearch(mode WebSearchMode) bool {
	normalized := NormalizeWebSearchMode(mode)
	return normalized == WebSearchModeAuto || normalized == WebSearchModeNative
}

// NativeWebSearchReporter is implemented by generators (the pool client) that
// can report what they have learned about the upstream's native web-search
// support. Learning is evidence-based and asymmetric, never a model-name
// table:
//
//   - unsupported is proven by the upstream rejecting the web-search request
//     parameters (the same detection the compatibility fallback retries on);
//   - supported is proven by a response carrying grounding evidence
//     (url_citation annotations, grounding metadata) — a provider that merely
//     *accepts* the parameters may be ignoring them, which must not count;
//   - anything else stays unknown, and auto mode keeps both routes available.
type NativeWebSearchReporter interface {
	// NativeWebSearchVerdict returns whether native web search works upstream.
	// known is false while there is no evidence either way.
	NativeWebSearchVerdict() (supported, known bool)
}

func NormalizeWebSearchContextSize(size string) string {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(size))
	default:
		return "medium"
	}
}
