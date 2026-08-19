# AgentGo

[![CI](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml/badge.svg)](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liliang-cn/agent-go/v3.svg)](https://pkg.go.dev/github.com/liliang-cn/agent-go/v3)
[![Go Report Card](https://goreportcard.com/badge/github.com/liliang-cn/agent-go/v3)](https://goreportcard.com/report/github.com/liliang-cn/agent-go/v3)
[![Release](https://img.shields.io/github/v/release/liliang-cn/agent-go)](https://github.com/liliang-cn/agent-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**本地优先的 Go Agent 框架。**

AgentGo 是一个 Go 库，用于构建本地运行、会用工具、有记忆、可互相组合的 agent。核心在 `pkg/agent`：一条透明的流式循环，一切能力皆是工具，确定性靠 lint 和运行时契约保证，而不是靠往 prompt 里加字。没有强制的 CLI、UI 或 server——它是拿来嵌入的。

[English](README.md)

## 安装

```bash
go get github.com/liliang-cn/agent-go/v3
```

需要 Go 1.25+。

## 快速开始

注入任意 OpenAI 兼容 provider，直接问：

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/providers"
)

func main() {
	llm, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		BaseURL:  "https://api.deepseek.com/v1",
		APIKey:   os.Getenv("DEEPSEEK_API_KEY"),
		LLMModel: "deepseek-chat",
	})
	if err != nil {
		log.Fatal(err)
	}

	svc, err := agent.New("assistant").
		WithLLM(llm).
		WithPrompt("You are a concise Go assistant.").
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	reply, err := svc.Ask(context.Background(), "What is AgentGo?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(reply)
}
```

不调用 `WithLLM` 时，配置从 `AGENTGO_HOME`（默认 `~/.agentgo`）加载，provider 存在 `data/agentgo.db` 里。配置驱动的版本见 `examples/quickstart`。

四个入口，底下是同一条循环：

- `svc.Ask(ctx, q)` — 单次问答，返回 `(string, error)`。
- `svc.Chat(ctx, q)` — 带 session 的多轮对话，返回 `*ExecutionResult`。
- `svc.Stream(ctx, q)` — 逐 token 输出（`<-chan string`）。
- `svc.RunStream(ctx, goal, opts...)` — 完整运行时事件：状态更新、工具调用、工具结果、增量文本。`Run()` 就是 `RunStream()` 加一个收集器。

## 核心概念

- **Agent** — 有名字的运行时，带指令、工具、记忆和 session，由 `agent.New(name).With...().Build()` 组装。
- **循环** — 一个流式状态机。子 agent 复用同一条循环，事件向上冒泡、回答被 lint、终态被 checkpoint。
- **工具** — agent 能做的一切：内置工具、你的函数、MCP server、skill、子 agent。
- **子 agent** — 用 `WithSubagents(...)` 注册，模型通过唯一的 `task(agent_name, prompt)` 工具触达。没有单独的 team/dispatcher/router 层。
- **Task** — 一等执行单元，带状态、事件、frame、checkpoint 和输出，落在 SQLite 里。
- **Memory** — 持久的本地上下文，和 cache、RAG 分开。后端可插拔。
- **Output lint** — 输出后的确定性检查，违反就让模型重答，而不是在 prompt 里写"请记住……"。

## 能力

### 自定义工具

注册一个 Go 函数，schema 就是普通的 JSON-Schema map：

```go
svc.AddTool("read_config", "Read a service's current configuration",
	map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"service": map[string]interface{}{"type": "string"},
		},
	},
	func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return loadConfig(args["service"].(string))
	})
```

内置工具包括 web 搜索、URL 抓取、时间、scratchpad，以及（挂了 sandbox 时的）命令执行和交付物扫描。

### 子 agent

```go
svc, _ := agent.New("lead").
	WithSubagents(
		agent.SubagentSpec{
			Name:         "researcher",
			Description:  "Gathers and summarises background information.",
			Instructions: "You research a topic and return a tight, factual brief.",
		},
		agent.SubagentSpec{
			Name:         "writer",
			Description:  "Turns notes into finished prose.",
			Instructions: "You write clear, plain prose from the notes you are given.",
		},
	).
	Build()

result, _ := svc.Run(ctx, "Research X and write two paragraphs about it.")
```

各种变体（basic、parallel、async、auto-delegation、filtering）见 `examples/subagent/`。

### 记忆

```go
svc, _ := agent.New("assistant").WithMemory().Build()

svc.Chat(ctx, "My name is Alice and I prefer short answers.")
result, _ := svc.Chat(ctx, "What do you know about me?")
```

内置 `store_type`：`file`（不需要 embedder）、`cortex`、`memoryflow`、`graphflow`（`WithGraphMemory()`，需要 `WithEmbedder`）。另有两个随框架发布的插件：`cortex-remote`（gRPC 访问共享 CortexDB）和 `mcp-memory`（任何带记忆工具的 MCP server，用 `tool.*` / `arg.*` / `result.*` 选项做映射——不预设任何工具名）。

接入自己的后端有三个接缝，框架代劳的程度依次递减：

1. `agent.MustRegisterMemoryStore("redis", factory)` — 按名字注册，之后用 `agent.WithMemoryStoreType("redis")` 选择。注册并发安全且严格：重名是错误，绝不静默覆盖。
2. `WithMemory(agent.WithMemoryStore(myStore))` — 直接注入实例。
3. `WithMemoryService(mySvc)` — 换掉整个服务，检索和注入策略都归你。

`domain.MemoryStore` 有十八个方法；嵌入 `memory.BaseStore`，只覆盖后端真能实现的那些——其余返回 `memory.ErrMemoryStoreUnsupported`，调用方按降级处理而不是报错。

可运行示例：`examples/memory-custom-store`、`examples/memory-remote-cortex`、`examples/memory-mcp`。

### Run memory（自动召回与捕获）

`RunMemory` 在一次 run 的开始和结束挂钩外部长期记忆系统。召回结果以 "Recalled context" 段注入 system prompt（有边界：5 秒超时、约 1 万字符上限、失败只记日志）；捕获在 run 结束后异步执行——run 永远不会被自己的记忆卡住。

```go
type RunMemory interface {
	RecallForRun(ctx context.Context, goal string) (string, error)
	CaptureRun(ctx context.Context, goal, finalText string) error
}

svc, _ := agent.New("ops").
	WithRunMemory(cortexbridge.NewRunMemory(cortexDB)). // 或你自己的实现
	Build()
```

`pkg/cortexbridge.NewRunMemory` 是 CortexDB 实现：把 `DECISION:` 这类标记行和实体存进类型化图谱，供后续 run 召回。端到端演示：`examples/graph-memory-experiment`。

### MCP

```go
svc, _ := agent.New("assistant").
	WithMCP(agent.WithMCPConfigPaths("./mcpServers.json")).
	Build()
```

Server 在 `mcpServers.json` 里声明（仓库根目录有样例），它们的工具和内置工具并列注册。可运行示例：`examples/mcp/basic`、`examples/mcp/advanced`。

### Skills

`WithSkills()` 从 `AGENTGO_HOME/skills`（或 `agent.WithSkillsPaths(...)`）加载可复用的 Markdown/YAML 工作流。`Options.RequiredSkills` 让 `Build()` 在指定 skill 未安装时直接失败。

### RAG

可选的文档检索，只有配置了 embedder 才进入主路径：

```go
svc, _ := agent.New("assistant").
	WithEmbedder(embedder). // 例如 providers.NewOpenAIEmbedderProvider(...)
	WithRAG().
	Build()
```

### 结构化输出

```go
type Brief struct {
	Ticker    string   `json:"ticker"    desc:"uppercase stock symbol"`
	KeyPoints []string `json:"key_points" desc:"3-4 short, factual takeaways"`
}

brief, err := agent.RunTyped[Brief](ctx, svc, "Summarize NVDA.")
```

`RunTyped[T]` 从结构体反射出 JSON Schema（tag 决定字段名、`desc`、可选性），返回解析好的 `T`。手写 schema 用 `agent.WithStructuredOutput(spec)` 这个 `RunOption`。两层保证：支持的 provider 走原生 `response_format`（被拒时自动回退），另有确定性 lint 校验最终文本、不合规就重答。可运行示例：`examples/structured-output`。

### Output lint

Agent 反复犯同一个错时，别再往指令里加句子——注册一个 lint：

```go
svc.RegisterOutputLint(agent.LintFunc{
	NameValue: "no_planning_only_finish",
	Fn: func(text string, ctx agent.LintContext) (bool, string) {
		if strings.HasSuffix(strings.TrimSpace(text), "Next steps:") {
			return false, "response reads like a plan; deliver the work or call task_blocked"
		}
		return true, ""
	},
})
```

每个 `Build()` 出来的服务自带内置 lint：`no_planning_only_finish`、`file_task_must_write`、`non_empty_final_answer`、`task_delivery_contract`（目标点名了交付动作——发邮件、写文件——就必须真调过对应工具才能算完成）。运行时用每 run 一次的小结构化调用理解用户要什么，不做短语匹配，因此各种语言下行为一致。自己声明则跳过这次调用：

```go
result, _ := svc.Run(ctx, "Name the largest planet.", agent.WithToolsDisabled())

result, _ = svc.Run(ctx, goal, agent.WithRequiredDeliverables(
	agent.DeliverableRequirement{Kind: "email", Description: "the summary"},
))

result, _ = svc.Run(ctx, goal, agent.WithConstraintExtraction(false))
```

被 block 的 run 是一种结果而不是错误：`result.Err()` 保持 nil，`result.Text()` 是 agent 的解释——用 `result.Blocked` 分支。

### Task、checkpoint 与重放

```go
store, _ := agent.NewStore("agentgo.db")
manager := agent.NewManager(store)
_ = manager.SeedDefaultAgent()

task, _ := manager.Tasks().Submit(ctx, agent.TaskSubmitOptions{
	SessionID: "demo-session",
	AgentName: "Assistant",
	Input:     "Check the current repository status.",
})
done, _ := manager.Tasks().Await(ctx, task.ID)
```

每个终态都会写一份 `TaskCheckpoint`。崩溃或被取消的 task 可以从最近的快照重放：

```go
resumed, _ := manager.Tasks().ResumeFromCheckpoint(ctx, task.ID, agent.CheckpointResumeOptions{
	FollowUp: "and now also do X",
})
```

底层的 `RunOption` 是 `agent.WithResumeMessages`。

### Sandbox 与调度

`pkg/sandbox` 提供隔离执行环境（本地进程和 Docker）；`WithSandbox(sb)` 挂上后启用命令执行和交付物工具。`pkg/scheduler` 跑 cron 风格的定时任务，executor 可插拔。`pkg/worktree` 是零依赖的 git worktree 辅助。

## Run 选项

按次传给 `Run` / `RunStream`：

| 选项 | 作用 |
| --- | --- |
| `WithMaxTurns(n)` | 限制循环轮数 |
| `WithTemperature(t)` / `WithMaxTokens(n)` | 采样参数 |
| `WithThinking(bool)` | 开关 provider 侧思维链（DeepSeek reasoner 的 `thinking.type` 形状）；工具密集的 run 上关掉能明显降延迟 |
| `WithToolsDisabled()` | 一个工具都不给；模型硬要调也会被拒绝 |
| `WithToolAllowlist(names)` / `WithToolDenylist(names)` | 收缩工具面 |
| `WithStructuredOutput(spec)` / `WithStructuredOutputType[T]()` | 强制 JSON 形状 |
| `WithRequiredDeliverables(...)` / `WithRequestedActions(...)` | 声明交付契约 |
| `WithConstraintExtraction(bool)` | 开关每 run 的约束抽取调用 |
| `WithSessionID(id)` / `WithTaskID(id)` / `WithRunID(id)` / `WithParentTaskID(id)` | 身份与谱系 |
| `WithResumeMessages(msgs)` | 从历史继续（checkpoint 重放） |
| `WithInputParts(...)` / `WithInputImages(paths...)` | 多模态输入 |
| `WithMaxBudgetUSD(x)` | 预估花费超预算即停 |
| `WithAutoCompaction(threshold, keep)` / `WithoutAutoCompaction()` | 上下文压缩策略 |
| `WithDebug(bool)` | 单次 run 的详细日志 |

Builder 侧选项（`agent.New(...).With...`）：`WithLLM`、`WithEmbedder`、`WithConfig`、`WithPrompt` / `WithSystemPrompt`、`WithMemory` / `WithGraphMemory` / `WithMemoryService`、`WithRunMemory`、`WithMCP`、`WithSkills`、`WithRAG`、`WithSubagents`、`WithSandbox`、`WithAutonomy`、`WithTool(s)`、`WithObserver`、`WithProgress`、`WithDBPath`、`WithDebug`，低频开关走 `WithOptions(agent.Options{...})`（权限策略、工具执行策略、必需 skill、扩展模块、observer）。

## Provider

`pkg/providers` 走 OpenAI 兼容 API，覆盖 OpenAI、DeepSeek、Ollama、LM Studio、vLLM、DashScope/Qwen 及多数代理：

```go
llm, _ := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
	BaseURL:  "http://localhost:11434/v1", // Ollama
	LLMModel: "qwen3",
})
```

Provider 的各种怪癖在库里处理，不用你操心：DeepSeek reasoner 拒绝钉死的 `tool_choice`、`response_format` 自动回退、DeepSeek/Ollama 的 `reasoning_content`、流式工具调用被拆成多个 delta、流式响应不带 usage 的 server。

**用量与缓存计量。** 每个 `domain.GenerationResult` 都带 provider 上报的 `Usage`（`domain.TokenUsage`）：`PromptTokens`、`CompletionTokens`、`CachedPromptTokens`——后者是 prompt 缓存命中的部分，按深度折扣计费（OpenAI 约 0.5x，DeepSeek 约 0.26x）。两家都是自动缓存；运行时会保持上下文前缀跨轮字节级稳定，让命中真正发生。Provider 没报时 `Usage` 为 nil。

**多 provider 池。** `pool.NewPool` 在多个 provider 间做负载均衡，支持选择策略、每 provider 并发上限和能力等级；池本身实现 generator 接口，可直接塞进 `WithLLM`：

```go
brain, _ := pool.NewPool(pool.PoolConfig{
	Enabled:  true,
	Strategy: pool.StrategyRoundRobin,
	Providers: []pool.Provider{
		{Name: "fast", BaseURL: base1, Key: key1, ModelName: "deepseek-chat", MaxConcurrency: 5},
		{Name: "local", BaseURL: "http://localhost:11434/v1", ModelName: "qwen3"},
	},
})
svc, _ := agent.New("assistant").WithLLM(brain).Build()
```

`pkg/providers.LLMPool` 是更底层的 `domain.LLMProvider` 池（round-robin / random / least-load 策略、故障转移、健康检查）。

## 可观测性

实现 `agent.Observer`（嵌入 `agent.BaseObserver`，只覆盖需要的回调），用 `WithObserver` 或 `Options.Observers` 注册。回调发生在模型 / 工具 / 子 agent / checkpoint 的接缝上，带稳定的 span 和 call ID 用于配对 start/end：

```go
type usage struct{ agent.BaseObserver }

func (u *usage) OnModelEnd(ctx context.Context, info agent.ModelInfo, res *agent.ModelResult, err error) {
	if res != nil {
		log.Printf("round=%d tokens=%d cached=%d dur=%dms",
			info.Round, res.TokensUsed, res.CachedTokens, res.DurationMs)
	}
}
```

`ModelResult.CachedTokens` 是 `TokensUsed` 里缓存命中的部分——命中部分大幅打折，只看 `TokensUsed` 会高估成本。`pkg/otelobserver` 把同一套回调桥接到 OpenTelemetry span（`otelobserver.New(tracerProvider)`）。

## 存储

默认布局：

```text
~/.agentgo/
├── data/
│   ├── agentgo.db     # 配置、provider、agent、task、checkpoint
│   └── cortex.db      # 可选的记忆/向量/图存储
├── memories/          # 启用文件记忆时
├── skills/            # 本地 skill
└── workspace/         # agent 工作目录
```

用 `AGENTGO_HOME` 环境变量覆盖主目录。

## 仓库结构

```text
pkg/agent         框架核心：agent、循环、工具、上下文、hook/lint、session、checkpoint、run memory
pkg/domain        共享类型：消息、生成结果、token 用量、provider 接口
pkg/providers     OpenAI 兼容 provider + LLMPool（故障转移、健康检查）
pkg/pool          provider 池 + token/成本核算
pkg/poolsvc       进程级全局 embedder 池服务
pkg/mcp           MCP 客户端、工具与 server
pkg/memory        持久记忆服务 + BaseStore
pkg/cortexbridge  CortexDB 后端的知识图谱 / RAG / RunMemory
pkg/rag           可选检索
pkg/skills        skill 加载
pkg/sandbox       本地 / Docker 执行沙箱
pkg/scheduler     cron 风格任务调度
pkg/otelobserver  Observer -> OpenTelemetry 桥
pkg/store         SQLite 存储
pkg/worktree      git worktree 辅助
eval/             行为评测（scenarios + runner）
examples/         可运行示例
```

## 开发

```bash
make test           # go test ./...
make check          # fmt + vet + test
make eval           # 行为评测，mock LLM，CI 安全
make eval-live      # 行为评测，真实 provider
```

工程路线和运维指引见 `CLAUDE.md` 和 `docs/dev/PLAN.md`。

## License

MIT
