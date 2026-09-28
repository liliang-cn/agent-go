// A fact that changed is replaced, not duplicated.
//
// "I live in Berlin", then a week later "I moved to Vienna": before write-time
// reconciliation both stayed current, and "where do I live?" was answered from
// whichever ranked first. Now the extraction call that already runs after each
// turn is shown the most similar existing memories and answers, per item,
// add / update (replaces target_id) / noop — at no extra model call. An update
// retires the old memory through the backend's MarkStale; the read path never
// injects a superseded memory, on any backend.
//
// This example needs no model: it performs the update by hand with
// Service.Supersede, which is exactly what the write path calls, and shows
// what the agent is told afterwards.
//
//	go run ./examples/memory-reconcile
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "memory-reconcile-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	fileStore, err := store.NewFileMemoryStore(dir)
	if err != nil {
		log.Fatal(err)
	}
	svc := memory.NewService(fileStore, nil, nil, memory.DefaultConfig())
	defer svc.Close()

	session := uuid.NewString()
	remember := func(content string, kind domain.MemoryKind, age time.Duration) *domain.Memory {
		m := &domain.Memory{
			ID: uuid.NewString(), Type: domain.MemoryTypeFact,
			SessionID: session, ScopeType: domain.MemoryScopeSession, ScopeID: session,
			Content: content, Importance: 0.9, CreatedAt: time.Now().Add(-age),
		}
		// Kind is set by the extraction call on the write path:
		// world, experience, opinion or observation.
		domain.SetMemoryKind(m, kind)
		if err := svc.Add(ctx, m); err != nil {
			log.Fatal(err)
		}
		return m
	}

	berlin := remember("User lives in Berlin", domain.MemoryKindWorld, 7*24*time.Hour)
	vienna := remember("User lives in Vienna", domain.MemoryKindWorld, 0)

	// What the write path does on op "update". A backend without MarkStale
	// answers ErrMemoryStoreUnsupported; the write path then keeps both and
	// logs it rather than pretending.
	if err := svc.Supersede(ctx, berlin.ID, vienna.ID); err != nil {
		if errors.Is(err, domain.ErrMemoryStoreUnsupported) {
			fmt.Println("this backend cannot supersede; both memories stay current")
		} else {
			log.Fatal(err)
		}
	}

	injected, _, err := svc.RetrieveAndInject(ctx, "where does the user live?", session)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("--- what the agent is told ---")
	fmt.Println(injected)

	// The old memory is history, not deleted.
	evo, err := svc.GetEvolution(ctx, berlin.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("--- history ---")
	fmt.Printf("%q superseded=%v\n", evo.Memory.Content, domain.MemoryIsSuperseded(evo.Memory))
	for _, child := range evo.Children {
		fmt.Printf("  -> replaced by %q\n", child.Memory.Content)
	}
}
