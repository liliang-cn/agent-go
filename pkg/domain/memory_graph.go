package domain

import (
	"context"
	"strings"
)

// MemoryGraph is what one memory is about, as a graph: the things it names
// and how they relate. The extraction call that writes a memory returns it in
// the same answer, so a fact like "my daughter Keke has piano on Fridays"
// becomes the nodes Keke and piano lesson and the edges between them, and a
// later question about Keke can follow edges instead of hoping the right
// sentence ranks first.
type MemoryGraph struct {
	Entities  []MemoryGraphEntity   `json:"entities,omitempty"`
	Relations []MemoryGraphRelation `json:"relations,omitempty"`
}

// MemoryGraphEntity is one thing a memory names.
type MemoryGraphEntity struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// MemoryGraphRelation is one edge a memory asserts, between entity names.
type MemoryGraphRelation struct {
	From string `json:"from"`
	Type string `json:"type"`
	To   string `json:"to"`
}

// Empty reports whether there is nothing to write.
func (g MemoryGraph) Empty() bool { return len(g.Entities) == 0 && len(g.Relations) == 0 }

// Names are the entity names, relation endpoints included, in first-seen
// order without repeats.
func (g MemoryGraph) Names() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" {
			return
		}
		k := strings.ToLower(n)
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		out = append(out, n)
	}
	for _, e := range g.Entities {
		add(e.Name)
	}
	for _, r := range g.Relations {
		add(r.From)
		add(r.To)
	}
	return out
}

// MemoryGraphWriter is a store that keeps a graph beside its memories.
//
// WriteMemoryGraph records what memoryID is about; DropMemoryGraph removes
// what it recorded, for a memory that has been replaced — its edges were
// true of the old fact and must not go on answering for the new one.
type MemoryGraphWriter interface {
	WriteMemoryGraph(ctx context.Context, memoryID string, g MemoryGraph) error
	DropMemoryGraph(ctx context.Context, memoryID string) error
}

// MemoryGraphSource is the source id a memory's graph is recorded under, so
// it can be found and removed as one piece.
func MemoryGraphSource(memoryID string) string { return "memory:" + strings.TrimSpace(memoryID) }

// MemoryServerEmbedding is a store that embeds queries and memories itself —
// a remote brain that owns its embedding model. A client-side vector is
// thrown away there, so the memory service does not compute one: every turn
// used to embed its question once for retrieval and again for write-time
// reconciliation, against an embedder whose answer was discarded (and, while
// that embedder was down, waited on a 503 to learn so).
type MemoryServerEmbedding interface {
	EmbedsServerSide() bool
}
