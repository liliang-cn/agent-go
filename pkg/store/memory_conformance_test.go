package store_test

import (
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
