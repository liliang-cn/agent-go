# PTC：让大模型写代码调用工具，而不是一轮一轮等工具结果

如果你做过 Agent，你大概率见过这样的执行过程：

用户问一个稍微复杂的问题。模型先说要查 A，于是发起一次工具调用。程序执行工具，把结果塞回上下文。模型看完结果后发现还要查 B，于是再发一次工具调用。程序再执行，再把结果塞回上下文。接着查 C、查 D、查 E。最后，模型在一堆工具返回的原始数据里做归纳、过滤、计算，然后给用户一个答案。

这个过程能工作，但它有几个明显的问题。

第一，慢。每一次工具调用都可能意味着一次模型往返。即使工具本身很快，模型生成、网络传输、上下文拼接、再次推理，都会叠加成端到端延迟。

第二，贵。很多工具结果其实只是中间数据。比如一份费用明细、日志文件、搜索结果、数据库查询结果，里面可能有大量字段对最终答案毫无意义。但传统工具调用会把这些原始结果送回模型上下文，让模型自己阅读和筛选。

第三，容易把上下文弄脏。模型真正需要的是“谁超预算”“哪些日志包含错误”“哪些订单异常”，而不是几千行 JSON、几十个 receipt URL、完整审批链、无关 metadata。

PTC，Programmatic Tool Calling，就是为了解决这个问题。

简单说，PTC 不是让模型直接一次次调用工具，而是让模型先写一段代码，再由这段代码在受控执行环境里调用工具、处理结果、筛选数据，最后只把小而干净的结论返回给模型或用户。

传统工具调用像这样：

```text
模型 -> 调用工具 A
程序 -> 返回 A 的完整结果给模型
模型 -> 调用工具 B
程序 -> 返回 B 的完整结果给模型
模型 -> 调用工具 C
程序 -> 返回 C 的完整结果给模型
模型 -> 读完所有原始结果后回答
```

PTC 像这样：

```text
模型 -> 写一段程序
程序 -> 在沙箱中执行
程序 -> callTool(A)、callTool(B)、callTool(C)
程序 -> 过滤、聚合、计算
程序 -> 只返回最终结构化结果
模型 -> 基于小结果回答
```

差别看起来只是“工具调用的位置变了”，但实际影响很大。工具调用从模型外层的对话协议，变成了代码执行环境里的普通函数调用。模型不再需要看所有原始数据，它只需要设计流程和读取最终摘要。

Claude Cookbook 里有一篇专门介绍 Programmatic Tool Calling 的文章。它用一个团队费用分析的例子做对比：传统工具调用需要把大量员工费用记录送回模型上下文；PTC 则让模型写代码去拉取、解析、过滤和汇总数据。这个例子里，传统方式消耗约 110,473 tokens，而 PTC 方式约 15,919 tokens，token 降低约 85.6%。这不是魔法，原因很朴素：无关数据没有进入模型上下文。

本文不讨论某个具体框架，而是从系统设计角度讲清楚 PTC 应该怎么理解、怎么实现、什么时候用、什么时候不要用。

## 一、为什么普通工具调用会浪费 token

先看一个常见需求：

> 找出工程部门在 Q3 超出差旅预算的员工。如果员工有特殊预算，以特殊预算为准；否则按标准预算 5000 美元判断。

系统里有三个工具：

```python
def get_team_members(department: str) -> list[dict]:
    """返回部门员工列表"""

def get_expenses(employee_id: str, quarter: str) -> list[dict]:
    """返回员工某季度费用明细"""

def get_custom_budget(user_id: str) -> dict:
    """返回员工特殊预算；没有则返回标准预算信息"""
```

传统工具调用大概是这样：

```python
messages = [{"role": "user", "content": user_query}]

while True:
    response = client.messages.create(
        model=model,
        messages=messages,
        tools=tools,
    )

    if response.stop_reason == "end_turn":
        print(response.text)
        break

    if response.stop_reason == "tool_use":
        messages.append({
            "role": "assistant",
            "content": response.content,
        })

        tool_results = []
        for tool_call in response.tool_calls:
            name = tool_call.name
            args = tool_call.input
            result = tool_functions[name](**args)
            tool_results.append({
                "type": "tool_result",
                "tool_use_id": tool_call.id,
                "content": json.dumps(result),
            })

        messages.append({
            "role": "user",
            "content": tool_results,
        })
```

这个循环没有错，它是很多 Agent 的基础。但问题在于：`get_expenses` 可能返回几十条甚至几百条记录，每条记录包含金额、类别、状态、商户、发票链接、审批链、地点、项目码等字段。模型最终只需要 approved travel 总额，却被迫读完整 JSON。

更麻烦的是顺序依赖。你必须先拿到员工列表，再拿每个人的费用，再计算谁超过标准预算，然后只对超过标准预算的人调用 `get_custom_budget`。这是一条有分支的流程，不是单次工具调用能自然表达的。

如果让模型一轮轮决定，它会多次往返。如果把所有工具结果都塞回上下文，又会浪费大量 token。

PTC 的核心价值就在这里：把“流程控制”和“中间数据处理”放到代码里。

## 二、PTC 的基本形态

PTC 模式下，模型输出的不再只是自然语言或一组 JSON 工具参数，而是一段可执行代码。代码运行在沙箱中，可以调用一个受控函数，例如：

```javascript
const result = callTool("tool_name", { key: "value" });
return result;
```

这里的 `callTool` 不是 JavaScript 内置能力，而是宿主系统注入给沙箱的函数。它负责把工具名和参数路由到真实工具处理器。

一个典型 PTC 代码可能是这样：

```javascript
const members = callTool("get_team_members", {
  department: "engineering"
});

const overBudget = [];

for (const member of members) {
  const expenses = callTool("get_expenses", {
    employee_id: member.id,
    quarter: "Q3"
  });

  const approvedTravel = expenses
    .filter(e => e.status === "approved")
    .filter(e => e.category === "travel")
    .reduce((sum, e) => sum + e.amount, 0);

  if (approvedTravel > 5000) {
    const budget = callTool("get_custom_budget", {
      user_id: member.id
    });

    const limit = budget.has_custom_budget
      ? budget.travel_budget
      : 5000;

    if (approvedTravel > limit) {
      overBudget.push({
        id: member.id,
        name: member.name,
        spent: approvedTravel,
        limit,
        over_by: approvedTravel - limit
      });
    }
  }
}

return {
  quarter: "Q3",
  department: "engineering",
  over_budget: overBudget
};
```

注意，这段代码里可能调用了很多次工具，但模型并不需要逐条阅读所有费用明细。原始明细只在沙箱中被处理，最终返回的是一个很小的对象。

这就是 PTC 的关键：

```text
模型负责写流程
沙箱负责执行流程
工具负责提供数据
代码负责过滤和计算
上下文只接收必要结果
```

## 三、PTC 不是“让模型随便执行代码”

一听到“让模型写代码执行”，很多人的第一反应是危险。这个担心是对的。PTC 不能等同于把模型生成的代码丢进系统 shell。

一个可用的 PTC 运行时至少需要这些边界：

1. 沙箱执行，而不是直接执行宿主进程代码。
2. 工具白名单，而不是任意函数调用。
3. 超时限制，避免死循环。
4. 最大工具调用次数，避免无限循环刷接口。
5. 最大输出大小，避免把大结果重新塞回上下文。
6. 禁止危险语法，例如 `eval`、动态 import、访问宿主全局对象。
7. 工具权限分级，读操作和写操作不同策略。
8. 执行历史可追踪，便于审计。

一个抽象的执行请求可以设计成这样：

```go
type ExecutionRequest struct {
	ID          string
	Code        string
	Language    string
	Context     map[string]any
	Tools       []string
	Timeout     time.Duration
	MaxMemoryMB int
}

type ExecutionResult struct {
	ID          string
	Success     bool
	ReturnValue any
	Logs        []string
	ToolCalls   []ToolCallRecord
	Error       string
	Duration    time.Duration
}

type ToolCallRecord struct {
	ToolName  string
	Arguments map[string]any
	Result    any
	Error     string
	Duration  time.Duration
}
```

工具路由器可以保持很简单：

```go
type ToolHandler func(ctx context.Context, args map[string]any) (any, error)

type ToolRouter struct {
	handlers map[string]ToolHandler
}

func (r *ToolRouter) Register(name string, handler ToolHandler) {
	r.handlers[name] = handler
}

func (r *ToolRouter) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	handler, ok := r.handlers[name]
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}
	return handler(ctx, args)
}
```

沙箱里暴露的 `callTool` 最终只做一件事：把调用转发给这个路由器。

```go
func bindCallTool(vm *Runtime, router *ToolRouter, ctx context.Context) {
	vm.Set("callTool", func(name string, args map[string]any) any {
		result, err := router.Call(ctx, name, args)
		if err != nil {
			return map[string]any{
				"ok":    false,
				"error": err.Error(),
			}
		}
		return map[string]any{
			"ok":   true,
			"data": result,
		}
	})
}
```

生产环境里还要加上调用计数、超时、并发控制、日志、输出截断和权限检查。

## 四、传统工具调用和 PTC 的本质差异

传统工具调用里，模型像一个调度员：

```text
我要工具 A
给我 A 的结果
我看完了，再要工具 B
给我 B 的结果
我看完了，再要工具 C
```

PTC 里，模型像一个程序员：

```text
我写一段程序
程序自己调用 A、B、C
程序自己筛选中间数据
程序返回最终结果
```

这带来三个变化。

第一，控制流从对话循环转移到了代码。循环、条件分支、聚合、排序、去重、分页，都可以自然表达。

第二，中间数据不必进入模型上下文。工具返回一千条数据，代码可以只返回前三个异常项。

第三，模型的认知负担降低。模型不用在上下文里手算几十条记录，也不用在自然语言里维护复杂状态。它只需要生成可靠代码。

这也解释了为什么 PTC 特别适合“批处理 + 过滤 + 汇总”的任务。

## 五、一个更完整的费用分析示例

假设我们有三个工具。

```go
type TeamMember struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Department string `json:"department"`
}

type Expense struct {
	ID       string  `json:"id"`
	MemberID string  `json:"member_id"`
	Category string  `json:"category"`
	Status   string  `json:"status"`
	Amount   float64 `json:"amount"`
	Quarter  string  `json:"quarter"`
}

func getTeamMembers(ctx context.Context, args map[string]any) (any, error) {
	department := args["department"].(string)
	return queryMembersByDepartment(department), nil
}

func getExpenses(ctx context.Context, args map[string]any) (any, error) {
	employeeID := args["employee_id"].(string)
	quarter := args["quarter"].(string)
	return queryExpenses(employeeID, quarter), nil
}

func getCustomBudget(ctx context.Context, args map[string]any) (any, error) {
	userID := args["user_id"].(string)
	return queryBudgetException(userID), nil
}
```

注册工具：

```go
router := NewToolRouter()
router.Register("get_team_members", getTeamMembers)
router.Register("get_expenses", getExpenses)
router.Register("get_custom_budget", getCustomBudget)
```

然后给模型的系统提示可以是：

```text
你可以输出 JavaScript 代码。
代码会在安全沙箱中执行。
使用 callTool(name, args) 调用工具。
必须 return 最终结果。
不要返回原始大数组，先过滤和聚合。
```

用户问题：

```text
找出 engineering 部门 Q3 真正超过差旅预算的人。
标准预算是 5000 美元。
如果某人超过标准预算，再检查他是否有特殊预算。
```

模型应该写出的代码类似：

```javascript
const membersRes = callTool("get_team_members", {
  department: "engineering"
});
if (!membersRes.ok) return membersRes;

const members = membersRes.data;
const candidates = [];

for (const m of members) {
  const expRes = callTool("get_expenses", {
    employee_id: m.id,
    quarter: "Q3"
  });
  if (!expRes.ok) return expRes;

  const travel = expRes.data
    .filter(e => e.status === "approved")
    .filter(e => e.category === "travel")
    .reduce((sum, e) => sum + Number(e.amount || 0), 0);

  if (travel > 5000) {
    candidates.push({
      id: m.id,
      name: m.name,
      travel_spend: travel
    });
  }
}

const result = [];

for (const c of candidates) {
  const budgetRes = callTool("get_custom_budget", {
    user_id: c.id
  });
  if (!budgetRes.ok) return budgetRes;

  const budget = budgetRes.data;
  const limit = budget.has_custom_budget
    ? budget.travel_budget
    : 5000;

  if (c.travel_spend > limit) {
    result.push({
      ...c,
      budget: limit,
      over_by: c.travel_spend - limit,
      budget_type: budget.has_custom_budget ? "custom" : "standard"
    });
  }
}

return {
  department: "engineering",
  quarter: "Q3",
  count: result.length,
  employees: result
};
```

最终返回给模型的数据可能只有几十行。模型再把它转成可读报告即可。

这就是 PTC 的高效之处：真正进入模型上下文的是结果，不是全部过程材料。

## 六、PTC 适合哪些场景

PTC 最适合下面几类任务。

### 1. 大量结构化数据的筛选和聚合

比如 CSV、JSON、日志、数据库结果、费用明细、监控指标。模型不应该逐行阅读全部内容。代码更适合做过滤、分组、求和、排序。

### 2. 多实体循环处理

比如“检查所有服务健康状态”“遍历所有仓库找风险”“对每个用户查询订单和退款”。传统工具调用会产生大量重复轮次，PTC 可以用循环表达。

### 3. 有顺序依赖的工具链

比如先查候选人，再对候选人做二次查询；先扫描日志，再读取命中的文件；先列出表，再按表结构生成查询。PTC 可以把状态留在代码变量里。

### 4. 中间结果很大，但最终答案很小

这是最重要的判断标准。如果工具返回很大，而最终只需要小结论，PTC 很可能有价值。

### 5. 需要精确计算

金额汇总、百分比、去重、集合差异、阈值判断、时间窗口统计，都应该交给代码，而不是让模型在自然语言上下文里心算。

## 七、什么时候不要用 PTC

PTC 不是银弹。

如果只是调用一个简单工具，普通工具调用更直接。

如果工具有强副作用，例如转账、删除资源、发送通知、修改生产配置，就不应该让模型在循环里自由调用。即使使用 PTC，也要加人工确认、幂等设计、限流和审计。

如果任务需要大量主观判断，而不是数据处理，PTC 价值也不大。例如写一篇观点文章、做产品命名、解释一个概念，普通模型生成就够了。

如果工具接口不稳定、错误率高、返回格式混乱，PTC 代码会变得脆弱。此时应该先规范工具契约，而不是急着上 PTC。

## 八、PTC 的工程架构

一个可靠 PTC 系统一般包含五层。

第一层是模型提示。它告诉模型什么时候写代码、怎么写代码、如何调用工具、如何返回结果。

第二层是代码提取。模型可能返回 `<code>...</code>`，也可能调用一个 `execute_javascript` 工具。系统要能稳定拿到代码。

第三层是沙箱运行时。可以是 JavaScript 解释器，或者受限 Python 环境。关键是隔离、限时、限内存、可中断。

第四层是工具路由。`callTool` 不直接访问业务函数，而是通过统一路由器做权限检查、参数校验、执行和记录。

第五层是结果整理。沙箱输出的 `return`、`console.log`、工具调用历史、错误信息，都要被包装成结构化结果，方便下一轮模型或最终用户读取。

一个简化流程如下：

```text
用户请求
  -> 模型生成代码
  -> 提取代码
  -> 校验代码
  -> 沙箱执行
  -> callTool 路由工具
  -> 记录工具调用
  -> 截断/整理结果
  -> 模型生成最终回答
```

## 九、代码校验和安全策略

PTC 的安全策略应该默认保守。

例如：

```go
type SecurityPolicy struct {
	AllowFileAccess bool
	AllowNetwork    bool
	AllowedTools    []string
	BlockedTools    []string
	ValidateCode    bool
	Forbidden       []*regexp.Regexp
}
```

常见禁止项包括：

```go
forbidden := []string{
	`eval\s*\(`,
	`Function\s*\(`,
	`import\s*\(`,
	`require\s*\(`,
	`process\s*\.`,
	`global\s*\.`,
	`__proto__`,
}
```

工具调用也要限制：

```go
type RuntimeLimits struct {
	Timeout        time.Duration
	MaxToolCalls   int
	MaxCodeBytes   int
	MaxOutputBytes int
	MaxMemoryMB    int
}
```

在 `callTool` 内部做计数：

```go
func guardedCallTool(
	ctx context.Context,
	router *ToolRouter,
	limits *RuntimeLimits,
) func(string, map[string]any) any {
	var count int

	return func(name string, args map[string]any) any {
		count++
		if count > limits.MaxToolCalls {
			return map[string]any{
				"ok": false,
				"error": "too many tool calls",
			}
		}

		result, err := router.Call(ctx, name, args)
		if err != nil {
			return map[string]any{
				"ok": false,
				"error": err.Error(),
			}
		}

		return map[string]any{
			"ok": true,
			"data": result,
		}
	}
}
```

不要相信模型会“自觉”少调用工具，也不要相信它不会写死循环。边界必须由运行时保证。

## 十、工具返回格式要稳定

PTC 对工具契约要求更高。因为模型写的是代码，代码会假设返回结构稳定。

不推荐这样：

```json
"success, here is your data: ..."
```

推荐这样：

```json
{
  "ok": true,
  "data": {
    "items": [],
    "count": 0
  },
  "error": ""
}
```

代码就可以统一处理：

```javascript
const res = callTool("search_logs", { service: "billing" });
if (!res.ok) {
  return { error: res.error };
}

const errors = res.data.items.filter(x => x.level === "error");
return { count: errors.length, errors: errors.slice(0, 10) };
```

工具描述也要告诉模型返回格式。很多 PTC 失败不是因为模型不会写代码，而是因为工具契约含糊，模型只能猜。

## 十一、PTC 和普通工具调用可以共存

一个成熟系统不应该强迫所有任务都走 PTC。

更合理的策略是：

```text
简单单次工具调用 -> 普通工具调用
多步骤小数据任务 -> 普通工具调用或 PTC 都可
多步骤大数据任务 -> 优先 PTC
高风险写操作 -> 普通工具调用 + 人工确认
批量读 + 聚合计算 -> PTC
```

也可以把 PTC 暴露成一个普通工具：

```json
{
  "name": "execute_javascript",
  "description": "Execute JavaScript in a sandbox. Use callTool(name, args) to call allowed tools.",
  "input_schema": {
    "type": "object",
    "properties": {
      "code": {
        "type": "string"
      }
    },
    "required": ["code"]
  }
}
```

这样模型仍然通过工具调用协议进入 PTC，但真正的业务工具被隐藏到 `callTool` 后面。模型只直接看到 `execute_javascript`，沙箱里再访问允许的工具列表。

这种设计有一个好处：工具暴露面更小。模型不需要在外层上下文看到几十个业务工具的完整 schema，只需要知道代码里能调用哪些工具，以及它们的简短说明。

## 十二、一个日志分析示例

再看一个更贴近日常工程的例子。

用户问：

```text
检查 billing 服务最近 1 小时的错误日志，按错误类型聚合，返回最常见的 5 类。
```

工具：

```javascript
// search_logs({ service, since }) -> { items: LogEntry[] }
// get_trace({ trace_id }) -> TraceDetail
```

PTC 代码：

```javascript
const logsRes = callTool("search_logs", {
  service: "billing",
  since: "1h"
});

if (!logsRes.ok) return logsRes;

const errors = logsRes.data.items
  .filter(x => x.level === "error")
  .map(x => ({
    message: x.message,
    code: x.error_code || "UNKNOWN",
    trace_id: x.trace_id
  }));

const groups = {};
for (const e of errors) {
  if (!groups[e.code]) {
    groups[e.code] = {
      code: e.code,
      count: 0,
      samples: []
    };
  }
  groups[e.code].count++;
  if (groups[e.code].samples.length < 3) {
    groups[e.code].samples.push(e);
  }
}

const top = Object.values(groups)
  .sort((a, b) => b.count - a.count)
  .slice(0, 5);

return {
  service: "billing",
  window: "1h",
  total_errors: errors.length,
  top_error_codes: top
};
```

如果普通工具调用直接返回一小时日志，模型可能要吞下几千行。PTC 只返回聚合后的 top 5，token 消耗和答案稳定性都会好很多。

## 十三、PTC 的调试体验

PTC 一定要保留执行历史。至少记录：

- 生成的代码
- 执行开始和结束时间
- 每次 `callTool` 的工具名
- 每次调用的参数
- 每次调用的返回值摘要
- 错误信息
- console 日志
- 最终 return 值

一个执行历史结构可以是：

```go
type ExecutionHistory struct {
	ID         string
	Code       string
	Result     ExecutionResult
	ExecutedAt time.Time
}
```

调试时最常见的问题有四类。

第一，模型没写代码，仍然用自然语言回答。解决方式是加强提示，或把 PTC 暴露成结构化工具。

第二，工具名写错。解决方式是提供精确工具列表，必要时加工具搜索机制，但不要让沙箱无限搜索。

第三，返回结构猜错。解决方式是让工具 schema 和描述包含明确返回格式。

第四，代码处理了太多数据，输出仍然很大。解决方式是限制输出大小，并在提示里要求只返回摘要、计数、样本。

## 十四、PTC 的提示模板

一个可复用的提示模板可以这样写：

```text
你可以使用程序化工具调用。

规则：
1. 当任务需要多次工具调用、循环、筛选、聚合或计算时，输出 JavaScript 代码。
2. 使用 callTool(name, args) 调用工具。
3. 只能调用工具列表中存在的工具。
4. 不要返回原始大数组；先过滤、聚合、截断。
5. 必须 return 最终结果。
6. 如果工具返回 { ok: false, error }，立即返回错误对象。
7. 不要使用 eval、Function、import、require。

返回格式：
<code>
const result = callTool("tool_name", { ... });
return result;
</code>
```

如果使用结构化 `execute_javascript` 工具，也可以要求模型直接调用该工具，而不是输出文本代码块。

## 十五、落地建议

第一步，不要一开始就做完整系统。先选一个“工具返回大、最终结果小”的场景，比如日志聚合、费用分析、搜索结果筛选。

第二步，把工具返回格式标准化。PTC 很依赖稳定 schema。

第三步，建立沙箱和权限边界。没有边界的 PTC 不是工程能力，而是安全事故。

第四步，加执行历史。没有历史就没法排查模型为什么写出某段代码。

第五步，加回退路径。模型不写代码、代码执行失败、工具失败，都应该能回到普通工具调用或明确报错。

第六步，持续观察 token。PTC 的目标不是“看起来更智能”，而是让中间数据少进入上下文，让多步骤工具链更可控。

## 结语

PTC 的本质不是“模型会写代码”这个噱头，而是把 Agent 系统里的控制流下沉到可执行程序中。

传统工具调用把每一步都摊在模型上下文里。PTC 则让模型生成一个小程序，让小程序在沙箱中调用工具、处理数据、保留状态、做精确计算，最后只把必要结果交还给模型。

它最适合大数据量、多工具、强依赖、可计算的任务。它不适合高风险副作用操作，也不应该绕过权限、审计和人工确认。

如果说普通工具调用让模型“会使用工具”，那么 PTC 让模型“会编排工具”。前者解决可用性，后者解决效率和规模。

当你的 Agent 开始频繁处理大量工具结果、上下文被 JSON 塞满、token 成本快速上升时，就该考虑 PTC 了。

参考资料：

- Claude Cookbook: Programmatic tool calling (PTC), https://platform.claude.com/cookbook/tool-use-programmatic-tool-calling-ptc

