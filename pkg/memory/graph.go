package memory

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
)

// Write-time graph extraction.
//
// Memories went into the store as sentences and nothing else: a simulated
// week of ordinary conversation — a person, his wife and daughter, a piano
// lesson, a peanut allergy, four holdings, a hospital visit — left 43
// memories and a graph with no nodes at all. Every graph tool CortexDB has
// (relations, paths, neighbours, ontology, rules, the graph half of recall)
// had nothing to work on, because nothing ever wrote to it. Recall already
// folds graph facts into its answer when the nodes exist.
//
// The extraction call that writes the memories now also says what each one
// is about — the entities it names and the relations it asserts — in the
// same answer, as pkg/timeaware and reconciliation do: no second model call.
// Nothing here reads the user's words; the model names the entities, and the
// store records them under the memory's source id so a memory that is
// replaced can take its edges with it.

// graphPromptRules tell the extraction call how to fill the graph fields.
func graphPromptRules() string {
	return `

For each extracted item also fill "graph_entities" and "graph_relations" with what the item is about:
- graph_entities: every concrete thing the item names — people, organisations, places, products, holdings, events, projects. "name" is the name it is known by (a person's own name, not "the user" or "my daughter", whenever the name is known from this conversation or the existing memories); keep one spelling for one thing across items. "type" is one word: person, organization, place, product, asset, event, project, concept or other.
- graph_relations: the relations the item states between those entities, as {"from", "type", "to"} using names from graph_entities. "type" is a short lowercase snake_case English verb phrase read from "from" to "to": spouse_of, daughter_of, father_of, works_at, allergic_to, holds, attends, scheduled_on, located_in, part_of.
Use empty arrays when the item names nothing concrete. Never invent an entity or relation the item does not state, and never use a memory id as an entity.`
}

// graphSchemaFields are grafted onto the extraction schema's item.
func graphSchemaFields() map[string]interface{} {
	return map[string]interface{}{
		"graph_entities": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{"type": "string"},
					"type": map[string]interface{}{"type": "string"},
				},
				"required": []string{"name", "type"},
			},
		},
		"graph_relations": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"from": map[string]interface{}{"type": "string"},
					"type": map[string]interface{}{"type": "string"},
					"to":   map[string]interface{}{"type": "string"},
				},
				"required": []string{"from", "type", "to"},
			},
		},
	}
}

// graphRequiredFields are required for the reason timeaware.RequiredFields
// gives: a model working through a long extraction prompt skips optional
// fields, and a skipped field reads exactly like "names nothing".
func graphRequiredFields() []string { return []string{"graph_entities", "graph_relations"} }

// graphsFromSummary reads each extracted item's graph out of the raw answer,
// index-aligned with the items. An answer without the fields gives empty
// graphs, not an error: the memories are still worth storing.
func graphsFromSummary(raw string) []domain.MemoryGraph {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var parsed struct {
		Memories []struct {
			Entities  []domain.MemoryGraphEntity   `json:"graph_entities"`
			Relations []domain.MemoryGraphRelation `json:"graph_relations"`
		} `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	out := make([]domain.MemoryGraph, len(parsed.Memories))
	for i, m := range parsed.Memories {
		out[i] = withoutIDNames(domain.MemoryGraph{Entities: m.Entities, Relations: m.Relations})
	}
	return out
}

// idLikeName matches what an entity name must not be: a memory id, or the
// front of one. The extraction prompt lists existing memories by id, and a
// model once returned one as an entity ("9f1d9d46"), which became a node
// nobody could name.
var idLikeName = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F-]+)?$`)

func withoutIDNames(g domain.MemoryGraph) domain.MemoryGraph {
	ok := func(n string) bool { return !idLikeName.MatchString(strings.TrimSpace(n)) }
	var out domain.MemoryGraph
	for _, e := range g.Entities {
		if ok(e.Name) {
			out.Entities = append(out.Entities, e)
		}
	}
	for _, r := range g.Relations {
		if ok(r.From) && ok(r.To) {
			out.Relations = append(out.Relations, r)
		}
	}
	return out
}

// writeMemoryGraph records a stored memory's graph when the store keeps one.
// A failure is logged, not returned: the memory itself is already stored.
func (s *Service) writeMemoryGraph(ctx context.Context, memoryID string, g domain.MemoryGraph) {
	if s == nil || g.Empty() {
		return
	}
	w, ok := s.store.(domain.MemoryGraphWriter)
	if !ok {
		return
	}
	if err := w.WriteMemoryGraph(ctx, memoryID, g); err != nil {
		agentgolog.WithModule("memory.autostore").Warn("memory stored, its graph was not",
			"memory_id", memoryID, "entities", len(g.Entities), "relations", len(g.Relations), "error", err)
	}
}

// dropMemoryGraph removes what a replaced memory put in the graph.
func (s *Service) dropMemoryGraph(ctx context.Context, memoryID string) {
	if s == nil {
		return
	}
	w, ok := s.store.(domain.MemoryGraphWriter)
	if !ok {
		return
	}
	if err := w.DropMemoryGraph(ctx, memoryID); err != nil {
		agentgolog.WithModule("memory.autostore").Warn("memory superseded, its graph edges stay",
			"memory_id", memoryID, "error", err)
	}
}
