package main

import (
	"context"
	"fmt"
	"log"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func main() {
	ctx := context.Background()

	// 1. 初始化全功能 Agent
	fmt.Println("正在初始化全功能 Agent...")
	svc, err := agent.New("RealtimeAgent").
		WithMCP().
		WithMemory().
		WithRAG().
		Build()
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}
	defer svc.Close()

	// 2. 建立实时会话 (内部会自动注入所有工具)
	fmt.Println("正在建立有状态的实时 WebSocket 连接...")
	session, err := svc.RunRealtime(ctx, nil)
	if err != nil {
		fmt.Printf("建立连接失败: %v\n", err)
		fmt.Println("注意: Realtime 模式需要支持 WebSocket 的 provider（如 OpenAI gpt-4o-realtime-preview）")
		return
	}
	defer session.Close()

	fmt.Println("✅ 已连接。你可以发送消息（支持工具调用和知识库查询）。")

	// 3. 发送带工具需求的请求
	err = session.Send(ctx, domain.Message{
		Role:    "user",
		Content: "搜索我的知识库中关于 'AgentGo' 的介绍，并告诉我现在的时间。",
	})
	if err != nil {
		log.Fatalf("发送失败: %v", err)
	}

	// 4. 处理双向流
	fmt.Print("Agent: ")
	for {
		result, err := session.Receive(ctx)
		if err != nil {
			log.Fatalf("\n会话中断: %v", err)
		}

		// 处理文本输出
		if result.Content != "" && result.Content != "Response completed" {
			fmt.Print(result.Content)
		}

		// 核心：处理工具调用
		if len(result.ToolCalls) > 0 {
			for _, tc := range result.ToolCalls {
				fmt.Printf("\n[工具调用] → %s(%v)\n", tc.Function.Name, tc.Function.Arguments)

				// 在实际应用中，这里应该调用 svc.ExecuteToolCalls
				// 为了演示，我们回复一个模拟结果
				session.Send(ctx, domain.Message{
					Role:    "tool",
					Content: "AgentGo 是一个高性能的本地 RAG 系统。当前时间是 2026-02-25。",
				})
			}
		}

		if result.Finished {
			fmt.Println("\n--- 任务完成 ---")
			break
		}
	}
}
