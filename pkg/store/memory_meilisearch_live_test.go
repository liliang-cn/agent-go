package store_test

import (
	"os"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory/memorystoretest"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// Meilisearch through the conformance suite, against a real server.
//
// Eventual is zero on purpose: Store waits for its own indexing task, so this
// backend is read-your-writes from the caller's side even though the server's
// API is asynchronous. If that ever regresses, this test is where it shows.
func TestMeilisearchLiveConformance(t *testing.T) {
	endpoint := os.Getenv("MEILISEARCH_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MEILISEARCH_ENDPOINT to run against a real Meilisearch")
	}
	memorystoretest.Run(t, func(t *testing.T) domain.MemoryStore {
		st, err := store.NewMeilisearchMemoryStore(store.MeilisearchConfig{
			Endpoint: endpoint,
			APIKey:   os.Getenv("MEILISEARCH_API_KEY"),
			Index:    "agentgo_conf_" + time.Now().Format("150405000000"),
			Timeout:  60 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}, memorystoretest.Options{})
}
