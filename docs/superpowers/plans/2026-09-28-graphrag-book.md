# 《Agentic GraphRAG》可借鉴项：计划与量化方法

来源：~/Downloads/Agentic-GraphRAG-读书笔记.md 最后一节。每一项都已对照代码核实过现状。书里的数字来自论文和厂商，这里不采信，每项的效果都要自己量。

## 总规则

- 每个 worker 负责一组改动，各自在独立的 git worktree 里做，只在自己的分支上 commit。commit message 只写一行标题，不带 body，不加任何 Co-Authored-By 或生成声明。不合并，不 push。
- agent-go：遵守 CLAUDE.md。只有一个 loop；不允许用短语表去读用户请求；新的 observer 能力做成可选接口；新的公开功能要带 `examples/<feature>/main.go`。门禁：`gofmt -l` 输出为空、`go vet ./...`、`go test ./...`、`go test ./eval/runner/`；改到 runtime、memory 或 subagent 时再加 `go test -race ./pkg/agent/... ./pkg/memory/...`。
- CortexDB：遵守它自己的 CLAUDE.md。门禁：`go build ./...`、`go test -race ./...`。新增的 MCP 工具要同时出现在工具清单和文档里。
- 交付内容：分支名、改了什么、**量出来的数字（改前 vs 改后）**，以及没能量成的部分如实说明。

## agent-go

### AG1 记忆：写入对账、类型、有效期（pkg/memory）
- 写入时对账：在现有的抽取调用里，顺带放入同作用域最相似的 5 条记忆，让模型为每条新内容给出 add / update / noop 和 target_id。结果为 update 的，调用 MarkStale（通过一个可选的 store 接口）。不新增模型调用。
- 召回时尊重有效期：已被取代的记忆不注入，或注入时明确标注；所有后端行为一致，并在 memorystoretest 里补一个用例。
- 记忆类型：world（世界事实）、experience（经验）、opinion（观点）、observation（观察），注入时带上类型标签。
- 量化：确定性场景「用户先说住在 A，后说搬到 B」，量旧答案率、重复条目数和模型调用次数，改前改后各一组。

### AG2 计划改为 DAG（PlanItem、计划工具、planSummaryForRun、lint）
- PlanItem 增加 ID 和 After（前置步骤）。计划工具拒绝成环，也拒绝勾选前置步骤未完成的步骤。计划摘要标出当前可以做的步骤。
- `types.go` 里的 `Step.DependsOn` 目前没有任何地方读，要么接上，要么删掉。
- lint：还有可做的步骤没有勾选时，不允许结束。
- LintContext 里加入 Plan。
- 量化：mock 场景，一个有依赖关系的计划（例如 3 个可并行的步骤加 1 个汇总步骤），量提前结束被拦下的次数；旧格式的计划要能照常读取。

### AG3 评测带成本 + eval-diff（eval/）
- RunResult 加 token、成本、每次通过的成本；增加 `make eval-diff`，对比两份结果 JSON，通过率下降就以非零状态退出。
- 量化：拿两份现有结果和一份人为降级的结果做演示，eval-diff 要能正确判定。

### AG4 工具层：参数执行前校验 + 跨 run 的可靠性
- 参数校验：在执行工具之前，按工具声明的 schema 校验参数，失败就把字段路径作为工具结果回给模型；不支持的 schema 特性直接放行。
- 可靠性：跨 run 持久化每个工具的成功次数、失败次数和延迟（接到 store 上，没有 store 就只放内存）；在工具索引行里显示失败率；再加一个可选的 observer。
- 量化：确定性场景，模型先发一个缺必填参数的调用，量「浪费的工具执行次数」和轮数的前后变化；再加一个可靠性计数的持久化测试。

## CortexDB

### CX1 只读的分析工具：verify_claims + 图健康检查
- verify_claims：输入是三元组列表，对每条判定 supported（被支持）、contradicted（被反驳，比如单值关系上有另一个值）或 absent（查无此边），并附上来源（fact_provenance）。全部确定性，不调模型。以 MCP 工具、Go API 两种方式提供，如果现有 gRPC 服务结构方便就一并提供。
- 健康检查：按 producer 统计增长、高出度和高入度的长尾、每天的 supersede 次数、违反时间不变式的情况（单值关系上同时存在多个未关闭的区间）。
- 量化：往种子图里埋进已知数量的编造三元组和被反驳的三元组，量精确率和召回率；每项健康检查都要在专门构造的坏数据上确实报警。

### CX2 写入路径：失效原因 + 实体消解先链接、后合并
- graph_*_history 增加 reason、superseded_by、producer 三个字段，由 RetractEdgeAt 和 SupersedeFact 等写入；在 temporal 查询里能读到。
- ResolveEntities：分数 0.95 以上才合并；0.85 到 0.95 之间只写 possiblySame 边，边上附带逐项证据（名称相似度、共同邻居数、类型是否一致），并进入 contract_needs_attention 等人确认；不再直接做破坏性合并。
- 量化：一组带标注的别名集，量误合并率和链接率的前后变化；再统计 history 行里有 reason 的比例。

### CX3 检索：路径检索 + 按关系类型设权重和深度
- search_paths（MCP 工具，也可以是 GraphRAG 的一个选项）：在种子实体之间做有界搜索，路径分数按距离衰减，返回边链，每条边附上它来自的 chunk。
- ExpandGraph / HybridSearch 支持按关系类型分别设置权重和最大深度。
- 量化：构造一个多跳问答小集合，并混入「相似但错误」的干扰内容，量 路径检索 vs 现有 chunk 检索 的命中率和干扰率；再量按类型设权重对 nDCG 或 precision@k 的影响。

### CX4 图分析：GlobalSearch 暴露 + 层级社区 + PageRank 缓存
- graphflow.GlobalSearch 做成 MCP 工具，由调用方显式选择使用，不按关键词自动路由。
- 层级社区：递归运行 Louvain，自底向上逐层生成摘要，查询时可以指定层级。
- rank_graph_nodes 读缓存（带时间戳），提供刷新参数和定时刷新。
- 量化：PageRank 在约 2000 个节点的图上，缓存前后的 p50 延迟；层级社区的层数和每层节点数；GlobalSearch 在一个全局问题集上的表现，没有模型可用时，至少跑通用 mock 生成器的完整流程。

## 执行

8 个 worker 并行。汇总时按「成功率或质量变化 + 代价」判断每项该不该合。
