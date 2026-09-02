# AgentGo

[![CI](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml/badge.svg)](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liliang-cn/agent-go/v3.svg)](https://pkg.go.dev/github.com/liliang-cn/agent-go/v3)
[![Go Report Card](https://goreportcard.com/badge/github.com/liliang-cn/agent-go/v3)](https://goreportcard.com/report/github.com/liliang-cn/agent-go/v3)
[![Release](https://img.shields.io/github/v/release/liliang-cn/agent-go)](https://github.com/liliang-cn/agent-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**一个 Go 库,提供一条可以放心跑很久的 agent 循环。**

AgentGo 不是聊天封装,也不是编排产品。它是**一条**流式循环——组装上下文、调用模型、执行工具、检查答案、终止——agent 能做的一切都以工具的形式进入这条循环;而 prompt 保证不了的事(必须存在的文件、必须发出的邮件、用户禁止的工具)由运行时强制执行,而不是靠多写几句提示。围绕这条循环的,是让一次运行能撑几个小时而不只是几分钟的部件:会话与可回放的检查点、上下文压缩、持久化的计划、重试、成本上限,以及一个把同一个任务跨多次运行推进下去的 supervisor。

没有 CLI、没有 UI、没有服务器。你把 `pkg/agent` 嵌进自己的程序。

[English](README.md)

## 安装

```bash
go get github.com/liliang-cn/agent-go/v3
```

需要 Go 1.25+。

## 快速开始

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

不写 `WithLLM` 时,provider 从 `AGENTGO_HOME`(默认 `~/.agentgo`)的 `data/agentgo.db` 读取。`examples/quickstart` 是配置驱动的版本。

所有入口跑的都是同一条循环:

| 调用 | 返回 | 用途 |
| --- | --- | --- |
| `svc.Ask(ctx, q)` | `(string, error)` | 一问一答 |
| `svc.Chat(ctx, q)` | `*ExecutionResult` | 多轮,保留会话状态 |
| `svc.Stream(ctx, q)` | `<-chan string` | 逐 token 输出 |
| `svc.Run(ctx, goal, opts...)` | `*ExecutionResult` | 带工具和运行选项的目标 |
| `svc.RunStream(ctx, goal)` / `RunStreamWithOptions(ctx, goal, opts...)` | `<-chan *Event` | 全部运行时事件:状态、工具调用、工具结果、增量输出、检查点 |
| `svc.RunSegments(ctx, goal, LongRunConfig{...})` | `*LongRunResult` | 一个需要跑很多次的任务 |

`Run` 就是 `RunStream` 加一个收集器。没有第二套非流式实现。

## 七个概念

`pkg/agent` 里刻意只有七样东西。放不进这七样的改动,多半不属于这里。

| 概念 | 是什么 |
| --- | --- |
| **Agent** | 名字、系统提示、模型、工具集、可选的子 agent——`agent.New(name).With...().Build()` |
| **Loop** | 一条流式状态机:组装上下文 → 调模型 → 执行工具 → 检查答案 → 终止。`Runtime.loop` 是唯一实现;子 agent 是同一个 service 上的子 runtime |
| **Tool** | agent 能做的一切:内置工具、你的 Go 函数、MCP 服务器、skill、子 agent。每个工具声明 `ReadOnly` / `ConcurrencySafe` / `Destructive` / `InterruptBehavior`,批处理、权限和取消都据此工作 |
| **Context** | 模型每一轮看到的东西:消息组装、按任务过滤的历史、近期/远期切分、压缩、skill 提示、召回的记忆 |
| **Hooks + Lints** | 确定性层。hook 包住整次运行和每次工具调用;lint 拒绝一个最终答案并强制重试 |
| **Session + Checkpoint** | 会话 UUID 拥有一段对话;每个终态写一个可回放的检查点,长跑还能每 N 轮写一个 |
| **Events** | 唯一的输出通道。observer、活动日志、UI 读的都是同一条流 |

没有 team、dispatcher、router、handoff 或角色层级。组合就是 `WithSubagents(...)`,它给模型一个工具:`task(agent_name, prompt)`。

## 一次运行怎么结束

运行有多种结局,只有一部分是错误:

```go
result, err := svc.Run(ctx, goal)
switch {
case err != nil:                       // 运行时本身失败
case result.Cancelled:                 // 有人按了停止;err 是 nil
case result.Blocked:                   // agent 无法继续;result.Text() 说明原因
case result.StopReason == agent.StopReasonMaxTurns:
	// 轮次预算用完;Success 可能仍为 true,因为运行时用手头的东西
	// 合成了一个答案——这和真正完成不是一回事
case result.Success:
}
```

`StopReason` 区分 `end_turn`、`max_turns`、`max_tokens`、`max_budget_usd`、`refusal`、`lint_exhausted`、`stop_hook`、`error_during_execution` 和 `cancelled`。`result.Usage` 是 provider 报告的 token 用量,含缓存命中拆分;`result.EstimatedCostUSD` 是它的价格。

## 能力

### 工具

注册一个 Go 函数,schema 就是一个 JSON-Schema map:

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

内置工具覆盖网页搜索、URL 抓取、日期时间解析、scratchpad 计划、工具搜索,以及挂上沙箱(`WithSandbox`)后沙箱内的 shell 与文件访问和交付物扫描。工具按名字排序后才给模型,因为工具 schema 位于 prompt 前缀里,前缀每轮变动会让 provider 的 prompt 缓存失效。

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

子 agent 是同一个 `Service` 上的子 runtime:自己的会话、更窄的工具面、不同的事件出口。它只返回最终答案,事件嵌套向上冒泡。配置了子 agent 也会暴露通用的委派工具(`delegate_to_subagent`、`delegate_async`、`subagent_send_message`);没配的 agent 不会看到它们。`WithDelegation(bool)` 可以强制开关。可运行的变体:`examples/subagent/`。

### 记忆

```go
svc, _ := agent.New("assistant").WithMemory().Build()

svc.Chat(ctx, "My name is Alice and I prefer short answers.")
result, _ := svc.Chat(ctx, "What do you know about me?")
```

记忆的两侧每一轮都跑,各只有一个开关:`WithMemoryRetrieval(false)` 和 `WithMemoryAutoStore(false)`(都是 `MemoryOption`)。自动存储由模型判断一次这一轮值不值得记;两侧都不会去看用户的措辞来做决定。

内置 `store_type`:`file`(不需要 embedder)、`cortex`、`memoryflow`、`graphflow`(`WithGraphMemory()`,需要 `WithEmbedder`)。另有两个插件:`cortex-remote`(经 gRPC 的共享 CortexDB)和 `mcp-memory`(任何带记忆工具的 MCP 服务器,完全由 `tool.*` / `arg.*` / `result.*` 选项映射——不假设任何工具名,也没有同义词表)。

自己的后端,按框架还替你做多少事从多到少排:

1. `agent.MustRegisterMemoryStore("redis", factory)`,然后 `WithMemoryStoreType("redis")`。注册是严格的:重名和内置名都是错误。
2. `WithMemory(agent.WithMemoryStore(myStore))`——注入实例。
3. `WithMemoryService(mySvc)`——替换整个服务;检索和注入策略归你。

`domain.MemoryStore` 有十八个方法;嵌入 `memory.BaseStore`,只实现你的后端能提供的——其余返回 `ErrMemoryStoreUnsupported`,调用方据此降级而不是失败。可运行:`examples/memory-custom-store`、`examples/memory-remote-cortex`、`examples/memory-mcp`。

### 运行记忆

`RunMemory` 在一次运行的两端接入外部长期记忆:召回把一段 "Recalled context" 注入系统提示(有界:5 秒、约 1 万字符、失败只记日志);捕获在运行结束后进行,绝不阻塞运行。

```go
type RunMemory interface {
	RecallForRun(ctx context.Context, goal string) (string, error)
	CaptureRun(ctx context.Context, goal, finalText string) error
}

svc, _ := agent.New("ops").
	WithRunMemory(cortexbridge.NewRunMemory(cortexDB)). // 或你自己的实现
	Build()
```

`pkg/cortexbridge.NewRunMemory` 是 CortexDB 实现。演示:`examples/graph-memory-experiment`。

### MCP、skill、RAG

```go
svc, _ := agent.New("assistant").
	WithMCP(agent.WithMCPConfigPaths("./mcpServers.json")). // MCP 服务器的工具
	WithSkills().                                          // AGENTGO_HOME/skills 下的 SKILL.md 工作流
	WithEmbedder(embedder).WithRAG().                      // 文档检索;只在有 embedder 时生效
	Build()
```

skill 不会全部塞进 prompt:运行时每轮通过 `<skill-discovery>` 提示只浮现一小撮相关的,并激活对应的 `skill_*` 工具。RAG 处处可选——没有 embedding 模型的安装照样有 agent、工具、MCP 和文件记忆。可运行:`examples/mcp/basic`、`examples/mcp/advanced`。

### 结构化输出

```go
type Brief struct {
	Ticker    string   `json:"ticker"    desc:"uppercase stock symbol"`
	KeyPoints []string `json:"key_points" desc:"3-4 short, factual takeaways"`
}

brief, err := agent.RunTyped[Brief](ctx, svc, "Summarize NVDA.")
```

`RunTyped[T]` 从结构体推导 JSON Schema 并返回解析好的 `T`;`WithStructuredOutput(spec)` / `WithStructuredOutputType[T]()` 是对应的运行选项。provider 支持时走原生 `response_format`(带回退),另有一条校验 lint 在不匹配时重新提示。可运行:`examples/structured-output`。

## 确定性层

当模型反复犯同一个错,解法不是再往 prompt 里加一句,而是写一条运行时强制执行的 lint:

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

lint 不通过就追加结构化反馈并重新提示,受重试预算约束;预算耗尽运行被阻塞。每个 service 自带四条:`no_planning_only_finish`、`file_task_must_write`、`non_empty_final_answer` 和 `task_delivery_contract`(目标点名了交付动作,就必须真的调用过对应工具**而且**这样的工具确实可用)。`LintContext` 同时带着"跑了什么"和"本可以跑什么",所以 lint 能分清"有能力却没用"和"根本没有"。

**硬约束在运行时,不在 prompt 里。** 用户禁止用工具,就一个都不给——工具列表清空,模型硬发的调用被拒并回注结构化反馈。一次运行必须交付什么,在循环开始时由一次温度 0、prompt 不含关键词的小型结构化调用解析一次。它从不靠匹配目标里的短语来判断,所以对每种语言表现一致。自己声明约束,这次调用就跳过:

```go
svc.Run(ctx, "Name the largest planet.", agent.WithToolsDisabled())
svc.Run(ctx, goal, agent.WithRequiredDeliverables(
	agent.DeliverableRequirement{Kind: "email", Description: "the summary"}))
svc.Run(ctx, goal, agent.WithConstraintExtraction(false)) // 完全关闭
```

这条规则是普遍的:**框架里任何地方都没有一张硬编码的短语或正则表,去读用户的请求并改变行为。** 排序(工具搜索、BM25)可以读请求;判决不行。对*模型输出*的检查——上面的 lint、拒答检测、只列计划的结尾——是输出侧,确定性检查就该在那里。

## 扩展

一个关注点常常同时碰到好几个接缝。PII 处理要遮蔽工具结果、拒绝泄露的最终答案、还想出现在运行的遥测里——三个接口、三次注册,却没有任何东西说明它们是一体的。`Extension` 就是这个捆绑:

```go
svc, _ := agent.New("support").
	WithExtensions(
		logging.New(os.Stderr), // pkg/extensions/logging — 活动日志
		pii.New(),              // pkg/extensions/pii     — 遮蔽工具结果、检查答案
		usage.New(),            // pkg/extensions/usage   — 按模型计 token,能定价的定价
	).
	Build()
```

扩展实现 `Name()` 和它需要的那几个可选能力;`Build()` 用类型断言逐个识别,接到对应的接缝上。扩展按列出的顺序在每个接缝运行。

| 能力 | 接缝 | 能做什么 |
| --- | --- | --- |
| `Observer` | 模型轮次、工具调用、重试、压缩、检查点、分段 | 看 |
| `OutputLint` | 最终答案 | 拒绝并强制重试 |
| `Module` | 工具注册表 | 加工具 |
| `ContextContributor` | 第一轮之前 | 追加系统消息——只能追加,不能改写 goal |
| `ToolCallFilter` | 工具执行前 | 改写参数,或带理由拒绝(模型看得到理由) |
| `ToolResultFilter` | 工具执行后 | 替换模型看到的结果;出错则关闭式失败 |
| `RunLifecycle` | 运行开始 / 结束 | 第一轮前否决一次运行;看到每次运行的结局 |
| `Lifecycle` | `Build()` / `Close()` | 打开和释放资源,按顺序启动、反序停止 |
| `HookProvider` | 任意 `HookEvent` | 兜底口:`stop`、`pre_compact`、子 agent 事件 |

这刻意不是中间件链。没有 `next()`:扩展不能包住循环、跳过阶段、或自己调模型——这正是"一条循环"能保持一条的原因。一个 `Service` 同时跑很多任务,每个扩展被所有任务共享,所以它的方法必须能并发调用;自带的三个都满足,`go test -race` 覆盖了十二个运行同时穿过同一个扩展全部接缝的情况。可运行:`examples/extensions`。

也不一定非得是 Go。`pkg/extensions/exec` 把插件当子进程跑,用 stdio 上的 JSON 行协议(协议 1,握手声明能力)把同一组接缝问过去:`exec.New("redact", []string{"python3", "plugins/redact.py"})` 就是一个普通的 `agent.Extension`。每一种失败都是那个接缝自己的关闭式答案——`after_tool` 超时或断管,模型就看不到那个结果;`before_tool` 拒绝调用;`lint` 拒绝答案;`run_start` 阻塞运行。协议细节在 `docs/extensions.md` 的 "Out-of-process plugins" 一节,可运行:`examples/extensions-exec`(自带 Python 参考插件)。

任何人都能写:它就是你自己模块里的一个 Go 类型,框架不需要知道它的存在。能力接口和它们的参数类型就是扩展 API,跟随模块的语义化版本。`pkg/extensiontest` 用脚本化的模型建一个真实的 service,让扩展在循环真正调用的接缝上被测试,背后不需要真模型。[docs/extensions.md](docs/extensions.md) 是契约;`examples/extensions-thirdparty` 是一个独立模块里的完整扩展。

## 长时程运行

必须跑几个小时的运行,不是一次更长的运行,而是很多次运行;框架的构造方式是让"活下来"所需的部件成为运行时的职责,而不是调用方的。

### 一个任务,多次运行

```go
result, err := svc.RunSegments(ctx,
	"Work through this in steps. Keep a scratchpad plan, and when you finish a "+
		"step record what it produced as its note — that note is all the next "+
		"stretch of work will have to go on.",
	agent.LongRunConfig{
		MaxSegments:            40,
		RoundsPerSegment:       60,
		MaxConsecutiveFailures: 3,
		MaxTotalCostUSD:        5,
		MaxDuration:            8 * time.Hour,
	})
if result.Done() { /* LongRunStopFinished:任务本身完成了 */ }
```

`RunSegments` 不是第二套引擎:它调 `Run`,读运行为什么停,再调 `Run`。每一段拿到**新的会话**——这正是让上下文不随一整天的任务持续增长的原因——而一个**任务 id** 贯穿所有段,检查点和计划因此保持一致。跨段传递的是真正确立下来的东西:计划(经 `PlanStore` 持久化,有 store 时默认 SQLite)、工作区、运行记忆。

两道门决定一段是*完成了任务*而不只是结束了:它的 `StopReason` 不是 `max_turns`,并且(除非 `AllowIncompletePlan`)存下的计划没有未勾选的步骤。`LongRunStop` 与 `StopReason` 刻意是两个类型:"任务完成了"和"supervisor 不再继续问了"是两句不同的话。supervisor 还会因 `consecutive_failures`、`time_limit`、`cost_limit`、`blocked`、`cancelled`、`segment_budget_exhausted` 和 `unproductive_segments`(用完轮次却什么都没改的段,失败预算是看不见它们的)而停下。`RunSegmentsStream` 是同一件事,把事件通道也暴露出来。

### 长跑需要运行时提供什么

| 问题 | 机制 |
| --- | --- |
| 轮次预算 | `AutonomyProfile.MaxRounds` / `WithMaxTurns`;撞上预算的运行报告 `max_turns`,不是成功 |
| provider 抖动 | `WithLLMRetries(n)`——502、限流、地区拒绝会重试;仅仅提到 "timeout" 的永久性 4xx 不会,`context.Canceled` 永远不会 |
| 截断 | 会推理的模型可能在写出一个字节前就花光输出预算。`finish_reason=length` 的空轮次是独立的结局,预算会被提高再问一次(有上限),而不是被判成拒答 |
| 上下文增长 | 到 token 阈值(默认 6 万,连工具参数一起数)就压缩,摘要放不下时摘要预算自动上调 |
| prompt 缓存 | `WithPromptCache(true)` 在前缀和历史尾部放显式断点;另外系统上下文和工具列表跨轮保持字节级稳定,隐式缓存才能命中 |
| 崩溃 | `AutonomyProfile.CheckpointEveryRounds` 在运行进行中就写快照;`ResumeFromCheckpoint` 用同一个任务 id 从最新快照继续 |
| 钱 | `WithMaxBudgetUSD` 限单次运行,`MaxTotalCostUSD` 限整个任务。价格来自 `pool.RegisterModelPricing`(你的)覆盖内置表(可能过时);没有价格的模型报告为*未知*而不是免费,运行会警告一次 |
| 重复调用 | 完全相同的重复工具调用会被答以"结果未变"——而任何改变状态的调用和压缩都会清空这份记录,因为写之后的重读是另一次读 |

`examples/long-run` 把单次运行配置到能跑几小时;`examples/segmented-run` 把一个任务跨很多段推进。

### 看着它跑

进行中的运行默认几乎不可见:对话结束时才进 store,事件只给调用 `RunStream` 的人。`agent.NewActivityLog(w)` 是一个把运行叙述成扁平可 grep 行的 `Observer`——每个模型轮次、工具调用、子 agent、重试、压缩、检查点各一行:

```go
logf, _ := os.Create("run.log")
svc, _ := agent.New("worker").WithObserver(agent.NewActivityLog(logf)).Build()
```

凡是不能交互式盯着的运行都挂上它。模型自己汇报的进度不是证据,日志才是。

## 取消与生命周期

停止是一种结局,不是错误。`Cancel()` 停掉 service 上所有运行,`CancelRun(id)` 停一个(用 `WithRunID` 命名),`CancelSession(id)` 停一段对话;`ActiveRuns()` 列出在飞的。运行以 `workflow_cancelled` 终止、写检查点以便续跑,返回 `result.Cancelled = true` 且 error 为 nil。三者在声明了 `InterruptBehavior: block` 的工具执行中途都会等待。定时执行(`pkg/scheduler`)有自己的按次注册表,停一次运行不碰定时器。可运行:`examples/cancel`。

一个 `Service` 只拥有一个长期资源:它的 store。`Close()` 幂等、取消在飞运行,之后所有入口都以 `ErrServiceClosed` 失败——指向已关闭 service 的调度器不可能继续跑那些历史悄悄写失败的轮次。历史写入失败是 ERROR 日志加一个 `EventTypeError` 事件,不是没人看的 warning。

## 任务、检查点与回放

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

resumed, _ := manager.Tasks().ResumeFromCheckpoint(ctx, task.ID,
	agent.CheckpointResumeOptions{FollowUp: "and now also do X"})
```

`Manager` 是应用层的宿主,不是编排器:它拥有 store,按名字缓存一个 `Service`,暴露任务接口。每个终态写一个 `TaskCheckpoint`(按任务封顶并修剪);`WithResumeMessages` 是回放底下的运行选项。任务历史、计划、记忆、发现的工具都按 `task_id` 划分范围。可运行:`examples/task-store`。

## 运行选项

按次传给 `Run` / `RunStreamWithOptions` / `RunSegments`:

| 选项 | 效果 |
| --- | --- |
| `WithMaxTurns(n)` | 本次运行的轮次预算 |
| `WithMaxTokens(n)` / `WithTemperature(t)` / `WithThinking(bool)` | 采样;`WithThinking(false)` 能降低工具密集型运行的延迟 |
| `WithLLMRetries(n)` | 放弃前重试 provider 的瞬时错误 |
| `WithMaxBudgetUSD(x)` | 估算花费超预算即停止 |
| `WithToolsDisabled()` / `WithToolAllowlist(names)` / `WithToolDenylist(names)` | 工具面 |
| `WithStructuredOutput(spec)` / `WithStructuredOutputType[T]()` | 强制 JSON 形状 |
| `WithRequiredDeliverables(...)` / `WithRequestedActions(...)` / `WithConstraintExtraction(bool)` | 交付契约 |
| `WithSessionID` / `WithTaskID` / `WithRunID` / `WithParentTaskID` / `WithPlanKey` | 身份、谱系、用哪份计划 |
| `WithResumeMessages(msgs)` / `WithPriorToolCalls(names)` | 从历史继续 |
| `WithInputParts(...)` / `WithInputImages(paths...)` | 多模态输入 |
| `WithAutoCompaction(threshold, keep)` / `WithoutAutoCompaction()` | 压缩策略 |
| `WithTenant(id)` | 标记这次运行属于谁,用于限额、取消和计费 |
| `WithDebug(bool)` | 单次运行的详细日志 |

Builder 选项:`WithLLM`、`WithEmbedder`、`WithConfig`、`WithPrompt` / `WithSystemPrompt`、`WithMemory` / `WithGraphMemory` / `WithMemoryService`、`WithRunMemory`、`WithMCP`、`WithSkills`、`WithRAG`、`WithSubagents`、`WithDelegation`、`WithLengthLimits`、`WithSandbox`、`WithAutonomy`、`WithPlanStore`、`WithTaskStore`、`WithPromptCache`、`WithExtensions`、`WithMaxConcurrentRuns`、`WithMaxRunsPerTenant`、`WithTimezone`、`WithBackgroundTasks`、`WithTool(s)`、`WithObserver`、`WithProgress`、`WithDBPath`、`WithDebug`,以及装低频旋钮的 `WithOptions(agent.Options{...})`(权限策略、工具执行策略、必需 skill、额外模块、observer)。

## Provider

`pkg/providers` 说 OpenAI 兼容协议——OpenAI、DeepSeek、Ollama、LM Studio、vLLM、DashScope/Qwen 和大多数网关:

```go
llm, _ := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
	BaseURL:  "http://localhost:11434/v1", // Ollama
	LLMModel: "qwen3",
})
```

provider 的怪癖在库里处理:拒绝固定 `tool_choice` 的推理模型、`response_format` 回退、`reasoning_content`、被拆开的流式工具调用增量、流式不报 usage 的服务器。可选请求字段(`web_search_options`、`tool_choice`、缓存标记)遵循同一个形状:发送、识别拒绝、剥掉、重试一次。原生网页搜索是**探测出来的,从不假设**:被拒证明不支持,响应里的 grounding 证据证明支持,仅仅被接受什么都证明不了——没有按模型名的能力表。

**用量与缓存计量。** 每个 `domain.GenerationResult` 带 provider 报告的 `Usage`:`PromptTokens`、`CompletionTokens`、`CachedPromptTokens`(缓存命中部分,按深折扣计费)和 `CacheWriteTokens`。provider 没报告时为 nil,这是诚实的未知而不是零。如果一次历史每轮都在增长的运行里缓存 token 读数为零,先弄清是缓存坏了还是报告坏了,再对循环下结论。

**多 provider 池。** `pool.NewPool` 在多个 provider 间负载均衡(轮询、随机、最少负载;按 provider 限并发;故障转移),并实现 generator 接口,可直接放进 `WithLLM`。`pkg/providers.LLMPool` 是 `domain.LLMProvider` 实例之上的底层池。

## 可观测性

实现 `agent.Observer`(嵌入 `agent.BaseObserver`,只覆盖需要的)并用 `WithObserver` 注册。回调:`OnModelStart/Delta/End`、`OnToolStart/End`、`OnSubAgentStart/End`、`OnCheckpoint`、`OnLint`、`OnModelRetry`、`OnCompaction`、`OnError`、`OnSegment`——带稳定的 span 和 call id 用于配对。模型轮次内的每次重试都有自己的回调,所以试了三次的轮次不会看起来和一次的一样。

```go
type usage struct{ agent.BaseObserver }

func (u *usage) OnModelEnd(ctx context.Context, info agent.ModelInfo, res *agent.ModelResult, err error) {
	if res != nil {
		log.Printf("round=%d tokens=%d cached=%d dur=%dms",
			info.Round, res.TokensUsed, res.CachedTokens, res.DurationMs)
	}
}
```

`pkg/otelobserver` 把全部回调桥到 OpenTelemetry:模型轮次/工具/子 agent 是 span,lint、重试、压缩、错误、分段是 span 事件;配上 `WithMeterProvider` 还有 metrics——调用数、token(含缓存拆分)、成本、重试、lint 拒绝。

**运行前。** `svc.Preview(ctx, goal, opts...)` 返回第一轮模型会收到的消息和工具,不调模型、不写任何东西:"它到底会看到什么"的干跑。

**运行中。** `agent.NewActivityLog(w)` 给人看;`agent.NewTraceWriter(w)` 把同一批事件写成 JSONL 给程序看,每行带 run/task/session id。循环写的每条日志也带这三个 id,`log.SetLogger` 能把框架的日志接到你自己的 `slog` handler 上。

**进程自己。** 上面的回调讲的都是 **agent 做了什么**,`agent.SampleProcess()` 讲的是 **程序在用什么**——存活堆、堆对象数、goroutine 数、GC、当前与峰值 RSS、累计 CPU、运行时长,不需要 cgo、不引入依赖。每次工具调用漏一个 goroutine,过不了任何 lint 也过不了任何测试;它在凌晨三点被 OOM killer 杀掉,而在那之前每一个 agent 指标都很健康。

```go
type watch struct{ agent.BaseObserver }

// 可选接口:每轮一次读数,加上每条终止路径上的最后一次。
// 没有 observer 要,就一次都不采样。
func (watch) OnResourceSample(_ context.Context, s agent.ResourceSample) {
	log.Printf("round=%d heap=%d goroutines=%d rss=%d",
		s.Round, s.Stats.HeapAllocBytes, s.Stats.Goroutines, s.Stats.RSSBytes)
}
```

同一批读数会以每轮一行 `"event":"resource"` 进入 `TraceWriter`——长跑任务的内存曲线就活在那里;`pkg/otelobserver` 则把它们发布成可观测量表(`agentgo.process.heap.bytes`、`.goroutines`、`.rss.bytes`、`.cpu.seconds` 等),**服务空闲、没有任何运行在跑的时候照样上报**,而那正是泄漏最容易看出来的时刻。某个平台读不到的数,报成"未知"而不是 0:0 看起来像一个不占内存的进程。

**一切之前。** `agent.Doctor(ctx, ...)` 检查一个 home——数据库、provider、记忆存储类型、MCP 配置、skills——报告哪里不对、怎么修,不调模型。它就是被删掉的 CLI 里 status 命令的替身。可运行:`examples/preview`、`examples/trace`、`examples/otel`、`examples/doctor`、`examples/resources`。

## 时间是相对说话那一刻的

1 号写下的"明天要去医院",说的是 2 号。2 号召回时,文本里still写着"明天"——没有锚点的模型会把复查安排到 3 号。这句话没错,它只是在说一个已经过去的日子。

`pkg/timeaware` 用模型解决它,**没有任何短语表**。`明天 → +1`、`tomorrow → +1` 这样的表只服务于有人想到要枚举的那几种语言,对其他所有人**静默地什么都不做**——而这看起来和"这段文字里没有日期"一模一样。

```go
svc, _ := agent.New("assistant").
	WithTimezone(shanghai).   // 用户所在的时区,不是服务器的
	Build()
```

- **写入时解析,零额外成本。** 时间字段搭在记忆写入本来就要发的那次抽取调用上,跑在后台 worker 里——agent 的这一轮**一步都不等它**。`timeaware.SchemaFields()` 和 `PromptRules(anchor)` 可以把同一份契约嫁接到你自己已有的结构化调用上;`Resolver.Resolve` 是独立路径,一次调用解一整批文本。
- **读取时一次调用都没有。** 每条召回的记忆都带上 `(written 2026-09-01, yesterday; "明天" = 2026-09-02, today)`,纯粹由两个时间戳算出来。
- **本地时间是最容易错的一处。** 锚点在交给模型之前先转成用户所在时区,提示词同时给出偏移量**和** IANA 时区名(偏移量表达不了夏令时规则),日历天的比较会把两端都先转过去——东京的 23:30 是维也纳同一天下午的 16:30。
- **降级是设计的一部分。** 没有模型、超时、答案解不开,记忆就保持未解析状态,但仍然带着"写于何时",这在任何语言里都成立。可运行:`examples/timeaware`。

## 后台任务

有些活人不会站着等:一次爬取、一次构建、一份跨一周日志的报告。让模型等着,对话就停住了,轮次预算也烧在一个只是慢的工具上。

宿主一直能起后台任务,现在 **agent 自己也能**:

```go
svc, _ := agent.New("assistant").
	WithBackgroundTasks(4).   // 给它 background_start / _check / _cancel
	Build()

task, _ := svc.StartBackgroundTask(ctx, goal, agent.WithBackgroundLabel("crawl"))
// …之后,在另一轮里
if t, ok := svc.BackgroundTask(task.ID); ok && t.Status.Done() {
	fmt.Println(t.Result)
}
```

它**不继承调用方的 context**——这正是它和子 agent 的根本区别:子 agent 跑在父运行的 context 下,父死它就死。它仍然是同一个 loop:同一个 Service 上的另一次运行,有自己的 session 和 run id,所以所有 observer、lint、hook、扩展对它一视同仁;它继承调用方的租户,`CancelTenant` 能够到它。`Close` 会先取消并排空还在跑的任务,再释放存储。

工具默认关闭,因为一个后台任务就是一整次运行、有自己的预算;而 `background_check` **绝不**返回还在跑的任务的结果——半截答案是它能返回的最误导人的东西。可运行:`examples/background`。

## 一个 Service 服务很多人

`Service` 一直就能同时跑很多任务。它缺的是**这次运行是谁的**——在共享的服务端上,这是"产品"和"事故"的区别。

```go
svc, _ := agent.New("support").
	WithMaxConcurrentRuns(64).  // 整个进程的上限
	WithMaxRunsPerTenant(4).    // 单个客户能占的份额
	Build()

res, err := svc.Run(ctx, goal, agent.WithTenant("acme"))
if errors.Is(err, agent.ErrTenantAtCapacity) {
	// 立刻拒绝,错误上带着数字——你自己决定丢弃、排队还是回 503
}
```

| 是什么 | 干什么 |
| --- | --- |
| `WithTenant(id)` | 给一次运行贴上不透明的归属标签 |
| `WithMaxConcurrentRuns(n)` / `WithMaxRunsPerTenant(n)` | 上限;0 = 不限,也是默认 |
| `Capacity()` | 在跑几个、上限多少、按租户怎么分 |
| `ActiveRunsForTenant(id)` / `CancelTenant(id)` | 看见和停掉某个客户的活 |
| `ActiveRun.Tenant` / `ExecutionResult.Tenant` | 把一次运行和它的花费归到人头上 |

这个标签**刻意不是**两样东西。它**不是身份**——身份仍然是 session UUID,记忆按 session 划域、历史按 task 过滤;**循环里没有任何地方读它**:一个能改变 agent 行为的租户字符串,就是用字符串匹配做配置。它只用于准入控制、批量取消和费用归属,别的一概不做。

准入是在**记录这次运行的同一把锁里**决定的,所以两个同时到达的调用者不可能都拿到最后一个名额;而且它**拒绝而不排队**:一个会把调用方无限期阻塞的库,是把容量问题变成了延迟谜题。可运行:`examples/multitenant`。

## 存储

```text
~/.agentgo/                  # 用 AGENTGO_HOME 覆盖
├── data/
│   ├── agentgo.db           # 配置、provider、agent、任务、检查点、计划(SQLite)
│   └── cortex.db            # 可选的记忆 / 向量 / 图存储
├── memories/                # 启用时的文件记忆
├── skills/                  # 本地 skill(SKILL.md)
└── workspace/               # agent 工作目录
```

身份就是会话 UUID。聊天和任务 API 里没有 user id。

## 仓库布局

```text
pkg/agent         框架本体:agent、循环、工具、上下文、hook/lint、扩展、会话、检查点、长跑
pkg/extensions    自带扩展:logging、pii、usage
pkg/extensiontest 用脚本化模型在真实循环里测试扩展
pkg/domain        共享类型:消息、生成结果、token 用量、provider 与 store 接口
pkg/providers     OpenAI 兼容 provider + LLMPool
pkg/pool          provider 池、token 估算、定价与成本
pkg/mcp           MCP 客户端、工具与服务器
pkg/memory        持久记忆服务 + BaseStore
pkg/cortexbridge  基于 CortexDB 的知识图谱 / RAG / RunMemory
pkg/rag           可选检索
pkg/skills        skill 加载与排序
pkg/sandbox       本地 / Docker 执行沙箱
pkg/scheduler     cron 式调度,按次取消
pkg/otelobserver  Observer -> OpenTelemetry
pkg/store         SQLite 存储、记忆存储插件
pkg/worktree      git worktree 辅助
eval/             行为评测框架:YAML 场景、mock 与 live runner
examples/         可运行示例,每个一个目录
```

## 开发

```bash
make check          # fmt + vet + test——发版门禁
go test -race ./pkg/agent/...
make eval           # 对脚本化 mock 模型跑行为评测,CI 安全
make eval-live      # 同一组场景对真实 provider
```

评测场景在 `eval/scenarios/`;一处 harness 改动(一条 lint、一次 prompt 削减、一处工具准备的调整)用 `make eval-live` 跑一遍并和上次的结果 JSON 做 diff 来验证。`CLAUDE.md` 记录了架构决策和只有长时间 soak 才挖得出来的 bug——改循环之前先读它。

## 许可证

MIT
