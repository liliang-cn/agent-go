// Command cortex-data-import shows how to expose CortexDB's data-import engine
// (importflow) to an AgentGo agent as tools, so the LLM can plan and run an
// import of tabular data (CSV / SQL dump) into CortexDB's RAG + knowledge-graph
// stores — with the schema→mapping plan inferred by the agent's own LLM.
//
// The agent gets two tools:
//
//	importflow_plan — introspect the data + propose a MappingPlan (RAG/KG) for review
//	importflow_run  — execute a plan, writing chunks + entities into CortexDB
//
// For LIVE databases (Postgres/MySQL) with PII desensitization, use the
// cortexbridge/connectorbridge sub-package instead (connector_introspect/plan/
// run/unmask). It is split out because it pulls heavy SQL-driver dependencies.
//
// Usage:
//
//	DASHSCOPE_API_KEY=sk-...  go run ./examples/cortex-data-import
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/cortexbridge"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	key := envOr("LLM_KEY", os.Getenv("DASHSCOPE_API_KEY"))
	if key == "" {
		log.Fatal("need LLM_KEY (or DASHSCOPE_API_KEY)")
	}
	brain, err := pool.NewPool(pool.PoolConfig{
		Enabled: true, Strategy: pool.StrategyRoundRobin,
		Providers: []pool.Provider{{
			Name:    "brain",
			BaseURL: envOr("LLM_BASE", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
			Key:     key, ModelName: envOr("LLM_MODEL", "qwen-plus"), MaxConcurrency: 5, Capability: 8,
		}},
	})
	if err != nil {
		log.Fatalf("brain: %v", err)
	}

	// Open a CortexDB handle (its own pure-Go SQLite file).
	dbPath := filepath.Join(os.TempDir(), "cortex-data-import.db")
	cortex, err := cortexdb.Open(cortexdb.DefaultConfig(dbPath))
	if err != nil {
		log.Fatalf("open cortexdb: %v", err)
	}
	defer cortex.Close()

	svc, err := agent.New("data-importer").WithLLM(brain).Build()
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	defer svc.Close()

	// Build an Importer whose schema-mapping inferer is the agent's own LLM,
	// then expose its tools (importflow_plan / importflow_run) on the agent.
	im := cortexbridge.NewImporter(cortex, svc.LLM)
	tools, err := cortexbridge.RegisterImportFlow(svc, im)
	if err != nil {
		log.Fatalf("register importflow: %v", err)
	}
	fmt.Printf("registered import tools: %v\n", tools)

	// Also expose the GraphRAG/query tools so the agent can read back what it
	// imported (search_text, knowledge_graph_query, ...).
	if _, err := cortexbridge.Register(svc, cortex); err != nil {
		log.Fatalf("register graphrag: %v", err)
	}

	ctx := context.Background()
	res, err := svc.Run(ctx, "Plan an import of any CSV the user provides into RAG and the knowledge "+
		"graph, then describe the proposed mapping. Use importflow_plan.")
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	fmt.Printf("\n%v\n", res.FinalResult)
}
