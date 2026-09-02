package pool

import "testing"

func TestPoolNamesItsModel(t *testing.T) {
	p, err := NewPool(PoolConfig{Enabled: true, Providers: []Provider{
		{Name: "a", BaseURL: "https://a.example/v1", Key: "k", ModelName: "model-one"},
		{Name: "b", BaseURL: "https://b.example/v1", Key: "k", ModelName: "model-two"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := p.GetModelName(); got != "model-one" {
		t.Fatalf("GetModelName = %q, want the first provider's model", got)
	}
	if got := p.UsageModel(); got != "model-one" {
		t.Fatalf("UsageModel = %q", got)
	}
	if got := p.GetBaseURL(); got != "https://a.example/v1" {
		t.Fatalf("GetBaseURL = %q", got)
	}
	var empty *Pool
	if empty.GetModelName() != "" || empty.GetBaseURL() != "" {
		t.Fatal("a nil pool must answer with empty strings, not panic")
	}
}
