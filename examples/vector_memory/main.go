package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func main() {
	ctx := context.Background()

	// 1. Initialize vector memory store
	memStore, err := store.NewMemoryStore("/tmp/agentgo-example-vector.db")
	if err != nil {
		log.Fatalf("Failed to create memory store: %v", err)
	}
	defer memStore.Close()

	// 2. Create embedder (using Ollama)
	provConfig := &domain.OpenAIProviderConfig{
		BaseProviderConfig: domain.BaseProviderConfig{Timeout: 30},
		BaseURL:            "http://localhost:11434/v1",
		APIKey:             "ollama",
		EmbeddingModel:     "qwen3-embedding:8b",
	}
	factory := providers.NewFactory()
	embedder, err := factory.CreateEmbedderProvider(ctx, provConfig)
	if err != nil {
		log.Fatalf("Failed to create embedder: %v", err)
	}

	// 3. Create memory service
	memSvc := memory.NewService(memStore, nil, embedder, memory.DefaultConfig())

	// 4. Add various memories
	fmt.Println("=== Adding memories ===")

	// User preferences
	preferences := []struct {
		content    string
		importance float64
	}{
		{"我喜欢吃火锅，特别喜欢麻辣锅底", 0.9},
		{"我喜欢听古典音乐，尤其是贝多芬和莫扎特", 0.8},
		{"我讨厌吃香菜", 0.7},
		{"我更喜欢深色的衣服", 0.6},
		{"我喜欢喝绿茶，不喜欢喝咖啡", 0.7},
	}
	for _, p := range preferences {
		mem := &domain.Memory{
			ID:         uuid.New().String(),
			Type:       domain.MemoryTypePreference,
			Content:    p.content,
			Importance: p.importance,
			SessionID:  "user-001",
			CreatedAt:  time.Now(),
		}
		if err := memSvc.Add(ctx, mem); err != nil {
			log.Printf("Failed to add preference: %v", err)
		} else {
			fmt.Printf("✓ Added preference: %s\n", p.content[:min(30, len(p.content))])
		}
	}

	// Facts
	facts := []struct {
		content    string
		importance float64
	}{
		{"我住在上海浦东", 0.8},
		{"我是一名软件工程师", 0.9},
		{"我主要使用 Go 语言开发", 0.8},
		{"我养了一只猫叫咪咪", 0.7},
		{"我每周去健身房三次", 0.6},
	}
	for _, f := range facts {
		mem := &domain.Memory{
			ID:         uuid.New().String(),
			Type:       domain.MemoryTypeFact,
			Content:    f.content,
			Importance: f.importance,
			SessionID:  "user-001",
			CreatedAt:  time.Now(),
		}
		if err := memSvc.Add(ctx, mem); err != nil {
			log.Printf("Failed to add fact: %v", err)
		} else {
			fmt.Printf("✓ Added fact: %s\n", f.content[:min(30, len(f.content))])
		}
	}

	// Skills
	skills := []struct {
		content    string
		importance float64
	}{
		{"我擅长 Python 编程", 0.8},
		{"我擅长数据分析", 0.7},
		{"我会弹钢琴", 0.6},
		{"我會打羽毛球", 0.5},
	}
	for _, s := range skills {
		mem := &domain.Memory{
			ID:         uuid.New().String(),
			Type:       domain.MemoryTypeSkill,
			Content:    s.content,
			Importance: s.importance,
			SessionID:  "user-001",
			CreatedAt:  time.Now(),
		}
		if err := memSvc.Add(ctx, mem); err != nil {
			log.Printf("Failed to add skill: %v", err)
		} else {
			fmt.Printf("✓ Added skill: %s\n", s.content[:min(30, len(s.content))])
		}
	}

	// Context
	contexts := []struct {
		content    string
		importance float64
	}{
		{"目前在做 AI 助手项目", 0.8},
		{"最近在学习机器学习", 0.7},
	}
	for _, c := range contexts {
		mem := &domain.Memory{
			ID:         uuid.New().String(),
			Type:       domain.MemoryTypeContext,
			Content:    c.content,
			Importance: c.importance,
			SessionID:  "user-001",
			CreatedAt:  time.Now(),
		}
		if err := memSvc.Add(ctx, mem); err != nil {
			log.Printf("Failed to add context: %v", err)
		} else {
			fmt.Printf("✓ Added context: %s\n", c.content[:min(30, len(c.content))])
		}
	}

	fmt.Println("\n=== Testing Vector Search ===")

	// 5. Test various searches
	searches := []string{
		"美食",
		"运动",
		"编程",
		"音乐",
		"宠物",
		"上海",
		"AI",
		"学习",
	}

	for _, query := range searches {
		fmt.Printf("\n--- Search: \"%s\" ---\n", query)
		results, err := memSvc.Search(ctx, query, 5)
		if err != nil {
			log.Printf("Search failed: %v", err)
			continue
		}

		if len(results) == 0 {
			fmt.Println("No results found")
			continue
		}

		for i, r := range results {
			fmt.Printf("%d. [%.2f] %s (%s)\n", i+1, r.Score, r.Content, r.Type)
		}
	}

	// 6. Test RetrieveAndInject (for LLM context)
	fmt.Println("\n=== Testing RetrieveAndInject ===")

	queries := []string{
		"我有什么爱好？",
		"我住在哪个城市？",
		"我擅长什么技术？",
		"我饮食习惯是什么？",
	}

	for _, query := range queries {
		fmt.Printf("\nQuery: %s\n", query)
		context, memories, err := memSvc.RetrieveAndInject(ctx, query, "user-001")
		if err != nil {
			log.Printf("RetrieveAndInject failed: %v", err)
			continue
		}

		fmt.Println("Retrieved memories:")
		for i, m := range memories {
			fmt.Printf("  %d. [%.2f] %s\n", i+1, m.Score, m.Content)
		}
		if context != "" {
			fmt.Println("\nFormatted context:")
			fmt.Println(context)
		}
	}

	// 7. List all memories
	fmt.Println("\n=== All Memories ===")
	allMems, total, err := memSvc.List(ctx, 20, 0)
	if err != nil {
		log.Printf("List failed: %v", err)
	} else {
		fmt.Printf("Total memories: %d\n\n", total)
		for _, m := range allMems {
			fmt.Printf("- [%s] %s (importance: %.2f)\n", m.Type, m.Content, m.Importance)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
