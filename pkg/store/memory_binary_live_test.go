package store_test

import (
	"os"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory/memorystoretest"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// Weaviate through the conformance suite, against a real server.
func TestWeaviateLiveConformance(t *testing.T) {
	endpoint := os.Getenv("WEAVIATE_ENDPOINT")
	if endpoint == "" {
		t.Skip("set WEAVIATE_ENDPOINT to run against a real Weaviate")
	}
	memorystoretest.Run(t, func(t *testing.T) domain.MemoryStore {
		st, err := store.NewWeaviateMemoryStore(store.WeaviateConfig{
			Endpoint: endpoint,
			APIKey:   os.Getenv("WEAVIATE_API_KEY"),
			Class:    "AgentgoConf" + time.Now().Format("150405000000"),
			Timeout:  60 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}, memorystoretest.Options{Eventual: 5 * time.Second})
}

// SurrealDB through the conformance suite, against a real server.
func TestSurrealLiveConformance(t *testing.T) {
	endpoint := os.Getenv("SURREALDB_ENDPOINT")
	if endpoint == "" {
		t.Skip("set SURREALDB_ENDPOINT to run against a real SurrealDB")
	}
	memorystoretest.Run(t, func(t *testing.T) domain.MemoryStore {
		st, err := store.NewSurrealMemoryStore(store.SurrealConfig{
			Endpoint: endpoint,
			User:     os.Getenv("SURREALDB_USER"),
			Pass:     os.Getenv("SURREALDB_PASS"),
			// A table per case, so one cannot read another's writes.
			Table:   "conf" + time.Now().Format("150405000000"),
			Timeout: 60 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}, memorystoretest.Options{Eventual: 5 * time.Second})
}
