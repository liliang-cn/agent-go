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

type duckDuckGoGoQueryEngine struct {
	client *http.Client
}

func NewDuckDuckGoGoQueryEngine() SearchEngine {
	return &duckDuckGoGoQueryEngine{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (d *duckDuckGoGoQueryEngine) Name() string {
	return "duckduckgo"
}

func (d *duckDuckGoGoQueryEngine) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	// DuckDuckGo Lite version (GET request with Lynx UA)
	// Using Lite version with Lynx UA avoids most CAPTCHA/bot detection issues
	searchURL := fmt.Sprintf("https://duckduckgo.com/lite/?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	// Use Lynx User-Agent to ensure we get the lightweight HTML version
	req.Header.Set("User-Agent", "Lynx/2.8.9rel.1 libwww-FM/2.14 SSL-MM/1.4.1 OpenSSL/1.1.1d")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch DuckDuckGo results: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	var results []SearchResult

	// Lite version uses tables for layout. Result links have class "result-link"
	doc.Find("a.result-link").Each(func(i int, s *goquery.Selection) {
		if len(results) >= maxResults {
			return
		}

		title := strings.TrimSpace(s.Text())
		link, _ := s.Attr("href")

		// Snippet is usually in the next row's cell with class .result-snippet
		snippet := ""

		tr := s.ParentsFiltered("tr").First()
		if tr.Length() > 0 {
			snippetTr := tr.Next()
			if snippetTr.Length() > 0 {
				snippetElem := snippetTr.Find(".result-snippet")
				if snippetElem.Length() > 0 {
					snippet = strings.TrimSpace(snippetElem.Text())
				}
			}
		}

		if link != "" && title != "" {
			// Clean up DuckDuckGo redirect URLs
			if strings.Contains(link, "duckduckgo.com/l/") {
				if u, err := url.Parse(link); err == nil {
					if actualURL := u.Query().Get("uddg"); actualURL != "" {
						if decoded, err := url.QueryUnescape(actualURL); err == nil {
							link = decoded
						}
					}
				}
			}

			// Ensure proper URL format
			if strings.HasPrefix(link, "//") {
				link = "https:" + link
			} else if !strings.HasPrefix(link, "http") {
				if !strings.Contains(link, "duckduckgo.com") {
					link = "https://" + link
				}
			}

			results = append(results, SearchResult{
				Title:   title,
				URL:     link,
				Snippet: snippet,
				Engine:  d.Name(),
			})
		}
	})

	return results, nil
}
