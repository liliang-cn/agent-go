package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/pkg/services"
	"github.com/spf13/cobra"
)

var (
	llmProvider string
	llmPrompt   string
	llmStream   bool
)

// llmCmd represents the LLM command group
var llmCmd = &cobra.Command{
	Use:   "llm",
	Short: "LLM operations - language model interactions",
	Long:  `Commands for interacting with language models through various providers.`,
}

// llmChatCmd handles chat interactions
var llmChatCmd = &cobra.Command{
	Use:   "chat [message]",
	Short: "Chat with an LLM",
	Long:  `Send a message to an LLM and get a response.`,
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		message := strings.Join(args, " ")

		ctx := context.Background()

		// Get global pool service
		poolService := services.GetGlobalPoolService()
		if !poolService.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		// Execute generation (streaming or non-streaming) using top-level Chat APIs
		chatOpts := services.ChatOptions{
			Provider:  strings.TrimSpace(llmProvider),
			MaxTokens: 30000,
			// SessionID: "cli-session", // Optional: could add a flag for this
		}

		if llmStream {
			err := poolService.StreamChat(ctx, message, chatOpts, func(chunk string) {
				fmt.Print(chunk)
			})
			if err != nil {
				return fmt.Errorf("streaming chat failed: %w", err)
			}
			fmt.Println()
		} else {
			resp, err := poolService.Chat(ctx, message, chatOpts)
			if err != nil {
				return fmt.Errorf("chat failed: %w", err)
			}
			fmt.Println(resp)
		}

		return nil
	},
}

// llmListCmd lists available models
var llmListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available LLM models",
	RunE: func(cmd *cobra.Command, args []string) error {
		if Cfg == nil {
			return fmt.Errorf("configuration not loaded")
		}

		fmt.Println("🤖 Available LLM Providers")
		fmt.Println("==========================")

		// List LLM pool providers
		for _, p := range Cfg.LLM.Providers {
			fmt.Printf("\n📦 %s\n", p.Name)
			fmt.Printf("   URL: %s\n", p.BaseURL)
			fmt.Printf("   Model: %s\n", p.ModelName)
			fmt.Printf("   Capability: %d/5\n", p.Capability)
			fmt.Printf("   Max Concurrency: %d\n", p.MaxConcurrency)
		}

		fmt.Printf("\n⚙️  Strategy: %s\n", Cfg.LLM.Strategy)

		return nil
	},
}

func init() {
	// Add subcommands
	llmCmd.AddCommand(llmChatCmd)
	llmCmd.AddCommand(llmListCmd)

	// Chat flags
	llmChatCmd.Flags().StringVarP(&llmProvider, "provider", "p", "", "LLM provider to use")
	llmChatCmd.Flags().BoolVarP(&llmStream, "stream", "s", false, "Stream the response")
}
