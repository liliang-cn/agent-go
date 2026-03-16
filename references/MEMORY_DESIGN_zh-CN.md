# AgentGo 多层级基于文件的 Memory 融合设计

## 1. 状态

- 状态: 设计草案
- 范围: 仅设计，不包含代码实现
- 目标: 将 `Global / Process / Thread` 这套层级记忆模型，与 AgentGo 当前已有的 file-based cognitive memory 机制融合

---

## 2. 设计背景

AgentGo 当前已经有一套可工作的 memory 子系统：

- Truth Store: Markdown + YAML frontmatter 文件
- Shadow Index: 可选 SQLite/vector 加速索引
- Retrieval: PageIndex 风格 `_index/` + 可选 vector/hybrid
- Evolution: `fact -> observation -> superseded observation`

当前能力的关键特征：

1. memory 的“内容类型”已经成型
   - `fact`
   - `observation`
   - `preference`
   - `skill`
   - `pattern`
   - `context`
2. memory 的“作用域”已经存在，但表达较隐式
   - `global`
   - `agent`
   - `project`
   - `user`
   - `session`
3. 文件存储是 object-per-memory，不是 single-file-per-scope
4. `Reflect()`、`EvidenceIDs`、`SupersededBy`、`RevisionHistory`、`_index` 和 hybrid/vector shadow index 都依赖这种 object-per-memory 粒度
5. LongRun 还有另一套工作区级 memory files
   - `MEMORY.md`
   - `AGENTS.md`
   - `SOUL.md`
   - `TOOLS.md`
   - `HEARTBEAT.md`

这意味着新设计不能简单替换为：

- 一个 `global_memory.md`
- 一个 `process_memory.md`
- 一个 `agent_memory.md`

否则会破坏现有演化链、索引粒度和 shadow index 架构。

---

## 3. 设计目标

### 3.1 目标

1. 在不破坏现有 cognitive memory truth store 的前提下，引入更清晰的层级 scope 模型
2. 让 `Global / Process / Thread` 成为 first-class concept
3. 保留现有 `fact / observation / preference / skill / pattern / context` 类型体系
4. 保持 file-first truth store，可人读、可调试、可手工修订
5. 支持 archive/封存，不影响默认运行时检索
6. 支持 scope-aware 搜索、检索优先级和调试
7. 为后续 UI/CLI 可视化浏览、promotion、archive 操作提供稳定模型

### 3.2 非目标

1. 本设计不重做 vector store
2. 本设计不替换 LongRun 的工作区 memory files
3. 本设计不试图把所有 memory 压缩成单文件
4. 本设计不在本阶段改变现有 `fact -> observation` 反射机制

---

## 4. 核心设计原则

### 4.1 二维模型，而不是单维替换

新设计采用二维模型：

- 维度 A: `scope`
  - 决定谁拥有、谁可见、检索优先级、生命周期
- 维度 B: `type`
  - 决定内容语义和认知演化方式

结论：

- `Global / Process / Thread` 属于 `scope`
- `fact / observation / preference / ...` 属于 `type`

### 4.2 Truth Store 继续保持 object-per-memory

每条 memory 继续是一个 Markdown 对象文件。  
`global_memory.md` / `process_memory.md` / `thread_memory.md` 可以存在，但只能是视图文件，不是唯一真相源。

### 4.3 LongRun 工作区 memory 与 cognitive memory 分离

LongRun 的 `MEMORY.md` 等文件继续服务于运行时工作上下文，不混入 `data/memories/` truth store。

### 4.4 Archive 与 Stale 分开

- `stale`: 被新记忆替代，仍在认知演化链上
- `archived`: 生命周期结束，默认不参与运行时检索

---

## 5. 逻辑模型

## 5.1 Scope 模型

建议的 scope 语义：

| 逻辑层 | 新 scope | 含义 | 典型拥有者 | 默认检索优先级 |
|---|---|---|---|---|
| L4 | `global` | 全系统共享长期记忆 | 所有 agent/squad | 低 |
| L3 | `squad` | Squad 内共享过程记忆 | 一个 squad/process | 中 |
| L2 | `agent` | 单 agent 的线程级记忆 | 某个 agent/thread | 高 |
| L1 | `session` | 单次交互会话上下文 | 一次 session | 最高 |

兼容保留：

| 现有 scope | 处理方式 |
|---|---|
| `project` | 兼容映射到 `squad`，短期内可继续复用 |
| `user` | 保留，作为跨会话用户级偏好层 |

### 5.2 用户方案的映射

| 用户术语 | 融合后的内部语义 |
|---|---|
| Global Memory | `scope=global` |
| Process Memory | `scope=squad` |
| Thread Memory | `scope=agent` |
| 临时对话上下文 | `scope=session` |

### 5.3 Type 模型

保留现有类型体系：

| type | 说明 |
|---|---|
| `fact` | 原始事实 |
| `observation` | 由多个事实反射出的综合观察 |
| `preference` | 用户或 agent 的稳定偏好、规则 |
| `skill` | 操作经验、步骤性知识 |
| `pattern` | 模式、规律、抽象结构 |
| `context` | 临时上下文、运行中间信息 |

### 5.4 关键规则

1. scope 不替代 type
2. 同一个 scope 下可以同时存在多种 type
3. `Reflect()` 默认只在同 scope 内聚合 `fact -> observation`
4. promotion 是 scope 迁移，不是 type 迁移

---

## 6. 数据结构设计

## 6.1 Domain 结构目标形态

建议的逻辑结构：

```go
type Memory struct {
    ID           string
    Type         MemoryType
    ScopeType    MemoryScopeType
    ScopeID      string
    SessionID    string
    Content      string
    Keywords     []string
    Tags         []string
    Importance   float64
    AccessCount  int
    LastAccessed time.Time
    Metadata     map[string]interface{}
    CreatedAt    time.Time
    UpdatedAt    time.Time

    EvidenceIDs     []string
    Confidence      float64
    ValidFrom       time.Time
    ValidTo         *time.Time
    SupersededBy    string
    SourceType      MemorySourceType
    Conflicting     bool
    RevisionHistory []MemoryRevision

    Archived     bool
    ArchivedAt   *time.Time
    ArchiveReason string
}
```

说明：

- `SessionID` 保留，用于兼容现有 bank-id 路径
- `ScopeType/ScopeID` 是新的标准作用域字段
- `Archived*` 是生命周期字段，不与 stale 混用

## 6.2 YAML Frontmatter

推荐的前置元数据：

```yaml
---
id: mem_01HXYZ
type: fact
scope_type: squad
scope_id: squad_001
layer: process
session_id: squad:squad_001
keywords:
  - api_gateway
  - trace_id
  - shared_constraint
tags:
  - active
  - shared
importance: 0.82
access_count: 0
last_accessed:
created_at: 2026-03-16T10:30:00Z
updated_at: 2026-03-16T10:30:00Z
metadata:
  squad_id: squad_001
  agent_id: Assistant
evidence_ids: []
confidence: 0.88
valid_from: 2026-03-16T10:30:00Z
valid_to:
superseded_by: ""
source_type: inferred
conflicting: false
revision_history: []
archived: false
archived_at:
archive_reason: ""
---
API 网关必须透传 X-Trace-ID，供下游链路追踪使用。
```

## 6.3 兼容规则

旧 frontmatter 没有 `scope_type/scope_id` 时，按以下方式推断：

1. `session_id == ""` 或 `session_id == "global"` -> `scope_type=global`
2. `session_id` 前缀为 `session:` -> `scope_type=session`
3. `session_id` 前缀为 `agent:` -> `scope_type=agent`
4. `session_id` 前缀为 `project:` -> `scope_type=squad`
5. 其他未识别值 -> 先按 legacy bank id 处理，并标记为兼容模式

---

## 7. 文件布局设计

## 7.1 Truth Store

继续保留现有 truth store 根布局：

```text
data/memories/
├── entities/
│   └── <memory-id>.md
├── streams/
│   └── <memory-id>.md
└── _index/
```

对象文件分布规则：

- `context` -> `streams/`
- 其余 type -> `entities/`

这样做的原因：

1. 与当前实现兼容
2. 不破坏现有 `readFile / Store / Reflect / Get / List`
3. 不需要第一阶段迁移所有文件路径

## 7.2 Index 布局

现有 `_index/` 只按 type 建索引。  
新设计建议扩展为：

```text
data/memories/_index/
├── types/
│   ├── observations.md
│   ├── facts.md
│   ├── preferences.md
│   ├── skills.md
│   ├── patterns.md
│   └── contexts.md
├── scopes/
│   ├── global.md
│   ├── squad__squad_001.md
│   ├── agent__Assistant.md
│   ├── session__abc123.md
│   └── user__u001.md
└── archive/
    ├── squad__squad_001.md
    └── agent__Assistant.md
```

索引语义：

- `types/*`: 继续支撑 observation-first 的认知检索
- `scopes/*`: 支撑分层 scope-aware 检索
- `archive/*`: 仅在显式查询历史时参与

## 7.3 View 布局

为了满足“按层级看一份总记忆文件”的调试和人工阅读诉求，引入物化视图：

```text
data/memories/_views/
├── global/
│   └── global_memory.md
├── squads/
│   └── squad_001/
│       └── process_memory.md
└── agents/
    └── Assistant/
        └── thread_memory.md
```

这些视图文件的性质：

1. 只读视图优先
2. 默认由 truth store 生成
3. 可用于 UI 展示、调试、人工审阅
4. 不作为唯一写入入口

## 7.4 Archive 布局

建议新增 archive manifest：

```text
data/memories/_archive/
└── manifests/
    ├── squad__squad_001__20260316T103000Z.md
    └── agent__Assistant__20260316T110500Z.md
```

manifest 记录：

- archive scope
- archive 时间
- archive 原因
- 被封存 memory ids
- 摘要
- promoted memories

---

## 8. 检索设计

## 8.1 Scope Chain

默认检索链建议为：

```text
session > agent(thread) > squad(process) > user > global
```

如果当前上下文不包含某一层，则跳过。

### 8.2 Retrieval Pipeline

推荐检索流程：

1. 从运行上下文构造 scope chain
2. 在每个 scope 下优先读取 active memories
3. 优先选择 `observation`、`preference`、高 importance 记录
4. 将候选交给 navigator 或 vector search
5. 融合排序并裁剪 topK

### 8.3 File-only 模式

当没有 embedder 时：

1. 先读 `scopes/<scope>.md`
2. 再读 `types/observations.md` 等 type index
3. 生成紧凑 TOC 给 `IndexNavigator`
4. LLM 从 TOC 中选出 ID
5. 回读对象文件

### 8.4 Hybrid 模式

当启用 hybrid 时：

1. vector search 在 shadow index 中按 scope bank 过滤
2. navigator 走 `_index/scopes` + `_index/types`
3. 两路结果用 RRF 融合

### 8.5 默认排序策略

建议的总分：

```text
total_score =
  semantic_score
  * scope_weight
  * freshness_weight
  * importance_weight
  * type_weight
```

建议默认权重：

| 因子 | 建议值 |
|---|---|
| session weight | 1.00 |
| agent weight | 0.90 |
| squad weight | 0.80 |
| user weight | 0.70 |
| global weight | 0.60 |

建议类型偏置：

| type | bias |
|---|---|
| observation | +0.15 |
| preference | +0.10 |
| fact | +0.05 |
| pattern | +0.05 |
| context | 视 scope 而定 |

### 8.6 Archive 与 Stale 的检索规则

- `archived=true` 默认不参与普通检索
- `stale=true` 默认降权，但调试或溯源时可见
- 显式 `include_archived=true` 时，可纳入 archive index

---

## 9. 写入与 Promotion 设计

## 9.1 写入流程

建议统一流程：

1. 事件发生
2. 提取候选 memory
3. 判断是否值得存储
4. 选择 type
5. 选择 scope
6. 提取 keywords
7. 写入 truth store 对象文件
8. 更新 index
9. 同步 shadow index
10. 触发必要的 reflect

## 9.2 Scope 选择策略

### 写入到 `session`

适用：

- 单轮或短期会话临时上下文
- 推理中间状态
- 本次任务结束后大概率无长期价值的信息

### 写入到 `agent`

适用：

- 当前 agent 的工作偏好
- 角色特定知识
- 某 agent 未来仍可复用的局部经验

### 写入到 `squad`

适用：

- 跨 agent 共享约束
- 任务共同背景
- 中间结论与共享状态

### 写入到 `global`

适用：

- 跨 squad 持久有效
- 系统级事实
- 全局规则、全局偏好、共享资源说明

## 9.3 Promotion 规则

promotion 仅改变 scope，不强制改变 type。

典型路径：

- `session -> agent`
- `agent -> squad`
- `squad -> global`

promotion 触发条件：

1. 复用次数达到阈值
2. 被多个下游任务引用
3. 被人工确认“升级”
4. reflection 或 review 判断其适合作为上层共享知识

promotion 的实现建议：

- 新建一条新 memory
- 原 memory 记录 revision 或 provenance
- 不直接“搬文件覆盖”，避免破坏历史链路

---

## 10. Reflect 与认知演化

## 10.1 维持现有演化模型

继续保留：

```text
fact -> observation -> superseded observation
```

### 10.2 反射的 scope 边界

默认规则：

- 一个 scope 内的 `fact` 优先在同 scope 内反射为 `observation`

例如：

- `agent` 范围内的多个事实 -> `agent` observation
- `squad` 范围内的多个事实 -> `squad` observation

这样避免不同层级的事实被过早混合。

### 10.3 跨 scope 综合

如确实需要跨 scope 综合，建议只允许：

- 上层 scope 在检索时“引用下层 observation”
- 不直接把下层 raw facts 混成上层 observation

也就是说：

- `squad` 可以引用多个 `agent` observation 形成 `squad` observation
- 但应避免直接用多个 `agent` raw fact 跨层乱合成

### 10.4 Evidence 与 Stale

仍保留：

- `EvidenceIDs`
- `SupersededBy`
- `RevisionHistory`

新增 archive 后，不改变这套关系。  
换句话说：

- memory 可以 stale
- memory 也可以 archived
- 两者语义独立

---

## 11. Archive 与生命周期设计

## 11.1 生命周期

| scope | 默认生命周期 | 封存策略 |
|---|---|---|
| session | 短 | 任务结束后压缩/归档/TTL 清理 |
| agent | 中 | 任务结束后保留有价值部分，其余归档 |
| squad | 中 | squad 完成后整体归档 process memory |
| global | 长 | 默认不自动归档 |

## 11.2 Archive 触发条件

### Session

- 任务结束
- session compact
- 时间 TTL 到期

### Agent

- agent 完成某一阶段任务
- thread 收敛
- 产生新的更高层 summary

### Squad

- squad 任务完成
- squad 被关闭
- process 被重置

### Global

- 人工归档
- 管理策略明确要求归档

## 11.3 Archive 行为

archive 不建议直接移动 truth store 对象文件到别处。  
推荐做法：

1. 原对象文件保留
2. frontmatter 标记 `archived=true`
3. 更新 active indexes 与 archive indexes
4. 生成 archive manifest

这样更利于：

- 兼容现有 `Get(id)`
- 保持历史路径稳定
- 避免 shadow index 引用丢失

## 11.4 Session 压缩

session 是最容易膨胀的层。建议：

1. 把短期 `context` 聚合为 session summary
2. 如果 summary 有复用价值，promotion 到 `agent`
3. 原始 session contexts 归档或清理

---

## 12. 视图文件设计

## 12.1 设计目的

视图文件用于满足以下需求：

1. 人工快速阅读某层当前记忆
2. UI 中按层查看 memory
3. 调试 scope 内的活跃知识
4. 在不扫全部对象文件的情况下做概要展示

## 12.2 示例: `global_memory.md`

```markdown
---
scope_type: global
generated_at: 2026-03-16T10:30:00Z
active_count: 12
archived_count: 41
---

# Global Memory

## Preferences
- [mem_a1] Agent mission: Never lie

## Observations
- [mem_b2] 当前系统默认 memory truth store 是 file-based markdown store

## Facts
- [mem_c3] RAG shadow index 使用 data/agentgo.db
```

## 12.3 示例: `process_memory.md`

```markdown
---
scope_type: squad
scope_id: squad_001
layer: process
generated_at: 2026-03-16T10:30:00Z
---

# Squad 001 Process Memory

## Active Shared Constraints
- [mem_01] API gateway 必须透传 X-Trace-ID

## Observations
- [mem_02] 前后端都依赖统一会话 ID 进行日志关联

## Recent Context
- [mem_03] QA 已确认 /chat session restore 可用
```

## 12.4 编辑策略

默认不建议人工直接编辑 view 文件。  
如果需要人工编辑，应将其作为“提案输入”，再由系统回写为对象级 memory。

---

## 13. API 与工具设计

## 13.1 服务接口建议

建议在现有 `MemoryService` 外补充更明确的 scoped API：

```go
type MemoryContext struct {
    SessionID string
    AgentID   string
    SquadID   string
    UserID    string
}

type MemoryWriteRequest struct {
    Type       domain.MemoryType
    Scope      domain.MemoryScope
    Content    string
    Keywords   []string
    Importance float64
    Metadata   map[string]interface{}
}

type MemorySearchRequest struct {
    Query           string
    Context         MemoryContext
    Types           []domain.MemoryType
    IncludeArchived bool
    TopK            int
}
```

## 13.2 Store 接口建议

在不破坏现有接口的前提下，可增补：

```go
BuildScopeView(ctx context.Context, scope domain.MemoryScope) error
ArchiveScope(ctx context.Context, scope domain.MemoryScope, reason string) error
Promote(ctx context.Context, memoryID string, targetScope domain.MemoryScope) error
SearchByScopeChain(ctx context.Context, query string, scopes []domain.MemoryScope, topK int, includeArchived bool) ([]*domain.MemoryWithScore, error)
```

## 13.3 UI / CLI 能力建议

### UI

建议新增：

1. scope filter
2. active / archived toggle
3. memory evolution viewer
4. promote to higher scope
5. build scope view
6. archive scope

### CLI

建议新增：

```bash
agentgo memory list --scope squad:squad_001
agentgo memory search "trace id" --scope-chain session:abc,agent:Assistant,squad:squad_001,global
agentgo memory archive --scope squad:squad_001 --reason "workflow completed"
agentgo memory promote <memory-id> --to global
agentgo memory view --scope squad:squad_001
```

---

## 14. 迁移设计

## 14.1 Phase 1: 元数据兼容扩展

目标：

- 不改 truth store 布局
- 增加 `scope_type/scope_id/archived/keywords`
- 保持旧文件可读

动作：

1. frontmatter 扩展
2. 读旧文件时自动推断 scope
3. 写新文件时同时写 `SessionID` 与显式 scope 字段

## 14.2 Phase 2: 新增 scope indexes

目标：

- 让 file-only navigator 支持 scope-aware 检索

动作：

1. `_index/scopes/*.md`
2. `_index/archive/*.md`
3. `ReadIndex()` 扩展为可读 type index 与 scope index

## 14.3 Phase 3: scope-aware retrieval

目标：

- `RetrieveAndInject()` 支持 `session > agent > squad > user > global`

动作：

1. 扩展 `DefaultScopeChain`
2. 明确 `project -> squad` 兼容映射
3. 检索排序引入 scope weight

## 14.4 Phase 4: 视图文件与 archive manifest

目标：

- 提供可读层级视图
- 增加 archive 能力

动作：

1. 生成 `_views`
2. 生成 `_archive/manifests`
3. active/archive index 分流

## 14.5 Phase 5: UI/CLI 可操作化

目标：

- 用户可直接查看、调试、promotion、archive

动作：

1. UI scope browser
2. CLI scope commands
3. debug reasoning 可展示 scope chain 和 memory selection

---

## 15. 示例场景

## 15.1 场景 A: Agent 形成局部经验

1. `Assistant` 在一次编码任务中总结出一个局部经验
2. 初始写入 `scope=agent:Assistant`, `type=skill`
3. 后续多次复用后，提升到 `scope=squad:squad_001`
4. 该经验成为 squad 内共享实践

## 15.2 场景 B: Squad 任务完成并封存

1. `squad_001` 完成产品迭代任务
2. 当前 process memory 中大部分 `context` 和中间 `fact` 标记 `archived=true`
3. 更稳定的 `observation` 与 `preference` 保持 active
4. 生成一个 `archive manifest`
5. 默认运行时不再检索已封存 process context

## 15.3 场景 C: Global 规则沉淀

1. 多个 squad 都反复确认同一系统规则
2. 该规则先在 squad 范围形成稳定 observation
3. 人工或策略将其 promotion 到 `global`
4. 后续所有 squad 都可读到该记忆

## 15.4 场景 D: Session 压缩

1. 某次 session 中产生大量 `context`
2. 系统在 compact 时生成一条 `session observation`
3. 如果其中某条结论具有长期价值，则 promotion 到 `agent`
4. 原始 session context 归档

---

## 16. 与现有实现的兼容关系

## 16.1 兼容点

本设计兼容以下现有机制：

1. file-based truth store
2. `_index` + navigator
3. hybrid shadow index
4. `Reflect()`
5. `EvidenceIDs`
6. `SupersededBy`
7. `RevisionHistory`

## 16.2 需要调整的点

后续实现中需要调整：

1. `SessionID` 不应继续独自承担全部 scope 语义
2. index 不能只按 type 建立
3. retrieval 不能只知道 `session/global`
4. archive 不能等同于 delete
5. view 文件不能成为 primary write path

## 16.3 明确不融合的部分

`pkg/agent/memory_files.go` 的工作区文件继续独立，不直接混入：

- `data/memories/entities/*.md`
- `data/memories/streams/*.md`

它们属于“运行时工作上下文”，不是“认知记忆 truth store”。

---

## 17. 风险与开放问题

## 17.1 风险

1. 如果把 scope 直接映射成目录，而不是 metadata，初期迁移成本会升高
2. 如果允许人工直接编辑 `_views`，容易与 truth store 脱节
3. 如果 archive 采用“物理移动文件”，会破坏现有 ID-based 获取与 shadow index 对齐
4. 如果跨 scope 反射边界不清晰，容易生成污染性的高层 observation

## 17.2 开放问题

1. `squad` 是否单独进 `MemoryScopeType`，还是短期复用 `project`
2. `user` 层在当前 AgentGo 产品语义里是否需要先保持只读/低优先级
3. 关键词提取是否走：
   - rule-based
   - llm extraction
   - embedding neighbor labels
   - 三者混合
4. archive manifest 是否需要额外 JSON 索引以加快大规模历史浏览

---

## 18. 最终建议

最终建议是：

1. 保留当前 object-per-memory file truth store
2. 将 `Global / Process / Thread` 明确建模为 scope hierarchy
3. 保留现有 type hierarchy，不做替换
4. 新增 scope metadata、scope indexes、views、archive manifests
5. 将 `global_memory.md / process_memory.md / thread_memory.md` 定位为可生成视图，而不是底层真相源
6. LongRun 的工作区 memory files 继续独立

一句话总结：

> AgentGo 应该采用“对象级 truth store + 作用域层级 + 类型层级 + 可生成视图”的融合设计，而不是退回到“每层一个大 Markdown 文件”的模型。
