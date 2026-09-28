// Package main shows how to declare that an LLM provider has its own web
// search, when building an agent over it.
//
// Undeclared, a provider is probed: each tool round sends both the OpenAI and
// the DashScope search field, and the MCP search tools stay until a response
// proves the provider searched. A declaration skips that: the verdict is known
// from the first request, only the named field is sent, and the MCP search
// tools are hidden (or, for "none", kept and no field is ever sent).
//
// The same declaration in config form, for providers in agentgo.toml:
//
//	[[llm.providers]]
//	name = "cpa"
//	base_url = "https://cpa.superleo.app/v1"
//	model_name = "gemini-3.8-flash-high"
//	native_web_search = "google_search"   # none | openai | dashscope | google_search
//
//	[llm.providers.native_web_search_options]   # sent inside the declared field
//	forced_search = true                        # e.g. DashScope's search_options
//
// A declaration keeps the MCP search tools available: it says the field
// works, not that the model will search with it. Only a response that shows
// the model searching (grounding / url_citation) takes them away.
//
// and for providers stored in agentgo.db, the native_web_search field of
// store.LLMProvider (poolsvc.Global().SaveProvider).
//
// Usage:
//
//	LLM_BASE_URL=$ALIYUN_MAAS_BASE_URL LLM_API_KEY=$ALIYUN_MAAS_API_KEY \
//	LLM_MODEL=qwen3.8-flash NATIVE_WEB_SEARCH=dashscope \
//	NATIVE_WEB_SEARCH_OPTIONS='{"forced_search":true}' \
//	go run ./examples/native-web-search
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	baseURL, key, model := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
	if baseURL == "" || model == "" {
		log.Fatal("set LLM_BASE_URL, LLM_API_KEY and LLM_MODEL")
	}

	llm, err := pool.NewClient("search-provider", baseURL, key, model)
	if err != nil {
		log.Fatal(err)
	}
	// The declaration. An unknown value is an error, not a silent fallback.
	// NATIVE_WEB_SEARCH_OPTIONS is JSON sent inside the declared field, e.g.
	// '{"forced_search":true}' for DashScope.
	var searchOptions map[string]interface{}
	if raw := os.Getenv("NATIVE_WEB_SEARCH_OPTIONS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &searchOptions); err != nil {
			log.Fatalf("NATIVE_WEB_SEARCH_OPTIONS: %v", err)
		}
	}
	if err := llm.SetNativeWebSearch(domain.NativeWebSearchFormat(os.Getenv("NATIVE_WEB_SEARCH")), searchOptions); err != nil {
		log.Fatal(err)
	}
	supported, known := llm.NativeWebSearchVerdict()
	fmt.Printf("declared: supported=%v known=%v\n", supported, known)

	svc, err := agent.New("researcher").WithLLM(llm).Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	res, err := svc.Run(ctx, "What happened in tech news today? Cite your sources.")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Text())
}
