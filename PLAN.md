# PLAN.md

> agent-go 的下一阶段路线：**Harness 化**。把藏在 prompt 字符串里的 hard-won 教训搬到代码里，让它们变成可见、必听、可量化的东西。
>
> 指导原则（Hashimoto）：**每一次 agent 重复犯的错，都应该变成 harness 里一段代码，而不是 prompt 里一段新的"不要..."。**

---

## 背景：我们在哪

这次（2026-04 月间）已经完成的事：

- **Task 一等公民**：`task_id` 一致性（v2.68.1 修复 SubmitAgentTask 双 UUID bug）。
- **跨 agent 实时流**：CLI 端 `chatTaskStreamRenderer` 把 `EventTypePartial` 实时渲染（v2.68.0）。
- **CLAUDE.md 重写**：去 RAG-first 化，明确 framework 定位（v2.68.0）。
- 历史已落地的 8 项（runtime kernel、tool metadata、PTC、skill surfacing、memory workflow、prompt engine、RAG 降级、task scoping）见文末 *附录 A*。

agent-go **不需要重建**——它已经在做 harness 该做的事。但分散在 prompt、helper、constraint 字符串里，没有统一的语义骨架。

---

## 三条 Track（按 ROI 排序）

### Track 1 — Output Lints（先做，最小可行）

把 instruction 字符串里那一堆 `Do not ...` / `IMPORTANT: ...` 硬规则，挪到 runtime 的 final-output 检查路径。违反就 reject + 反馈错误，让模型在同一轮里自愈。

**为什么先做这个**：改动局部、收益立刻可见、能为后续 Track 提供"可量化"的对照基线。

> **状态**：Phase 1 ✅ 完成 / Phase 2 ✅ 完成 / Phase 3 ✅ 完成（已剪 dispatcher/archivist instruction，TeamManager 自动 wiring built-in lint）。

**Phase 1 — 基础设施（半天）**

- 新建 `pkg/agent/guardrails/output_lint.go`：
  - `type OutputLint interface { Name() string; Check(text string, ctx LintContext) (ok bool, reason string) }`
  - `type LintRegistry`：按 agent name + 全局两层注册。
  - `LintContext`：包含 `AgentName`, `TaskID`, `ToolCalls []string`, `TurnIndex int`。
- 接到 runtime：`pkg/agent/runtime.go` 在发出 `EventTypeComplete` 之前调一次 `registry.RunFor(agentName, finalText, ctx)`；任一 lint 失败 → emit 一条 `EventTypeError(content="lint:<name>: <reason>")` → loop 继续而不是终止。
- 加 retry budget：单轮最多 retry N 次（默认 2），超过就直接 task_blocked，避免死循环。

**Phase 2 — 第一组 lints（半天）**

| Lint | Agent | 触发模式 | 替代 instruction |
|---|---|---|---|
| `dispatcher_no_bounce_back` | Dispatcher | 含 "I will route" / "let me dispatch" / "我会让 ... 处理" / "将由 ... 完成" | `defaultDispatcherInstructions` 里那段 anti-bounce 规则 |
| `archivist_no_relative_time` | Archivist | 写记忆时含 `明天`/`后天`/`下周.`/`tomorrow`/`next \w+`（且未同时含绝对日期） | Archivist instruction 里 *"Never store a relative time reference"* 那段 |
| `no_planning_only_finish` | 全部 | final text 以 *"我会..."* / *"接下来将..."* / *"Next steps:"* 结尾且没调过 `task_complete`/`task_blocked` | Finish-Or-Block 合约第 2 条 |

**Phase 3 — 验证（半天）**

- 每加一条 lint 写一个表驱动测试（pass / fail / fail-then-self-heal 三组）。
- 砍掉对应的 instruction 段落，重新跑全套行为测试（见 Track 3 — 这里先靠手工 smoke）。
- 量化：instruction string 长度变化（目标：dispatcher / archivist 每个砍 30%+）。

**Acceptance criteria**

- [x] `OutputLintRegistry` 单测全绿（`output_lint_test.go`：10 cases）
- [x] 整合测试：lint fail → retry → complete；lint always fail → block；no lint → 透传（`output_lint_runtime_test.go`：3 cases，含 race）
- [x] 三条 builtin lint 各自表驱动测试（`output_lints_builtin_test.go`：30+ cases）
- [x] CLI 真实 smoke 跑通（无 lint 注册时 streaming 路径不变）
- [x] **量化结果**：Dispatcher instruction 1550→1406 字符（-9.3%）；Archivist 1241→952 字符（-23.3%）。低于 PLAN.md 原 30% 目标——诚实说明：现有三条 lint 只覆盖特定 failure mode，剩下的 instruction 多数是语义规则（角色边界、工具选择、escalation 格式），不能用确定性 regex 表达。后续如果再加新 lint，可以继续剪。
- [x] **TeamManager 自动 wiring**：`buildServiceForModel` 末尾调 `applyBuiltInOutputLints`，built-in agent 服务出生就带 lint，library 用户调 `agent.New(...).Build()` 行为不变（向后兼容）
- [x] retry 退出条件有上限（`defaultLintRetryBudget=2`）+ emit 明确 EventTypeError / EventTypeBlocked

**估时**：1.5 天 → 实际 ~半天（Phase 1+2，Phase 3 prompt 删减延后）。

---

### Track 2 — Task Checkpoint / Rollback

> **状态**：✅ MVP 完成（schema + storage + writer + Resume API + CLI `task replay` / `task checkpoints` + tests + 真机端到端验证）。仍未做：tool-call 粒度的中间快照、auto-prune 策略、UI 集成。

长 task（≥10 分钟、≥10 轮 tool）跑到一半失败只能重跑。该有的能力：每次终态工具调用 / 关键 milestone 后落 snapshot，失败时回到最近成功点而不是 t=0。

**为什么排第二**：是真正能让 agent-go 跑长任务的前提。OpenAI 那个"5 个月 100 万行不写代码"的实验，没明说但显然有这一层。

**Phase 1 — 数据模型（1 天）**

- `pkg/task/types.go` 加 `Checkpoint`：
  ```go
  type Checkpoint struct {
      ID         string
      TaskID     string
      Index      int       // 0..N
      AfterTool  string    // task_complete / 关键 read / 关键 write
      Messages   []Message // task-scoped history snapshot
      MemoryDelta map[string]any
      DiscoveredTools []string
      CreatedAt  time.Time
  }
  ```
- `pkg/agent/store.go` + `pkg/store/agentgo_db.go` 加 `task_checkpoints` 表 + CRUD。

**Phase 2 — 落 snapshot（1 天）**

- 在 `forwardRuntimeEvents`（`pkg/agent/async_tasks.go:440`）感知到以下事件时落 checkpoint：
  - `task_complete` / `task_blocked` 子工具调用前
  - 任意 `Destructive=false` 的 tool 成功调用后（read 类廉价、值得攒）
  - 每 N 轮（默认 5）兜底
- snapshot 写库走后台 channel + dedup，不阻塞 runtime。

**Phase 3 — Resume（1.5 天）**

- 新 API：`manager.Tasks().Resume(ctx, taskID, ResumeOptions{FromCheckpoint: <id|"latest">})`。
- 复活 task：从 checkpoint 重建 messages + memory + discovered tools，runtime 从下一轮继续。
- 失败 task 自动建议最近 checkpoint：`Tasks().Get(...)` 返回的 task 上加 `LastSuccessfulCheckpoint *Checkpoint`。
- CLI：`agentgo task resume <task_id> [--from <checkpoint_id>]`。

**Acceptance criteria**

- [ ] 一个故意 panic 的 mock agent 跑到第 3 轮挂掉，resume 后能从第 2 轮 checkpoint 恢复并跑完。
- [ ] checkpoint 写入不阻塞 runtime（race detector 干净）。
- [ ] snapshot 体积有上限（默认 256KB / 个），超过就压缩或丢早期。
- [ ] CLI `task resume` 端到端跑通。

**估时**：3-4 天。

---

### Track 3 — 行为级 Eval Harness

> **状态**：Phase 1 ✅ / Phase 2 ✅ / Phase 3 ✅。**升级版（mock + live profile + CLI 模块化）也完成**：
> - `mode: live` scenario 走真 LLM；`agentgo eval --profile=live` 调本地 provider pool
> - 每条 scenario 可声明 `runs: N` 聚合 pass rate
> - Pretty terminal table + 时间戳 JSON dump 到 `eval/results/`
> - `make eval` / `make eval-verbose` / `make eval-live` / `make eval-all` 四个 target
> - 真跑：`live_responder_short_answer` 2/2 通过、`live_dispatcher_no_bounce_back` 2/2 通过

`go test` 测代码正确性，**没人测 agent 行为正确性**。需要一组 scenario，每个 scenario 描述：input + expected tool sequence + expected final-text shape，每次 framework 改动跑一遍输出数字。

**为什么排第三**：是前两条的"乘数"。Track 1 砍 instruction 砍得对不对、Track 2 checkpoint 选点选得好不好——只有 eval 能给出数字。

**Phase 1 — Scenario 格式（1 天）**

- 新建 `eval/` 目录，每个 scenario 一个 YAML：
  ```yaml
  name: dispatcher_routes_memory_save
  input: "记一下明天和老王 6 点吃饭"
  expected:
    target_agent: Archivist
    tool_calls_must_include: [task_complete]
    tool_calls_must_not_include: [route_builtin_request_again]
    final_text_matches: '已记下.*\d{4}-\d{2}-\d{2}'  # 必须是绝对日期
    max_turns: 5
  ```
- 跑器：`eval/runner/runner.go`。注入 mock LLM 或真模型（profile 切换）。

**Phase 2 — 起步 scenario 集（1 天）**

10-15 个，覆盖：
- 每个 built-in agent 的 happy path 各 1
- Track 1 的三条 lint 各对应 1 个 success + 1 个 should-self-heal
- 跨 agent delegation 1 个（Dispatcher → Operator → Verifier）
- 长任务 + checkpoint 1 个（配合 Track 2）

**Phase 3 — CI 集成（半天）**

- `make eval` 跑 mock-LLM profile（确定性，可在 CI 跑）。
- `make eval-live` 用真 provider（本地手动跑）。
- 输出指标：`pass_rate / avg_turns / avg_token_cost / lint_self_heal_rate`，落到 `eval/results/<timestamp>.json`。

**Acceptance criteria**

- [ ] `make eval` 在 CI 全绿，pass rate ≥ 95%。
- [ ] 至少有一次"改 framework → eval 数字变化"的真实记录（哪怕只是 token cost 降 5%）。
- [ ] 文档说明怎么加新 scenario。

**估时**：2.5 天。

---

## 范围控制（**不**做的事）

- ❌ **不做** "六大模块" 一次全上。这次只做 lint / checkpoint / eval 三件。其它（observability、human-in-the-loop、context compaction）等这三件落地后再评估。
- ❌ **不把** human-gate 做成 framework 默认。Destructive 工具加可选 `RequiresConfirmation` 字段够了。agent-go 的设计偏自治，硬塞默认 confirmation 反而拧着走。
- ❌ **不把** 所有 prompt 都搬到 lint。判断标准：能用确定性规则表达的搬，需要语义理解的留在 prompt。
- ❌ **不动** 当前已经 work 的 streaming / PTC / skill resolver / memory workflow，除非和上面三件 track 直接冲突。

---

## 顺序与并行

```text
Track 1 (lints)         ─────▶ ✅ Phase 1+2
                                       │
                                       ▼ provides hookable harness
Track 3 (eval harness)  ───────────────▶ ✅ Phase 1+2+3
                                                     │
                                                     ▼ measures behavior
Track 1 Phase 3 (砍 prompt) ─────────────────────────▶ ✅ done
                                                     │
                                                     ▼ behaviors validated
Track 2 (checkpoint)    ──────────────────────────────────▶ build ──▶ done
```

- Track 1 Phase 1+2 已完成。
- **Track 3 紧随 Track 1（已调整次序）**：先建测量再建机制，是 harness 思维本身。eval 出来后才能量化"剪 prompt 剪得对不对"。
- Track 1 Phase 3（砍 instruction 字符串）放在 eval 之后做——没 eval 撑腰，剪 prompt 是赌博。
- Track 2 最后做，因为它最伤筋动骨；做完之后 Track 3 加上"长任务 + checkpoint"那个 scenario 闭环。

总估时（顺序串行）：**~7-8 个工作日**。可以中间穿插 v2.69.x 的小 patch 发布。

---

## 版本计划

| 里程碑 | 版本 | 内容 |
|---|---|---|
| Track 1 完成 | v2.69.0 | output lint registry + 3 条初始 lint + dispatcher/archivist instruction 瘦身 |
| Track 3 Phase 1+2 完成 | v2.70.0 | `eval/` 目录 + runner + 起步 scenario 集 |
| Track 3 CI 接入 | v2.70.1 | `make eval` 接入 |
| Track 2 完成 | v2.71.0 | task checkpoint + resume + CLI 接入 |
| 三 track 整体收敛 | v2.72.0 | 文档 + harness engineering 章节加进 CLAUDE.md / AGENTS.md |

---

## 附录 A：已完成的"AgentGo 学到了什么"（旧路线收敛）

旧 PLAN.md 罗列的 9 项中，至少 8 项已经成型：

1. ✅ Runtime kernel 化（`runtime.go` / `service_execution.go` / `subagent.go` 共享主干）
2. ✅ Tool 执行状态机（`ReadOnly` / `ConcurrencySafe` / `Destructive` / `InterruptBehavior`）
3. ✅ PTC deferred discovery 模式（`pkg/agent/ptc_integration.go`）
4. ✅ Skill metadata + relevance（`when_to_use` + `paths` + `ResolveForModel`）
5. ✅ Skill surfacing 走 `<skill-discovery>` reminder 注入
6. ✅ Memory workflow（`MEMORY.md` + `_session/*.md` + 后台 durable writer）
7. ✅ Prompt 工程化（template + section + dynamic 三层注册）
8. ✅ RAG 降级为 optional（无 embedding 也能完整跑）
9. ✅ Task identity（v2.68.1 修复 ID 一致性 bug 后，task_id 路径完全打通）

旧路线里还遗留的"runtime 单一 state object"、"工具加载 request-prep 中心层"——可以继续往后排，但不放进本计划，等 harness 三件事落地后再看是否还相关。

---

## 一句话

> 把"不要 ..."从 prompt 搬到 lint。把"task 跑挂了"从重头来变成回到上一站。把"agent 表现好不好"从感觉变成数字。
>
> 这三件事一做完，agent-go 就不再是"一堆聪明补丁"，而是一个真正意义上有 harness 的 agent framework。
