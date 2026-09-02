// Changing the memory backend without rebuilding the agent.
//
// Which backend a service uses was decided once, at construction. Moving a
// user from local file memory to a shared brain meant building a second
// Service and throwing the first away — with its conversation, its
// scratchpad and its in-flight runs.
//
//	go run ./examples/memory-swap
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

func backend(dir string) *memory.Service {
	st, err := store.NewFileMemoryStore(dir)
	if err != nil {
		log.Fatal(err)
	}
	cfg := memory.DefaultConfig()
	cfg.MinScore = 0
	return memory.NewService(st, nil, nil, cfg)
}

func main() {
	svc, err := agent.New("assistant").Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()
	ctx := context.Background()

	personal, _ := os.MkdirTemp("", "personal")
	shared, _ := os.MkdirTemp("", "shared")
	defer os.RemoveAll(personal)
	defer os.RemoveAll(shared)

	one, two := backend(personal), backend(shared)
	_ = one.Add(ctx, &domain.Memory{ID: "a", Type: domain.MemoryTypeFact,
		Content: "the gateway port is 47821", SessionID: "s", CreatedAt: time.Now(), Importance: 0.9})
	_ = two.Add(ctx, &domain.Memory{ID: "b", Type: domain.MemoryTypeFact,
		Content: "the gateway port is 51000", SessionID: "s", CreatedAt: time.Now(), Importance: 0.9})

	show := func(label string) {
		// What the agent is actually shown. This is the assertion that
		// matters: a store-level round trip passes while the agent sees
		// nothing, because a scope mismatch filters every row out.
		text, _, err := svc.MemoryService().RetrieveAndInject(ctx, "what is the gateway port?", "s")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%-9s %s\n", label+":", memoryLineOf(text))
	}

	svc.SetMemoryService(one)
	show("personal")

	// Swap when the service is idle. A run already in flight reads the
	// backend at each point it needs one, so one mid-turn could retrieve
	// from the old and store into the new.
	if len(svc.ActiveRuns()) == 0 {
		// The returned service is already drained and closed: it held a
		// writer goroutine with queued extractions, and dropping the
		// pointer would have stranded them.
		svc.SetMemoryService(two)
	}
	show("shared")

	// nil turns memory off. The run still works; it stops remembering.
	svc.SetMemoryService(nil)
	fmt.Println("memory enabled:", svc.MemoryEnabled())
}

// memoryLineOf pulls the line that carries an actual memory out of the
// injected block, which is the part worth looking at.
func memoryLineOf(text string) string {
	if strings.TrimSpace(text) == "" {
		return "(nothing injected)"
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "gateway port") {
			return strings.TrimSpace(line)
		}
	}
	return "(no memory about the port)"
}
