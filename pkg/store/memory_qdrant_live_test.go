package store_test

import (
	"os"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory/memorystoretest"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// Qdrant through the same conformance suite, against a real server.
//
//	QDRANT_ENDPOINT=http://192.168.123.64:6333 go test ./pkg/store -run TestQdrantLive -v
func TestQdrantLiveConformance(t *testing.T) {
	endpoint := os.Getenv("QDRANT_ENDPOINT")
	if endpoint == "" {
		t.Skip("set QDRANT_ENDPOINT to run against a real Qdrant")
	}
	memorystoretest.Run(t, func(t *testing.T) domain.MemoryStore {
		// A collection per case: these are cheap in Qdrant and it keeps one
		// case from reading another's writes.
		st, err := store.NewQdrantMemoryStore(store.QdrantConfig{
			Endpoint:   endpoint,
			APIKey:     os.Getenv("QDRANT_API_KEY"),
			Collection: "agentgo_conf_" + time.Now().Format("150405.000000"),
			Timeout:    60 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}, memorystoretest.Options{Eventual: 5 * time.Second})
}
