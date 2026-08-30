package pool

import (
	"sort"
	"strings"
	"sync"
)

// Model pricing.
//
// Three things were wrong with the hardcoded table this replaces, and only the
// first one is obvious.
//
//  1. It went stale. Prices move and model families arrive; a table in the
//     framework's source can only ever describe the models whoever last edited
//     it happened to know about.
//  2. A model it had never heard of priced at zero. Silently. That is not a
//     rough estimate, it is a wrong one that looks like a cheap run — and
//     LongRunConfig.MaxTotalCostUSD is a *stop condition* built on this
//     number, so an unknown model quietly removed the only spending ceiling a
//     run that lasts hours has.
//  3. It billed cache hits at full price. Once prompt caching works, most of a
//     long run's prompt tokens are hits at a fraction of the rate, and a
//     cache-blind total overstates the bill by more than it estimates.
//
// So: an operator can state their own prices and that always wins, the bundled
// table is an explicitly-fallible fallback, and "I do not know this model's
// price" is a value a caller can read rather than a zero it cannot distinguish
// from free.

// ModelPricing is what one model costs, in USD per 1000 tokens.
type ModelPricing struct {
	// InputPer1K is the rate for prompt tokens that were not cache hits.
	InputPer1K float64
	// CachedInputPer1K is the rate for prompt tokens the provider served from
	// its prompt cache. Zero means "same as InputPer1K" — a provider without
	// a cache discount, not a free one.
	CachedInputPer1K float64
	// OutputPer1K is the rate for completion tokens, reasoning included:
	// providers bill reasoning as output whether or not the caller sees it.
	OutputPer1K float64
}

var (
	pricingMu       sync.RWMutex
	registeredPrice = map[string]ModelPricing{}
)

// RegisterModelPricing states the price for every model whose name contains
// pattern (matched case-insensitively, longest pattern first). Registrations
// win over the bundled table, so an operator who knows their contract never
// depends on this package being up to date. Registering the same pattern twice
// replaces it; an empty pattern is ignored.
func RegisterModelPricing(pattern string, p ModelPricing) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return
	}
	pricingMu.Lock()
	defer pricingMu.Unlock()
	registeredPrice[pattern] = p
}

// UnregisterModelPricing removes a registration.
func UnregisterModelPricing(pattern string) {
	pricingMu.Lock()
	defer pricingMu.Unlock()
	delete(registeredPrice, strings.ToLower(strings.TrimSpace(pattern)))
}

// fallbackPricing is the bundled table. It is a convenience for development
// and nothing more: it is not maintained against provider price changes, it
// covers only what someone happened to add, and anything it does not name is
// reported as unknown rather than guessed. Production callers should register
// their own rates.
var fallbackPricing = map[string]ModelPricing{
	"gpt-3.5-turbo":     {InputPer1K: 0.0005, OutputPer1K: 0.0015},
	"gpt-4o":            {InputPer1K: 0.005, OutputPer1K: 0.015},
	"gpt-4-turbo":       {InputPer1K: 0.01, OutputPer1K: 0.03},
	"gpt-4":             {InputPer1K: 0.03, OutputPer1K: 0.06},
	"gpt-5.5":           {InputPer1K: 0.005, OutputPer1K: 0.015},
	"gpt-5":             {InputPer1K: 0.005, OutputPer1K: 0.015},
	"o1":                {InputPer1K: 0.015, OutputPer1K: 0.06},
	"o3-mini":           {InputPer1K: 0.0011, OutputPer1K: 0.0044},
	"claude-3-opus":     {InputPer1K: 0.015, OutputPer1K: 0.075},
	"claude-3-sonnet":   {InputPer1K: 0.003, OutputPer1K: 0.015},
	"claude-3-haiku":    {InputPer1K: 0.00025, OutputPer1K: 0.00125},
	"claude-3.5-sonnet": {InputPer1K: 0.003, OutputPer1K: 0.015},
	"claude-4":          {InputPer1K: 0.005, OutputPer1K: 0.025},
	// DeepSeek publishes a cache-hit rate an order of magnitude below its
	// miss rate, which is the whole reason the cached field exists here.
	"deepseek-v4-flash": {InputPer1K: 0.00027, CachedInputPer1K: 0.00003, OutputPer1K: 0.0011},
	"deepseek-v4-pro":   {InputPer1K: 0.00054, CachedInputPer1K: 0.00006, OutputPer1K: 0.00218},
	"deepseek-chat":     {InputPer1K: 0.00027, CachedInputPer1K: 0.00003, OutputPer1K: 0.0011},
	"deepseek-coder":    {InputPer1K: 0.00054, CachedInputPer1K: 0.00006, OutputPer1K: 0.00218},
	"deepseek-reasoner": {InputPer1K: 0.00055, CachedInputPer1K: 0.00006, OutputPer1K: 0.0022},
}

// LookupModelPricing resolves a model's rates, reporting whether any source
// knew them. Registrations are consulted before the bundled table, and within
// each the longest matching pattern wins so "gpt-5.5" beats "gpt-5".
func LookupModelPricing(model string) (ModelPricing, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return ModelPricing{}, false
	}

	pricingMu.RLock()
	registered := make(map[string]ModelPricing, len(registeredPrice))
	for k, v := range registeredPrice {
		registered[k] = v
	}
	pricingMu.RUnlock()

	for _, table := range []map[string]ModelPricing{registered, fallbackPricing} {
		keys := make([]string, 0, len(table))
		for k := range table {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
		for _, k := range keys {
			if strings.Contains(name, k) {
				return table[k], true
			}
		}
	}
	return ModelPricing{}, false
}

// CalculateCostDetailed prices one call, splitting the prompt into the part
// served from cache and the part that was not, and reports whether the model's
// rates were known at all. A false second return means the cost is 0 because
// nothing could price it — never because the call was free.
//
// inputTokens is the provider's full prompt count, cache hits included, which
// is how every provider reports it; cachedInputTokens is the hit portion of
// that same number.
func CalculateCostDetailed(model string, inputTokens, cachedInputTokens, outputTokens int) (float64, bool) {
	pricing, known := LookupModelPricing(model)
	if !known {
		return 0, false
	}
	if cachedInputTokens < 0 {
		cachedInputTokens = 0
	}
	if cachedInputTokens > inputTokens {
		cachedInputTokens = inputTokens
	}
	cachedRate := pricing.CachedInputPer1K
	if cachedRate <= 0 {
		cachedRate = pricing.InputPer1K
	}
	fresh := inputTokens - cachedInputTokens

	cost := float64(fresh)/1000.0*pricing.InputPer1K +
		float64(cachedInputTokens)/1000.0*cachedRate +
		float64(outputTokens)/1000.0*pricing.OutputPer1K
	return cost, true
}

// CalculateCost prices a call whose cache split is unknown, charging the whole
// prompt at the uncached rate. Prefer CalculateCostDetailed wherever the
// provider reported a cache hit count.
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
	cost, _ := CalculateCostDetailed(model, inputTokens, 0, outputTokens)
	return cost
}
