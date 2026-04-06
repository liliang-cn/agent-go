# AgentGo

**面向 Agent / Team 的 Go 框架，内置本地优先 AI 能力。**

[English](README.md) · [API 参考](references/API.md) · [架构文档](references/ARCHITECTURE.md)

AgentGo 是一个 Go 框架，用于构建 Agent / Team 系统。providers、MCP、skills、memory、RAG、router 和 PTC 都是挂在同一套运行时核心上的能力层，CLI / UI 只是可选适配器。

```bash
go get github.com/liliang-cn/agent-go/v2
```

---

## 核心能力

| 能力            | 说明                                                                      |
| --------------- | ------------------------------------------------------------------------- |
| **RAG**         | 文档摄入 → 分块 → Embedding → SQLite 向量存储 → 混合检索                  |
| **Agent**       | 多轮推理循环，支持工具调用、规划和会话记忆                                |
| **Memory**      | **认知记忆层**: Hindsight 式演化 (事实 → 观察) + PageIndex 式推理导航     |
| **工具**        | MCP（模型上下文协议）、Skills（YAML+Markdown）、自定义内联工具            |
| **PTC**         | LLM 写 JavaScript，工具在 Goja 沙箱中运行 — 减少模型往返次数              |
| **Streaming**   | 逐 Token 通道输出；完整事件流可见工具调用过程                             |
| **多 Provider** | OpenAI、Anthropic、Azure、DeepSeek、Ollama — 运行时可切换                 |
| **Team**        | 持久化 captain / specialist、异步团队任务队列、运行态状态与跨进程任务追踪 |
| **Operator**    | 内置执行型 Agent，带文件系统/网页工具以及 PTY / coding-agent 会话能力     |

---

## 概念模型

理解 AgentGo，最简单的方法是把它看成 8 个协同子系统：

### 1. LLM

LLM 是执行核心，其他能力都围绕它组织。

- 它提供统一的生成接口，Agent、RAG、PTC、工具选择都基于它工作。
- Provider 通过全局 pool 在运行时切换。
- standalone agent、captain、specialist、built-in agent 最终都跑在同一套 LLM 抽象上。

可以理解为：`prompt + tools + policy -> model call`

### 2. RAG

RAG 是知识检索层。

- 负责文档摄入、分块、embedding、向量存储和检索。
- 查询时把相关上下文注入 Agent 或查询流程。
- 它解决的是“外部知识/项目知识”，不是长期用户偏好。

可以理解为：`documents -> retrieval context`

### 3. Memory

Memory 是系统内部的长期上下文层。

- 存储事实、偏好、观察和长期可复用的信息。
- 它和 cache 不同，也和 RAG 文档不同。
- 没有 embedder 时，文件记忆模式仍然可用。

可以理解为：`系统长期学到了什么`

### 4. MCP

MCP 是工具传输层。

- 统一了内置工具和外部工具的接入方式。
- AgentGo 默认始终带有内置 filesystem 和 websearch。
- 它是 agent 与文件、网页、外部操作能力交互的主要方式。

可以理解为：`agent 怎么接触外部世界`

### 5. Skills

Skills 是 Markdown/YAML 形式的可复用工作流。

- 它比原始工具更高层。
- 用来承载领域经验、步骤化流程和可复用操作规范。
- 可以配置为用户调用，也可以允许模型调用。

可以理解为：`可复用的专家工作流`

### 6. PTC

PTC（Programmatic Tool Calling）是程序化编排层。

- 模型不再一次只给一个工具调用，而是直接写 JavaScript 来编排工具。
- 它可以减少多步工具调用时的模型往返。
- 非常适合工具密集、需要流程控制和数据整理的任务。

可以理解为：`LLM 写出来的工具编排代码`

### 7. Agent

Agent 是基础运行单元。

- 它有自己的 instructions、工具能力、可选的 RAG / Memory / PTC / Skills，以及会话执行循环。
- Agent 可以是 built-in，也可以是用户自定义。
- 内置 standalone agents 当前包括 `Concierge`、`Assistant`、`Operator`、`Stakeholder`。

常见定位：

- `Assistant`：通用直接执行
- `Operator`：执行、验证、PTY 会话、coding-agent 调用
- `Stakeholder`：产品/业务视角
- `Concierge`：入口与编排

### 8. Team

Team 是建立在 Agent 之上的持久化团队层。

- 一个 team 有一个 `captain` 和多个 `specialists`
- `captain` 本质上仍然是 agent，只是带有团队编排规则
- captain 对实现类任务优先走异步 team work
- team task 状态会持久化，所以新的 CLI 进程也能继续查看和跟踪

可以理解为：`带队列和状态的持久化多 Agent 协作`

### API 形状

从 API 的角度看，这 8 个概念大致映射为：

- **LLM / Agent 运行时**
  - `Ask`, `Chat`, `Run`, `RunStream`
- **RAG**
  - `WithRAG`, `rag_query`，以及文档摄入/查询流程
- **Memory**
  - `WithMemory`, `memory_save`, `memory_recall`
- **MCP**
  - `WithMCP`，内置 filesystem/websearch，以及外部 MCP server
- **Skills**
  - `WithSkills`，skill 注册与调用
- **PTC**
  - `WithPTC`, `execute_javascript`, `callTool()`
- **Team**
  - `CreateTeam`, `JoinTeam`, `DispatchTask`, `SubmitTeamTask`, `GetTask`

实际分层可以简单记成：

`LLM -> tools/PTC -> Agent -> Team`

而 `RAG / Memory / MCP / Skills` 都是可以挂到 Agent 上的能力层。

---

## 快速开始

### 简单问答

```go
svc, _ := agent.New("assistant").
    WithPrompt("你是一个智能助手。").
    Build()
defer svc.Close()

reply, _ := svc.Ask(ctx, "什么是 Go 语言？")
fmt.Println(reply)
```

### 接入 RAG（文档知识库）

```go
svc, _ := agent.New("assistant").
    WithPrompt("根据提供的文档回答问题。").
    WithRAG().
    WithDBPath("~/.agentgo/data/agent.db").
    Build()
defer svc.Close()

// 一次性摄入
svc.Run(ctx, "摄入 ./docs/ 目录")

// 查询
reply, _ := svc.Ask(ctx, "规范文档里关于错误处理是怎么说的？")
```

### 带认知记忆的多轮对话

```go
svc, _ := agent.New("assistant").
    WithMemory().
    Build()
defer svc.Close()

svc.Chat(ctx, "我叫 Alice，在 Go 团队工作。")
reply, _ := svc.Chat(ctx, "我在哪个团队？")
// → "你在 Go 团队，Alice。" (通过向量与索引混合搜索召回)
```

### CLI 交互

运行带记忆可视化的交互式对话：

```bash
# 启动交互对话，并显示检索到的记忆和推理过程
go run ./cmd/agentgo-cli chat --show-memory

# 开启 JavaScript 沙箱用于处理复杂逻辑
go run ./cmd/agentgo-cli chat --with-ptc
```

运行 `Team` 工作流：

```bash
# 创建一个独立 Agent
agentgo agent add Scout --description "独立执行任务的特工" \
  --instructions "独立工作，直接回答，必要时使用工具。"

# 查看或更新 Agent
agentgo agent show Scout
agentgo agent update Scout --model openai/gpt-5-mini

# 直接运行这个 Agent
agentgo agent run --agent Scout "总结当前仓库结构"

# 内置 standalone agents 默认可用
agentgo agent show Concierge
agentgo agent show Operator

# 创建一个 team（会自动创建默认 captain）
agentgo team add "Docs Team" --description "文档和发布说明"

# 让独立 Agent 加入 team
agentgo agent join Scout --team "Docs Team" --role specialist

# 通过默认领队 Agent 和某个 team agent 执行任务
agentgo team go "@Captain @Scout 总结 UI 和后端的关系，并写入 workspace/ui_backend_overview.md"

# 查看 team 当前运行态；有 running/queued 任务时会自动 follow
agentgo team status "Docs Team"

# 直接通过内置 Operator 执行操作型任务
agentgo agent run --agent Operator "在 workspace/operator_probe.txt 写入文本：OPERATOR_OK"

# 让 Agent 退出 team
agentgo agent leave Scout

# 不再需要时删除这个 team
agentgo team delete "Docs Team"
```

---

## 认知记忆层 (Hindsight & PageIndex)

AgentGo 实现了一个受 **Hindsight** (认知分层) 和 **PageIndex** (结构化导航) 启发的演化记忆层。

| 概念                    | 说明                                                                       |
| ----------------------- | -------------------------------------------------------------------------- |
| **事实 (Facts)**        | 从对话中提取的原子数据点（如“用户喜欢 Go”）。                              |
| **观察 (Observations)** | 通过 **Reflect()** 将多个事实整合出的高层洞察。                            |
| **层次化索引**          | `_index/` 目录下的 Markdown 摘要，用于极速的推理导航。                     |
| **混合检索**            | 并行运行 **向量搜索** (相似度) 与 **索引导航** (逻辑推理)，通过 RRF 融合。 |
| **可追溯性**            | 每条观察都追踪其 **证据 ID (EvidenceIDs)**，提供清晰的认知审计链。         |

### 记忆演化流程

1. **提取**: Agent 在对话中识别出一个事实。
2. **索引**: 事实存入带 YAML 元数据（置信度、来源类型）的 Markdown 文件。
3. **反思**: 定期（如每 5 条事实）触发后台 `Reflect()`，将事实合并为高层观察。
4. **更迭**: 当信息变化时，旧记忆被标记为 `stale`（过时），并通过 `SupersededBy` 链接到新记忆。

---

## Builder

```go
// 实现自己的 Module
type Module interface {
    ID() string
    RegisterTools(registry *ToolRegistry) error
}

svc, _ := agent.New("agent").
    WithModule(NewMyCustomModule()).
    Build()
```

---

## 带执行语义的工具注册

现在工具可以直接声明自己的运行时语义。这样 runtime 可以更好地处理：

- 并发批处理
- 取消行为
- 权限策略
- 流式状态更新

```go
svc, _ := agent.New("assistant").Build()
defer svc.Close()

svc.Register(
    agent.BuildTool("workspace_summary").
        Description("返回当前工作区的简要摘要。").
        ReadOnly(true).
        InterruptBehavior(agent.InterruptBehaviorCancel).
        Handler(func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
            return map[string]any{
                "workspace": "current project",
                "mode":      "demo",
            }, nil
        }).
        Build(),
)

svc.AddToolWithMetadata(
    "write_release_note",
    "写入一份发布说明文件。",
    map[string]interface{}{
        "type": "object",
        "properties": map[string]interface{}{
            "path": map[string]interface{}{"type": "string"},
            "body": map[string]interface{}{"type": "string"},
        },
        "required": []string{"path", "body"},
    },
    func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
        return "ok", nil
    },
    agent.ToolMetadata{
        Destructive:       true,
        InterruptBehavior: agent.InterruptBehaviorBlock,
    },
)
```

---

## Agent APIs

### 运行时调用

| 方法                      | 返回值                      | 会话            | 适用场景          |
| ------------------------- | --------------------------- | --------------- | ----------------- |
| `Ask(ctx, prompt)`        | `(string, error)`           | 无              | 单次问答          |
| `Chat(ctx, prompt)`       | `(*ExecutionResult, error)` | 有（自动 UUID） | 对话式交互        |
| `Run(ctx, goal, ...opts)` | `(*ExecutionResult, error)` | 可选            | 完整 Agent 循环   |
| `Stream(ctx, prompt)`     | `<-chan string`             | 无              | 实时 Token 输出   |
| `ChatStream(ctx, prompt)` | `<-chan string`             | 有              | 对话式 + 实时输出 |
| `RunStream(ctx, goal)`    | `(<-chan *Event, error)`    | 可选            | 完整事件可见性    |

```go
result, _ := svc.Run(ctx, "目标",
    agent.WithMaxTurns(20),
    agent.WithTemperature(0.7),
    agent.WithSessionID("my-session"),
    agent.WithStoreHistory(true),
)

result.Text()        // 最终回答（字符串）
result.Err()         // 非 nil 表示 Agent 报告了错误
result.HasSources()  // true 表示使用了 RAG 检索结果
```

现在 `RunStream()` 的 `state_update` 事件里会带更多运行时字段，例如：

- `turn_stage`
- `loop_transition`
- `transition_reason`
- `tool_state`
- `preferred_agent`
- `requires_tools`
- `transition`

### Standalone Agent 管理

在 manager 层，standalone agent 是持久化的命名运行单元：

- `CreateAgent`, `UpdateAgent`, `DeleteAgent`
- `GetAgentByName`, `ListAgents`, `ListStandaloneAgents`
- `GetAgentService`
- `GetAgentStatus`, `ListAgentStatuses`

内置 standalone agents（`Concierge`、`Assistant`、`Operator`、`Stakeholder`）会自动 seed，也可以像普通命名 agent 一样直接运行和查看。

### 内置 Agent 委派 APIs

用户自定义的 standalone agent 默认会获得一组 built-in delegation 接口：

- `list_builtin_agents`
- `delegate_builtin_agent`
- `submit_builtin_agent_task`
- `get_delegated_task_status`

这组接口的意义是：

- 把执行类工作交给 `Operator`
- 把通用工作交给 `Assistant`
- 把业务/范围判断交给 `Stakeholder`

---

## 程序化工具调用（PTC）

使用 `WithPTC()` 时，LLM 生成 JavaScript 代码而非 JSON 工具调用。代码在 Goja 沙箱中运行，可使用 `callTool()`：

```go
svc, _ := agent.New("analyst").
    WithPTC().
    WithTool(teamDef, teamHandler, "data").
    WithTool(expenseDef, expenseHandler, "data").
    Build()

// LLM 可以这样写代码：
//   const team = callTool("get_team", { dept: "eng" });
//   return team.members.map(m => ({
//     name: m.name,
//     spend: callTool("get_expenses", { id: m.id }).total
//   }));
```

**适用场景：** 多个依赖工具调用一次完成、数据进入上下文前先转换、需要条件逻辑的工具调用。

---

## 记忆系统

`memory` 和 `cache` 在 AgentGo 里是两个不同层次：

| 组件       | 存储            | 用途                                     |
| ---------- | --------------- | ---------------------------------------- |
| **Memory** | Markdown/SQLite | 长期、可解释、可演化的事实/观察/偏好     |
| **Cache**  | 内存或文件      | 短期、可失效、可重建的查询/向量/响应结果 |

```go
// 启用 DB 记忆（自动从每次对话中学习）
svc, _ := agent.New("agent").WithMemory().Build()

// LongRun Agent 自动共享同一个 DB 记忆
lr, _ := agent.NewLongRun(svc).
    WithInterval(5 * time.Minute).
    WithWorkDir("~/.agentgo/longrun").
    Build()
```

对于文件型 memory store，现在还提供了 prompt 入口索引和 session 记忆辅助接口：

```go
fileStore, _ := store.NewFileMemoryStore("./memory")

_ = fileStore.WriteSessionMemory("session-123", "当前草稿：保持简洁、专业。")
sessionNote, _ := fileStore.ReadSessionMemory("session-123")
entrypoint, _ := fileStore.ReadEntrypoint() // 读取 MEMORY.md
headers, _ := fileStore.SelectRelevantHeaders(context.Background(), "Go 后端 简洁 风格", 3)

fmt.Println(sessionNote)
fmt.Println(entrypoint)
fmt.Println(headers)
```

无 Embedder 时优雅降级：`memory` 默认可以直接走文件存储与索引导航。

如果你只想先跑通本地优先方案，建议这样理解：

- `memory` 先用文件版，保证可读、可编辑、可追踪
- `cache` 先用文件版或内存版，保证查询加速，但不要承载长期知识
- 文件 memory 现在会维护 `MEMORY.md`、`_session/*.md`，并支持 header selector

---

## 自主 Agent（LongRun）

LongRun 按计划运行 Agent，带有持久化任务队列：

```go
lr, _ := agent.NewLongRun(svc).
    WithInterval(10 * time.Minute).
    WithMaxActions(5).
    Build()

lr.AddTask(ctx, "监控 RSS 订阅并总结新条目", nil)
lr.Start(ctx)
// ...
lr.Stop()
```

特性：SQLite 任务队列、心跳文件、定时调度、与父 Agent 共享 DB 记忆。

---

## 多 Agent 协作

```go
// Handoffs — 专业 Agent 路由
orchestrator.RegisterAgent(researchAgent)
orchestrator.RegisterAgent(writerAgent)
// LLM 通过 transfer_to_* 工具调用路由到对应 Agent

// SubAgents — 作用域委托
coordinator := agent.NewSubAgentCoordinator()
resultChan  := coordinator.RunAsync(ctx, subAgent)
results     := coordinator.WaitAll(ctx)
```

### 内置 Standalone Agents

AgentGo 默认会 seed 这些 standalone agents：

- `Concierge` — `agentgo chat` 的 intake / orchestration agent
- `Assistant` — 通用执行型 Agent
- `Operator` — 执行导向 Agent，适合文件操作、验证、PTY 会话和 coding-agent 调用
- `Stakeholder` — 产品 / 业务代表

可以直接查看：

```bash
agentgo agent show Concierge
agentgo agent show Assistant
agentgo agent show Operator
agentgo agent show Stakeholder
```

### Operator

`Operator` 是内置的执行型 Agent。除了常规 filesystem / web / RAG / memory 能力外，它还暴露了：

- 通用 PTY 会话工具：
  - `start_pty_session`
  - `send_pty_input`
  - `get_pty_session`
  - `list_pty_sessions`
  - `interrupt_pty_session`
  - `stop_pty_session`
- 面向 coding-agent 的专用工具：
  - `start_coding_agent_session`
  - `send_coding_agent_prompt`
  - `get_coding_agent_session`
  - `list_coding_agent_sessions`
  - `interrupt_coding_agent_session`
  - `stop_coding_agent_session`
  - `run_coding_agent_once`

直接使用示例：

```bash
agentgo agent run --agent Operator "在 workspace/operator_probe.txt 写入文本：OPERATOR_OK"
agentgo agent run --agent Operator "调用 codex，让它输出精确文本：RES_FROM_CODEX"
```

如果只是一次性拿结果，`run_coding_agent_once` 是最稳的入口。对于 `codex`，AgentGo 会优先走非交互式 `codex exec -`，而不是让模型自己猜 shell 命令。

### 自定义 Agent 委派内置 Agent

用户自定义的 standalone agent 默认会拿到一组“内置 Agent 委派工具”：

- `list_builtin_agents`
- `delegate_builtin_agent`
- `submit_builtin_agent_task`
- `get_delegated_task_status`

当前可委派的 built-in agents：

- `Assistant`
- `Operator`
- `Stakeholder`

这样自定义 Agent 在保留自身角色和能力的同时，可以显式把执行型工作交给 `Operator`，把业务判断交给 `Stakeholder`。

## Team APIs

AgentGo 也提供面向 `独立 Agent / Team / Team Agent` 的持久化管理 API。`captain` 只是 team 内的一种 agent 角色。

```go
store, err := agent.NewStore(filepath.Join(cfg.DataDir(), "agent.db"))
if err != nil {
    panic(err)
}

manager := agent.NewTeamManager(store)
if err := manager.SeedDefaultMembers(); err != nil {
    panic(err)
}

scout, err := manager.CreateAgent(ctx, &agent.AgentModel{
    Name:         "Scout",
    Kind:         agent.AgentKindAgent,
    Description:  "独立执行任务的特工",
    Instructions: "独立工作并直接回答。",
})
if err != nil {
    panic(err)
}

docsTeam, err := manager.CreateTeam(ctx, &agent.Team{
    Name:        "Docs Team",
    Description: "文档和发布说明",
})
if err != nil {
    panic(err)
}

writer, err := manager.JoinTeam(ctx, scout.Name, docsTeam.ID, agent.AgentKindSpecialist)
if err != nil {
    panic(err)
}

result, err := manager.DispatchTask(ctx, writer.Name, "编写 workspace/ui_backend_overview.md")
if err != nil {
    panic(err)
}
fmt.Println(result)
```

Captain 的运行时行为：

- `CreateTeam()` 或 `agentgo team add` 创建自定义 team 时，会自动生成一个默认 captain。
- Captain 的 system prompt 会带上本 team 的 roster 和成员职责摘要。
- 对于实现类任务，Captain 优先使用异步团队任务（`submit_team_async`）。
- Team shared task 会持久化，新的 CLI 进程也能通过 `get_task_status` / `list_team_tasks` 查看。
- Captain 默认不使用通用的 `delegate_to_subagent`。

### 核心 Team-Manager APIs

- `CreateAgent`, `UpdateAgent`, `DeleteAgent`, `GetAgentByName`, `ListAgents`, `ListStandaloneAgents`
- `JoinTeam`, `LeaveTeam`, `GetAgentService`
- `CreateTeam`, `ListTeams`, `GetTeamByName`
- `AddTeamAgent`, `CreateTeamAgent`, `ListTeamAgents`, `GetTeamAgentByName`
- `AddCaptain`, `AddSpecialist`, `ListCaptains`, `ListSpecialists`（角色化辅助方法）
- `DispatchTask`, `DispatchTaskStream`
- `EnqueueSharedTask`, `ListSharedTasks`
- `SubmitAgentTask`, `SubmitTeamTask`, `GetTask`, `ListSessionTasks`

### Team 运行态 / 状态 APIs

面向编排和监控的常用入口：

- `GetTeamStatus`, `ListTeamStatuses`
- `GetAgentStatus`, `ListAgentStatuses`
- `GetLeadAgentForTeam`
- `SubscribeTask`
- `DispatchTaskStreamWithOptions`
- `ChatWithMemberStream`
- `ChatWithMemberStreamWithOptions`

从概念上可以把 API 分成三层：

- **Standalone agent APIs**：创建、运行、查看、更新
- **Team APIs**：建队、入队、派活、追踪异步任务
- **Built-in delegation APIs**：让自定义 agent 明确委派 `Assistant`、`Operator`、`Stakeholder`

---

## 确定性规划（Plan-Review-Execute）

```go
plan, _   := svc.Plan(ctx, "部署新服务")
// 检查 plan.Steps，按需修改
result, _ := svc.Execute(ctx, plan.ID)
```

---

## 配置与存储

运行时目录布局由 `AGENTGO_HOME` 决定，默认是 `~/.agentgo`。
结构化运行时配置存放在 `data/agentgo.db`。

### 目录结构（默认 `home = ~/.agentgo`）

```
~/.agentgo/
├── mcpServers.json           ← MCP 服务器定义
├── data/
│   ├── agentgo.db            ← 控制面：providers、运行时配置、agent/team 元数据
│   ├── cortex.db             ← 大脑存储：memory、vector、graph、knowledge
│   └── memories/             ← 文件型记忆存储（Markdown + YAML frontmatter）
├── skills/                  ← SKILL.md 技能文件
├── intents/                 ← Intent 意图文件
└── workspace/               ← Agent 工作目录
```

### SQLite 文件说明

| 文件                  | 默认路径                        | 用途                                                     |
| --------------------- | ------------------------------- | -------------------------------------------------------- |
| `agentgo.db`          | `$home/data/agentgo.db`         | 运行时配置、providers、MCP/skills 路径、agent/team 元数据 |
| `cortex.db`           | `$home/data/cortex.db`          | 大脑存储：memory、vector、graph、knowledge               |
| `history.db` _(可选)_ | 通过 `WithHistoryDBPath()` 指定 | 详细工具调用日志，仅在 `WithStoreHistory(true)` 时创建   |

### Memory 存储类型

| `store_type`    | 存储方式                                                        | 是否需要 Embedder |
| --------------- | --------------------------------------------------------------- | ----------------- |
| `file` _(默认)_ | `data/memories/entities/*.md` 和 `data/memories/streams/*.md` | 否                |
| `cortex`        | `data/cortex.db`                                                | 否                |

### 运行时设置

AgentGo 会根据 `AGENTGO_HOME` 自动派生运行期存储布局：

- 工作区：`$home/workspace`
- MCP 文件系统白名单：`$home/workspace`
- 大脑数据库：`$home/data/cortex.db`
- 记忆存储：`$home/data/memories`，当 `memory.store_type = "cortex"` 时改为 `$home/data/cortex.db`
- 缓存目录：`$home/data/cache`

其余结构化运行时设置保存在 `agentgo.db`，包括：

- LLM providers 和 pool strategy
- embedding providers 和 `rag.embedding_model`
- MCP 配置路径
- skills 加载路径
- 每个 agent 的 preferred provider/model

---

## Provider 配置

Providers 存放在 `agentgo.db` 中，通过 CLI/UI/runtime API 管理。

支持：OpenAI · Anthropic · Azure OpenAI · DeepSeek · Ollama（本地）

---

## 示例

```
examples/
├── quickstart/               — 最简入门示例
├── agent/
│   ├── agent_usage/          — Builder 模式、工具注册
│   ├── multi_agent_orchestration/ — Handoffs + 流式输出
│   ├── longrun/              — 自主定时 Agent
│   └── realtime_chat/        — WebSocket 会话
├── rag/                      — 文档摄入 + 问答
├── memory/
│   ├── chat_with_memory/     — DB 记忆 + 对话
│   └── smart_fusion/         — 记忆合并
├── ptc/
│   ├── custom_tools/         — JS 沙箱工具编排
│   └── memory_chat/          — PTC + 记忆
├── skills/                   — Skill 文件
└── mcp/                      — MCP 工具服务器
```

---

## License

MIT — Copyright (c) 2024–2026 AgentGo Authors
