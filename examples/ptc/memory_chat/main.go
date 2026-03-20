package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	homeDir, _ := os.UserHomeDir()
	testDir := filepath.Join(homeDir, ".agentgo", "data", "ptc_memory_chat_test")
	_ = os.RemoveAll(testDir)
	os.MkdirAll(testDir, 0755)

	fmt.Println("=== PTC + Memory + Chat 综合测试 ===")

	// 定义工具参数类型（在 builder 链之前声明）
	type calcArgs struct {
		A int `json:"a"`
		B int `json:"b"`
	}

	// 1. 创建带 PTC + Memory 的 Agent — 工具在 builder 链中直接注册
	svc, err := agent.New("PTCMemoryBot").
		WithDBPath(filepath.Join(testDir, "agent.db")).
		WithPTC().
		WithMemory(
			agent.WithMemoryPath(filepath.Join(testDir, "memories")),
			agent.WithMemoryStoreType("file"),
		).
		WithTool(agent.NewTool(
			"add_numbers",
			"Add two numbers and return the result",
			func(ctx context.Context, args *calcArgs) (interface{}, error) {
				return map[string]interface{}{"sum": args.A + args.B}, nil
			},
		)).
		WithTool(agent.NewTool(
			"multiply_numbers",
			"Multiply two numbers and return the result",
			func(ctx context.Context, args *calcArgs) (interface{}, error) {
				return map[string]interface{}{"product": args.A * args.B}, nil
			},
		)).
		WithDebug().
		Build()
	if err != nil {
		log.Fatalf("创建 Agent 失败: %v", err)
	}
	defer svc.Close()

	fmt.Println("--- Agent Configuration ---")
	fmt.Println("  PTC:      true")
	fmt.Println("  Memory:   true")
	fmt.Println("  MCP:      false")
	fmt.Println("  Debug:    true")
	fmt.Println("---------------------------")

	// ========== 第一轮对话：PTC + Memory 保存 ==========
	fmt.Println("【第一轮对话】使用 PTC 计算并保存偏好到 Memory")
	fmt.Println("用户: 我是一名数据分析师，请用 add_numbers 计算 100 + 200，并记住我的职业。")

	chatCtx1, cancel1 := context.WithTimeout(ctx, 180*time.Second)
	result1, err := svc.Chat(chatCtx1, "我是一名数据分析师，请用 add_numbers 计算 100 + 200，并记住我的职业。")
	cancel1()
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("PTCMemoryBot: %s\n\n", result1.Text())

	// ========== 第二轮对话：PTC 多工具调用 ==========
	fmt.Println("【第二轮对话】使用 PTC 调用多个工具")
	fmt.Println("用户: 用 multiply_numbers 计算 15 * 4，然后用 add_numbers 加上 100。")

	chatCtx2, cancel2 := context.WithTimeout(ctx, 180*time.Second)
	result2, err := svc.Chat(chatCtx2, "用 multiply_numbers 计算 15 * 4，然后用 add_numbers 把结果加上 100。")
	cancel2()
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("PTCMemoryBot: %s\n\n", result2.Text())

	// ========== 第三轮对话：Memory 检索 ==========
	fmt.Println("【第三轮对话】验证 Memory 检索")
	fmt.Println("用户: 你还记得我的职业是什么吗？")

	chatCtx3, cancel3 := context.WithTimeout(ctx, 180*time.Second)
	result3, err := svc.Chat(chatCtx3, "你还记得我的职业是什么吗？")
	cancel3()
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("PTCMemoryBot: %s\n\n", result3.Text())

	// ========== 检查记忆文件 ==========
	fmt.Println("【检查记忆文件】")
	files, _ := filepath.Glob(filepath.Join(testDir, "memories", "entities", "*.md"))
	fmt.Printf("已保存 %d 条记忆:\n", len(files))
	for _, f := range files {
		content, _ := os.ReadFile(f)
		preview := string(content)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		fmt.Printf("\n📄 %s\n%s\n", filepath.Base(f), preview)
	}

	// ========== 第四轮对话：PTC + Memory 结合 ==========
	fmt.Println("\n【第四轮对话】PTC 计算结合 Memory 检索")
	fmt.Println("用户: 根据我的职业，计算适合我的幸运数字：用 multiply_numbers 计算 7 * 7。")

	chatCtx4, cancel4 := context.WithTimeout(ctx, 180*time.Second)
	result4, err := svc.Chat(chatCtx4, "根据你记住的我的职业，计算一个幸运数字：用 multiply_numbers 计算 7 * 7。")
	cancel4()
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("PTCMemoryBot: %s\n", result4.Text())

	fmt.Println("\n=== 测试完成 ===")
	fmt.Println("PTC + Memory + Chat 集成正常工作！")
}
