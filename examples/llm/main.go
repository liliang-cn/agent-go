package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
)

func main() {
	// 加载配置
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 初始化全局 LLM 池
	pool := services.GetGlobalPoolService()
	if err := pool.Initialize(context.Background(), cfg); err != nil {
		log.Fatalf("Failed to initialize pool: %v", err)
	}

	// ============================================================
	// 创建不同的 LLM 服务
	// ============================================================

	// 从全局池获取不同的 LLM (使用 provider 名称)
	// pool.Client 实现了 domain.Generator 接口
	llmMiniMax, err := pool.GetLLMByName("minimax")
	if err != nil {
		log.Fatalf("Failed to get MiniMax LLM: %v", err)
	}

	llmOllama, err := pool.GetLLMByName("ollama")
	if err != nil {
		log.Fatalf("Failed to get Ollama LLM: %v", err)
	}

	// 或者获取默认 LLM (自动选择)
	llmDefault, err := pool.GetLLM()
	if err != nil {
		log.Fatalf("Failed to get default LLM: %v", err)
	}

	// ============================================================
	// Agent 1 - 使用 MiniMax (快速响应)
	// ============================================================
	fmt.Println("=== Creating Agent 1: fast-agent (MiniMax) ===")
	agent1, err := agent.New("fast-agent").
		WithLLM(llmMiniMax).
		WithConfig(cfg).
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent1: %v", err)
	}

	// ============================================================
	// Agent 2 - 使用 Ollama (本地模型)
	// ============================================================
	fmt.Println("=== Creating Agent 2: local-agent (Ollama) ===")
	agent2, err := agent.New("local-agent").
		WithLLM(llmOllama).
		WithConfig(cfg).
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent2: %v", err)
	}

	// ============================================================
	// Agent 3 - 使用默认 LLM (从配置的第一个 provider)
	// ============================================================
	fmt.Println("=== Creating Agent 3: default-agent (default) ===")
	agent3, err := agent.New("default-agent").
		WithLLM(llmDefault).
		WithConfig(cfg).
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent3: %v", err)
	}

	// ============================================================
	// 展示 Agent 信息
	// ============================================================
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("Agent Info:")
	fmt.Println(strings.Repeat("=", 50))

	info1 := agent1.Info()
	fmt.Printf("Agent 1: %s | Model: %s | BaseURL: %s\n", info1.Name, info1.Model, info1.BaseURL)

	info2 := agent2.Info()
	fmt.Printf("Agent 2: %s | Model: %s | BaseURL: %s\n", info2.Name, info2.Model, info2.BaseURL)

	info3 := agent3.Info()
	fmt.Printf("Agent 3: %s | Model: %s | BaseURL: %s\n", info3.Name, info3.Model, info3.BaseURL)

	// ============================================================
	// 串行执行测试
	// ============================================================
	ctx := context.Background()
	testQuery := "你好，请用一句话介绍你自己"

	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("Serial Execution:")
	fmt.Println(strings.Repeat("=", 50))

	// 测试 Agent 1
	fmt.Println("\nTesting Agent 1 (MiniMax)...")
	result1, err := agent1.Run(ctx, testQuery)
	if err != nil {
		log.Printf("Agent 1 error: %v", err)
	} else {
		fmt.Printf("Agent 1 Result: %s\n", result1.FinalResult)
	}

	// 测试 Agent 2
	fmt.Println("\nTesting Agent 2 (Ollama)...")
	result2, err := agent2.Run(ctx, testQuery)
	if err != nil {
		log.Printf("Agent 2 error: %v", err)
	} else {
		fmt.Printf("Agent 2 Result: %s\n", result2.FinalResult)
	}

	// 测试 Agent 3
	fmt.Println("\nTesting Agent 3 (default)...")
	result3, err := agent3.Run(ctx, testQuery)
	if err != nil {
		log.Printf("Agent 3 error: %v", err)
	} else {
		fmt.Printf("Agent 3 Result: %s\n", result3.FinalResult)
	}

	// ============================================================
	// 并行执行测试
	// ============================================================
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("Parallel Execution (Goroutines):")
	fmt.Println(strings.Repeat("=", 50))

	var wg sync.WaitGroup
	var mu sync.Mutex

	// 并行执行结果
	parallelResults := make(map[string]string)
	parallelErrors := make(map[string]error)

	queries := []string{
		"你好，请用一句话介绍你自己",
		"今天天气怎么样？",
		"帮我写一首小诗",
	}

	// 为每个 agent 创建一个 goroutine
	agents := []*agent.Service{agent1, agent2, agent3}
	agentNames := []string{"MiniMax", "Ollama", "default"}

	for i, ag := range agents {
		wg.Add(1)
		go func(a *agent.Service, name string, query string) {
			defer wg.Done()

			fmt.Printf("\n[%s] Starting: %s\n", name, query)

			result, err := a.Run(ctx, query)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				parallelErrors[name] = err
				fmt.Printf("[%s] Error: %v\n", name, err)
			} else {
				parallelResults[name] = fmt.Sprintf("%s", result.FinalResult)
				fmt.Printf("[%s] Done!\n", name)
			}
		}(ag, agentNames[i], queries[i])
	}

	// 等待所有 goroutine 完成
	wg.Wait()

	// 打印结果
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("Parallel Results:")
	fmt.Println(strings.Repeat("=", 50))

	for i, name := range agentNames {
		if err, ok := parallelErrors[name]; ok {
			fmt.Printf("%s: Error - %v\n", name, err)
		} else if result, ok := parallelResults[name]; ok {
			fmt.Printf("%s (%s): %s\n", name, agents[i].Info().Model, result)
		}
	}

	// 释放 LLM clients
	pool.ReleaseLLM(llmMiniMax)
	pool.ReleaseLLM(llmOllama)
	pool.ReleaseLLM(llmDefault)
}
