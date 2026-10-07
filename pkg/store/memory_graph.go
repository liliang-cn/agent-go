package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A memory's graph goes into CortexDB through the same two tools a person or
// an agent would call — upsert_entities and upsert_relations — recorded under
// the memory's own source id. The remote store calls them over the wire, the
// embedded one in process; the arguments are built once, here.

// memoryGraphCall is one tool call: its name and JSON arguments.
type memoryGraphCall struct {
	Name string
	Args []byte
}

// memoryGraphCalls turns a memory's graph into the tool calls that record it.
// A relation's endpoints are entities too: one the extraction named only in a
// relation still gets its node, so the edge has somewhere to land.
func memoryGraphCalls(memoryID string, g domain.MemoryGraph) ([]memoryGraphCall, error) {
	source := domain.MemoryGraphSource(memoryID)
	meta := map[string]any{"memory_id": memoryID, "source": "memory"}

	types := map[string]string{}
	for _, e := range g.Entities {
		if n := strings.TrimSpace(e.Name); n != "" && strings.TrimSpace(e.Type) != "" {
			types[strings.ToLower(n)] = strings.TrimSpace(e.Type)
		}
	}
	var entities []map[string]any
	for _, n := range g.Names() {
		e := map[string]any{"name": n, "metadata": meta}
		if t := types[strings.ToLower(n)]; t != "" {
			e["type"] = t
		}
		entities = append(entities, e)
	}
	var relations []map[string]any
	for _, r := range g.Relations {
		from, to, typ := strings.TrimSpace(r.From), strings.TrimSpace(r.To), strings.TrimSpace(r.Type)
		if from == "" || to == "" || strings.EqualFold(from, to) {
			continue
		}
		rel := map[string]any{"from": from, "to": to, "provenance": "llm", "metadata": meta}
		if typ != "" {
			rel["type"] = typ
		}
		relations = append(relations, rel)
	}

	var calls []memoryGraphCall
	if len(entities) > 0 {
		args, err := json.Marshal(map[string]any{"document_id": source, "entities": entities})
		if err != nil {
			return nil, err
		}
		calls = append(calls, memoryGraphCall{Name: "upsert_entities", Args: args})
	}
	if len(relations) > 0 {
		args, err := json.Marshal(map[string]any{"document_id": source, "relations": relations})
		if err != nil {
			return nil, err
		}
		calls = append(calls, memoryGraphCall{Name: "upsert_relations", Args: args})
	}
	return calls, nil
}

// dropMemoryGraphCall removes everything a memory put in the graph: its
// edges, and the entities only it asserted. Entities other memories also
// name are detached, not deleted.
func dropMemoryGraphCall(memoryID string) (memoryGraphCall, error) {
	args, err := json.Marshal(map[string]any{"document_id": domain.MemoryGraphSource(memoryID)})
	return memoryGraphCall{Name: "delete_document_graph", Args: args}, err
}

// runMemoryGraphCalls runs calls in order through call, stopping at the
// first that fails.
func runMemoryGraphCalls(ctx context.Context, calls []memoryGraphCall, call func(context.Context, memoryGraphCall) error) error {
	for _, c := range calls {
		if err := call(ctx, c); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return nil
}
