package search

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

type braveSearchEngine struct {
	client *http.Client
}

func NewBraveSearchEngine() SearchEngine {
	return &braveSearchEngine{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (b *braveSearchEngine) Name() string {
	return "brave"
}

func (b *braveSearchEngine) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	searchURL := fmt.Sprintf("https://search.brave.com/search?q=%s", url.QueryEscape(query))

	allocCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()

	var results []SearchResult
	var nodes []*cdp.Node

	// Navigate and wait for results
	err := chromedp.Run(allocCtx,
		chromedp.Navigate(searchURL),
		chromedp.Sleep(3*time.Second), // Let page fully load
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search Brave: %w", err)
	}

	// Try multiple selectors for Brave results
	selectors := []string{
		`div[data-type="web"] .snippet`,
		`.snippet`,
		`#results .result-card`,
		`#results article`,
		`.search-result`,
		`div[data-testid="web-result"]`,
	}

	for _, selector := range selectors {
		chromedp.Run(allocCtx, chromedp.Nodes(selector, &nodes, chromedp.ByQueryAll))
		if len(nodes) > 0 {
			break
		}
	}

	// If still no nodes, try to get any result container
	if len(nodes) == 0 {
		chromedp.Run(allocCtx, chromedp.Nodes(`#results > div`, &nodes, chromedp.ByQueryAll))
	}

	for i, node := range nodes {
		if i >= maxResults {
			break
		}

		var title, link, snippet string

		// Try various selectors for title
		chromedp.Run(allocCtx,
			chromedp.Text(`.snippet-title`, &title, chromedp.ByQuery, chromedp.FromNode(node)),
		)

		if title == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`h3`, &title, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if title == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`a[data-testid="result-title"]`, &title, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if title == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`a`, &title, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		// Try various selectors for link
		chromedp.Run(allocCtx,
			chromedp.AttributeValue(`.result-header a`, "href", &link, nil, chromedp.ByQuery, chromedp.FromNode(node)),
		)

		if link == "" {
			chromedp.Run(allocCtx,
				chromedp.AttributeValue(`.snippet-title`, "href", &link, nil, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if link == "" {
			chromedp.Run(allocCtx,
				chromedp.AttributeValue(`a[data-testid="result-title"]`, "href", &link, nil, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if link == "" {
			chromedp.Run(allocCtx,
				chromedp.AttributeValue(`a`, "href", &link, nil, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		// Try various selectors for snippet
		chromedp.Run(allocCtx,
			chromedp.Text(`.snippet-description`, &snippet, chromedp.ByQuery, chromedp.FromNode(node)),
		)

		if snippet == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`[data-testid="result-description"]`, &snippet, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if snippet == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`.desc`, &snippet, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if snippet == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`p`, &snippet, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if link != "" {
			if !strings.HasPrefix(link, "http") {
				link = "https://" + link
			}

			results = append(results, SearchResult{
				Title:   strings.TrimSpace(title),
				URL:     link,
				Snippet: strings.TrimSpace(snippet),
				Engine:  b.Name(),
			})
		}
	}

	return results, nil
}
