package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Memory retrieval is on by default, and the only way to turn it off is the
// explicit option — never a guess about what the user's request "looks like".
func TestMemoryRetrievalIsOnByDefault(t *testing.T) {
	cfg := testAgentConfig(t.TempDir())
	ctx := context.Background()

	store := newMinimalStore()
	if err := store.Store(ctx, &domain.Memory{Content: "the deploy key lives in 1Password", Type: domain.MemoryTypeFact}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	b := New("recalling-agent").WithConfig(cfg).WithMemory(WithMemoryStore(store))
	memSvc, _, err := b.buildMemoryService(cfg, nil, nil)
	if err != nil {
		t.Fatalf("buildMemoryService() error = %v", err)
	}

	_, mems, err := memSvc.RetrieveAndInjectWithContext(ctx, "1password", domain.MemoryQueryContext{SessionID: "s1"})
	if err != nil {
		t.Fatalf("RetrieveAndInjectWithContext() error = %v", err)
	}
	if len(mems) == 0 {
		t.Fatal("retrieval returned nothing; memory retrieval must run by default")
	}
}

func TestWithMemoryRetrievalFalseSkipsRetrieval(t *testing.T) {
	cfg := testAgentConfig(t.TempDir())
	ctx := context.Background()

	store := newMinimalStore()
	if err := store.Store(ctx, &domain.Memory{Content: "the deploy key lives in 1Password", Type: domain.MemoryTypeFact}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	b := New("silent-agent").WithConfig(cfg).WithMemory(
		WithMemoryStore(store),
		WithMemoryRetrieval(false),
	)
	memSvc, _, err := b.buildMemoryService(cfg, nil, nil)
	if err != nil {
		t.Fatalf("buildMemoryService() error = %v", err)
	}

	formatted, mems, err := memSvc.RetrieveAndInjectWithContext(ctx, "1password", domain.MemoryQueryContext{SessionID: "s1"})
	if err != nil {
		t.Fatalf("RetrieveAndInjectWithContext() error = %v", err)
	}
	if formatted != "" || len(mems) != 0 {
		t.Fatalf("retrieval ran despite WithMemoryRetrieval(false): %q / %d memories", formatted, len(mems))
	}
}
