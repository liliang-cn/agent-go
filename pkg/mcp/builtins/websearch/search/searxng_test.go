package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearXNGEngineParsesJSONResults(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"Go 1.26 Release Notes","url":"https://go.dev/doc/go1.26","content":"What's new"},
			{"title":"","url":"https://skipped.example","content":"no title, dropped"},
			{"title":"Go Blog","url":"https://go.dev/blog/go1.26","content":"released"}
		]}`))
	}))
	defer srv.Close()

	results, err := NewSearXNGEngine(srv.URL).Search(context.Background(), "golang 1.26", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (titleless dropped): %+v", len(results), results)
	}
	if results[0].Title != "Go 1.26 Release Notes" || results[0].Engine != "searxng" || results[0].Snippet != "What's new" {
		t.Fatalf("first result mismapped: %+v", results[0])
	}
	if gotPath != "/search?q=golang+1.26&format=json" {
		t.Fatalf("request path = %q", gotPath)
	}
}

func TestSearXNGEngineErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// What a SearXNG instance without json in search.formats answers.
		http.Error(w, "403 Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := NewSearXNGEngine(srv.URL).Search(context.Background(), "q", 3); err == nil {
		t.Fatal("expected an error on 403 (json format not enabled)")
	}
}

// With SEARXNG_BASE_URL set, both searchers gain the engine and try it first;
// without it, nothing changes.
func TestSearXNGConfigurationShapesEngineSet(t *testing.T) {
	t.Setenv(searxngBaseURLEnv, "")
	if _, ok := defaultEngines()["searxng"]; ok {
		t.Fatal("searxng engine present without configuration")
	}
	if p := enginePriority(); p[0] != "bing" {
		t.Fatalf("unconfigured priority starts with %q, want bing", p[0])
	}

	t.Setenv(searxngBaseURLEnv, "http://192.0.2.1:43877/")
	engines := defaultEngines()
	if _, ok := engines["searxng"]; !ok {
		t.Fatal("searxng engine missing despite SEARXNG_BASE_URL")
	}
	if p := enginePriority(); p[0] != "searxng" {
		t.Fatalf("configured priority starts with %q, want searxng", p[0])
	}
	// Scrapers stay as fallbacks.
	for _, name := range []string{"bing", "brave", "duckduckgo"} {
		if _, ok := engines[name]; !ok {
			t.Fatalf("scraping fallback %s missing", name)
		}
	}
}
