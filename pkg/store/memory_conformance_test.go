package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory/memorystoretest"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// The built-in file backend against the same suite every new backend has to
// pass. It is here to keep the suite honest as much as the backend: a
// conformance suite nothing runs drifts into passing everything.
func TestFileMemoryStoreConformance(t *testing.T) {
	memorystoretest.Run(t, func(t *testing.T) domain.MemoryStore {
		st, err := store.NewFileMemoryStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return st
	}, memorystoretest.Options{})
}

// The embedded CortexDB backend, which also implements MarkStale — so the
// superseded-memory case runs for real on two backends, not one.
func TestCortexMemoryStoreConformance(t *testing.T) {
	memorystoretest.Run(t, func(t *testing.T) domain.MemoryStore {
		st, err := store.NewCortexMemoryStore(filepath.Join(t.TempDir(), "memory.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.InitSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}, memorystoretest.Options{})
}

var (
	_ domain.MemoryStaleMarker = (*store.FileMemoryStore)(nil)
	_ domain.MemoryStaleMarker = (*store.MemoryStore)(nil)
)
