package pool

import (
	"encoding/json"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Token accounting, and specifically the cache half of it.
//
// The pool client did not read the `usage` object at all, so every result it
// returned carried a nil Usage. Everything downstream that reasons about cost
// — the runtime's per-round accounting, the budget cap, any question of the
// form "is the prompt cache working" — was reading zero on this path and had
// no way to know it was reading zero rather than measuring nothing.
//
// That is tolerable for a chat turn and not for a run that lasts hours, where
// the cache-hit ratio is the difference between an affordable run and an
// absurd one.

// poolUsage is the union of the shapes providers report token accounting in.
// Every field is optional; a provider that omits one leaves it zero.
type poolUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`

	// OpenAI's nested form.
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`

	// DeepSeek reports the same number flat, alongside its complement
	// (prompt_cache_miss_tokens), which we do not need: misses are
	// prompt_tokens minus hits.
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`

	// Anthropic-style explicit caching, as passed through by gateways that
	// front it. Reads are the discount; writes are the surcharge for
	// establishing a breakpoint, and they are the reason a run can look more
	// expensive on its first pass and much cheaper on every one after.
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// parsePoolUsage extracts token accounting from a raw chat-completions
// response body. It returns nil when the provider reported none — an honest
// "unknown" that a caller can distinguish from a genuine zero.
func parsePoolUsage(raw []byte) *domain.TokenUsage {
	var envelope struct {
		Usage *poolUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Usage == nil {
		return nil
	}
	u := envelope.Usage

	// The read-side hit count arrives under whichever name the provider
	// chose. They are the same quantity, so take whichever is present
	// rather than adding them — a gateway that reports two of them is
	// reporting one number twice.
	cached := u.PromptCacheHitTokens
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > cached {
		cached = u.PromptTokensDetails.CachedTokens
	}
	if u.CacheReadInputTokens > cached {
		cached = u.CacheReadInputTokens
	}

	usage := &domain.TokenUsage{
		PromptTokens:       u.PromptTokens,
		CompletionTokens:   u.CompletionTokens,
		CachedPromptTokens: cached,
		CacheWriteTokens:   u.CacheCreationInputTokens,
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 &&
		usage.CachedPromptTokens == 0 && usage.CacheWriteTokens == 0 {
		return nil
	}
	return usage
}
