// Package main demonstrates explicit prompt-cache breakpoints and, more
// importantly, how to tell whether they did anything.
//
// An agent loop resends its whole conversation every round, so the prompt is
// the part of the bill that grows with the length of a run. Providers handle
// that in one of two ways:
//
//   - OpenAI and DeepSeek cache automatically. The only lever is keeping the
//     prompt prefix byte-stable between rounds, which the framework already
//     does. Leave WithPromptCache off.
//   - Anthropic — directly, or behind an OpenAI-compatible gateway — caches
//     only what is explicitly marked. Without a marker every round re-pays for
//     the entire history. Turn WithPromptCache on.
//
// Which one you are talking to is not something a model name reveals, so it is
// configured, not guessed. What the setting actually achieved is then read
// back from the provider's own numbers rather than assumed: CacheWriteTokens
// is non-zero only when a breakpoint was really established, and
// CachedPromptTokens counts what a later round got at a discount.
//
// Usage:
//
//	go run examples/prompt-cache/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	svc, err := agent.New("prompt-cache").
		// Safe to leave on against a provider that ignores or rejects the
		// markers: a rejection is retried once without them, and a provider
		// that ignores them is no worse off than with caching off.
		WithPromptCache(true).
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// Two turns in one session. The first pays to establish the cache; the
	// second is the one that should come back cheaper, because its prefix —
	// system prompt, tool schemas, and the whole first exchange — is already
	// warm behind the breakpoints.
	sessionID := "prompt-cache-demo"
	questions := []string{
		"In two sentences, what is a prompt cache?",
		"And in two more, why does it matter to an agent loop specifically?",
	}

	for i, q := range questions {
		result, err := svc.Run(ctx, q, agent.WithSessionID(sessionID))
		if err != nil {
			log.Fatalf("turn %d: %v", i+1, err)
		}
		fmt.Printf("\n── turn %d ───────────────────────────────\n", i+1)
		fmt.Printf("Q: %s\nA: %s\n", q, result.Text())
		if u := result.Usage; u != nil {
			fmt.Printf("prompt tokens: %d (of which cached: %d), cache writes: %d\n",
				u.PromptTokens, u.CachedPromptTokens, u.CacheWriteTokens)
		} else {
			fmt.Println("this provider reported no token usage — nothing to measure here")
		}
	}

	fmt.Println()
	fmt.Println("Reading the numbers:")
	fmt.Println("  cache writes > 0 on turn 1     → the breakpoints were honoured")
	fmt.Println("  cached > 0 and rising on turn 2 → the second turn reused them")
	fmt.Println("  both zero on every turn         → the endpoint ignores the markers;")
	fmt.Println("                                    turn WithPromptCache off, it is buying nothing")
}
