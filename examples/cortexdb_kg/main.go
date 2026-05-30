// Command cortexdb_kg shows how to use CortexDB (pure-Go knowledge graph /
// RAG / memory) from inside an AgentGo agent.
//
// Architecture note: AgentGo uses a CGO SQLite driver and its own agentgo.db;
// CortexDB uses pure-Go modernc.org/sqlite and its own cortex.db. They are
// independent files and do not conflict. We open CortexDB ourselves and expose
// its toolbox to the agent via RegisterCortexDB (see bridge.go).
//
// It demonstrates two integration styles:
//
//	A) expose CortexDB's toolbox via pkg/cortexbridge so the LLM drives the graph
//	B) wrap CortexDB behind one semantic tool ("recall_about") you control
//
// Run (needs an LLM configured in AGENTGO_HOME / agentgo.toml):
//
//	go run ./examples/cortexdb_kg
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/cortexbridge"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/graph"
)

const ex = "https://example.com/"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 1. Open CortexDB (its own pure-Go SQLite file).
	dbPath := "cortex_kg_demo.db"
	_ = os.Remove(dbPath)
	defer func() { _ = os.Remove(dbPath) }()

	cortex, err := cortexdb.Open(cortexdb.DefaultConfig(dbPath))
	if err != nil {
		log.Fatalf("open cortexdb: %v", err)
	}
	defer func() { _ = cortex.Close() }()

	// Register a readable prefix for nicer exports/queries.
	if _, err := cortex.UpsertKnowledgeNamespace(ctx, cortexdb.KnowledgeGraphNamespaceUpsertRequest{
		Prefix: "ex", URI: ex,
	}); err != nil {
		log.Fatalf("namespace: %v", err)
	}

	// 2. Build the agent.
	svc, err := agent.New("kg-assistant").
		WithPrompt("You manage a knowledge graph. To remember facts call " +
			"knowledge_graph_upsert; to answer questions call knowledge_graph_query " +
			"with SPARQL or recall_about with an entity local name. Prefix is " +
			"ex: <https://example.com/>.").
		Build()
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	defer svc.Close()

	// 3a. Integration style A: expose a focused slice of CortexDB's toolbox
	//     via the reusable pkg/cortexbridge package.
	registered, err := cortexbridge.Register(svc, cortex, cortexbridge.WithAllow(
		"knowledge_graph_upsert",
		"knowledge_graph_query",
		"knowledge_graph_find",
		"knowledge_graph_shacl_validate",
	))
	if err != nil {
		log.Fatalf("register cortexdb tools: %v", err)
	}
	fmt.Printf("registered CortexDB tools: %v\n", registered)

	// 3b. Integration style B: one semantic, read-only tool you fully control.
	svc.AddToolWithMetadata(
		"recall_about",
		"Recall every fact stored about an entity (pass its local name, e.g. 'alice').",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"entity": map[string]interface{}{
					"type":        "string",
					"description": "entity local name under the ex: namespace",
				},
			},
			"required": []string{"entity"},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			entity, _ := args["entity"].(string)
			return cortex.QueryKnowledgeGraph(ctx, cortexdb.KnowledgeGraphQueryRequest{
				Query: fmt.Sprintf(
					`PREFIX ex: <%s> SELECT ?p ?o WHERE { ex:%s ?p ?o . }`, ex, entity),
			})
		},
		agent.ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: agent.InterruptBehaviorCancel},
	)

	// 4. Seed a couple of facts directly (so the demo works even if the model
	//    chooses not to call the upsert tool first).
	seedFacts(ctx, cortex)

	// 5. Let the agent answer using the tools.
	fmt.Println("=== Ask the KG-backed agent ===")
	reply, err := svc.Ask(ctx, "Who owns the Apollo project, and what is alice's role? "+
		"Use recall_about / knowledge_graph_query to check before answering.")
	if err != nil {
		log.Fatalf("ask: %v", err)
	}
	fmt.Println(reply)
}

func seedFacts(ctx context.Context, cortex *cortexdb.DB) {
	iri := func(local string) graph.RDFTerm { return graph.NewIRI(ex + local) }
	if _, err := cortex.UpsertKnowledgeGraph(ctx, cortexdb.KnowledgeGraphUpsertRequest{
		Triples: []cortexdb.KnowledgeGraphTriple{
			{Subject: iri("alice"), Predicate: graph.NewIRI(ex + "role"), Object: graph.NewLiteral("Engineering Manager")},
			{Subject: iri("alice"), Predicate: graph.NewIRI(ex + "owns"), Object: iri("Apollo")},
			{Subject: iri("Apollo"), Predicate: graph.NewIRI(ex + "ships"), Object: graph.NewLiteral("Friday")},
		},
	}); err != nil {
		log.Fatalf("seed facts: %v", err)
	}
}
