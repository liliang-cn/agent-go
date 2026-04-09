package main

import (
	"context"
	"fmt"
	"log"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
)

func main() {
	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 初始化全局 LLM 池
	pool := services.GetGlobalPoolService()
	if err := pool.Initialize(context.Background(), cfg); err != nil {
		log.Fatalf("Failed to initialize pool: %v", err)
	}

	// ============================================================
	// 创建启用 PTC 的 Agent
	// ============================================================

	llm, err := pool.GetLLM()
	if err != nil {
		log.Fatalf("Failed to get LLM: %v", err)
	}

	agentSvc, err := agent.New("ptc-tool-search-agent").
		WithLLM(llm).
		WithConfig(cfg).
		WithPTC(). // 启用 PTC 模式
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// ============================================================
	// 注册一个简单的测试工具
	// ============================================================

	// 注册天气工具
	agentSvc.RegisterTool(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "get_weather",
			Description: "Get weather for a location",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"location": map[string]interface{}{
						"type":        "string",
						"description": "Location to get weather for",
					},
				},
				"required": []string{"location"},
			},
		},
		DeferLoading: true,
	}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return fmt.Sprintf("北京天气：晴，25°C，湿度50%%"), nil
	})

	info := agentSvc.Info()
	fmt.Println("=== Agent Info ===")
	fmt.Printf("Name: %s\n", info.Name)
	fmt.Printf("PTC Enabled: %v\n", info.PTCEnabled)

	// ============================================================
	// 测试 Claude Code 风格的两阶段发现：
	// 先 search_available_tools / tool_search_tool_*，再按精确名字 callTool
	// ============================================================

	fmt.Println("\n=== Testing PTC tool discovery ===")
	fmt.Println("Query: 'weather', Instruction: '查询北京的天气'")

	// 手动调用 SearchAndExecute 来测试底层 discovery 行为。
	result, err := agentSvc.SearchAndExecute(context.Background(), "weather", "查询北京的天气", "")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("✅ Result: %v\n", result)
	}

	// ============================================================
	// 使用 Run 测试
	// ============================================================

	fmt.Println("\n=== Running Agent (PTC Mode) ===")
	fmt.Println("Query: '查询北京的天气'")
	fmt.Println("Expected behavior: the model should search for a weather tool if it does not know the exact name, then call the exact tool inside execute_javascript.")

	result2, err := agentSvc.Run(context.Background(), "查询北京的天气")
	if err != nil {
		log.Printf("Agent error: %v", err)
	} else {
		fmt.Printf("\n=== Final Result ===\n%s\n", result2.FinalResult)
	}

	// 释放资源
	pool.ReleaseLLM(llm)
}
