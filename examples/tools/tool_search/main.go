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
	// 创建带有多个延迟加载工具的 Agent
	// ============================================================

	// 获取默认 LLM
	llm, err := pool.GetLLM()
	if err != nil {
		log.Fatalf("Failed to get LLM: %v", err)
	}

	// 创建 Agent
	agentSvc, err := agent.New("tool-search-agent").
		WithLLM(llm).
		WithConfig(cfg).
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// ============================================================
	// 注册多个延迟加载工具（模拟大量 MCP/Skills 工具）
	// ============================================================

	// 1. 常用工具（非延迟）
	agentSvc.RegisterTool(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "get_time",
			Description: "Get the current time",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"timezone": map[string]interface{}{
						"type":        "string",
						"description": "Timezone (optional)",
					},
				},
			},
		},
		DeferLoading: false,
	}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return "Current time: 2026-03-07 12:00:00", nil
	})

	// 2. 天气工具（延迟加载）
	weatherTools := []string{
		"get_weather", "get_weather_forecast", "get_weather_history",
		"get_weather_alerts", "get_weather_by_location", "get_weather_comparison",
	}
	for _, name := range weatherTools {
		agentSvc.RegisterTool(domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        name,
				Description: fmt.Sprintf("Get %s information", name),
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"location": map[string]interface{}{
							"type":        "string",
							"description": "Location",
						},
					},
					"required": []string{"location"},
				},
			},
			DeferLoading: true, // 延迟加载
		}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return fmt.Sprintf("Weather for %v: Sunny, 25°C", args["location"]), nil
		})
	}

	// 3. 数据库工具（延迟加载）
	dbTools := []string{
		"query_database", "insert_record", "update_record", "delete_record",
		"list_tables", "describe_table", "get_schema",
	}
	for _, name := range dbTools {
		agentSvc.RegisterTool(domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        name,
				Description: fmt.Sprintf("Database operation: %s", name),
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"table": map[string]interface{}{
							"type":        "string",
							"description": "Table name",
						},
					},
				},
			},
			DeferLoading: true,
		}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return fmt.Sprintf("Executed %s on table: %v", name, args["table"]), nil
		})
	}

	// ============================================================
	// 展示工具信息
	// ============================================================

	info := agentSvc.Info()
	fmt.Println("=== Agent Info ===")
	fmt.Printf("Name: %s\n", info.Name)
	fmt.Printf("Model: %s\n", info.Model)

	registry := agentSvc.GetToolRegistry()

	// ============================================================
	// 测试 Tool Search 功能（直接调用 API）
	// ============================================================

	fmt.Println("\n=== Testing Tool Search API ===")

	// 测试正则搜索
	fmt.Println("\n--- Regex Search: 'weather' ---")
	results, err := registry.ExecuteToolSearch("weather", "regex")
	if err != nil {
		log.Printf("Search error: %v", err)
	} else {
		for _, t := range results {
			fmt.Printf("  ✓ Found: %s\n    %s\n", t.Function.Name, t.Function.Description)
		}
	}

	// 测试 BM25 搜索
	fmt.Println("\n--- BM25 Search: 'database query' ---")
	results, err = registry.ExecuteToolSearch("database query", "bm25")
	if err != nil {
		log.Printf("Search error: %v", err)
	} else {
		for _, t := range results {
			fmt.Printf("  ✓ Found: %s\n    %s\n", t.Function.Name, t.Function.Description)
		}
	}

	// ============================================================
	// 运行 Agent（会使用 search_available_tools 搜索工具）
	// ============================================================

	fmt.Println("\n=== Running Agent ===")
	fmt.Println("Query: '帮我查一下北京的天气'")

	result, err := agentSvc.Run(context.Background(), "帮我查一下北京的天气")
	if err != nil {
		log.Printf("Agent error: %v", err)
	} else {
		fmt.Printf("Result: %s\n", result.FinalResult)
	}

	// 另一个测试：数据库查询
	fmt.Println("\n=== Running Agent (Database) ===")
	fmt.Println("Query: '列出所有表'")

	result2, err := agentSvc.Run(context.Background(), "列出所有表")
	if err != nil {
		log.Printf("Agent error: %v", err)
	} else {
		fmt.Printf("Result: %s\n", result2.FinalResult)
	}

	// 释放资源
	pool.ReleaseLLM(llm)
}
