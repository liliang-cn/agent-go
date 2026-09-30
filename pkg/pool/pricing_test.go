package pool

import "testing"

// An unknown model must be reported as unknown, not priced at zero: a zero is
// indistinguishable from a free call, and MaxTotalCostUSD is built on it.
func TestUnknownModelIsUnpricedNotFree(t *testing.T) {
	cost, known := CalculateCostDetailed("some-model-nobody-listed", 100000, 0, 50000)
	if known {
		t.Fatal("expected an unlisted model to be unknown")
	}
	if cost != 0 {
		t.Fatalf("expected 0 cost alongside unknown, got %f", cost)
	}
}

// A registration prices every model whose name contains its pattern.
func TestARegisteredPatternPricesTheModelsItNames(t *testing.T) {
	RegisterModelPricing("gpt-4o", ModelPricing{InputPer1K: 1, OutputPer1K: 2})
	defer UnregisterModelPricing("gpt-4o")

	cost, known := CalculateCostDetailed("gpt-4o-2026-05-01", 1000, 0, 1000)
	if !known {
		t.Fatal("expected the registration to be found")
	}
	if cost != 3 {
		t.Fatalf("expected 1+2=3, got %f", cost)
	}
}

// Cache hits are billed at the cache rate. Without this a long run's estimate
// overstates the bill by most of its prompt spend.
func TestCachedPromptTokensAreBilledAtTheCacheRate(t *testing.T) {
	RegisterModelPricing("pricing-test-model", ModelPricing{
		InputPer1K: 1, CachedInputPer1K: 0.1, OutputPer1K: 2,
	})
	defer UnregisterModelPricing("pricing-test-model")

	// 10k prompt tokens of which 8k were cache hits, plus 1k output.
	cost, known := CalculateCostDetailed("pricing-test-model", 10000, 8000, 1000)
	if !known {
		t.Fatal("expected known pricing")
	}
	want := 2.0*1 + 8.0*0.1 + 1.0*2 // 2 + 0.8 + 2
	if diff := cost - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("want %f, got %f", want, cost)
	}

	blind := CalculateCost("pricing-test-model", 10000, 1000)
	if blind <= cost {
		t.Fatalf("cache-blind pricing (%f) should exceed cache-aware (%f)", blind, cost)
	}
}

// Longest match wins, so a specific entry is never shadowed by a generic one.
func TestLongestPricingPatternWins(t *testing.T) {
	RegisterModelPricing("gpt-5", ModelPricing{InputPer1K: 1})
	RegisterModelPricing("gpt-5.5", ModelPricing{InputPer1K: 2})
	defer UnregisterModelPricing("gpt-5")
	defer UnregisterModelPricing("gpt-5.5")
	if p, _ := LookupModelPricing("gpt-5.5-turbo"); p.InputPer1K != 2 {
		t.Fatalf("unexpected pricing for gpt-5.5: %+v", p)
	}
}

// Nothing is priced until someone says what it costs: a bundled table went
// stale and priced models nobody runs any more.
func TestNothingIsPricedUntilRegistered(t *testing.T) {
	for _, m := range []string{"gpt-4o", "claude-3-opus", "deepseek-chat", "gemini-3.8-flash-high"} {
		if _, known := LookupModelPricing(m); known {
			t.Errorf("%s is priced with nothing registered", m)
		}
	}
}
