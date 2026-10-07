# AgentGo

[![CI](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml/badge.svg)](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liliang-cn/agent-go/v3.svg)](https://pkg.go.dev/github.com/liliang-cn/agent-go/v3)
[![Go Report Card](https://goreportcard.com/badge/github.com/liliang-cn/agent-go/v3)](https://goreportcard.com/report/github.com/liliang-cn/agent-go/v3)
[![Release](https://img.shields.io/github/v/release/liliang-cn/agent-go)](https://github.com/liliang-cn/agent-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

一个跑 agent 循环的 Go 库：工具、记忆、MCP、技能、会话、检查点、输出校验、长任务。没有 CLI、没有 UI、没有服务端——把 `pkg/agent` 嵌进你自己的程序。

![架构](docs/architecture.png)

[English](README.md)

## 安装

```bash
go get github.com/liliang-cn/agent-go/v3
```

Go 1.25+。

## 使用

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

func main() {
	llm, err := pool.NewClient("deepseek", "https://api.deepseek.com/v1", os.Getenv("DEEPSEEK_API_KEY"), "deepseek-v4-flash")
	if err != nil {
		log.Fatal(err)
	}

	svc, err := agent.New("assistant").
		WithLLM(llm).
		WithPrompt("你是一个简洁的 Go 助手。").
		WithMemory(agent.WithMemoryStoreType("file")).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	reply, err := svc.Ask(context.Background(), "AgentGo 是什么？")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(reply)
}
```

不传 `WithLLM` 时，模型来自 `AGENTGO_HOME/data/agentgo.db` 里配置的 provider。

| 调用 | 返回 | 用途 |
| --- | --- | --- |
| `svc.Ask(ctx, q)` | `(string, error)` | 一问一答 |
| `svc.Chat(ctx, q)` | `*ExecutionResult` | 多轮，保留会话 |
| `svc.Stream(ctx, q)` | `<-chan string` | 逐 token 输出 |
| `svc.Run(ctx, goal, opts...)` | `*ExecutionResult` | 带工具和运行选项的目标 |
| `svc.RunStream(ctx, goal)` / `RunStreamWithOptions` | `<-chan *Event` | 每一个运行时事件 |
| `svc.RunSegments(ctx, goal, LongRunConfig{...})` | `*LongRunResult` | 一个任务跨多次运行 |
| `svc.Steer(sessionID, msg)` / `SteerRun(runID, msg)` | `bool` | 往正在跑的 run 里塞一条消息；没有在跑的返回 false |
| `agent.NewStanding(svc, ...)` → `Add` / `Deliver` / `WakeNow` / `Status` | `*Standing` | 常驻职责：自己定时醒、按计划醒、事件唤醒、只读巡检 |

把记忆、知识图谱、联网搜索、计划接进宿主程序：[docs/getting-started.md](docs/getting-started.md)。可运行示例，每个功能一个目录：[`examples/`](examples/)。

## Builder 选项

| 选项 | 作用 |
| --- | --- |
| `WithLLM(gen)` / `WithConfig(cfg)` | 模型，或一份列出 provider 的配置 |
| `WithPrompt(s)` / `WithSystemPrompt(s)` | 系统提示词 |
| `WithMemory(opts...)` / `WithGraphMemory()` / `WithMemoryService(svc)` | 持久记忆；`store_type` 可选 file、cortex、memoryflow、graphflow、cortex-remote、mcp-memory、mem0、qdrant、meilisearch、weaviate、surrealdb，或用 `RegisterMemoryStore` 注册 |
| `WithEmbedder(e)` / `WithRAG()` | 文档检索 |
| `WithMCP(opts...)` / `WithSkills()` | MCP 服务器的工具；SKILL.md 工作流 |
| `WithTool(s)(...)` / `AddToolWithMetadata` | 自定义工具；`ToolMetadata{ReadOnly, Destructive, OutputLimit, ...}` |
| `WithSubagents(specs...)` | 一个 `task(agent_name, prompt)` 工具 |
| `WithSandbox(sb)` / `WithAutonomy(profile)` | 工具在哪里跑；轮数预算 |
| `WithPlanStore(ps)` / `WithTaskStore(ts)` / `WithRunMemory(rm)` | 计划、任务、运行记忆的持久化 |
| `WithExtensions(...)` / `WithObserver(...)` | 接缝扩展：logging、pii、usage、bashguard、exec；观察者：ActivityLog、TraceWriter、OTel |
| `WithPromptCache(...)` / `WithToolOutputLimit(n)` | 提示缓存断点；单个工具结果上限 |
| `WithMaxConcurrentRuns(n)` / `WithMaxRunsPerTenant(n)` / `WithBackgroundTasks(max)` | 并发容量 |
| `WithDecisionEngine(e, conf)` / `WithTimezone(loc)` / `WithOptions(agent.Options{...})` | 其余 |

## 运行选项

| 选项 | 作用 |
| --- | --- |
| `WithMaxTurns(n)` / `WithMaxTokens(n)` / `WithTemperature(t)` / `WithThinking(bool)` | 预算与采样 |
| `WithMaxBudgetTokens(n)` | 可选的单次运行 token 预算（输入 + 输出）；整个任务用 `LongRunConfig.MaxTotalTokens`。0 = 不限 |
| `WithLLMRetries(n)` | provider 临时错误时重试 |
| `WithToolsDisabled()` / `WithToolAllowlist(names)` / `WithToolDenylist(names)` | 工具面 |
| `WithStructuredOutput(spec)` / `WithStructuredOutputType[T]()` | 强制 JSON 结构 |
| `WithRequiredDeliverables(...)` / `WithRequestedActions(...)` / `WithConstraintExtraction(bool)` | 交付契约 |
| `WithSessionID` / `WithTaskID` / `WithRunID` / `WithParentTaskID` / `WithPlanKey` / `WithTenant` | 身份、谱系、归属 |
| `WithModel(model, provider...)` | 这次运行用哪个模型 |
| `WithMemoryUser(id)` / `WithoutMemoryAutoStore()` | 谁的记忆；跳过自动写入 |
| `WithResumeMessages(msgs)` / `WithPriorToolCalls(names)` | 从历史继续 |
| `WithInputImages(paths...)` / `WithInputAudio` / `WithInputFiles` / `WithInputParts(...)` | 多模态输入 |
| `WithAutoCompaction(threshold, keep)` / `WithCompactionClipping(n)` / `WithoutAutoCompaction()` | 上下文压缩 |
| `WithDebug(bool)` | 单次运行的详细日志 |

## Provider

任何 OpenAI 兼容端点：OpenAI、DeepSeek、Ollama、vLLM、DashScope/Qwen、各类网关。

```go
llm, err := pool.NewPool(pool.PoolConfig{
	Enabled:  true,
	Strategy: pool.StrategyRoundRobin,
	Providers: []pool.Provider{{
		Name:            "deepseek",
		BaseURL:         "https://api.deepseek.com/v1",
		Key:             os.Getenv("DEEPSEEK_API_KEY"),
		ModelName:       "deepseek-v4-flash",
		NativeWebSearch: "none", // none | openai | dashscope | google_search
	}},
})
```

`pool.NewPool` 在多个 provider 间负载均衡。框架只报 token，不算钱：`ExecutionResult.Usage` 给出 prompt、completion 和缓存命中的 token 数，宿主要算成本就用自己的单价去乘。

## 存储

```text
~/.agentgo/                  # AGENTGO_HOME
├── data/agentgo.db          # 配置、provider、会话、任务、检查点、计划
├── data/cortex.db           # 可选的记忆 / 向量 / 图
├── data/memories/           # 文件记忆
├── skills/                  # SKILL.md
└── workspace/               # agent 工作目录
```

## 包

```text
pkg/agent         agent、循环、工具、上下文、hooks/lints、会话、检查点、长任务
pkg/domain        共享类型与接口
pkg/providers     OpenAI 兼容 provider、LLMPool
pkg/pool          provider 池、token 计数、上下文窗口、内置联网搜索
pkg/memory        记忆服务、BaseStore、memorystoretest
pkg/store         SQLite 存储、记忆后端插件
pkg/cortexbridge  CortexDB 工具、RunMemory
pkg/mcp           MCP 客户端与服务端
pkg/skills        技能加载与排序
pkg/rag           文档检索
pkg/sandbox       本地 / Docker 沙箱
pkg/scheduler     可按次取消的调度
pkg/extensions    logging、pii、usage、bashguard、exec
pkg/extensiontest 用真实循环测试扩展
pkg/otelobserver  Observer → OpenTelemetry
pkg/timeaware     存储文本里的相对时间
pkg/decision      廉价的封闭问题引擎
eval/             行为评测
examples/         可运行示例
```

## 开发

```bash
make check          # fmt + vet + test
go test -race ./pkg/agent/...
make eval           # 脚本化模型，CI 可跑
make eval-live      # 真实 provider
```

架构决策记在 `CLAUDE.md`。

## License

MIT
