# 工具发现死循环 / 空输出诊断

来源：agentbench 用 `gemini-3.6-flash-high` 跑 superai 50 题的完整工具调用日志。

结论先说：**问题在 agent-go 核心（`pkg/agent`），不在 superai-desktop。**
`search_available_tools` / `task_complete` / `task_blocked` / `execute_javascript`
四个工具全部由框架注册，superai-desktop 的 `backend/` 里没有它们的定义。

| 工具 | 注册位置 |
|---|---|
| `search_available_tools` | `pkg/agent/builder.go:741` |
| `task_complete` | `pkg/agent/service.go:370` |
| `task_blocked` | `pkg/agent/service.go:390` |
| `execute_javascript` | `pkg/ptc/` + `pkg/agent/ptc_integration.go`（PTC，默认开） |

---

## 观测数据

50 题，`gemini-3.6-flash-high`：

| 指标 | 值 |
|---|---|
| `search_available_tools` 调用次数 | 55（几乎每题至少 1 次） |
| `execute_javascript` 调用次数 | 86（最高频工具，1.7 次/题） |
| 平均工具调用 | 5.2 次/题（最高 18 次） |
| 总耗时 | 约 9 分钟（对照组 superleo 同模型约 2 分钟） |
| 效率分 | 39%（对照组 55%） |

典型轨迹：

```
home-allowance   (算零花钱 + 发邮件)
  search_available_tools → search_available_tools → search_available_tools
  → search_mcp_servers → task_blocked
  final output: ""            # 四次搜索，零次真正干活

finance-interest
  search_available_tools ×3 → (无终止信号)
  final output: ""            # 用户什么都收不到

personal-planet  (题目明确要求"不许用任何工具")
  execute_javascript → execute_javascript → search_available_tools
  → execute_javascript → task_complete
  答案正确（木星），但违反了显式指令，且调了 4 次工具答常识题
```

---

## 根因 #1：tool-discovery 没有预算，去重只挡完全相同的参数

`handleDuplicateToolCalls`（`pkg/agent/service_execution.go:1064`）的去重 key 是：

```go
key := fmt.Sprintf("%s:%v", tc.Function.Name, tc.Function.Arguments)
```

参数逐字进 key。模型换个 query 词——`"send email"` → `"email"` → `"mail sender"`——
就是三个不同的 key，全部放行执行。命中同 key 时（`:1083`）才会折叠成
"already executed" 提示。

`prevToolCalls` 本身作用域是整个 run（`service_execution.go:400` 初始化，
`:408` 存进 `state.PrevToolCalls`），所以不是每轮重置的问题，纯粹是 key 太严格。

全仓库 grep 不到任何"发现类工具调用次数上限"的概念——没有预算，没有
连续搜索计数，唯一兜底是 `MaxTurns`。

**而且提示词在主动教它循环。** 三处：

- `pkg/agent/system_prompt_sections.go:177`
  `"If you are unsure which exact tool fits a request, call search_available_tools before claiming the capability is unavailable."`
- `pkg/agent/ptc_integration.go:461`
  `"If you do NOT know the exact tool name, first call search_available_tools or tool_search_tool_bm25..."`
- `pkg/agent/ptc_integration.go:405` 同义的一句

而唯一的反向约束是 `system_prompt_sections.go:180`：
`"Never repeat the same tool call with identical arguments."` —— 这句话本身
就在暗示"换个参数就可以再搜一次"，和去重 key 的实现精确同构地失效。

**缺的那一句**：搜过 N 轮仍无匹配工具时，应该用自身知识直接作答，或
`task_blocked` 说明能力缺失。现在没有任何地方写这个出口。

### 修法（已落地）

**预算挂在 context 上，不挂在执行循环的 state 上。** 这是这次改动最关键的
设计决定：工具搜索从两个方向到达 registry——聊天协议的工具调用，和模型在
PTC 沙箱里写的 `callTool('search_available_tools', ...)`。后者由
`pkg/ptc/runtime/goja/runtime.go:318` 直接调 handler，**根本不经过执行循环**。
预算如果只挂在循环里，等于把搜索循环赶进 `execute_javascript` 里继续跑。

新增 `pkg/agent/context_discovery_budget.go` + `tool_discovery_budget.go`：

1. **`discoveryBudget`（主要机制）**：`maxToolDiscoveryCallsPerRun = 3`，
   带 mutex（聊天路径的工具是并发执行的）。三种判定：
   - `discoveryAllowed` — 新查询，预算内，放行
   - `discoveryRepeat` — 本 run 已搜过，返回"别重复搜"引导，**不消耗额度**
     （否则沙箱里循环同一个 query 就能把额度耗光）
   - `discoveryExhausted` — 额度用尽，返回 `toolDiscoveryBudgetGuidance`，
     明确给出口：*"Do not search for tools again. Either answer the user
     directly from your own knowledge, or call task_blocked naming the
     specific capability that is missing."*

2. **两个入口装配预算**，都用 `ensureDiscoveryBudget`（已有则不覆盖，
   所以嵌套执行共享同一份额度，不会每层重置）：
   - `runtime.go` `Runtime.Run` —— 流式路径，紧挨着已有的 `withToolUseSink`
   - `service.go` `Service.Run` 的 `runCtx` —— 非流式路径，**也是走
     `runPTCExecution` 的那条**

3. **三个执行点消费预算**（覆盖两条路径的全部搜索入口）：
   - `builder.go` 的 `search_available_tools` handler（聊天 + PTC 共用同一个
     闭包，PTC router 由 `SyncToPTCRouter` 拿到的就是它）
   - `service_tool_dispatch.go` 的 `IsToolSearchTool` 分支
     （`tool_search_tool_bm25` / `_regex` 走的是另一条 dispatch）

4. **归一化（次要）**：`toolCallSignature` 对 search 类工具走
   `normalizeQueryText`——小写、按非字母数字切词、排序去重。非 search 工具
   保持原来的 `name:args` 逐字形式（写后重读必须算不同调用）。

5. **提示词**：`system_prompt_sections.go` 和 `ptc_integration.go` 各加一条
   有界发现的规则；`"Never repeat the same tool call with identical arguments."`
   改成 `"Never repeat a tool call that cannot return anything new."`，
   去掉"换个参数就行"的暗示。

**没有预算时行为不变。** `Service` 被当普通库对象直接用（`agent.New(...).Build()`，
不走 runtime 循环）时 context 里没有预算，发现能力保持无界——不能因为这个
改动破坏 embedder。有专门的测试守着这条。

**关于归一化，要诚实说明**：它只能收敛格式差异（`"Send  EMAIL"` == `"email send"`），
**收敛不了** `"send email"` / `"email"` / `"mail sender"`——那需要词干还原和同义词，
太脆。所以真正止住循环的是预算，不是归一化。原计划里"让这三个 query 收敛到
同一个 key"的写法是过于乐观的，这里更正。

### 回归测试

`tool_discovery_budget_ptc_test.go`
- `TestDiscoveryBudgetAllowsDistinctQueriesThenExhausts` — 预算原语本身：
  3 个不同 query 放行，重复不消耗额度，第 4 个不同 query 耗尽
- `TestToolSearchHandlerRefusesPastDiscoveryBudget` — 通过
  `ToolRegistry.Call`（沙箱调用 handler 的等价形式）验证 handler 层拦截
- `TestToolSearchWithoutBudgetInContextIsUnbounded` — 没装预算时不拦截

`tool_discovery_budget_sandbox_test.go`
- `TestPTCSandboxSearchesRespectDiscoveryBudget` — **端到端跑真实 Goja
  JavaScript**，5 次改写搜索只有 3 次真搜。这条专门验证整条 context 链
  没有断：`Runtime.Run → ExecuteCode → ptc.Service.Execute
  （`context.WithTimeout` 保留 value）→ goja `state.ctx` → `handler(state.ctx, args)`。
  链上任何一环丢了 context value 这条测试就会挂。

`tool_discovery_budget_run_test.go`
- `TestServiceRunPropagatesCallerDiscoveryBudgetIntoPTC` — 调用方给的预算
  能穿到沙箱里
- `TestServiceRunEstablishesDiscoveryBudgetWhenCallerHasNone` — 用一个探针
  工具确认 `Service.Run` 自己会装预算（**这条一开始是红的**，非流式路径
  当时确实没装）

`tool_discovery_budget_test.go`
- `TestToolSearchDedupIgnoresCasingSpacingAndWordOrder` — 归一化折叠

`service_execution_duplicate_test.go` 里原来硬编码内部 key 格式的那条
seed 改成调用 `toolCallSignature`，让它测行为而不是测 key 格式。

---

## 根因 #2：task_blocked 可能产出空字符串（纯 bug）—— 已修复

> 状态：已修复。见本节末尾。

`taskTerminalToolResult`（`pkg/agent/task_contract.go`）在 `blocker` / `reason` /
`result` 三个 key 都缺失或为空时返回调用方给的 `fallback`。工具 handler
（`pkg/agent/service.go:409`）传的 fallback 是人话；但 runtime 拿的是另一份。

**具体是哪条路径**（这一点值得说清楚，因为流式和非流式表现不同）：

- **流式拦截路径（`runtime.go:547`）是安全的。**
  `buildStreamingTurnCallbacks`（`runtime_tool_adapter.go:47-52`）在
  `res == ""` 时**不会**终止，而是继续流式，只有 `res` 非空才同时设置
  `taskTerminalName` 和 `taskTerminalResult`。所以 `taskTerminalName != ""`
  时 final 必然非空。
- **漏的是 post-turn 兜底路径（`runtime.go:607`）。** 上面那个回调的注释写着
  "if they never do, the stream ends naturally and the post-turn terminal
  handler recovers from the full result" —— 而这个 recovery handler 是：

  ```go
  final := taskTerminalToolResult(tc.Function.Name, tc.Function.Arguments, result.Content)
  if tc.Function.Name == "task_blocked" {
      r.blockRun(goal, final, nil, false)
  ```

  参数没填 + `result.Content` 为空 → `final == ""` → `blockRun(goal, "", ...)`
  → `blockRunWithStop` 把 `Content: strings.TrimSpace("")` 发出去 → 用户收到空串。

这解释了 `home-order-pizza` / `work-call-client` / `travel-book-hotel` /
`finance-transfer` 这类"本该礼貌拒绝"的场景全部变成静默失败——judge 给 0 分。

### 修法（已落地）

没有在两个调用点各打一个补丁，而是收敛到唯一的出口 `blockRunWithStop`
（`pkg/agent/runtime.go`）：blocker 去空白后为空则降级到 `defaultBlockedText`。
这样流式拦截、post-turn 兜底、预算上限、lint 耗尽——所有 blocked 路径都
不可能再发出空事件，将来新增的调用点也自动被覆盖。

`defaultBlockedText` 定义在 `pkg/agent/task_contract.go`，工具 handler
（`service.go:409`）改用同一个常量，两条路径从此共享一个字符串。

回归测试：`pkg/agent/runtime_blocked_text_test.go` 的
`TestBlockedRunWithoutBlockerArgStillHasText` —— 脚本一个无参数、无正文的
`task_blocked` 工具调用走完整 `RunStream`，断言 `EventTypeBlocked` 的
Content 非空。修复前该测试失败（空串），修复后通过。

**注意这只保证"非空"，不保证"好"。** 真正的 "我没有打电话的能力" 这种人话
仍然要靠模型填 `blocker` 参数；框架能做的是把静默失败变成一句诚实的兜底。
要让 judge 真正加分，还需要在提示词或 lint 层要求 `blocker` 写具体原因。

---

## 根因 #3：PTC 是默认锤子，不区分任务类型

`execute_javascript` 默认开，且暴露与否不看任务性质。

框架里已经有识别常识问答的能力——`looksLikeInformationSeekingQuery`
（`pkg/agent/service_execution.go:215`，识别疑问句前缀 + 中英双语）——但它只在
`prepareTurnInputs`（`service_helpers.go:558`）里用来过滤工具定义，**没有**
用来抑制 PTC 暴露。

结果就是 `personal-planet` 那种"说出最大的行星、不许用工具"的题，模型仍然
看得到 `execute_javascript` 并把它当默认动作，直接违反显式指令。

### 建议修法

在 `ptcAvailableCallToolsWithPolicy` / PTC 暴露决策处加门槛：纯常识问答
（`looksLikeInformationSeekingQuery` 命中且没有文件/网络/计算类意图），或
用户 prompt 里有显式"不要使用工具"指令时，不暴露 `execute_javascript`。

---

## 根因 #4（次要）：缺失/不可靠的实操工具

- 所有"需要发邮件"的任务，轨迹里没有一次能确认邮件真的发出。
  `finance-tax` 内部把 $1,870 算对了，但没有邮件发送证据。
- `health-pill-reminder` 的 judge 直接写 `"reminder service was unavailable"`
  ——提醒工具自身报不可用。
- `work-competitor-stock` 的 MSFT 报价引用了未来日期（2026-07-30）且混用了
  收盘价和盘前数据，judge 判定不可靠。没有专门的行情工具，靠
  `web_search` + JS 拼凑，容易编造细节。

这一类属于 superai 侧的工具供给问题，不是 agent-go 控制流问题，但会和
根因 #2 叠加：工具不可用时如果 `task_blocked` 能给出人话，至少能得分。

---

## 验证计划

改动落地时按这个顺序验证（不要直接跳到 agentbench，太慢且不确定）：

### 1. agent-go 单测

`go test ./pkg/agent/ -race`，需要新增覆盖：

- **#1**：✅ 已完成 —— 4 个测试文件共 7 条，聊天协议层 + PTC 沙箱层
  （含真实 Goja 端到端）+ 两个入口的装配，见上文 #1「回归测试」。
- **#2**：✅ 已完成 —— `runtime_blocked_text_test.go`。（流式拦截路径本身安全，
  实际只需覆盖 post-turn 兜底路径，理由见上文 #2。）
- **#3**：新测——纯常识问答的 goal 下，PTC 工具不出现在暴露的工具列表里。

### 2. mock eval

`make eval`。在 `eval/scenarios/` 新增 MockLLM 场景（格式见
`eval/scenarios/happy_path_default_lints_pass.yaml`）：

- `tool_discovery_budget_stops_search_loop.yaml` — `llm_replies` 脚本三次
  不同 query 的搜索，`expect.status: completed`，`final_text_match` 匹配
  直接作答的内容
- ~~`blocked_always_has_human_text.yaml`~~ —— **写不了**。`llm_replies` 是
  `[]string`（`eval/runner/scenario.go:66`），`MockLLM` 里完全没有 `ToolCalls`
  的概念，无法脚本化一次工具调用。要覆盖 #2 必须先给 eval harness 加
  tool-call 脚本能力，那是另一个独立改动。#2 目前由 `pkg/agent` 的
  end-to-end 单测覆盖（走完整 `RunStream`，比 mock 场景更强）。

这一层是确定性的、CI 安全的，先保证不回归。

### 3. agentbench 复跑

`/Users/liliang/Things/AI/base/eval-go/agentbench`，同一批 50 题、同一模型
（`gemini-3.6-flash-high`），对比这几个指标：

| 指标 | 当前基线 | 目标 |
|---|---|---|
| `search_available_tools` 总次数 | 55 | < 25 |
| `execute_javascript` 总次数 | 86 | < 50 |
| 平均工具调用/题 | 5.2 | < 3 |
| 空输出题数 | ≥ 2（home-allowance, finance-interest） | 0 |
| 总耗时 | ~9 min | < 4 min |
| 效率分 | 39% | 向 55%（superleo 对照）靠拢 |

---

## 优先级

1. ~~**#2 task_blocked 空输出**~~ —— ✅ 已修复。
2. ~~**#1 discovery 预算**~~ —— ✅ 已修复，聊天协议层 + PTC 沙箱层都覆盖。
3. **#3 PTC 门槛** —— 修的是指令遵循，收益偏行为质量而非分数。
4. **#4 工具供给** —— superai 侧的事，和 agent-go 无关。

#1 和 #2 都已落地，现在可以跑 agentbench 复测了。
