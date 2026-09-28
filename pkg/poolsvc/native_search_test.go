package poolsvc

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

var _ domain.NativeWebSearchReporter = (*llmServiceWrapper)(nil)

// A declaration stored in agentgo.db reaches the service an agent is built
// over. This is the path every Manager agent takes, and before the wrapper
// reported a verdict at all it was always unknown here.
func TestDeclaredProviderVerdictReachesServiceWrapper(t *testing.T) {
	svc := &Service{}
	cfg := newPoolTestConfig(t)
	seedPoolDB(t, cfg,
		[]*store.LLMProvider{
			{Name: "cpa", BaseURL: "http://cpa.example/v1", Key: "x", ModelName: "gemini-a", MaxConcurrency: 2, Capability: 5, NativeWebSearch: "google_search", Enabled: true},
			{Name: "deepseek", BaseURL: "http://deepseek.example/v1", Key: "x", ModelName: "deepseek-chat", MaxConcurrency: 2, Capability: 4, NativeWebSearch: "none", Enabled: true},
		},
		nil, "round_robin", "round_robin", "",
	)
	if err := svc.Initialize(context.Background(), cfg); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	for provider, want := range map[string]bool{"cpa": true, "deepseek": false} {
		llm, err := svc.GetLLMServiceWithHint(pool.SelectionHint{PreferredProvider: provider})
		if err != nil {
			t.Fatal(err)
		}
		reporter, ok := llm.(domain.NativeWebSearchReporter)
		if !ok {
			t.Fatal("service wrapper does not report a native web-search verdict")
		}
		if supported, known := reporter.NativeWebSearchVerdict(); supported != want || !known {
			t.Errorf("%s: verdict = (%v, %v), want (%v, true)", provider, supported, known, want)
		}
	}
}

func TestProviderDeclarationRoundTripsThroughStore(t *testing.T) {
	cfg := newPoolTestConfig(t)
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.SaveProvider(&store.LLMProvider{Name: "cpa", BaseURL: "http://x", ModelName: "m", NativeWebSearch: " Google_Search ",
		NativeWebSearchOptions: map[string]interface{}{"forced_search": true}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetProvider("cpa")
	if err != nil {
		t.Fatal(err)
	}
	if got.NativeWebSearch != "google_search" {
		t.Fatalf("stored native_web_search = %q, want google_search", got.NativeWebSearch)
	}
	if p := store.ToPoolProvider(got); p.NativeWebSearch != "google_search" || p.NativeWebSearchOptions["forced_search"] != true {
		t.Fatalf("pool provider = %q %v, want google_search with forced_search", p.NativeWebSearch, p.NativeWebSearchOptions)
	}
	if err := db.SaveProvider(&store.LLMProvider{Name: "bad2", BaseURL: "http://x", ModelName: "m",
		NativeWebSearchOptions: map[string]interface{}{"forced_search": true}, Enabled: true}); err == nil {
		t.Fatal("saved options on an undeclared provider")
	}

	err = db.SaveProvider(&store.LLMProvider{Name: "bad", BaseURL: "http://x", ModelName: "m", NativeWebSearch: "gogle", Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "gogle") {
		t.Fatalf("saving an unknown declaration: err = %v, want it refused", err)
	}
}
