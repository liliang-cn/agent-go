# PLAN.md

## AgentGo 当前已经学到了什么

AgentGo 已经不再只是“会聊天、会调工具”的壳子，很多关键架构决策已经开始成型。

1. Runtime 开始像内核。
   `pkg/agent/runtime.go`、`pkg/agent/service_execution.go`、`pkg/agent/runtime_query_state.go` 已经有显式 stage、loop transition、tool state、streaming/shared execution 主干。

2. 工具执行语义已经成体系。
   `pkg/agent/tool_registry.go` 和相关 orchestration/runtime 逻辑已经把 `ReadOnly`、`ConcurrencySafe`、`Destructive`、`InterruptBehavior` 接入了 batching、permission、cancel、state update。

3. PTC 已经学到了 Claude Code 的 deferred discovery 模式。
   `pkg/agent/ptc_integration.go`、`pkg/agent/service_tools.go`、`pkg/agent/tool_discovery_state.go` 现在支持 search-first、按 session 激活、从历史恢复 discovered tools、compact 保留。

4. Skill 已经不再只是附属模块。
   `../skills-go/skill/skill.go`、`../skills-go/skill/registry.go`、`pkg/skills/service.go` 已经有 `when_to_use`、`paths`、`ResolveForModel(...)`，并且 `agent-go` 会在每轮 prompt 里显式 surfacing relevant skills。

5. Skill surfacing 已经开始像 Claude Code。
   现在不是把所有技能硬塞给模型，而是通过 `<skill-discovery>` reminder 把当前任务最相关的一小撮 skill 暴露出来，并激活对应 `skill_*` 工具。

6. Memory 已经从“只有检索”进化成 workflow。
   `pkg/store/file_memory.go` 和 `pkg/memory/service.go` 现在有 `MEMORY.md`、`_session/*.md`、header selector、后台 durable writer。

7. Prompt 已经工程化。
   `pkg/prompt/service.go`、`pkg/agent/system_prompt_sections.go` 已经是 template registry + section registry + dynamic sections。

8. 默认产品心智已经纠偏。
   embedding / RAG 已经被降级成 optional，不再作为默认前提；没有 embedding model 时，基础 Agent / MCP / Memory / PTC 仍然可用。

9. 任务隔离开始落地。
   `pkg/domain/types.go`、`pkg/agent/types.go`、`pkg/agent/service_helpers.go`、`pkg/agent/service_execution.go` 现在已经有 `task_id`、task-scoped history filtering、task-scoped runtime event 字段，并开始沿 async/team dispatch 传递。

## AgentGo 还缺什么

现在已经有很强的雏形，但离“真正定型的 Agent framework 内核”还有几块硬骨头。

1. 任务隔离还只是第一版。
   现在已经有 `task_id` 和 task-scoped history filtering，但 task 还没有成为真正的一等执行对象，很多逻辑仍然是 session-first、task-second。

2. Runtime 还没有完全收成单一状态机。
   虽然共享了很多主干，但 `runtime.go`、`service_execution.go`、`subagent.go` 仍然不是一个真正完全统一的中心状态对象。

3. Skill surfacing 已经是 reminder/delta 了，但还不是完整 attachment protocol。
   现在能工作，也已经像 Claude Code，但还没有它那套完整的 message-level attachment discipline。

4. 工具加载策略还没完全统一在一个中心层。
   现在 deferred/search/PTC visibility 已经有了，但逻辑仍然分散在 builder、helpers、PTC integration、tool registry 多处。

5. Task 调度虽然开始和 runtime 对齐了，但还没有一个真正统一的 task runtime policy。
   例如：
   - 任务如何终止
   - 任务如何 compact
   - 任务如何跨会话恢复
   - 任务如何做 partial success / retry
   这些还没有完全抽成统一模型。

6. Frontdesk / built-in agents 的完成判定还不够稳。
   skill 命中后虽然能正确调用，但还会继续多跑几轮，没有足够快地 `task_complete`。

7. Skill ranking 现在还只是第一版。
   已经有 `when_to_use + paths + ResolveForModel(...)`，但还不够预算化，也没有像 Claude Code 那样的“显式 discover more skills”那一整层。

## AgentGo 下一步该做什么

按优先级，只做下面五件，不再发散。

1. 把 `task` 做成真正一等执行对象。
   现在已经有 `task_id`，下一步应该把：
   - compact
   - memory
   - discovered tools
   - retry / recovery
   都以 task 为边界统一起来，而不是继续只有 session 级逻辑再打补丁。

2. 把 runtime 真正收成单一状态机。
   继续压 `pkg/agent/runtime.go` 和 `pkg/agent/service_execution.go`，让 streaming / non-streaming / subagent 只剩输出模式差异。

3. 修 Frontdesk / built-in runtime 的完成判定。
   现在 skill/tool 结果已经足够明确时，应该尽快 `task_complete`，不要继续空转多轮。这是用户最直接能感知到的效率问题。

4. 把 skill 提升成真正的 capability router 第一层。
   现在已经有 relevant skill discovery，下一步应该明确：
   - skill first
   - tool search second
   - raw tool call last
   并让这条策略成为 runtime 默认，而不是只靠 prompt 约束。

5. 统一工具加载的 request-prep 层。
   把：
   - direct tools
   - deferred tools
   - discovered tools
   - ptc-visible tools
   - model-visible tools
   统一进一层 request/tool preparation policy。这样后面不管是 Claude Code 风格 ToolSearch，还是 direct-call-first 策略，都能在一个中心层里切换。

## 总结

AgentGo 现在已经学到了 runtime 内核、deferred tool discovery、skill metadata、memory workflow、prompt assembly、task identity 这些关键模式。

真正还缺的是把这些能力彻底统一成 task-driven framework，而不是继续停留在“session-driven chat agent + 很多聪明补丁”。
