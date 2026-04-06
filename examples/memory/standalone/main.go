package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func main() {
	ctx := context.Background()

	// 1. Setup temporary directory for file-based memory
	tempDir := filepath.Join(os.TempDir(), "agentgo-memory-test")
	os.RemoveAll(tempDir)
	defer os.RemoveAll(tempDir)

	fmt.Printf("Testing Memory Module with FileStore at: %s\n\n", tempDir)

	// 2. Initialize FileMemoryStore
	fileStore, err := store.NewFileMemoryStore(tempDir)
	if err != nil {
		log.Fatalf("Failed to create store: %v", err)
	}

	// 3. Initialize Memory Service (without LLM/Embedder for standalone test)
	// This will use basic text search and direct storage
	svc := memory.NewService(fileStore, nil, nil, memory.DefaultConfig())

	fmt.Println("--- 1. Adding Memories ---")
	memories := []*domain.Memory{
		{
			ID:         "mem-1",
			Type:       domain.MemoryTypeFact,
			Content:    "The project AgentGo is a modular local-first RAG system.",
			Importance: 0.9,
			CreatedAt:  time.Now(),
		},
		{
			ID:         "mem-2",
			Type:       domain.MemoryTypePreference,
			Content:    "The user prefers using Go for backend development.",
			Importance: 0.8,
			CreatedAt:  time.Now(),
		},
		{
			ID:         "mem-3",
			Type:       domain.MemoryTypeSkill,
			Content:    "The agent knows how to use MCP tools to interact with external systems.",
			Importance: 0.95,
			CreatedAt:  time.Now(),
		},
	}

	for _, m := range memories {
		err := svc.Add(ctx, m)
		if err != nil {
			fmt.Printf("Failed to add memory %s: %v\n", m.ID, err)
		} else {
			fmt.Printf("✅ Added: [%s] %s\n", m.Type, m.ID)
		}
	}

	fmt.Println("\n--- 2. Session Memory (SESSION.md style) ---")
	if err := fileStore.WriteSessionMemory("demo-session", "Current draft is a technical overview. Keep the tone concise and precise."); err != nil {
		log.Fatalf("Failed to write session memory: %v", err)
	}
	sessionMemory, err := fileStore.ReadSessionMemory("demo-session")
	if err != nil {
		log.Fatalf("Failed to read session memory: %v", err)
	}
	fmt.Printf("Session Memory:\n%s\n", sessionMemory)

	fmt.Println("\n--- 3. Checking Files on Disk ---")
	// FileMemoryStore saves facts/preferences in 'entities' and context in 'streams'
	files, _ := filepath.Glob(filepath.Join(tempDir, "entities", "*.md"))
	for _, f := range files {
		fmt.Printf("📄 Found file: %s\n", filepath.Base(f))
		content, _ := os.ReadFile(f)
		fmt.Println("   Content Preview (first 100 chars):")
		preview := string(content)
		if len(preview) > 100 {
			preview = preview[:100] + "..."
		}
		fmt.Printf("   %s\n", preview)
	}

	fmt.Println("\n--- 4. MEMORY.md Entrypoint ---")
	entrypoint, err := fileStore.ReadEntrypoint()
	if err != nil {
		log.Fatalf("Failed to read MEMORY.md entrypoint: %v", err)
	}
	fmt.Println(entrypoint)

	fmt.Println("\n--- 5. Listing Memories via Service ---")
	list, total, err := svc.List(ctx, 10, 0)
	if err != nil {
		log.Fatalf("Failed to list: %v", err)
	}
	fmt.Printf("Total memories: %d\n", total)
	for _, m := range list {
		fmt.Printf("🔹 [%s] %s: %s\n", m.Type, m.ID, m.Content)
	}

	fmt.Println("\n--- 6. Selecting Relevant Memory Headers ---")
	query := "Go backend tools modular local-first"
	fmt.Printf("Selecting headers for query: '%s'\n", query)
	headers, err := fileStore.SelectRelevantHeaders(ctx, query, 3)
	if err != nil {
		fmt.Printf("Header selection failed: %v\n", err)
	} else {
		fmt.Printf("Selected %d headers:\n", len(headers))
		for _, h := range headers {
			fmt.Printf("🧭 [%s] importance=%.2f summary=%s\n", h.Type, h.Importance, h.Summary)
		}
	}

	fmt.Println("\n--- 7. RetrieveAndInject() Prompt Context ---")
	contextBlock, retrieved, err := svc.RetrieveAndInject(ctx, query, "demo-session")
	if err != nil {
		fmt.Printf("RetrieveAndInject failed: %v\n", err)
	} else {
		fmt.Printf("Retrieved %d memories\n", len(retrieved))
		fmt.Println(contextBlock)
	}

	fmt.Println("\n--- 8. Deleting Memory ---")
	err = svc.Delete(ctx, "mem-2")
	if err != nil {
		fmt.Printf("Failed to delete: %v\n", err)
	} else {
		fmt.Println("✅ Deleted mem-2")
		if _, err := os.Stat(filepath.Join(tempDir, "entities", "mem-2.md")); os.IsNotExist(err) {
			fmt.Println("✅ File physically removed from disk.")
		}
	}

	fmt.Println("\n--- Test Completed Successfully ---")
}
