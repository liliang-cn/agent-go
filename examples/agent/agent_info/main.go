package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

func main() {
	// 加载配置
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// ============================================================
	// 创建一个最小可运行的 Agent
	// ============================================================
	fmt.Println("=== Creating Simple Agent ===")

	agentSvc, err := agent.New("info-agent").
		WithConfig(cfg).
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	ctx := context.Background()

	// ============================================================
	// 展示 Agent 基本信息
	// ============================================================
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("Agent Information")
	fmt.Println(strings.Repeat("=", 60))

	info := agentSvc.Info()

	fmt.Printf("\n📋 Basic Info:\n")
	fmt.Printf("   ID:      %s\n", info.ID)
	fmt.Printf("   Name:    %s\n", info.Name)
	fmt.Printf("   Status:  %s\n", info.Status)
	fmt.Printf("   Debug:   %v\n", info.Debug)

	fmt.Printf("\n🤖 LLM Config:\n")
	fmt.Printf("   Model:   %s\n", info.Model)
	fmt.Printf("   BaseURL: %s\n", info.BaseURL)

	fmt.Printf("\n⚡ Features:\n")
	fmt.Printf("   RAG Enabled:    %v\n", info.RAGEnabled)
	fmt.Printf("   PTC Enabled:    %v\n", info.PTCEnabled)
	fmt.Printf("   Memory Enabled: %v\n", info.MemoryEnabled)
	fmt.Printf("   MCP Enabled:    %v\n", info.MCPEnabled)
	fmt.Printf("   Skills Enabled: %v\n", info.SkillsEnabled)

	// ============================================================
	// 展示可用工具
	// ============================================================
	fmt.Printf("\n🔧 Available Tools (%d):\n", len(info.Tools))
	for i, tool := range info.Tools {
		fmt.Printf("   %d. %s\n", i+1, tool)
	}

	// ============================================================
	// 展示 MCP 服务器信息 (如果启用)
	// ============================================================
	if info.MCPEnabled && agentSvc.MCP != nil {
		fmt.Printf("\n🌐 MCP Servers:\n")
		servers := agentSvc.MCP.ListServers()
		for _, server := range servers {
			status := "❌ Stopped"
			if server.Running {
				status = "✅ Running"
			}
			fmt.Printf("   - %s: %s (Tools: %d)\n", server.Name, status, server.ToolCount)
		}
	}

	// ============================================================
	// 展示 Skills 信息 (如果启用)
	// ============================================================
	if info.SkillsEnabled && agentSvc.Skills != nil {
		fmt.Printf("\n🎯 Skills:\n")
		skillsList, err := agentSvc.Skills.ListSkills(ctx, skills.SkillFilter{})
		if err != nil {
			fmt.Printf("   Error loading skills: %v\n", err)
		} else {
			fmt.Printf("   Total: %d skills\n", len(skillsList))
			for i, skill := range skillsList {
				if i >= 5 { // 只显示前5个
					fmt.Printf("   ... and %d more\n", len(skillsList)-5)
					break
				}
				enabled := "✅"
				if !skill.Enabled {
					enabled = "❌"
				}
				fmt.Printf("   %s %s: %s\n", enabled, skill.ID, skill.Description)
			}
		}
	}

	// ============================================================
	// 简单问答测试
	// ============================================================
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("Simple Ask Test")
	fmt.Println(strings.Repeat("=", 60))

	fmt.Printf("\n📊 Status: %s\n", agentSvc.Status())
	fmt.Printf("📊 IsRunning: %v\n", agentSvc.IsRunning())
	fmt.Println("\n💬 Question: 你好，请用一句话介绍你自己")

	answer, err := agentSvc.Ask(ctx, "你好，请用一句话介绍你自己")
	if err != nil {
		fmt.Printf("❌ Ask failed: %v\n", err)
		return
	}

	fmt.Printf("✅ Answer: %s\n", answer)

	// 再次检查状态
	fmt.Printf("\n📊 Status after run: %s\n", agentSvc.Status())
	fmt.Printf("📊 IsRunning after run: %v\n", agentSvc.IsRunning())

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("Done!")
	fmt.Println(strings.Repeat("=", 60))
}
