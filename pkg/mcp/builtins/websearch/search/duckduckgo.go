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

type duckDuckGoSearchEngine struct {
	client *http.Client
}

func NewDuckDuckGoSearchEngine() SearchEngine {
	return &duckDuckGoSearchEngine{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (d *duckDuckGoSearchEngine) Name() string {
	return "duckduckgo"
}

func (d *duckDuckGoSearchEngine) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	searchURL := fmt.Sprintf("https://duckduckgo.com/?q=%s", url.QueryEscape(query))

	allocCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()

	var results []SearchResult
	var nodes []*cdp.Node

	// Navigate and wait for page to load
	err := chromedp.Run(allocCtx,
		chromedp.Navigate(searchURL),
		chromedp.Sleep(3*time.Second), // Let page fully load
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search DuckDuckGo: %w", err)
	}

	// Try multiple selectors for DuckDuckGo results
	selectors := []string{
		`[data-testid="result"]`,
		`article[data-testid="result"]`,
		`.react-results--main .result`,
		`.results .result`,
		`#links .result`,
		`.nrn-react-div article`,
		`li[data-layout="organic"]`,
	}

	for _, selector := range selectors {
		chromedp.Run(allocCtx, chromedp.Nodes(selector, &nodes, chromedp.ByQueryAll))
		if len(nodes) > 0 {
			break
		}
	}

	// If still no nodes, try broader selectors
	if len(nodes) == 0 {
		chromedp.Run(allocCtx, chromedp.Nodes(`article`, &nodes, chromedp.ByQueryAll))
	}

	for i, node := range nodes {
		if i >= maxResults {
			break
		}

		var title, link, snippet string

		// Try various selectors for title
		chromedp.Run(allocCtx,
			chromedp.Text(`h2`, &title, chromedp.ByQuery, chromedp.FromNode(node)),
		)

		if title == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`.result__title`, &title, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if title == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`[data-testid="result-title"]`, &title, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if title == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`a`, &title, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		// Try various selectors for link
		chromedp.Run(allocCtx,
			chromedp.AttributeValue(`h2 a`, "href", &link, nil, chromedp.ByQuery, chromedp.FromNode(node)),
		)

		if link == "" {
			chromedp.Run(allocCtx,
				chromedp.AttributeValue(`.result__title a`, "href", &link, nil, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if link == "" {
			chromedp.Run(allocCtx,
				chromedp.AttributeValue(`[data-testid="result-title"]`, "href", &link, nil, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if link == "" {
			chromedp.Run(allocCtx,
				chromedp.AttributeValue(`a`, "href", &link, nil, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		// Try various selectors for snippet
		chromedp.Run(allocCtx,
			chromedp.Text(`[data-result="snippet"]`, &snippet, chromedp.ByQuery, chromedp.FromNode(node)),
		)

		if snippet == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`.result__snippet`, &snippet, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if snippet == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`span`, &snippet, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if snippet == "" {
			chromedp.Run(allocCtx,
				chromedp.Text(`p`, &snippet, chromedp.ByQuery, chromedp.FromNode(node)),
			)
		}

		if link != "" {
			// Clean up DuckDuckGo redirect URLs
			if strings.Contains(link, "duckduckgo.com/l/") {
				// Try to extract actual URL from DDG redirect
				if u, err := url.Parse(link); err == nil {
					if actualURL := u.Query().Get("uddg"); actualURL != "" {
						if decoded, err := url.QueryUnescape(actualURL); err == nil {
							link = decoded
						}
					}
				}
			}

			if strings.HasPrefix(link, "//") {
				link = "https:" + link
			} else if !strings.HasPrefix(link, "http") {
				// Skip relative links that are likely DDG internal
				if !strings.Contains(link, "duckduckgo.com") {
					link = "https://" + link
				}
			}

			results = append(results, SearchResult{
				Title:   strings.TrimSpace(title),
				URL:     link,
				Snippet: strings.TrimSpace(snippet),
				Engine:  d.Name(),
			})
		}
	}

	return results, nil
}
