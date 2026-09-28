// Package main wires the pieces a real host uses together — a model,
// memory, a declared built-in web search, a CortexDB knowledge graph and a
// durable plan — in the order that works, and shows each one doing its job.
//
// Every line here is something a host got wrong at least once:
//
//   - AGENTGO_HOME points somewhere of our own, so an example never writes
//     into ~/.agentgo.
//   - Memory is agent-go's (WithMemory). cortexbridge.Register then leaves
//     CortexDB's own memory_* tools out, so the model has one memory_save,
//     the one that reconciles and writes to the configured backend.
//   - A provider's built-in web search is declared, not assumed; declared is
//     not proven, so any MCP search tools stay as a fallback.
//   - Plans persist without configuration: Build puts them in the service's
//     own database, next to the checkpoints.
//
// Usage (any OpenAI-compatible endpoint):
//
//	LLM_BASE_URL=https://api.deepseek.com/v1 LLM_API_KEY=$DEEPSEEK_API_KEY \
//	LLM_MODEL=deepseek-v4-flash go run ./examples/integration
//
// With a provider that searches (optional):
//
//	NATIVE_WEB_SEARCH=dashscope NATIVE_WEB_SEARCH_OPTIONS='{"forced_search":true}'
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/cortexbridge"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	baseURL, key, model := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
	if baseURL == "" || model == "" {
		log.Fatal("set LLM_BASE_URL, LLM_API_KEY and LLM_MODEL")
	}

	// 1. A home of our own. Everything agent-go persists — agentgo.db with
	//    sessions, checkpoints and plans, and the file memory — lives here.
	home, err := os.MkdirTemp("", "agentgo-integration-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(home)
	os.Setenv("AGENTGO_HOME", home)

	// 2. The model, with its built-in search declared if it has one.
	llm, err := pool.NewClient("main", baseURL, key, model)
	if err != nil {
		log.Fatal(err)
	}
	if format := os.Getenv("NATIVE_WEB_SEARCH"); format != "" {
		var opts map[string]interface{}
		if raw := os.Getenv("NATIVE_WEB_SEARCH_OPTIONS"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &opts); err != nil {
				log.Fatalf("NATIVE_WEB_SEARCH_OPTIONS: %v", err)
			}
		}
		if err := llm.SetNativeWebSearch(domain.NativeWebSearchFormat(format), opts); err != nil {
			log.Fatal(err)
		}
	}

	// 3. The agent, with memory. "file" needs no embedder and no server.
	svc, err := agent.New("assistant").
		WithLLM(llm).
		WithMemory(agent.WithMemoryStoreType("file")).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	// 4. CortexDB for the knowledge graph. Opened by us, closed by us.
	cortex, err := cortexdb.Open(cortexdb.DefaultConfig(filepath.Join(home, "cortex.db")))
	if err != nil {
		log.Fatal(err)
	}
	defer cortex.Close()
	tools, err := cortexbridge.Register(svc, cortex)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("CortexDB tools registered: %d (memory_* left to agent-go: %v)\n\n",
		len(tools), !contains(tools, "memory_save"))

	// Each step is its own session, the way separate conversations are: what
	// carries across is memory, the graph and the plan — not the transcript.
	step := func(title, prompt string) *agent.ExecutionResult {
		fmt.Printf("== %s\n> %s\n", title, prompt)
		res, err := svc.Run(ctx, prompt, agent.WithSessionID(uuid.NewString()))
		if err != nil {
			log.Fatalf("%s: %v", title, err)
		}
		fmt.Printf("tools: %s\n%s\n\n", strings.Join(res.ToolsUsed, ", "), strings.TrimSpace(res.Text()))
		return res
	}

	step("remember", "Please remember: I live in Chengdu and my team ships the billing service.")
	step("update", "Update what you know: I moved to Beijing last month.")
	step("recall", "Which city do I live in? Answer with the city only.")
	step("graph", "Record in the knowledge graph that the billing service depends on the ledger service "+
		"and the ledger service depends on Postgres. Then, from the graph, list everything billing depends on, directly or not.")

	// The plan a run keeps lives in agentgo.db; a resumed task reads it back.
	if s := svc.PlanSummary(""); s != "" {
		fmt.Println("== plan\n" + s)
	}
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
