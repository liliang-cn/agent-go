package extraction

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/chromedp/chromedp"
	"github.com/go-shiori/go-readability"
)

// HybridExtractor uses chromedp for rendering and go-readability for content extraction
type HybridExtractor struct {
	timeout time.Duration
}

func NewHybridExtractor() *HybridExtractor {
	return &HybridExtractor{
		timeout: 30 * time.Second,
	}
}

// ExtractContent extracts the main content from a webpage using Readability and Markdown conversion
func (e *HybridExtractor) ExtractContent(ctx context.Context, targetURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	allocCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()

	var htmlContent string
	var pageTitle string

	// 1. Fetch rendered HTML via chromedp
	err := chromedp.Run(allocCtx,
		chromedp.Navigate(targetURL),
		chromedp.WaitReady("body"),
		chromedp.Title(&pageTitle),
		chromedp.OuterHTML("html", &htmlContent),
	)

	if err != nil {
		return "", fmt.Errorf("failed to fetch rendered HTML from %s: %w", targetURL, err)
	}

	// 2. Use Readability to extract main content
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL %s: %w", targetURL, err)
	}

	article, err := readability.FromReader(strings.NewReader(htmlContent), parsedURL)
	if err != nil {
		// Fallback to title only if readability fails
		if pageTitle != "" {
			return fmt.Sprintf("# %s\n\n(Readability failed to extract main content)", pageTitle), nil
		}
		return "", fmt.Errorf("failed to parse content with readability: %w", err)
	}

	// 3. Convert Article HTML to Markdown
	markdown, err := htmltomarkdown.ConvertString(article.Content)
	if err != nil {
		// Fallback to text if markdown conversion fails
		return fmt.Sprintf("# %s\n\n%s", article.Title, article.TextContent), nil
	}

	// Clean up the markdown
	finalMarkdown := CleanText(markdown)

	// Combine Title and Markdown
	var result strings.Builder
	if article.Title != "" {
		result.WriteString(fmt.Sprintf("# %s\n\n", article.Title))
	} else if pageTitle != "" {
		result.WriteString(fmt.Sprintf("# %s\n\n", pageTitle))
	}

	result.WriteString(finalMarkdown)

	return result.String(), nil
}

// ExtractSummary extracts a summary-friendly version of the content
func (e *HybridExtractor) ExtractSummary(ctx context.Context, url string, maxLength int) (string, error) {
	content, err := e.ExtractContent(ctx, url)
	if err != nil {
		return "", err
	}

	// Truncate if necessary
	if len(content) > maxLength {
		truncated := content[:maxLength]
		lastPeriod := strings.LastIndex(truncated, ". ")
		if lastPeriod > maxLength/2 {
			content = truncated[:lastPeriod+1]
		} else {
			content = truncated + "..."
		}
	}

	return content, nil
}

// ExtractMultiple extracts content from multiple URLs concurrently
func (e *HybridExtractor) ExtractMultiple(ctx context.Context, urls []string) map[string]string {
	results := make(map[string]string)

	// For simplicity and to avoid browser instance explosion, we'll do this sequentially
	// or with a very small concurrency limit in real use.
	// Here we reuse the shared browser logic if needed, but for now we'll call ExtractContent.

	for _, targetURL := range urls {
		content, err := e.ExtractContent(ctx, targetURL)
		if err != nil {
			results[targetURL] = fmt.Sprintf("Error: %v", err)
		} else {
			results[targetURL] = content
		}
	}

	return results
}
