package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/liliang-cn/agent-go/pkg/agent"
)

func main() {
	ctx := context.Background()
	homeDir, _ := os.UserHomeDir()
	testDir := filepath.Join(homeDir, ".agentgo", "data", "auto_memory_test")
	_ = os.RemoveAll(testDir)
	os.MkdirAll(testDir, 0755)

	// 1. 使用链式 Builder API 初始化 Agent 并开启自动记忆 Hook
	svc, err := agent.New("AutoAgent").
		WithDBPath(filepath.Join(testDir, "agent.db")).
		WithMemory(agent.WithMemoryPath(filepath.Join(testDir, "memories"))).
		Build()
	if err != nil {
		log.Fatalf("Failed to init: %v", err)
	}
	defer svc.Close()

	fmt.Println("=== 测试自动记忆 Hook ===")
	goal := "My favorite programming language is Go because of its simplicity."
	fmt.Printf("Goal: %s\n", goal)

	res, err := svc.Run(ctx, goal)
	if err != nil {
		log.Fatalf("Run failed: %v", err)
	}
	fmt.Printf("Agent Result: %s\n", res.Text())

	// 2. 检查磁盘
	files, _ := filepath.Glob(filepath.Join(testDir, "memories", "entities", "*.md"))
	if len(files) > 0 {
		fmt.Printf("\n✅ Hook 成功检测到重要信息并保存！\n")
		content, _ := os.ReadFile(files[0])
		fmt.Printf("记忆内容:\n%s\n", string(content))
	} else {
		fmt.Println("\n❌ 未能自动保存记忆。")
	}
}
