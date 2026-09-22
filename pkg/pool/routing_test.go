package pool

import (
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func routingPool(t *testing.T) *Pool {
	t.Helper()
	p, err := NewPool(PoolConfig{
		Enabled:  true,
		Strategy: StrategyLeastLoad,
		Providers: []Provider{
			{Name: "cheap", BaseURL: "http://cheap.example/v1", Key: "x", ModelName: "cheap-model", MaxConcurrency: 2, Capability: 2},
			{Name: "smart", BaseURL: "http://smart.example/v1", Key: "x", ModelName: "smart-model", MaxConcurrency: 2, Capability: 5},
		},
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

func TestClientForRoutesByModel(t *testing.T) {
	p := routingPool(t)
	for _, want := range []string{"cheap-model", "smart-model"} {
		client, err := p.clientFor(&domain.GenerationOptions{Model: want})
		if err != nil {
			t.Fatalf("clientFor(%s): %v", want, err)
		}
		if got := client.GetModelName(); got != want {
			t.Errorf("routed to %s, want %s", got, want)
		}
		p.Release(client)
	}
}

func TestClientForRoutesByProvider(t *testing.T) {
	p := routingPool(t)
	client, err := p.clientFor(&domain.GenerationOptions{Provider: "smart"})
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	defer p.Release(client)
	if got := client.GetProviderName(); got != "smart" {
		t.Errorf("routed to provider %s, want smart", got)
	}
}

func TestUnroutedOptionsTakeThePoolStrategy(t *testing.T) {
	// The overwhelmingly common case: nothing named, nothing changed. This is
	// what keeps routing from costing anything for callers who do not use it.
	p := routingPool(t)
	for _, opts := range []*domain.GenerationOptions{nil, {}, {Temperature: 0.4}} {
		client, err := p.clientFor(opts)
		if err != nil {
			t.Fatalf("clientFor: %v", err)
		}
		if client == nil {
			t.Fatal("no client for unrouted options")
		}
		p.Release(client)
	}
}

func TestAModelNobodyServesStillAnswers(t *testing.T) {
	// A preference the pool cannot meet must not fail the run: the pool is
	// whatever someone configured, and a routing mistake should not become an
	// outage. The caller is told what really answered instead — see
	// TestStampReportsWhatActuallyAnswered.
	p := routingPool(t)
	client, err := p.clientFor(&domain.GenerationOptions{Model: "a-model-nobody-configured"})
	if err != nil {
		t.Fatalf("an unmatched preference failed the call: %v", err)
	}
	defer p.Release(client)
	if got := client.GetModelName(); got == "a-model-nobody-configured" {
		t.Fatal("the pool invented a client for an unconfigured model")
	}
}

func TestStampReportsWhatActuallyAnswered(t *testing.T) {
	// Without this a route that silently fell back is indistinguishable from
	// one that took effect, which is the whole hazard of a preference-shaped
	// API.
	p := routingPool(t)
	client, err := p.clientFor(&domain.GenerationOptions{Model: "cheap-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release(client)

	res := stamp(&domain.GenerationResult{Content: "hi"}, client)
	if res.Model != "cheap-model" || res.Provider != "cheap" {
		t.Errorf("stamped %s/%s, want cheap-model/cheap", res.Model, res.Provider)
	}
	if got := stamp(nil, client); got != nil {
		t.Error("stamping a nil result invented one")
	}
}
