package pool

import (
	"errors"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestPoolRequestEnableSearch(t *testing.T) {
	// native web search -> both web_search_options and enable_search present
	req := buildPoolGenerateWithToolsRequest("m", nil, nil, &domain.GenerationOptions{WebSearchMode: domain.WebSearchModeAuto})
	if req["enable_search"] != true {
		t.Fatalf("expected enable_search=true, got %v", req["enable_search"])
	}
	if _, ok := req["web_search_options"]; !ok {
		t.Fatal("expected web_search_options present")
	}
	// no web search -> neither present
	req2 := buildPoolGenerateWithToolsRequest("m", nil, nil, &domain.GenerationOptions{})
	if _, ok := req2["enable_search"]; ok {
		t.Fatal("did not expect enable_search without web search mode")
	}
	// rejection on enable_search triggers fallback detection
	if !shouldRetryPoolWithoutNativeWebSearch(&domain.GenerationOptions{WebSearchMode: domain.WebSearchModeAuto}, errors.New("unsupported field enable_search")) {
		t.Fatal("expected retry-without-web-search on enable_search error")
	}
}
