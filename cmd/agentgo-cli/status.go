package main

import (
	"context"
	"fmt"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check the status of LLM provider connections",
	Long:  `Check if configured LLM providers are available and can be connected to.`,
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Show config location
	fmt.Printf("📁 Config: %s\n", viper.ConfigFileUsed())
	if Cfg != nil {
		fmt.Printf("📁 Home: %s\n", Cfg.Home)
		fmt.Printf("📁 MCP Servers: %s\n", Cfg.MCPServersPath())
	}
	fmt.Println()

	poolService := services.GetGlobalPoolService()
	if !poolService.IsInitialized() {
		return fmt.Errorf("pool service not initialized")
	}

	fmt.Println("🔍 Checking LLM Pool status...")
	llmStatus := poolService.GetLLMStatus()
	if len(llmStatus) == 0 {
		fmt.Println("⚠️  No LLM providers configured")
	} else {
		for name, status := range llmStatus {
			fmt.Printf("\n📝 LLM Provider (%s):\n", name)
			if status.Healthy {
				fmt.Printf("   ✅ Healthy\n")
			} else {
				fmt.Printf("   ❌ Unhealthy\n")
			}
			fmt.Printf("   Model: %s\n", status.ModelName)
			fmt.Printf("   Capability: %d/5\n", status.Capability)
			fmt.Printf("   Active: %d/%d\n", status.ActiveRequests, status.MaxConcurrency)
		}
	}

	fmt.Println("\n🔍 Checking Embedding Pool status...")
	embedStatus := poolService.GetEmbeddingStatus()
	if len(embedStatus) == 0 {
		fmt.Println("⚠️  No Embedding providers configured")
	} else {
		for name, status := range embedStatus {
			fmt.Printf("\n🔢 Embedding Provider (%s):\n", name)
			if status.Healthy {
				fmt.Printf("   ✅ Healthy\n")
			} else {
				fmt.Printf("   ❌ Unhealthy\n")
			}
			fmt.Printf("   Model: %s\n", status.ModelName)
			fmt.Printf("   Capability: %d/5\n", status.Capability)
			fmt.Printf("   Active: %d/%d\n", status.ActiveRequests, status.MaxConcurrency)
		}
	}

	// Test actual connectivity
	fmt.Println("\n🔍 Testing connectivity...")
	client, err := poolService.GetLLM()
	if err != nil {
		return fmt.Errorf("failed to get LLM client: %w", err)
	}

	if err := client.Health(ctx); err != nil {
		fmt.Printf("❌ LLM health check failed: %v\n", err)
	} else {
		fmt.Printf("✅ LLM service is responsive\n")
	}
	poolService.ReleaseLLM(client)

	return nil
}
