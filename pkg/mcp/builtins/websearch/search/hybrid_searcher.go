package search

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/mcp/builtins/websearch/extraction"
)

// HybridMultiEngineSearcher combines goquery search with chromedp extraction
type HybridMultiEngineSearcher struct {
	engines   map[string]SearchEngine
	extractor *extraction.HybridExtractor
}

// NewHybridSearcher creates a new hybrid searcher
func NewHybridSearcher() MultiEngineSearcher {
	return &HybridMultiEngineSearcher{
		engines: map[string]SearchEngine{
			"bing":       NewBingGoQueryEngine(),
			"brave":      NewBraveGoQueryEngine(),
			"duckduckgo": NewDuckDuckGoGoQueryEngine(),
		},
		extractor: extraction.NewHybridExtractor(),
	}
}

// Search performs a search and optionally extracts content
func (h *HybridMultiEngineSearcher) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	// Select and use search engine
	engine := h.selectEngine(opts.Engines)
	if engine == nil {
		return nil, fmt.Errorf("no search engine available")
	}

	// Get search results using goquery (fast)
	results, err := engine.Search(ctx, query, opts.MaxResults)
	if err != nil {
		// Try fallback engines
		results, err = h.fallbackSearch(ctx, query, opts.MaxResults, engine.Name())
		if err != nil {
			return nil, fmt.Errorf("all search engines failed: %w", err)
		}
	}

	// Extract content if requested (using chromedp)
	if opts.ExtractContent && len(results) > 0 {
		h.extractContentIntelligently(ctx, results)
	}

	return results, nil
}

// DeepSearch performs search across multiple engines with content extraction
func (h *HybridMultiEngineSearcher) DeepSearch(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var allResults []SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	engines := h.getEngines(opts.Engines)
	if len(engines) == 0 {
		return nil, fmt.Errorf("no search engines available")
	}

	resultsPerEngine := opts.MaxResults / len(engines)
	if resultsPerEngine < 1 {
		resultsPerEngine = 1
	}

	// Search with all engines concurrently
	for _, engine := range engines {
		wg.Add(1)
		go func(eng SearchEngine) {
			defer wg.Done()

			results, err := eng.Search(ctx, query, resultsPerEngine)
			if err != nil {
				fmt.Printf("Engine %s failed: %v\n", eng.Name(), err)
				return
			}

			mu.Lock()
			allResults = append(allResults, results...)
			mu.Unlock()
		}(engine)
	}

	wg.Wait()

	if len(allResults) == 0 {
		return nil, fmt.Errorf("no results from any search engine")
	}

	// Always extract content for deep search
	h.extractContentIntelligently(ctx, allResults)

	// Limit final results
	if len(allResults) > opts.MaxResults {
		allResults = allResults[:opts.MaxResults]
	}

	return allResults, nil
}

// extractContentIntelligently uses chromedp to extract real content
func (h *HybridMultiEngineSearcher) extractContentIntelligently(ctx context.Context, results []SearchResult) {
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 2) // Limit concurrent browser instances

	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Use the hybrid extractor for better content
			content, err := h.extractor.ExtractSummary(ctx, results[idx].URL, 3000)
			if err == nil {
				results[idx].Content = content
				results[idx].ExtractedAt = time.Now()
			}
		}(i)
	}

	wg.Wait()
}

// SearchAndAggregate searches and returns aggregated content ready for summarization
func (h *HybridMultiEngineSearcher) SearchAndAggregate(ctx context.Context, query string, maxResults int) (string, error) {
	results, err := h.Search(ctx, query, SearchOptions{
		MaxResults:     maxResults,
		ExtractContent: true,
		Timeout:        45 * time.Second,
	})
	if err != nil {
		return "", err
	}

	// Aggregate all content
	var aggregated string
	aggregated += fmt.Sprintf("# Search Results for: %s\n\n", query)
	
	for i, result := range results {
		aggregated += fmt.Sprintf("## %d. %s\n", i+1, result.Title)
		aggregated += fmt.Sprintf("**Source:** %s\n", result.URL)
		aggregated += fmt.Sprintf("**Engine:** %s\n\n", result.Engine)
		
		// Always include snippet as it often contains the key fact (zero-click info)
		if result.Snippet != "" {
			aggregated += fmt.Sprintf("**Snippet:** %s\n\n", result.Snippet)
		}
		
		if result.Content != "" {
			// Limit content per result
			content := result.Content
			if len(content) > 1500 {
				content = content[:1500] + "..."
			}
			aggregated += fmt.Sprintf("**Extracted Content:**\n%s", content)
		}
		
		aggregated += "\n\n---\n\n"
	}

	return aggregated, nil
}

func (h *HybridMultiEngineSearcher) selectEngine(preferred []string) SearchEngine {
	if len(preferred) > 0 {
		for _, name := range preferred {
			if engine, ok := h.engines[name]; ok {
				return engine
			}
		}
	}

	// Default priority
	priorityOrder := []string{"duckduckgo", "bing", "brave"}
	for _, name := range priorityOrder {
		if engine, ok := h.engines[name]; ok {
			return engine
		}
	}

	return nil
}

func (h *HybridMultiEngineSearcher) fallbackSearch(ctx context.Context, query string, maxResults int, failedEngine string) ([]SearchResult, error) {
	priorityOrder := []string{"duckduckgo", "bing", "brave"}

	for _, name := range priorityOrder {
		if name == failedEngine {
			continue
		}

		if engine, ok := h.engines[name]; ok {
			results, err := engine.Search(ctx, query, maxResults)
			if err == nil {
				return results, nil
			}
		}
	}

	return nil, fmt.Errorf("all fallback engines failed")
}

func (h *HybridMultiEngineSearcher) getEngines(names []string) []SearchEngine {
	if len(names) == 0 {
		names = []string{"duckduckgo", "bing", "brave"}
	}

	var engines []SearchEngine
	for _, name := range names {
		if engine, ok := h.engines[name]; ok {
			engines = append(engines, engine)
		}
	}

	return engines
}