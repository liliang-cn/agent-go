package agent

import "github.com/liliang-cn/agent-go/v3/pkg/domain"

// Prompt caching, from the agent's side.
//
// An agent loop resends its whole conversation every round, so the prompt is
// the part of the bill that grows. On providers that cache automatically the
// framework's job is only to keep the prefix byte-stable — which is why the
// system context reports the hour rather than the second, and why recalled
// memory is appended at the end of the system prompt rather than the middle.
//
// On providers that cache only what is marked, keeping the prefix stable buys
// nothing on its own: without a breakpoint every round re-pays for the entire
// history. WithPromptCache turns the markers on.
//
// It is off by default and named rather than detected. Whether an endpoint
// honours a marker is a fact about that endpoint, and there is no model name
// that reveals it — a gateway serving "claude-…" may or may not pass the field
// through. What the markers actually did is answered afterwards, by the
// provider's own numbers: TokenUsage.CacheWriteTokens is non-zero only if a
// breakpoint was really established, and CachedPromptTokens counts what a
// later round got for free.

// WithPromptCache enables explicit prompt-cache breakpoints for every request
// this service makes. Use it with an Anthropic-backed endpoint (directly or
// through an OpenAI-compatible gateway that passes cache_control through); on
// OpenAI or DeepSeek, whose caches are automatic, leave it off.
//
// A provider that rejects the markers is retried once without them, so
// turning this on cannot break a run — at worst it costs one extra round trip
// on the first call.
func (b *Builder) WithPromptCache(enabled bool) *Builder {
	if enabled {
		b.promptCache = domain.PromptCacheExplicit
	} else {
		b.promptCache = domain.PromptCacheOff
	}
	return b
}

// promptCacheMode reports the mode this service sends with each request.
func (s *Service) promptCacheMode() domain.PromptCacheMode {
	if s == nil {
		return domain.PromptCacheOff
	}
	return s.promptCache
}
