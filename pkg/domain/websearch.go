package domain

import (
	"fmt"
	"strings"
)

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

// NativeWebSearchFormat is what an operator declares about one provider's
// built-in web search: whether it has one, and which request field turns it
// on.
//
// It is a wire format rather than a yes/no because "yes" alone leaves the
// client guessing what to send, and guessing is how an undeclared provider
// works today — it sends both the OpenAI and the DashScope field and learns
// from the response. A declaration replaces that learning with a statement:
// the verdict is known from the first request and only the named field is
// sent. Silence never moves a declared verdict; an upstream that rejects the
// declared field does, and is logged as an error, because searching was
// declared and cannot happen.
//
// The empty value means "not declared" and keeps the evidence-based learning.
type NativeWebSearchFormat string

const (
	// NativeWebSearchUndeclared keeps auto-detection.
	NativeWebSearchUndeclared NativeWebSearchFormat = ""
	// NativeWebSearchNone declares that the provider has no built-in search:
	// no search field is ever sent and the MCP search tools stay.
	NativeWebSearchNone NativeWebSearchFormat = "none"
	// NativeWebSearchOpenAI sends `web_search_options` (OpenAI search models
	// and gateways that follow them).
	NativeWebSearchOpenAI NativeWebSearchFormat = "openai"
	// NativeWebSearchDashScope sends `enable_search: true` (Qwen on DashScope).
	NativeWebSearchDashScope NativeWebSearchFormat = "dashscope"
	// NativeWebSearchGoogle adds `{"google_search": {}}` to the request's
	// tools (Gemini's Google Search grounding, as OpenAI-compatible Gemini
	// gateways accept it).
	NativeWebSearchGoogle NativeWebSearchFormat = "google_search"
)

// ParseNativeWebSearchFormat validates a declared format. An unknown value is
// an error rather than a fallback: a typo in a declaration would otherwise
// silently become "not declared", which is exactly the state the operator was
// trying to leave.
func ParseNativeWebSearchFormat(s string) (NativeWebSearchFormat, error) {
	switch f := NativeWebSearchFormat(strings.ToLower(strings.TrimSpace(s))); f {
	case NativeWebSearchUndeclared, NativeWebSearchNone, NativeWebSearchOpenAI,
		NativeWebSearchDashScope, NativeWebSearchGoogle:
		return f, nil
	default:
		return "", fmt.Errorf("unknown native_web_search %q (want none, openai, dashscope or google_search)", s)
	}
}

// NativeWebSearchEvidence is an optional companion to NativeWebSearchReporter.
// A supported verdict can come from an operator's declaration or from a
// response that showed the model searching; only the second proves the model
// will use the capability, so only it may take the MCP search tools away. A
// declaration keeps both routes open. Reporters that do not implement this
// are treated as proven, which is what their verdict always meant.
type NativeWebSearchEvidence interface {
	NativeWebSearchProven() bool
}

// ValidateNativeWebSearchOptions rejects options that have nowhere to go:
// options are sent inside the declared field, so they need a format that has
// one.
func ValidateNativeWebSearchOptions(format NativeWebSearchFormat, options map[string]interface{}) error {
	if len(options) == 0 {
		return nil
	}
	switch format {
	case NativeWebSearchOpenAI, NativeWebSearchDashScope, NativeWebSearchGoogle:
		return nil
	default:
		return fmt.Errorf("native_web_search_options need native_web_search set to openai, dashscope or google_search (got %q)", string(format))
	}
}

func NormalizeWebSearchContextSize(size string) string {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(size))
	default:
		return "medium"
	}
}
