package search

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

type bingSearchEngine struct {
	client *http.Client
}

func NewBingSearchEngine() SearchEngine {
	return &bingSearchEngine{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (b *bingSearchEngine) Name() string {
	return "bing"
}

func (b *bingSearchEngine) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	searchURL := fmt.Sprintf("https://www.bing.com/search?q=%s", url.QueryEscape(query))

	allocCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()

	var results []SearchResult

	// Navigate and wait for results
	err := chromedp.Run(allocCtx,
		chromedp.Navigate(searchURL),
		chromedp.Sleep(3*time.Second), // Let page fully load
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search Bing: %w", err)
	}

	// Use JavaScript to extract search results directly
	var jsResults []map[string]string
	err = chromedp.Run(allocCtx,
		chromedp.Evaluate(`
			(() => {
				const results = [];
				const items = document.querySelectorAll('.b_algo, li.b_algo, #b_results > li');
				
				for (let i = 0; i < Math.min(items.length, `+fmt.Sprintf("%d", maxResults)+`); i++) {
					const item = items[i];
					const titleElem = item.querySelector('h2 a, h2, a');
					const linkElem = item.querySelector('h2 a, a[href]');
					const snippetElem = item.querySelector('.b_caption p, .b_caption, p');
					
					if (linkElem && linkElem.href) {
						results.push({
							title: titleElem ? titleElem.innerText : '',
							url: linkElem.href,
							snippet: snippetElem ? snippetElem.innerText : ''
						});
					}
				}
				return results;
			})()
		`, &jsResults),
	)

	if err == nil && len(jsResults) > 0 {
		// Successfully got results via JavaScript
		for _, jsResult := range jsResults {
			title := jsResult["title"]
			link := jsResult["url"]
			snippet := jsResult["snippet"]

			if link != "" {
				results = append(results, SearchResult{
					Title:   strings.TrimSpace(title),
					URL:     link,
					Snippet: strings.TrimSpace(snippet),
					Engine:  b.Name(),
				})
			}
		}
	} else {
		// Fallback approach: try to get any text from the page
		var pageText string
		chromedp.Run(allocCtx,
			chromedp.Evaluate(`
				(() => {
					const results = [];
					// Try to find any links in the results area
					const links = document.querySelectorAll('#b_results a[href*="http"]');
					for (let i = 0; i < Math.min(links.length, `+fmt.Sprintf("%d", maxResults)+`); i++) {
						if (!links[i].href.includes('bing.com') && 
							!links[i].href.includes('microsoft.com')) {
							results.push({
								title: links[i].innerText || 'Result ' + (i+1),
								url: links[i].href,
								snippet: ''
							});
						}
					}
					return results;
				})()
			`, &pageText),
		)
	}

	return results, nil
}
