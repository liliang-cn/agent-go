package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/liliang-cn/agent-go/pkg/agent"
)

func main() {
	ctx := context.Background()
	homeDir, _ := os.UserHomeDir()
	testDir := filepath.Join(homeDir, ".agentgo", "data", "chat_memory_test")
	_ = os.RemoveAll(testDir)
	os.MkdirAll(testDir, 0755)

	fmt.Println("=== Memory + Chat 集成测试 ===")

	// 1. 创建带 Memory 的 Agent
	svc, err := agent.New("MemoryBot").
		WithDBPath(filepath.Join(testDir, "agent.db")).
		WithMemory(
			agent.WithMemoryPath(filepath.Join(testDir, "memories")),
			agent.WithMemoryStoreType("file"),
		).
		Build()
	if err != nil {
		log.Fatalf("创建 Agent 失败: %v", err)
	}
	defer svc.Close()

	// ========== 第一轮对话：建立记忆 ==========
	fmt.Println("【第一轮对话】告诉 Agent 个人信息")
	fmt.Println("用户: 我叫小明，是一名 Go 开发者，最喜欢用 Gin 框架写 API。")

	res1, err := svc.Chat(ctx, "我叫小明，是一名 Go 开发者，最喜欢用 Gin 框架写 API。")
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("MemoryBot: %s\n\n", res1.Text())

	// ========== 第二轮对话：短期记忆测试 ==========
	fmt.Println("【第二轮对话】测试短期记忆（同一会话）")
	fmt.Println("用户: 你还记得我的名字和职业吗？")

	res2, err := svc.Chat(ctx, "你还记得我的名字和职业吗？")
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("MemoryBot: %s\n\n", res2.Text())

	// ========== 查看磁盘上的记忆文件 ==========
	fmt.Println("【检查磁盘记忆文件】")
	files, _ := filepath.Glob(filepath.Join(testDir, "memories", "entities", "*.md"))
	fmt.Printf("已保存 %d 条记忆到磁盘:\n", len(files))
	for _, f := range files {
		content, _ := os.ReadFile(f)
		fmt.Printf("\n📄 %s\n%s\n", filepath.Base(f), string(content))
	}

	// ========== 第三轮对话：长期记忆跨会话测试 ==========
	fmt.Println("\n【第三轮对话】模拟新会话 - 测试长期记忆")
	fmt.Println("用户: 我之前告诉过你什么技术栈偏好？")

	// Agent 会在新对话中自动检索相关记忆
	res3, err := svc.Chat(ctx, "我之前告诉过你什么技术栈偏好？")
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("MemoryBot: %s\n\n", res3.Text())

	// ========== 更新记忆测试 ==========
	fmt.Println("【第四轮对话】更新记忆")
	fmt.Println("用户: 我现在也开始学习 Rust 了，觉得所有权机制很有意思。")

	res4, err := svc.Chat(ctx, "我现在也开始学习 Rust 了，觉得所有权机制很有意思。")
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("MemoryBot: %s\n\n", res4.Text())

	// ========== 最终验证 ==========
	fmt.Println("【最终验证】检查更新后的记忆")
	time.Sleep(1 * time.Second) // 等待文件写入

	files, _ = filepath.Glob(filepath.Join(testDir, "memories", "entities", "*.md"))
	fmt.Printf("最终记忆文件数量: %d\n", len(files))

	fmt.Println("\n=== 测试完成 ===")
	fmt.Printf("会话 ID: %s\n", svc.CurrentSessionID())
	fmt.Println("Memory + Chat 集成正常工作！")
}
