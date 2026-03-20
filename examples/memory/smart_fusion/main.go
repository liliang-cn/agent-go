package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func main() {
	ctx := context.Background()
	homeDir, _ := os.UserHomeDir()
	// 设置独立的测试目录
	testDataDir := filepath.Join(homeDir, ".agentgo", "data", "file_memory_test")
	_ = os.RemoveAll(testDataDir) // 清理旧数据
	os.MkdirAll(testDataDir, 0755)

	// 1. 使用链式 Builder API 初始化 Agent
	// 使用 "file" 存储类型 - 仅 Markdown 文件
	svc, err := agent.New("FileMemoryAgent").
		WithDBPath(filepath.Join(testDataDir, "agent.db")).
		WithMemory(
			agent.WithMemoryPath(filepath.Join(testDataDir, "memories")),
			agent.WithMemoryStoreType("file"), // file-only storage
		).
		Build()
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}
	defer svc.Close()

	fmt.Println("=== 步骤 1: 存入初始记忆 ===")
	_, err = svc.Run(ctx, "请记住：我最喜欢的编程语言是 Go，因为它非常简洁高效。")
	if err != nil {
		log.Fatalf("执行失败: %v", err)
	}

	// 检查磁盘文件
	files, _ := filepath.Glob(filepath.Join(testDataDir, "memories", "entities", "*.md"))
	if len(files) > 0 {
		fmt.Printf("✅ 已在磁盘生成记忆文件: %s\n", filepath.Base(files[0]))
		content, _ := os.ReadFile(files[0])
		fmt.Printf("文件内容预览:\n%s\n", string(content))
	}

	fmt.Println("\n=== 步骤 2: 智能融合 (Smart Fusion) ===")
	// 找到刚才的 ID
	var memID string
	if len(files) > 0 {
		memID = strings.TrimSuffix(filepath.Base(files[0]), ".md")
	}

	if memID != "" {
		goal := fmt.Sprintf("请更新记忆 %s：我现在也开始学习 Rust 了，我觉得它的所有权机制很硬核。请保留我之前对 Go 的喜爱。", memID)
		_, err = svc.Run(ctx, goal)
		if err != nil {
			log.Fatalf("更新失败: %v", err)
		}

		content, _ := os.ReadFile(files[0])
		fmt.Printf("融合后的文件内容:\n%s\n", string(content))
	}

	fmt.Println("\n=== 步骤 3: 自动感知 (Sitemap) ===")
	res, err := svc.Run(ctx, "根据你记住的信息，总结一下我的编程语言偏好。")
	if err != nil {
		log.Fatalf("查询失败: %v", err)
	}
	fmt.Printf("Agent 回答: %s\n", res.Text())

	fmt.Println("\n=== 步骤 4: 物理删除 ===")
	if memID != "" {
		_, err = svc.Run(ctx, fmt.Sprintf("请删除记忆 %s，我不想要它了。", memID))
		if err == nil {
			if _, err := os.Stat(files[0]); os.IsNotExist(err) {
				fmt.Println("✅ 记忆文件已从磁盘物理删除。")
			}
		}
	}
}
