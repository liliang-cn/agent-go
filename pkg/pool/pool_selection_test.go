package pool

import "testing"

func TestPoolGetWithHintPrefersProviderAndCapability(t *testing.T) {
	p, err := NewPool(PoolConfig{
		Enabled:  true,
		Strategy: StrategyLeastLoad,
		Providers: []Provider{
			{Name: "fast", BaseURL: "http://fast.example/v1", Key: "x", ModelName: "fast-model", MaxConcurrency: 2, Capability: 2},
			{Name: "smart", BaseURL: "http://smart.example/v1", Key: "x", ModelName: "smart-model", MaxConcurrency: 2, Capability: 5},
		},
	})
	if err != nil {
		t.Fatalf("NewPool failed: %v", err)
	}

	client, err := p.GetWithHint(SelectionHint{
		PreferredProvider: "smart",
		MinCapability:     4,
	})
	if err != nil {
		t.Fatalf("GetWithHint failed: %v", err)
	}
	defer p.Release(client)

	if got := client.GetModelName(); got != "smart-model" {
		t.Fatalf("expected preferred provider model, got %s", got)
	}
}

func TestPoolGetWithHintFallsBackByCapabilityWhenPreferredBusy(t *testing.T) {
	p, err := NewPool(PoolConfig{
		Enabled:  true,
		Strategy: StrategyLeastLoad,
		Providers: []Provider{
			{Name: "primary", BaseURL: "http://primary.example/v1", Key: "x", ModelName: "primary-model", MaxConcurrency: 1, Capability: 5},
			{Name: "backup", BaseURL: "http://backup.example/v1", Key: "x", ModelName: "backup-model", MaxConcurrency: 2, Capability: 4},
		},
	})
	if err != nil {
		t.Fatalf("NewPool failed: %v", err)
	}

	busyClient, err := p.GetWithHint(SelectionHint{PreferredProvider: "primary", MinCapability: 4})
	if err != nil {
		t.Fatalf("initial GetWithHint failed: %v", err)
	}
	defer p.Release(busyClient)

	client, err := p.GetWithHint(SelectionHint{PreferredProvider: "primary", MinCapability: 4})
	if err != nil {
		t.Fatalf("fallback GetWithHint failed: %v", err)
	}
	defer p.Release(client)

	if got := client.GetModelName(); got != "backup-model" {
		t.Fatalf("expected fallback provider model, got %s", got)
	}
}

func TestPoolGetByProviderReturnsMatchingClient(t *testing.T) {
	p, err := NewPool(PoolConfig{
		Enabled:  true,
		Strategy: StrategyLeastLoad,
		Providers: []Provider{
			{Name: "primary", BaseURL: "http://primary.example/v1", Key: "x", ModelName: "primary-model", MaxConcurrency: 2, Capability: 5},
			{Name: "backup", BaseURL: "http://backup.example/v1", Key: "x", ModelName: "backup-model", MaxConcurrency: 2, Capability: 3},
		},
	})
	if err != nil {
		t.Fatalf("NewPool failed: %v", err)
	}

	client, err := p.GetByProvider("backup")
	if err != nil {
		t.Fatalf("GetByProvider failed: %v", err)
	}
	defer p.Release(client)

	if got := client.GetModelName(); got != "backup-model" {
		t.Fatalf("expected backup-model, got %s", got)
	}
}

func TestPoolGetByModelReturnsMatchingClient(t *testing.T) {
	p, err := NewPool(PoolConfig{
		Enabled:  true,
		Strategy: StrategyLeastLoad,
		Providers: []Provider{
			{Name: "primary", BaseURL: "http://primary.example/v1", Key: "x", ModelName: "primary-model", MaxConcurrency: 2, Capability: 5},
			{Name: "backup", BaseURL: "http://backup.example/v1", Key: "x", ModelName: "backup-model", MaxConcurrency: 2, Capability: 3},
		},
	})
	if err != nil {
		t.Fatalf("NewPool failed: %v", err)
	}

	client, err := p.GetByModel("primary-model")
	if err != nil {
		t.Fatalf("GetByModel failed: %v", err)
	}
	defer p.Release(client)

	if got := client.GetModelName(); got != "primary-model" {
		t.Fatalf("expected primary-model, got %s", got)
	}
}

func TestPoolGetByProviderAndModelReturnsRequestedModel(t *testing.T) {
	p, err := NewPool(PoolConfig{
		Enabled:  true,
		Strategy: StrategyLeastLoad,
		Providers: []Provider{
			{
				Name:           "qc",
				BaseURL:        "http://qc.example/v1",
				Key:            "x",
				ModelName:      "gpt-5.4",
				Models:         []string{"gpt-5.4", "gpt-5-mini"},
				MaxConcurrency: 2,
				Capability:     5,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewPool failed: %v", err)
	}

	client, err := p.GetByProviderAndModel("qc", "gpt-5-mini")
	if err != nil {
		t.Fatalf("GetByProviderAndModel failed: %v", err)
	}
	defer p.Release(client)

	if got := client.GetModelName(); got != "gpt-5-mini" {
		t.Fatalf("expected requested model gpt-5-mini, got %s", got)
	}
}
