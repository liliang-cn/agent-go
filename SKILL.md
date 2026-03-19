---
name: agent-go
description: Use AgentGo for local RAG (Retrieval Augmented Generation) operations including document ingestion, semantic search, and Q&A. Supports multi-provider LLM, MCP tools, Skills, and cognitive memory (Hindsight/PageIndex). Use when building local knowledge bases, AI assistants with evolving long-term memory.
license: MIT
metadata:
  author: liliang-cn
  version: "2.50.0"
  github: https://github.com/liliang-cn/agentgo
---

# AgentGo - Local RAG System with Cognitive Memory

AgentGo is a **local-first RAG + Agent framework** that features an evolving memory layer. It transitions from raw data points to high-level insights via autonomous reflection.

## When to Use

- Build local knowledge base from documents
- AI assistants with **evolving long-term memory** (Hindsight)
- Reasoning-based retrieval for long documents (PageIndex)
- Q&A systems backed by your own data
- AI agents with MCP tools and Skills
- Multi-provider LLM (OpenAI, Anthropic, Ollama)

## Quick Start

### As a Library

```go
package main

import (
    "context"
    "fmt"
    "github.com/liliang-cn/agent-go/pkg/agent"
)

func main() {
    ctx := context.Background()

    svc, _ := agent.New("my-agent").
        WithPrompt("You are a helpful assistant.").
        WithRAG().
        WithMemory(). // Enables Cognitive Memory (Facts -> Observations)
        WithDebug(false).
        Build()
    defer svc.Close()

    // Conversational chat with memory recall
    result, _ := svc.Chat(ctx, "I am a Go developer.")
    fmt.Println(result.Text())

    // Memory visibility: check what the agent "knows" and why
    for _, m := range result.Memories {
        fmt.Printf("[%s] %s (Confidence: %.2f)\n", m.Type, m.Content, m.Confidence)
    }
}
```

### CLI Usage

```bash
# Ingest documents
agentgo ingest ./documents/

# Chat with memory transparency
agentgo chat "What do you know about my projects?" --show-memory
```

## Core Features

### 1. Cognitive Memory (Hindsight)
AgentGo doesn't just store facts; it learns patterns.
- **Facts**: Atomic data from chat.
- **Observations**: Synthesized insights from multiple facts via `Reflect()`.
- **Traceability**: Track the evolution from raw fact to high-level belief.

### 2. Reasoning Retrieval (PageIndex)
For massive documents or when embeddings are weak:
- **Index Navigator**: LLM-driven tree search over hierarchical summaries.
- **Hybrid Search**: Parallel Vector + Index reasoning fused via RRF.

### 3. RAG Operations
Ingest docs → chunk → embed → SQLite vector store → hybrid search.

### 4. Multi-Provider LLM
OpenAI, Anthropic, Ollama, DeepSeek, and Azure.

### 5. Skills & MCP
Extensible via YAML+Markdown Skills or standard MCP tool servers.

## Architecture

```
┌──────────────────────────────────────────────┐
│                AgentGo System                   │
├──────────────────────────────────────────────┤
│  RAG Store │ LLM Pool │ Cognitive Memory     │
│       ┌────┴──────────┴────────┐             │
│       │      Agent Service     │ ← Reflect   │
│       │  ToolRegistry (Unified)│   Engine    │
│       │  Index Navigator       │ ← PageIndex │
│       └────────────────────────┘             │
└──────────────────────────────────────────────┘
```

## Builder Pattern

```go
svc, _ := agent.New("name").
    WithRAG().
    WithMemory().
    WithMemoryReflect(5).      // Auto-reflect after 5 new facts
    WithMemoryHybrid().       // Enable Vector + Index search
    WithMemoryBank("mission", []string{"directive1"}).
    Build()
```

## Invocation API

| Method | Session | Cognitive Memory |
|---|---|---|
| `Ask(ctx, p)` | No | Implicit recall |
| `Chat(ctx, p)` | Yes | Fact extraction + Recall |
| `Run(ctx, g)` | Optional | Full evolution + Reflect |

## See Also

- [API Reference](references/API.md) - Detailed API
- [Architecture](references/ARCHITECTURE.md) - Design decisions
- [Configuration](references/CONFIG.md) - Config options
