package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// SearXNG is the one engine here that is not a scraper. A self-hosted
// SearXNG instance exposes a stable JSON API (/search?format=json) that
// aggregates dozens of upstream engines, so it does not break when a search
// site changes its markup and it does not get rate-limited per client — the
// instance's own IP and engine rotation absorb that. When an instance is
// configured it is preferred over every scraping engine, which stay as
// fallbacks.
//
// Configuration is one environment variable, read at searcher construction:
//
//	SEARXNG_BASE_URL=http://192.168.123.61:43877
//
// The instance must have the json format enabled in its settings.yml
// (search.formats: [html, json]); without it the endpoint answers 403.

const searxngBaseURLEnv = "SEARXNG_BASE_URL"

// searxngBaseURL returns the configured instance URL, or "" when none is.
func searxngBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(searxngBaseURLEnv)), "/")
}

type searxngEngine struct {
	baseURL string
	client  *http.Client
}

// NewSearXNGEngine creates a SearchEngine backed by a SearXNG instance's
// JSON API.
func NewSearXNGEngine(baseURL string) SearchEngine {
	return &searxngEngine{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *searxngEngine) Name() string { return "searxng" }

func (s *searxngEngine) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	if maxResults <= 0 {
		maxResults = 10
	}
	endpoint := fmt.Sprintf("%s/search?q=%s&format=json", s.baseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("searxng returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("searxng response parse failed: %w", err)
	}

	results := make([]SearchResult, 0, min(len(payload.Results), maxResults))
	for _, r := range payload.Results {
		if len(results) >= maxResults {
			break
		}
		if strings.TrimSpace(r.URL) == "" || strings.TrimSpace(r.Title) == "" {
			continue
		}
		results = append(results, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
			Engine:  "searxng",
		})
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("searxng returned no results")
	}
	return results, nil
}

// defaultEngines builds the engine map every searcher shares: the scraping
// engines always, plus searxng when an instance is configured.
func defaultEngines() map[string]SearchEngine {
	engines := map[string]SearchEngine{
		"bing":       NewBingGoQueryEngine(),
		"brave":      NewBraveGoQueryEngine(),
		"duckduckgo": NewDuckDuckGoGoQueryEngine(),
	}
	if base := searxngBaseURL(); base != "" {
		engines["searxng"] = NewSearXNGEngine(base)
	}
	return engines
}

// enginePriority is the order engines are tried in: the configured SearXNG
// instance first — a stable JSON API beats parsing someone else's HTML —
// then the scrapers.
func enginePriority() []string {
	if searxngBaseURL() != "" {
		return []string{"searxng", "bing", "brave", "duckduckgo"}
	}
	return []string{"bing", "brave", "duckduckgo"}
}
