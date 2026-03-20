// Package main demonstrates LongRun mode for autonomous agent operation
//
// LongRun mode enables the agent to run autonomously on a schedule,
// similar to OpenClaw's heartbeat daemon.
//
// Features:
//   - Heartbeat scheduling (configurable interval)
//   - HEARTBEAT.md checklist processing
//   - Task queue with persistence
//   - Approval gates for high-risk actions
//   - WebSocket Gateway for control (ws://127.0.0.1:18789/ws)
//   - Memory files (MEMORY.md, AGENTS.md, SOUL.md, TOOLS.md)
//   - Full integration with MCP tools and Skills
//
// Usage:
//
//	go run examples/agent/longrun/main.go
//	go run examples/agent/longrun/main.go --add-task "Check email for urgent messages"
//	go run examples/agent/longrun/main.go --status
//	go run examples/agent/longrun/main.go --memory
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

func main() {
	// Flags
	addTask := flag.String("add-task", "", "Add a task to the queue")
	status := flag.Bool("status", false, "Show LongRun status")
	showMemory := flag.Bool("memory", false, "Show memory files")
	interval := flag.Duration("interval", 30*time.Minute, "Heartbeat interval")
	enableMCP := flag.Bool("mcp", false, "Enable MCP tools")
	enableSkills := flag.Bool("skills", true, "Enable Skills")
	debug := flag.Bool("debug", false, "Enable debug mode")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Setup
	homeDir, _ := os.UserHomeDir()
	dbPath := filepath.Join(homeDir, ".agentgo", "data", "longrun_demo.db")
	workDir := filepath.Join(homeDir, ".agentgo", "longrun")
	skillsPath := filepath.Join(homeDir, ".agents", "skills")

	os.MkdirAll(filepath.Dir(dbPath), 0755)
	os.MkdirAll(workDir, 0755)

	// Create agent builder
	builder := agent.New("longrun-demo").
		WithDBPath(dbPath).
		WithDebug(*debug)

	// Add MCP if enabled
	if *enableMCP {
		builder = builder.WithMCP(agent.WithMCPConfigPaths("examples/mcpServers.json"))
	}

	// Add Skills if enabled
	if *enableSkills {
		builder = builder.WithSkills(agent.WithSkillsPaths(skillsPath))
	}

	// Build agent
	svc, err := builder.Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	defer svc.Close()

	// Create LongRun service using Builder pattern
	longRun, err := agent.NewLongRun(svc).
		WithInterval(*interval).
		WithWorkDir(workDir).
		WithMaxActions(3).
		WithApproval(true).
		Build()
	if err != nil {
		log.Fatalf("Failed to create LongRun service: %v", err)
	}

	// Handle commands
	if *addTask != "" {
		task, err := longRun.AddTask(ctx, *addTask, nil)
		if err != nil {
			log.Fatalf("Failed to add task: %v", err)
		}
		fmt.Printf("✅ Task added: %s (ID: %s)\n", task.Goal, task.ID)
		return
	}

	if *status {
		st := longRun.GetStatus()
		fmt.Println("--- LongRun Status ---")
		fmt.Printf("Running:     %v\n", st["running"])
		fmt.Printf("Last Run:    %v\n", st["last_run"])
		fmt.Printf("Interval:    %v\n", st["heartbeat_interval"])
		fmt.Printf("Pending:     %v tasks\n", st["pending_tasks"])
		fmt.Printf("Work Dir:    %v\n", st["work_dir"])

		// Show available tools
		fmt.Println()
		fmt.Println("--- Agent Capabilities ---")
		if svc.Skills != nil {
			skills, _ := svc.Skills.ListSkills(ctx, skills.SkillFilter{})
			fmt.Printf("Skills:      %d loaded\n", len(skills))
		}
		if svc.MCP != nil {
			tools := svc.MCP.GetAvailableTools(ctx)
			fmt.Printf("MCP Tools:   %d available\n", len(tools))
		}
		return
	}

	if *showMemory {
		files := longRun.GetMemory().List()
		fmt.Println("--- Memory Files ---")
		for _, f := range files {
			fmt.Printf("\n### %s ###\n", f.Name)
			fmt.Println(f.Content)
		}
		return
	}

	// Start LongRun service
	fmt.Println("=== LongRun Demo ===")
	fmt.Printf("Heartbeat Interval: %v\n", interval)
	fmt.Printf("Work Directory: %s\n", workDir)
	fmt.Println()

	// Show capabilities
	if svc.Skills != nil {
		skillList, _ := svc.Skills.ListSkills(ctx, skills.SkillFilter{})
		fmt.Printf("Skills:      %d loaded\n", len(skillList))
	}
	if svc.MCP != nil {
		tools := svc.MCP.GetAvailableTools(ctx)
		fmt.Printf("MCP Tools:   %d available\n", len(tools))
	}
	fmt.Println()
	fmt.Println("Press Ctrl+C to stop")
	fmt.Println()

	if err := longRun.Start(ctx); err != nil {
		log.Fatalf("Failed to start LongRun: %v", err)
	}

	// Wait for shutdown
	<-sigChan
	fmt.Println("\nShutting down...")
	longRun.Stop()
	fmt.Println("LongRun stopped")
}
