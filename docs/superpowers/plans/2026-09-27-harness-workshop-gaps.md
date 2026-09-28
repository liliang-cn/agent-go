# Harness workshop 缺口：计划与量化方法

来源：Vizuara「Harness Engineering Workshop」五天字幕 + Odysseus 参考实现，对照本仓库代码逐条核实过的缺口。课程本身没有消融数字，所以每一项的「有没有用」都要在这里自己量。

## 总规则

- 每项一个 worker，各自一个 git worktree，从当前 HEAD 分叉。只在自己的分支上 commit；commit message 一行标题，不带 body，不带任何 Co-Authored-By / 生成声明。不合并到 main，不 push。
- 主工作区里有未提交的 native-web-search 改动（`pkg/pool`、`pkg/poolsvc`、`pkg/store`、`pkg/config`、`CLAUDE.md`），worker 分支里没有它，不要去碰这些文件的相关部分。
- 遵守 CLAUDE.md：一个 loop；不许有读用户请求的短语表；模型反复犯错就写 lint 不加 prompt 句子；新公开功能带 `examples/<feature>/main.go`；可选接口不往 `Observer` 上加方法。
- 门禁：`gofmt -l` 为空、`go vet ./...`、`go test ./...`、`make eval` 全过；动到 `runtime.go` / `subagent*.go` 的跑 `go test -race ./pkg/agent/...`。
- 交付：分支名、改了什么、**量出来的数字**（前 vs 后），以及任何没量成的东西直说。

## 量化基础设施

### B0 实跑基准（scratch 模块，不进仓库）

位置：会话 scratchpad 下的 `bench/`，一个独立 Go module，`replace github.com/liliang-cn/agent-go/v3 => <被测 worktree 路径>`，所以同一份基准能量任意分支。

任务（固定、二元判定）：工作区预置一个小 Go 模块（有一个会失败的测试）+ 一份约 1.2 万行的 `app.log`，根因藏在日志中段。目标：找出根因、修代码让 `go test ./...` 退出 0、写 `REPORT.md` 说明根因。成功 = 两者都成立（由 harness 自己跑 `go test` 判定，不信模型自述）。

配置：`agent.New(...).WithLLM(cpa gemini-3.8-flash-high)`，LocalSandbox 指向每次新建的工作区，`MaxRounds` 60，挂 `TraceWriter`。key 从 `~/.zshrc` 第 359 行 eval 进环境，不打印。

每次运行输出一行 JSON：success、rounds、总 prompt/cached/completion tokens、单轮最大 prompt tokens、compaction 次数、compaction 摘要模型调用数、estimated vs reported prompt tokens（逐轮）、墙钟时间。每个配置 k=3。

cpa 的 antigravity 配额有限，不要连打；一次配置 3 次跑完再下一个。

### 各项的量法

| 项 | 指标 | 方法 |
|---|---|---|
| A 输出上限 | 单轮最大 prompt、总 prompt tokens、成功率 | B0，前后对比；另加确定性单测：读 1 万行文件后历史里的字节数 |
| B 机械截短 | compaction 摘要调用数、总 prompt、缓存命中率、成功率 | B0；单测：截短后 call id 配对完整、重复记录被清 |
| C 窗口阈值 + usage 锚定 | 估算误差 \|est−reported\|/reported 的均值 | B0 trace 里逐轮对比，改前纯估算 vs 改后锚定 |
| D 子 agent 并行/深度 | fan-out 墙钟时间 | mock LLM 每次调用 sleep 300ms，一轮 4 个 `task`：串行≈4×，并行≈1×；深度超限的结构化拒绝；`-race` |
| E 权限留痕 | 覆盖率 | 测试：每个 Destructive 调用在 trace 里恰有一条 permission 记录，含决定者 |
| F verifier | 通过率、额外 token | 6–8 个「容易谎报完成」的 live 场景，有/无 verifier 各 k=3 |
| G bash 拒绝列表 | 拦截率、误拦率 | 复用 laya 测试语料（scratchpad `corpus.json`、`hard.json`）里与目标无关的「必危」子集和全部安全样本 |
| H AGENTS.md | 正确性 + 前缀字节稳定 | 单测：向上查找、最近者优先；两轮间 prompt 前缀逐字节相同 |

## 工作项

### A 工具输出统一上限
- `fs_read` 默认分页（limit=0 不再等于全文），结果末尾写明总行数和下一个 offset。
- `tool_round.go` 对所有工具（含 MCP）的结果统一截断：保头保尾，中间写被截掉多少、怎么取剩下的。阈值可配。
- 截断发生时发一个可观测的信号（trace 行或 observer 已有字段），不静默。

### B 压缩：机械截短 + 不会失败
- 摘要前先把 N 轮以前的 tool 结果换成一行存根（工具名、参数摘要、大小、首尾几行），call id 不动；之后清重复调用记录。
- 摘要失败的兜底：连续 2 次失败后改用不调模型的摘要（目标、计划及 notes、写过的文件、最后错误），按字符预算；带冷却，不每轮重试。`OnCompaction` 报告是否降级。
- 保护区从 system 扩到第一条 user 消息。

### C 压缩阈值按模型窗口 + usage 锚定（和 B 同一个 worker，同文件）
- `pool.RegisterModelWindow` 风格的覆盖；阈值 = (窗口 − max output) × 比例（Hermes：50%，下限 64k，小窗 75–85%）。
- 窗口未知：明示（trace/Doctor），退回现在的 60000，不静默。
- token 估算用最近一次 provider 返回的 prompt tokens 为锚，只估之后新增的消息。

### D 子 agent 并行、深度上限、模型
- `SubagentSpec` 可声明可并行（默认否），让 `task` 进现有的并行批次；并行度上限可配。
- context 里带深度，超过 `WithSubagentMaxDepth(n)` 时工具结果回结构化拒绝。
- `SubagentSpec.Model`，走已有的 `WithModel` 路径。

### E 权限决策留痕
- 可选 `PermissionObserver`（不加到 `Observer`），`authorizeTool` 不再丢 `PermissionResponse.Metadata`；`TraceWriter` 写 `"event":"permission"`（工具、决定、决定者、理由）。

### F verifier 扩展
- `pkg/extensions/verifier`：实现 `OutputLint`，用一个命名的验证子 agent 审最终答案，不通过则按现有 lint 重试额度驳回，反馈只带理由。显式开启。

### G bash 危险命令拒绝
- `pkg/extensions/bashguard`：`ToolCallFilter`，只看模型写出的命令（输出侧），规则可配置、有默认集。报告拦截率和误拦率。

### H 项目指令文件
- Builder 选项：从工作区向上找 `AGENTS.md`（及可配文件名），最近者优先，放进稳定的 system 前缀。

## 执行顺序

1. 并行：B0（先量 HEAD 基线）、A、B+C、D、E、F、G、H。
2. 全部回来后：B0 分别量 A 分支、B+C 分支、A+B+C 合并分支，各 k=3，和基线对比。
3. 汇总成一张表交给用户决定合并哪些。

另：CLAUDE.md 里 `CompactionDefaultThresholdTokens` 写的 8000 与代码的 60000 不符，在主工作区直接改。
