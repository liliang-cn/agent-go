package mcp

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/mcp/builtins/websearch/extraction"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp/builtins/websearch/search"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Server struct {
	mcpServer *server.MCPServer
	searcher  search.MultiEngineSearcher
}

func NewServer() (*Server, error) {
	mcpServer := server.NewMCPServer(
		"mcp-websearch-server",
		"1.0.0",
	)

	s := &Server{
		mcpServer: mcpServer,
		searcher:  search.NewHybridSearcher(),
	}

	if err := s.registerTools(); err != nil {
		return nil, fmt.Errorf("failed to register tools: %w", err)
	}

	return s, nil
}

// GetMCPServer returns the underlying MCPServer for use with mcp-go
func (s *Server) GetMCPServer() *server.MCPServer {
	return s.mcpServer
}

func (s *Server) registerTools() error {
	mcpServer := s.mcpServer

	// websearch_basic
	mcpServer.AddTool(mcp.Tool{
		Name:        "websearch_basic",
		Description: "Basic web search returning titles, URLs and snippets from a single search engine",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "the search query to execute",
				},
				"max_results": map[string]any{
					"type":        "number",
					"description": "maximum number of results to return",
				},
			},
			Required: []string{"query"},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := request.GetString("query", "")
		maxResults := request.GetInt("max_results", 10)

		results, err := s.searcher.Search(ctx, query, search.SearchOptions{MaxResults: maxResults})
		if err != nil {
			return nil, err
		}
		var content string
		for i, result := range results {
			content += fmt.Sprintf("### Result %d\n**Title:** %s\n**URL:** %s\n**Snippet:** %s\n\n", i+1, result.Title, result.URL, result.Snippet)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Type: "text", Text: content}},
		}, nil
	})

	// websearch_with_content
	mcpServer.AddTool(mcp.Tool{
		Name:        "websearch_with_content",
		Description: "Web search with intelligent content extraction from result pages",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "the search query to execute",
				},
				"max_results": map[string]any{
					"type":        "number",
					"description": "maximum number of results to return",
				},
				"extract_content": map[string]any{
					"type":        "boolean",
					"description": "whether to extract full page content",
				},
			},
			Required: []string{"query"},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := request.GetString("query", "")
		maxResults := request.GetInt("max_results", 5)

		results, err := s.searcher.Search(ctx, query, search.SearchOptions{MaxResults: maxResults, ExtractContent: true})
		if err != nil {
			return nil, err
		}
		var content string
		for i, result := range results {
			content += fmt.Sprintf("### Result %d\n**Title:** %s\n**URL:** %s\n", i+1, result.Title, result.URL)
			if result.Content != "" {
				ext := result.Content
				if len(ext) > 1500 {
					ext = ext[:1500] + "..."
				}
				content += fmt.Sprintf("\n**Content:**\n%s\n", ext)
			}
			content += "\n---\n\n"
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Type: "text", Text: content}},
		}, nil
	})

	// websearch_multi_engine
	mcpServer.AddTool(mcp.Tool{
		Name:        "websearch_multi_engine",
		Description: "Comprehensive search across multiple engines with content extraction",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "the search query to execute",
				},
				"max_results": map[string]any{
					"type":        "number",
					"description": "maximum number of results to return",
				},
				"engines": map[string]any{
					"type":        "array",
					"description": "search engines to use",
					"items":       map[string]any{"type": "string"},
				},
			},
			Required: []string{"query"},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := request.GetString("query", "")
		maxResults := request.GetInt("max_results", 10)
		engines := request.GetStringSlice("engines", nil)

		results, err := s.searcher.DeepSearch(ctx, query, search.SearchOptions{MaxResults: maxResults, Engines: engines, ExtractContent: true})
		if err != nil {
			return nil, err
		}
		var content string
		for i, result := range results {
			content += fmt.Sprintf("### Result %d\n**Title:** %s\n**URL:** %s\n", i+1, result.Title, result.URL)
			if result.Content != "" {
				ext := result.Content
				if len(ext) > 1500 {
					ext = ext[:1500] + "..."
				}
				content += fmt.Sprintf("\n**Content:**\n%s\n", ext)
			}
			content += "\n---\n\n"
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Type: "text", Text: content}},
		}, nil
	})

	// websearch_ai_summary
	mcpServer.AddTool(mcp.Tool{
		Name:        "websearch_ai_summary",
		Description: "Search and return AI-ready aggregated content optimized for analysis and summarization",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "the search query to execute",
				},
				"max_results": map[string]any{
					"type":        "number",
					"description": "maximum number of results to return",
				},
			},
			Required: []string{"query"},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := request.GetString("query", "")
		maxResults := request.GetInt("max_results", 5)

		if hs, ok := s.searcher.(*search.HybridMultiEngineSearcher); ok {
			aggregated, err := hs.SearchAndAggregate(ctx, query, maxResults)
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{mcp.TextContent{Type: "text", Text: aggregated}},
			}, nil
		}
		return nil, fmt.Errorf("aggregation not supported")
	})

	// fetch_page_content
	mcpServer.AddTool(mcp.Tool{
		Name:        "fetch_page_content",
		Description: "Directly fetch and extract the main content from a specific URL using Readability and Markdown conversion",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "the URL of the page to fetch content from",
				},
			},
			Required: []string{"url"},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url := request.GetString("url", "")
		if url == "" {
			return nil, fmt.Errorf("URL is required")
		}
		content, err := extraction.NewHybridExtractor().ExtractContent(ctx, url)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Type: "text", Text: content}},
		}, nil
	})

	// take_screenshot
	mcpServer.AddTool(mcp.Tool{
		Name:        "take_screenshot",
		Description: "Capture a screenshot of a webpage",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "the URL of the page to screenshot",
				},
				"full_page": map[string]any{
					"type":        "boolean",
					"description": "whether to take a full page screenshot",
				},
			},
			Required: []string{"url"},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url := request.GetString("url", "")
		fullPage := request.GetBool("full_page", false)
		if url == "" {
			return nil, fmt.Errorf("URL is required")
		}
		imgData, err := extraction.NewChromedpExtractor().CaptureScreenshot(ctx, url, fullPage)
		if err != nil {
			return nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.ImageContent{
					Type:     "image",
					Data:     base64.StdEncoding.EncodeToString(imgData),
					MIMEType: "image/png",
				},
				mcp.TextContent{
					Type: "text",
					Text: fmt.Sprintf("Successfully captured screenshot of %s (%d bytes).", url, len(imgData)),
				},
			},
		}, nil
	})

	// deep_read_page
	mcpServer.AddTool(mcp.Tool{
		Name:        "deep_read_page",
		Description: "Deep read a webpage by extracting main content and intelligently crawling related sub-pages. Returns structured markdown with main content and linked page summaries. Useful for comprehensive page analysis.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "the URL of the page to deep read",
				},
				"max_links": map[string]any{
					"type":        "number",
					"description": "maximum number of sub-pages to crawl (default 10, max 20)",
				},
				"cross_domain": map[string]any{
					"type":        "boolean",
					"description": "allow crawling cross-domain links (default false, same-domain only)",
				},
			},
			Required: []string{"url"},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url := request.GetString("url", "")
		if url == "" {
			return nil, fmt.Errorf("URL is required")
		}

		maxLinks := request.GetInt("max_links", 10)
		crossDomain := request.GetBool("cross_domain", false)

		var opts []extraction.DeepReaderOption
		if maxLinks > 0 {
			opts = append(opts, extraction.WithMaxLinks(maxLinks))
		}
		if crossDomain {
			opts = append(opts, extraction.WithSameDomain(false))
		}

		reader := extraction.NewDeepReader(opts...)
		result, err := reader.DeepRead(ctx, url)
		if err != nil {
			return nil, err
		}

		markdown := result.ToMarkdown()
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Type: "text", Text: markdown}},
		}, nil
	})

	return nil
}
