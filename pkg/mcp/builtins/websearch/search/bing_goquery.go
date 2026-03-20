package search

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type bingGoQueryEngine struct {
	client *http.Client
}

func NewBingGoQueryEngine() SearchEngine {
	return &bingGoQueryEngine{
		client: &http.Client{
			Timeout: 10 * time.Second,
			// Set user agent to avoid blocking
			Transport: &http.Transport{},
		},
	}
}

func (b *bingGoQueryEngine) Name() string {
	return "bing"
}

func (b *bingGoQueryEngine) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	searchURL := fmt.Sprintf("https://www.bing.com/search?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	// Set headers to appear more like a real browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Bing results: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	var results []SearchResult

	// Try multiple selectors for Bing results
	doc.Find(".b_algo, li.b_algo").Each(func(i int, s *goquery.Selection) {
		if i >= maxResults {
			return
		}

		// Extract title and link
		titleElem := s.Find("h2 a").First()
		if titleElem.Length() == 0 {
			titleElem = s.Find("a").First()
		}

		title := strings.TrimSpace(titleElem.Text())
		link, _ := titleElem.Attr("href")

		// Extract snippet
		snippet := strings.TrimSpace(s.Find(".b_caption p").Text())
		if snippet == "" {
			snippet = strings.TrimSpace(s.Find(".b_caption").Text())
		}
		if snippet == "" {
			snippet = strings.TrimSpace(s.Find("p").First().Text())
		}

		if link != "" && title != "" {
			// Clean up Bing redirect URLs if needed
			if strings.Contains(link, "bing.com/ck/a") {
				// For now, keep the redirect URL
				// In production, you might want to follow the redirect
			}

			results = append(results, SearchResult{
				Title:   title,
				URL:     link,
				Snippet: snippet,
				Engine:  b.Name(),
			})
		}
	})

	// If no results found with .b_algo, try other selectors
	if len(results) == 0 {
		doc.Find("#b_results h2").Each(func(i int, s *goquery.Selection) {
			if i >= maxResults {
				return
			}

			linkElem := s.Find("a").First()
			title := strings.TrimSpace(linkElem.Text())
			link, _ := linkElem.Attr("href")

			if link != "" && title != "" {
				results = append(results, SearchResult{
					Title:   title,
					URL:     link,
					Snippet: "",
					Engine:  b.Name(),
				})
			}
		})
	}

	return results, nil
}
