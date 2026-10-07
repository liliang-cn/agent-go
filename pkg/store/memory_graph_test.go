package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// The embedded store writes a memory's graph into its own CortexDB and takes
// it out again when the memory is replaced.
func TestMemoryStoreWritesAndDropsMemoryGraph(t *testing.T) {
	s, err := NewMemoryStore(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	stats := func() (nodes, edges int) {
		st, err := s.db.Graph().GetGraphStatistics(ctx)
		if err != nil {
			t.Fatalf("graph statistics: %v", err)
		}
		return int(st.NodeCount), int(st.EdgeCount)
	}

	g := domain.MemoryGraph{
		Entities:  []domain.MemoryGraphEntity{{Name: "周明远", Type: "person"}, {Name: "可可", Type: "person"}},
		Relations: []domain.MemoryGraphRelation{{From: "可可", Type: "daughter_of", To: "周明远"}},
	}
	if err := s.WriteMemoryGraph(ctx, "m1", g); err != nil {
		t.Fatalf("WriteMemoryGraph: %v", err)
	}
	nodes, edges := stats()
	if nodes < 2 || edges < 1 {
		t.Fatalf("after writing: %d nodes, %d edges; want the two people and the edge between them", nodes, edges)
	}
	if err := s.DropMemoryGraph(ctx, "m1"); err != nil {
		t.Fatalf("DropMemoryGraph: %v", err)
	}
	if _, edges := stats(); edges != 0 {
		t.Fatalf("after dropping the memory's graph: %d edges remain", edges)
	}
}
