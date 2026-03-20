package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
	"github.com/stretchr/testify/require"
)

func TestMemoryIntegrationLiveEmbeddingSmoke(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("AGENTGO_TEST_EMBED_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("AGENTGO_TEST_EMBED_KEY"))
	model := strings.TrimSpace(os.Getenv("AGENTGO_TEST_EMBED_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("live embedding smoke test requires AGENTGO_TEST_EMBED_BASE_URL, AGENTGO_TEST_EMBED_KEY, and AGENTGO_TEST_EMBED_MODEL")
	}

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "live-memory.db")

	memStore, err := store.NewMemoryStore(dbPath)
	require.NoError(t, err)
	defer memStore.Close()

	factory := providers.NewFactory()
	embedder, err := factory.CreateEmbedderProvider(ctx, &domain.OpenAIProviderConfig{
		BaseProviderConfig: domain.BaseProviderConfig{Timeout: 60 * time.Second},
		BaseURL:            baseURL,
		APIKey:             apiKey,
		EmbeddingModel:     model,
	})
	require.NoError(t, err)

	service := NewService(memStore, nil, embedder, nil)

	require.NoError(t, service.Add(ctx, &domain.Memory{
		ID:         "live-tea",
		SessionID:  "live-session",
		Type:       domain.MemoryTypePreference,
		Content:    "Alice prefers jasmine tea in the morning.",
		Importance: 0.9,
	}))

	require.NoError(t, service.Add(ctx, &domain.Memory{
		ID:         "live-code",
		SessionID:  "live-session",
		Type:       domain.MemoryTypeFact,
		Content:    "The deployment pipeline is implemented in Go and Docker.",
		Importance: 0.5,
	}))

	results, err := service.Search(ctx, "What drink does Alice prefer in the morning?", 2)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Equal(t, "live-tea", results[0].ID)
}
