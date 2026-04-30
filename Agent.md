# 不要再做"一个无所不能的 Agent"了：AgentGo 是怎么把一群 Agent 拆开又粘起来的

如果你最近也在做 Agent，你大概率走过这条路：

第一版，写一个 prompt 长得像章程的"超级助手"。它能聊天、能调工具、能查记忆、能做规划。Demo 里挺好。

第二版，开始出问题。任务一复杂，模型就开始"我接下来会..."、"下一步可以..."、"建议你..."。明明能直接做，它非要说一遍它打算怎么做，然后停下来等你点头。

第三版，更糟。你给它接了 10 个 MCP 工具，它每轮都要把 10 个工具的 schema 装进上下文。一通对话下来，token 翻了两番，回答还没第二版准。

到这一步，大多数人会做两件事之一。要么把 prompt 越写越长，越写越像合同；要么开始给一个 agent 加一堆"模式"——这是咨询模式，那是执行模式，再加个"反思"模式。最后你会发现，这玩意已经不是一个 agent 了，是一个有人格分裂症的 agent。

AgentGo 的答案很简单：**不要让一个 agent 演七个角色，让七个 agent 各演一个角色。**

听起来像废话。但真做下来，发现这背后牵扯的设计远比"多 agent"三个字复杂。这篇文章把 AgentGo 当前的 team / agent 设计拆开来讲，包括它为什么这么分、调度链怎么走、每个角色的边界在哪、以及几个反直觉的工程取舍。

---

## 一、AgentGo 的内置 agent 名单

打开 `pkg/agent/team_defaults.go`，能看到一份默认 agent 清单。先把名字摆出来，后面再讲它们各自的边界：

- **Dispatcher**：调度员。永远在线，唯一的用户入口。
- **IntentRouter**：意图路由。判断"用户到底想干嘛"，挑下游 agent。
- **PromptOptimizer**：提示优化。把用户的口语化请求改写成下游 agent 能直接干活的指令。
- **Responder**：通用助手。负责日常问答、解释、起草。
- **Operator**：执行专员。文件操作、命令运行、MCP 工具、设备控制都归它。
- **Evaluator**：产品/业务代理。负责需求澄清、优先级、验收标准。
- **Archivist**：记忆专员。负责长期事实、偏好、日程、记忆清洁。
- **Verifier**：核验员。专门做事后验证、答案置信度判断、冲突检测。
- **Orchestrator**：团队协调员。当多个 agent 组成一个 team 时，由它做团队级的拆分协调。

九个角色，每个都有非常明确的"我不干什么"。这一点比"我能干什么"更重要。

举一个具体例子。Dispatcher 的 instruction 第一句话是：

> Your only job is intake, routing, status inspection, task planning, and task dispatch. **Do not do substantive work yourself** unless the user is asking for dispatch metadata, agent or team status, task status, or task-plan coordination.

翻成人话：Dispatcher 不写代码，不查文件，不答专业问题。它只负责把用户的请求转给真正干活的 agent。

为什么要这样卡？因为只要给 Dispatcher 留一个"如果是简单问题你也可以自己答"的口子，模型一定会不停地自己答。它会觉得"反正我也能说，何必再调一层"。一旦它开始自答，整个调度结构就坍塌了——你以为 Operator 在执行，其实 Dispatcher 在编。

让一个 agent 不干活，比让它干活难。

---

## 二、三层调度：Dispatcher 不要再"自己想办法"了

AgentGo 的核心调度链长这样：

```
用户输入
   ↓
Dispatcher（永远在线，决定要不要 route）
   ↓
route_builtin_request 工具
   ↓
   ├── PromptOptimizer  ← 并行
   └── IntentRouter      ← 并行
   ↓
（IntentRouter 选出 target_agent）
（PromptOptimizer 给出 cleaner prompt）
   ↓
target_agent（Operator / Responder / Archivist / ...）
   ↓
（如果是高置信度敏感场景）
   ↓
Verifier
   ↓
最终回到 Dispatcher，由它返回给用户
```

`pkg/agent/dispatcher_route.go:84` 这一段值得专门看一眼：

```go
var wg sync.WaitGroup
wg.Add(2)

go func() {
    defer wg.Done()
    routerRaw, routerErr = dispatch(ctx, defaultIntentRouterAgentName, ...)
}()

go func() {
    defer wg.Done()
    optimizerRaw, optimizerErr = dispatch(ctx, defaultPromptOptimizerAgentName, ...)
}()

wg.Wait()
```

IntentRouter 和 PromptOptimizer 是**并行**跑的，不是串行。这两件事之间没有依赖：路由判断不需要看到优化后的 prompt，优化也不需要先知道路由结果。把它们串起来只会平白增加一倍延迟。

很多 Agent 框架在这里会犯一个错误：**把"多个步骤"误以为是"必须串行的多个步骤"**。一旦你养成"调度 = 流水线"的思维，整个系统的尾延迟会大到没法用。

PromptOptimizer 这个角色也值得单独讲一句。它的存在不是为了让 prompt"更漂亮"，而是为了**把噪音从下游 agent 的上下文里抠掉**。用户原始请求里常常带着很多对下游 agent 没用的信息：寒暄、口头禅、上下文铺垫、自我修正。PromptOptimizer 的 instruction 写得很硬：

> Preserve facts, dates, constraints, names, and intent. Do not invent missing details, do not change commitments, and do not do the downstream work yourself.

它不是改写者，是过滤器。

### 一个真实例子：怎么处理"记一下明天和老王吃饭"

把上面这条调度链落到一个具体例子上，看一下每一步发生了什么。

用户在 chat 里输入：

> 记一下明天和老王 6 点吃饭

第一步，Dispatcher 收到这条消息。它的 instruction 已经被钉死了——这不是"task-plan 类"的请求（用户没说"创建计划"或者"work plan"），所以 Dispatcher 直接调 `route_builtin_request(prompt="记一下明天和老王 6 点吃饭")`。

第二步，`routeBuiltInRequest` 起两条 goroutine：

- IntentRouter 拿到这条 prompt，看到"记一下"+ 时间 + 人物 + 事件，输出 `{target_agent: "Archivist", needs_optimization: false, reason: "记忆类请求"}`。
- PromptOptimizer 同时跑，但因为这条请求已经够干净，它的输出对最终 prompt 影响很小。

第三步，路由结果出来后，`buildFinalBuiltInDispatchPrompt` 把原始请求 + 优化后 prompt + decision 拼成最终的下游 instruction，并 append 上 Execution Contract（"不要把任务弹回 Dispatcher"、"基于工具结果验证完成"、"Finish-Or-Block"）。dispatchTaskID 是 `<parent>:dispatch`，用于后续追踪。

第四步，Archivist 拿到这条优化后的 prompt。它的 instruction 写得很具体：

> IMPORTANT: always resolve relative date and time references (明天, 后天, 下周一, ...) to absolute calendar dates and clock times using the current date/time injected in the runtime context before storing.

这一句不是漂亮话。如果它把"明天"当字面值存了，三天后用户问"老王那顿饭是哪天"，模型就得猜。所以 Archivist 必须把"明天"解析成"2026-04-29"再写入。这是写记忆时强制的规范。

第五步，Archivist 调 `task_complete(result="已记下：2026-04-29 18:00 与老王吃饭")`，runtime 状态机收到终态工具调用，task 转入 completed。

第六步——也是最容易忽略的一步——Verifier 是否要跑？`shouldVerifyBuiltInDispatchCompletion` 判定这条请求是低风险写入，不触发 Verifier。整条链结束。

整个过程从用户输入到最终回复，调用链清晰可追：每个 agent 都有自己的 task_id，每一次模型调用都有事件流，CLI 端能看到 IntentRouter / PromptOptimizer 并行启动，Archivist 流式输出，最后给出确认。

如果换一个稍微危险的请求，比如：

> 把昨天那条记忆删掉

Archivist 的处理路径会不一样。它不会直接删，而是会发出 `VERIFIER_NEEDED: candidate=<删除目标>; reason=可能存在歧义`，触发 Verifier 介入做二次确认。这条 escalation 路径就是上文提到的"Verifier opt-in"机制——用例本身决定是否要核验，不是框架统一拉满。

---

## 三、Finish-Or-Block：拒绝"我下一步会..."

如果你只能从 AgentGo 拿走一个设计，那就拿走这个：**Finish-Or-Block Contract**。

`pkg/agent/task_contract.go` 里这段 prompt 短到可以背下来：

```
Finish-Or-Block Contract:
- Do not stop at planning when the task can be executed now.
- Do not end with "next steps", "would do", "should do", or
  "I can do this next" unless external input is genuinely required.
- Continue until the task is completed, blocked, failed, or yielded.
- If completed, provide the verified result and concrete evidence
  when tools or filesystem/device actions were involved.
- If blocked, call task_blocked with the concrete blocker and
  evidence of what was attempted.
- Prefer verification over explanation.
```

这段被 append 到几乎每一个会真正干活的 agent 的 instruction 末尾（`team_defaults.go:56`）。它解决的是 Agent 框架最常见也最致命的一个 failure mode：**模型说它要做什么，然后就停下来等你确认**。

你大概见过这样的回复：

> 好的，我接下来会读取 README.md，分析它的结构，然后总结成五个要点。

——然后它就停了。它没读，也没分析。它只是说了一遍它会做什么。

这背后的原因很 trivial：模型 RLHF 训练里被教导要"礼貌"、"不擅自行动"、"先确认再做"。这种偏置在 chat 场景下是优点，在 agent 场景下是致命伤。Finish-Or-Block 就是把这个偏置硬扳回来。

合约里两个动作要特别注意：

1. `task_complete(result=...)`：必须给出"已完成"的具体证据，不是"已经做了"，是"这是结果"。
2. `task_blocked(blocker=..., evidence=...)`：必须说清是什么挡住了你，以及你尝试了什么。

任何**没有走到** complete / blocked / failed / yielded 任一终态的轮次，都算系统故障，不算"agent 在等用户"。这是 AgentGo 不允许"礼貌停顿"的根本原因。

附带一个工程后果：因为终态强制由工具调用触发（`isTaskTerminalToolName`），框架可以靠工具名做严格的状态机判定，不再依赖解析模型生成的自然语言来判断"这一轮算不算结束"。

---

## 四、Task 是一等公民，不再是 session 的附庸

很多 Agent 框架的状态模型只有一个单位：session（或者叫 conversation）。一个用户、一段对话、一段记忆。但只要你做过一点像样的 agent 系统，你就知道这远远不够。

AgentGo 的 `pkg/domain/types.go` 和 `pkg/agent/types.go` 现在把 **task** 当作和 session 同级的执行边界。每个 dispatch、每个 delegation、每个 plan item 都有自己的 `task_id`。

这件事的具体表现：

- **历史过滤按 task 走**：Operator 在 task A 的上下文里看到的历史，不会泄漏 task B 的中间产物。一个 session 同时跑两条任务链，互相不会污染上下文。
- **memory 写入按 task 标签**：`pkg/store/file_memory.go` 的 `_session/*.md` 现在带 task 维度。事后排查能精确定位"是哪一次执行写下了这条记忆"。
- **discovered tools 按 task 缓存**：当 PTC 在某个 task 里搜索过工具，缓存的"已发现工具集"是 task-scoped 的，不会和别的 task 串味。
- **取消按 task 粒度**：用户中断当前请求，只取消当前 task，不连坐它的兄弟 task。

为什么这件事重要？因为只要你的系统支持后台任务、支持 team 级 delegation、支持 task plan，session 这个边界就太粗了。你需要更细的隔离单元。

更现实的一个例子：

> 用户：帮我把这个 PR 的 review 起草一份，然后帮我把昨天的会议纪要写下来。

session 级实现里，这两件事会在同一段历史里混着跑。模型能"看见"它刚才在 review 里说了什么，于是会把 review 的语气带到会议纪要里。task 级实现里，两件事各走各的 task_id，历史天然隔离。

session 是身份的边界。task 才是执行的边界。两者不该是同一个东西。

---

## 五、跨 agent 的流式：用户应该能看见 Operator 正在打字

普通 chat 里，模型流式输出大家都见过：token 一个一个蹦出来，用户立刻有反馈。

但跨 agent delegation 不一样。当 Dispatcher 把任务转给 Operator，绝大多数框架的实现是：等 Operator 跑完，把整段结果丢回来，再由 Dispatcher 在 chat 里展示。中间的等待时间，用户看到的是一个空白。

这不是"等待"问题，是**信息不透明**问题。Operator 可能正在调一个慢的 MCP 工具，可能正在做长输出，也可能卡住了。但用户看不到，他只能猜。

AgentGo 在 `pkg/agent/team_manager.go:1354` 的 `dispatchTaskWithOptionalStream` 做了显式的可选流式分发：

```go
func (m *TeamManager) dispatchTaskWithOptionalStream(...) {
    if !stream {
        return m.dispatchTaskWithOptions(...)
    }
    sink := eventSinkFromContext(ctx)
    if sink == nil {
        return m.dispatchTaskWithOptions(...)
    }

    events, err := m.dispatchTaskStream(...)
    if err != nil {
        return "", err
    }
    return collectDispatchStreamResult(events, sink)
}
```

关键点是 `eventSinkFromContext(ctx)`：调用方在 context 里注入一个回调，下游 agent 的每一个 partial token、每一个工具调用事件，都会被实时塞回这个回调。

更妙的是嵌套传播：当 A delegate 给 B 之后，B 的事件会沿 A 的 event sink 一路冒泡，最终到达最外层的订阅者。这意味着 CLI 或者 UI 只要订阅一次，就能看到整条调用链的实时活动。

这背后还有一层细节。AgentGo 的 `forwardRuntimeEvents`（`pkg/agent/async_tasks.go:440`）会把每个 streamed `Event` 立刻包成 `TaskEvent.Runtime`，订阅 `SubscribeTask` 的客户端不会等待——事件来一个就转发一个。

光有这套机制还不够。如果调用方（CLI、UI、上游 agent）不主动订阅，所有这些 partial event 就只是在框架里转一圈然后被丢弃。最近 AgentGo 的 CLI 才把这一段彻底接上：在 `cmd/agentgo-cli/chat_tasks.go` 增加了一个 `chatTaskStreamRenderer`，把 partial event 实时渲染到终端，并在最后任务完成时合并掉重复的 final block。

设计原则其实只有一句：**stream 是默认的，buffered 是退路，不是反过来。**

---

## 六、PTC：让 agent 写代码，不要让它一轮一轮等工具结果

AgentGo 的另一个非典型设计是 PTC，Programmatic Tool Calling。

普通工具调用长这样：

```
模型 → 调用工具 A
程序 → 把 A 的完整结果返回模型
模型 → 调用工具 B
程序 → 把 B 的完整结果返回模型
模型 → 看完所有原始数据后回答
```

每一次工具调用都是一次模型往返。每一次返回都把原始结果完整塞回上下文。如果工具结果很大（日志、明细、搜索结果），上下文很快被原始数据淹没。Claude Cookbook 给出的对比数字是：传统调用 ~110k token，PTC 方式 ~16k token，下降 ~85%。

PTC 把控制流下沉到沙箱：

```
模型 → 写一段 JavaScript
沙箱 → 执行，里面调用 callTool(...) 多次
代码 → 在沙箱里过滤、聚合、计算
代码 → 只 return 最终结构化结果
模型 → 基于小结果回答
```

AgentGo 的 PTC 跑在 Goja 沙箱里（`pkg/ptc/`），是**默认开启**的。这个选择本身就是个观点：当一个任务可以用循环和过滤表达时，让模型写代码比让它做 N 轮"思考-调工具-再思考"要稳得多，便宜得多。

PTC 也有它不擅长的场景：高副作用的写操作、需要主观判断的内容生成、工具契约不稳定的调用。AgentGo 给 PTC 划了一条线——`task_complete` 和 `task_blocked` 这种"终结类"工具不允许在沙箱里调用，必须由模型在主对话循环里直接调用。原因很简单：**任务终态是 runtime 状态机的转移点，不能藏在沙箱代码里**。

这套机制和上面的 Finish-Or-Block、Task 第一公民其实是同一个工程纪律：**关键状态转移必须显式可见，不能埋在生成内容里。**

---

## 七、几个反直觉的工程取舍

写到这里，你可能注意到 AgentGo 的设计里有几个看起来"多余"的东西。它们值得专门讲一下，因为这些选择是在踩过坑之后才有的。

**1. 不让 Dispatcher 拥有完整工具集。**

Dispatcher 只有 dispatch 和 status 类工具——`route_builtin_request`、`list_teams`、`get_task_status`、`task_plan_*` 等等。它**没有**文件读写、没有 MCP、没有命令执行。理由前面讲过：一旦它能"自己干"，它就会自己干，就不会再调下游。

把工具从 Dispatcher 身上拿掉，是为了保持调度结构的可观测性。

**2. PromptOptimizer 和 IntentRouter 是两个 agent，不是一个。**

完全可以合并。一个 agent 同时返回 `{target_agent, optimized_prompt}` 是技术可行的。但 AgentGo 把它们分开，理由是：意图分类是一个低维分类问题，prompt 优化是一个高维改写问题。两者用同一个 prompt 一起做，模型会偏向其中一边，另一边的质量塌陷。分开做，每个角色 prompt 短，决策清晰。

代价是多一次模型调用。但因为是并行的（前面讲过），延迟开销几乎可以忽略。

**3. Verifier 不会自动跑。**

很多框架的"反思"模块默认每轮都跑。AgentGo 不一样：Verifier 只在 `shouldVerifyBuiltInDispatchCompletion` 判定为高置信度敏感场景时才被触发（比如 Archivist 主动给出 `VERIFIER_NEEDED:` 前缀的时候）。

为什么？因为强制反思相当于把成本翻倍，但收益只在少数场景明显。把验证设成 opt-in、由下游 agent 主动 escalate，比统一拉满开销更工程化。

**4. Memory ≠ Cache ≠ RAG。**

`pkg/memory` 是持久化的对话/任务记忆，写入 file-backed `MEMORY.md` 和 `_session/*.md`。`pkg/cache` 是进程内的临时缓存。`pkg/rag` 只在配置了 embedding 模型时才会激活。

很多人把这三件事混在一起做。最后的结果是：你以为它在记你，其实它每次都重新检索；你以为它在缓存，其实它把缓存内容也写进了"长期记忆"。AgentGo 把三者从概念到实现都拆开，是因为它们的失效模式完全不同：

- 记忆失效：信息不再准确
- 缓存失效：信息陈旧但还可恢复
- 检索失效：信息不在当前 chunk 里

一个统一的接口处理不了这三种失效。

**5. 一个裸 AgentGo（没接 embedding 模型）也是完整可用的。**

很多 RAG-first 的框架会强行假设 embedding 是基础设施。AgentGo 不假设。没 embedding，Agent + MCP + Memory + PTC 都还能正常跑。RAG 只是 _一个可选能力_，不是 _系统的前置条件_。

---

## 八、Standalone 与 Team：两种不同尺寸的协作

前面讲的 Dispatcher / Operator / Archivist 这一组，其实是 **standalone 内置 agent**。它们直接挂在系统层面，不属于任何具体 team。

但 AgentGo 还有第二种结构：**Team**。一个 team 内部有自己的 Orchestrator，下面挂自己的成员 agent。team 的 Orchestrator 和系统级 Dispatcher 的区别是：

- Dispatcher 永远在线，是用户的唯一入口。
- Orchestrator 只在 team 被显式激活时介入，处理团队内部的任务拆分。

为什么需要这一层？因为有些工作天然是"团队工作"。一个文档团队可能包括 Researcher、Writer、Editor 三个角色，它们之间的协作模式不应该被 Dispatcher 直接管。Dispatcher 只需要知道"这个请求归 docs team"，然后把任务交给 docs team 的 Orchestrator。Orchestrator 再决定让 Researcher 先查、Writer 起稿、Editor 收尾。

`team_defaults.go:144` 里 `defaultBuiltInOrchestrator` 给出的 instruction 短到不能再短：

> You are Orchestrator, the built-in orchestrator agent for <team>. Handle direct team requests when possible and coordinate specialists when that improves the result.

注意 "when possible" 和 "when that improves the result" 这两个限定。Orchestrator 不被强制要求一定要做协调——如果它自己能直接答，它就直接答。这条规则避免了"凡事都拆"的过度协调成本。

这种"两级调度"的好处是边界清晰：

- 用户不需要知道 team 的存在，全部走 Dispatcher。
- Team 内部成员不需要知道 Dispatcher 的存在，由 Orchestrator 接手。
- 跨 team 的协调由 system-level dispatch 触发，不是 team 之间互通有无。

把它和单层调度对比一下：如果只有 Dispatcher 一层，所有协作都得在它的工具集里展开。Dispatcher 的工具列表会膨胀到非常难维护，并且每次新增一个 team 都要改 Dispatcher 的 prompt。两级结构里，Dispatcher 只认 team，team 内部怎么玩是 team 自己的事。

---

## 九、一句话总结

如果你回头看上面这些，会发现 AgentGo 在做一件很朴素的事：**把一个 agent 的所有职责，一个一个地拆开**。

- 调度和执行分开 → Dispatcher vs 下游
- 路由和改写分开 → IntentRouter vs PromptOptimizer
- 干活和验证分开 → Operator vs Verifier
- 身份边界和执行边界分开 → session vs task
- 控制流和数据流分开 → 主对话 vs PTC 沙箱
- 缓存、记忆、检索分开 → cache vs memory vs RAG
- 流式和聚合分开 → event sink 默认开，buffered 是退路
- 终态和过程分开 → task_complete / task_blocked 是显式工具调用，不是自然语言

每一对分开，初看都像"过度设计"。但只要你在 agent 系统里跑过几次踩雷，就会知道：**真正的复杂度不来自"做多 agent 系统"，而来自"假装一个 agent 能做完所有事"**。

单 agent 的诱惑是整洁。多 agent 的麻烦是显式。但显式才是工程的代价，整洁不过是临时的体面。

下一次你做 Agent，问自己一个问题：**这个 agent 能不能拒绝做某件事？** 如果它什么都能做，那它什么都做不好。如果它什么都不肯做，那它一定是一个调度员。

AgentGo 选了后者。

---

项目地址：<https://github.com/liliang-cn/agent-go>
