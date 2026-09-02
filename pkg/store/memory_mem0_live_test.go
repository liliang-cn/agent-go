package store_test

import (
	"os"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory/memorystoretest"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// The mem0 backend against a real mem0 server, through the same conformance
// suite the built-in file backend runs.
//
// Skipped unless MEM0_ENDPOINT is set, because it needs a server: this is the
// test that catches what a fake cannot — the scope mapping, the id the server
// mints, and whether a memory written through the service comes back out of
// the PROMPT rather than merely out of the store.
//
//	MEM0_ENDPOINT=http://192.168.123.64:43917 MEM0_API_KEY=… \
//	  go test ./pkg/store -run TestMem0Live -v
func TestMem0LiveConformance(t *testing.T) {
	endpoint := os.Getenv("MEM0_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MEM0_ENDPOINT to run against a real mem0 server")
	}
	memorystoretest.Run(t, func(t *testing.T) domain.MemoryStore {
		// A fresh agent id per case, so one case cannot read another's
		// writes on a server that outlives the test run.
		st, err := store.NewMem0MemoryStore(store.Mem0Config{
			Endpoint: endpoint,
			APIKey:   os.Getenv("MEM0_API_KEY"),
			UserID:   "agentgo-conformance",
			AgentID:  "case-" + time.Now().Format("150405.000000"),
			Timeout:  60 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}, memorystoretest.Options{
		// mem0 embeds server-side and writes through Postgres; a write is
		// searchable quickly but not instantly.
		Eventual: 10 * time.Second,
	})
}

// The same suite with NO agent id configured.
//
// This is the case that caught the real bug: with an agent id set, every
// search filters on a key that happens to be on every record, and a backend
// filtering by the wrong owner still passes. Without one, a session-scoped
// memory written under mem0's run_id has to be found some other way — and
// the first implementation could not find it at all. A store's defaults have
// to work, because the defaults are what most callers get.
func TestMem0LiveConformanceWithDefaults(t *testing.T) {
	endpoint := os.Getenv("MEM0_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MEM0_ENDPOINT to run against a real mem0 server")
	}
	memorystoretest.Run(t, func(t *testing.T) domain.MemoryStore {
		st, err := store.NewMem0MemoryStore(store.Mem0Config{
			Endpoint: endpoint,
			APIKey:   os.Getenv("MEM0_API_KEY"),
			// Deliberately no UserID and no AgentID: the defaults.
			Timeout: 60 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}, memorystoretest.Options{Eventual: 10 * time.Second})
}
