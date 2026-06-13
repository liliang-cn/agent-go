// Package main is a single-user backend demo of "SuperAI" — the AI assistant
// from the SuperLeo PRD — built entirely on the AgentGo framework. It exists to
// exercise AgentGo's core capabilities end-to-end (NO multi-tenant / user
// isolation / SaaS plumbing — just the brain):
//
//   - intent understanding + tool calling  (建日程 / 记事 / 记人物 / 设提醒)
//   - built-in resolve_datetime so the model never miscomputes relative dates
//   - knowledge-graph–aware memory (WithGraphMemory + graph_recall): the loop
//     queries entities/relations, not just vector similarity
//   - emotion tagging that would drive the 3D avatar  (情绪: 标签)
//
// The assistant is one agent.Service with custom Go tools backed by a tiny
// in-process store. A scripted run plays the PRD scenarios S1/S3/S4/S8, then a
// FRESH session asks recall questions (S6) to prove memory + records persist
// beyond the original conversation.
//
// Requirements: DASHSCOPE_API_KEY (OpenAI-compatible Qwen endpoint).
//
// Usage:
//
//	DASHSCOPE_API_KEY=sk-... go run ./examples/superai
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
)

// ----------------------------------------------------------------------------
// Tiny in-process store (stands in for the PRD's Record/Schedule/Person tables)
// ----------------------------------------------------------------------------

type store struct {
	mu        sync.Mutex
	schedules []map[string]any
	records   []map[string]any
	persons   map[string]map[string]any
	reminders []map[string]any
}

func newStore() *store { return &store{persons: map[string]map[string]any{}} }

func ok(data any) map[string]any { return map[string]any{"ok": true, "data": data} }

// ----------------------------------------------------------------------------
// main
// ----------------------------------------------------------------------------

func main() {
	apiKey := os.Getenv("DASHSCOPE_API_KEY")
	if apiKey == "" {
		log.Fatal("DASHSCOPE_API_KEY is required (OpenAI-compatible Qwen endpoint)")
	}
	model := envOr("DASHSCOPE_MODEL", "qwen-plus")
	embModel := envOr("DASHSCOPE_EMBED_MODEL", "text-embedding-v4")
	const base = "https://dashscope.aliyuncs.com/compatible-mode/v1"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Brain (LLM) + embedder, both DashScope.
	brain, err := pool.NewPool(pool.PoolConfig{
		Enabled:  true,
		Strategy: pool.StrategyRoundRobin,
		Providers: []pool.Provider{{
			Name: "dashscope", BaseURL: base, Key: apiKey,
			ModelName: model, MaxConcurrency: 5, Capability: 8,
		}},
	})
	if err != nil {
		log.Fatalf("build brain: %v", err)
	}
	embedder, err := providers.NewOpenAIEmbedderProvider(&domain.OpenAIProviderConfig{
		BaseURL: base, APIKey: apiKey, EmbeddingModel: embModel,
	})
	if err != nil {
		log.Fatalf("build embedder: %v", err)
	}

	// Isolated home so cortex memory db lives in a temp dir.
	home, _ := os.MkdirTemp("", "superai-")
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	_ = os.MkdirAll(cfg.DataDir(), 0o755)

	db := newStore()

	now := time.Now()
	persona := fmt.Sprintf(`你是 SuperAI，一个有温度的随身 AI 生活/工作助手。
当前系统时间：%s %s（%s），时区 %s。
凡涉及相对时间（今天/明天/后天/大后天/这周五/下周一/下下周一/下个月3号/N天后…），都【必须先调用 resolve_datetime 工具】把它换算成绝对时间，再用返回的 rfc3339 去建日程/设提醒。绝不要自己心算日期，你算星期会出错。

职责：
- 从用户的自然语言里识别意图，主动调用工具把事情记录下来：
  约定/会面/活动 → add_schedule（把"周五下午三点"等相对时间换算成绝对时间 RFC3339）；
  提到某个人 → upsert_person（记住关系与偏好）；
  工作进展/踩的坑 → add_record(type=work，挂到 project)；
  生活/心情/日记 → add_record(type=diary)；随手笔记 → add_record(type=note)；
  打卡/习惯 → add_record(type=habit)；
  需要按时提醒/周期提醒 → set_reminder。
- 重要：只要用户在陈述一件发生过的事、或要求记录/提醒，你必须先调用对应的工具把它存下来，再回复确认。不要因为"记忆里好像有"就跳过记录，也不要回答"没找到记忆"之类的话。
- 用户提问（如"我周五有没有约""老王在忙啥"）时，结合召回到的记忆和检索工具回答。
- 当问题涉及"谁/和谁/什么关系/相关的人或事"时，优先调用 graph_recall（知识图谱召回，会顺着实体关系扩展），再结合结构化检索工具作答。
- 回答用中文，简短、自然、有人情味，像朋友而不是工具。
- 每条回复的最后，单独一行输出情绪标签，格式严格为：情绪: <中性|开心|思考|惊讶|关心|抱歉>。
  这个标签会驱动 3D 形象的表情，请根据语境选择。

严禁输出英文、日文或韩文，一律用中文回复。`,
		now.Format("2006-01-02"), now.Format("15:04:05"), weekdayCN(now), now.Format("-07:00"))

	svc, err := agent.New("SuperAI").
		WithPrompt(persona).
		WithConfig(cfg).
		WithLLM(brain).
		WithEmbedder(embedder).
		WithGraphMemory(). // graphflow 图存储 + graph_recall 工具（实体/关系扩展）
		Build()
	if err != nil {
		log.Fatalf("build SuperAI: %v", err)
	}
	defer svc.Close()

	registerTools(svc, db)

	fmt.Printf("=== SuperAI backend demo (AgentGo) ===\nbrain=%s  embed=%s  home=%s\n",
		model, embModel, home)

	// ---- Phase A: 一段对话，喂入 PRD 场景 S1/S4/S3/S8 ----
	sessionA := uuid.NewString()
	fmt.Printf("\n########## 会话 A（记录阶段）session=%s ##########\n", short(sessionA))
	phaseA := []string{
		"我刚跟老王约了这周五下午三点在楼下星巴克喝咖啡，他最近在看 AI 创业。",           // S1
		"今天把登录模块做完了，遇到一个 token 过期没刷新的坑，记到「SuperLeo」项目里。", // S4
		"今天有点累，不过晚上和大学室友吃了顿火锅，挺开心的。",                     // S3
		"以后每天晚上 22:00 提醒我喝杯水。",                           // S8
		"下下周一上午十点提醒我交季度报告。",                              // 远期相对时间 → resolve_datetime
		"大后天下午三点陪我妈去体检。",                                 // 另一种说法
		"下个月3号上午九点开 SuperLeo 项目评审会。",                     // 跨月 + 几号
	}
	for _, msg := range phaseA {
		turn(ctx, svc, sessionA, msg)
	}

	// ---- Phase B: 全新会话，验证跨会话长期记忆 + 结构化检索（S6）----
	sessionB := uuid.NewString()
	fmt.Printf("\n########## 会话 B（全新 session，验证记忆与检索）session=%s ##########\n", short(sessionB))
	phaseB := []string{
		"我这周五是不是有约？跟谁、在哪？",    // 跨会话召回 + 日程检索
		"老王最近在忙啥来着？",          // 长期记忆（人物档案）
		"跟 AI 创业有关的人或事都有哪些？",  // 知识图谱：实体关系扩展（graph_recall）
		"帮我看看最近的工作记录，有没有踩坑的。", // 记录检索
		"我设过哪些提醒？",            // 提醒检索
	}
	for _, msg := range phaseB {
		turn(ctx, svc, sessionB, msg)
	}

	// ---- 最终落库状态 ----
	dumpState(db)
}

// turn runs one conversational turn and prints intent → tools → reply → emotion.
func turn(ctx context.Context, svc *agent.Service, sessionID, msg string) {
	fmt.Printf("\n🧑 用户：%s\n", msg)
	res, err := svc.Run(ctx, msg,
		agent.WithSessionID(sessionID),
		agent.WithMemoryRecallShortcut(false), // action assistant: never let memory hijack a tool turn
	)
	if err != nil {
		fmt.Printf("   ⚠️  错误：%v\n", err)
		return
	}
	if len(res.ToolsUsed) > 0 {
		fmt.Printf("   🔧 调用工具：%s（共 %d 次）\n", strings.Join(res.ToolsUsed, ", "), res.ToolCalls)
	}
	if len(res.Memories) > 0 {
		fmt.Printf("   🧠 召回记忆 %d 条：%s\n", len(res.Memories), firstMemory(res.Memories))
	}
	reply, emotion := splitEmotion(res.Text())
	fmt.Printf("🦁 SuperAI：%s\n", strings.TrimSpace(reply))
	if emotion != "" {
		fmt.Printf("   %s 情绪=%s\n", emoji(emotion), emotion)
	}
}

// ----------------------------------------------------------------------------
// Tools — the assistant's hands. Each returns a stable {ok,data} shape (PTC-safe).
// ----------------------------------------------------------------------------

func registerTools(svc *agent.Service, db *store) {
	str := func(a map[string]any, k string) string {
		if v, ok := a[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	strSlice := func(a map[string]any, k string) []string {
		out := []string{}
		if raw, ok := a[k].([]any); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, strings.TrimSpace(s))
				}
			}
		}
		return out
	}
	write := agent.ToolMetadata{InterruptBehavior: agent.InterruptBehaviorBlock}
	read := agent.ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: agent.InterruptBehaviorCancel}

	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	s := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	arr := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}

	// resolve_datetime: 框架内置的确定性日期工具（模型理解、Go 计算，永不差一天）。
	agent.RegisterDateTimeTool(svc)

	svc.AddToolWithMetadata("add_schedule", "新建一条日程/约会。时间请用 RFC3339 绝对时间（先用 resolve_datetime 换算）。",
		obj(map[string]any{
			"title": s("日程标题"), "start_at": s("开始时间 RFC3339，如 2026-06-12T15:00:00+08:00"),
			"location": s("地点"), "participants": arr("参与人姓名"),
		}, "title", "start_at"),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			rec := map[string]any{
				"id": short(uuid.NewString()), "title": str(a, "title"), "start_at": str(a, "start_at"),
				"location": str(a, "location"), "participants": strSlice(a, "participants"),
			}
			db.schedules = append(db.schedules, rec)
			return ok(rec), nil
		}, write)

	svc.AddToolWithMetadata("list_schedules", "列出全部日程。", obj(map[string]any{}),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			return ok(db.schedules), nil
		}, read)

	svc.AddToolWithMetadata("add_record", "记录一条内容：日记/工作/笔记/习惯。",
		obj(map[string]any{
			"type": s("类型：diary|work|note|habit"), "title": s("简短标题"),
			"body": s("正文内容"), "tags": arr("标签"), "project": s("所属项目（工作记录用）"),
		}, "type", "body"),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			rec := map[string]any{
				"id": short(uuid.NewString()), "type": str(a, "type"), "title": str(a, "title"),
				"body": str(a, "body"), "tags": strSlice(a, "tags"), "project": str(a, "project"),
				"occurred_at": time.Now().Format(time.RFC3339),
			}
			db.records = append(db.records, rec)
			return ok(rec), nil
		}, write)

	svc.AddToolWithMetadata("search_records", "按关键词检索记录，可选按 type 过滤。",
		obj(map[string]any{"query": s("关键词"), "type": s("可选：diary|work|note|habit")}, "query"),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			q, typ := strings.ToLower(str(a, "query")), str(a, "type")
			hits := []map[string]any{}
			for _, r := range db.records {
				if typ != "" && r["type"] != typ {
					continue
				}
				blob := strings.ToLower(fmt.Sprintf("%v %v %v %v", r["title"], r["body"], r["tags"], r["project"]))
				if q == "" || strings.Contains(blob, q) {
					hits = append(hits, r)
				}
			}
			return ok(hits), nil
		}, read)

	svc.AddToolWithMetadata("upsert_person", "新建或更新一个人物档案（关系、偏好、最近动态）。",
		obj(map[string]any{
			"name": s("姓名"), "relation": s("关系，如同事/朋友/室友"), "note": s("偏好或最近动态"),
		}, "name"),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			name := str(a, "name")
			p := db.persons[name]
			if p == nil {
				p = map[string]any{"name": name}
			}
			if v := str(a, "relation"); v != "" {
				p["relation"] = v
			}
			if v := str(a, "note"); v != "" {
				p["note"] = v
			}
			db.persons[name] = p
			return ok(p), nil
		}, write)

	svc.AddToolWithMetadata("set_reminder", "设置提醒，可周期重复。",
		obj(map[string]any{
			"title": s("提醒内容"), "remind_at": s("首次提醒时间 RFC3339 或每日时间 HH:MM"),
			"recurrence": s("重复规则，如 daily/weekly/none"),
		}, "title", "remind_at"),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			rec := map[string]any{
				"id": short(uuid.NewString()), "title": str(a, "title"),
				"remind_at": str(a, "remind_at"), "recurrence": orDefault(str(a, "recurrence"), "none"),
			}
			db.reminders = append(db.reminders, rec)
			return ok(rec), nil
		}, write)

	svc.AddToolWithMetadata("list_reminders", "列出全部提醒。", obj(map[string]any{}),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			return ok(db.reminders), nil
		}, read)
}

// ----------------------------------------------------------------------------
// pretty printing / helpers
// ----------------------------------------------------------------------------

func dumpState(db *store) {
	db.mu.Lock()
	defer db.mu.Unlock()
	fmt.Printf("\n========== 落库状态（单用户，进程内）==========\n")
	fmt.Printf("日程 %d 条:\n", len(db.schedules))
	for _, r := range db.schedules {
		fmt.Printf("  • %s @ %s %v 参与:%v\n", r["title"], r["start_at"], r["location"], r["participants"])
	}
	fmt.Printf("记录 %d 条:\n", len(db.records))
	for _, r := range db.records {
		proj := ""
		if p, _ := r["project"].(string); p != "" {
			proj = " [" + p + "]"
		}
		fmt.Printf("  • (%s)%s %s — %s\n", r["type"], proj, r["title"], r["body"])
	}
	names := make([]string, 0, len(db.persons))
	for n := range db.persons {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("人物 %d 个:\n", len(names))
	for _, n := range names {
		p := db.persons[n]
		fmt.Printf("  • %s（%v）%v\n", p["name"], p["relation"], p["note"])
	}
	fmt.Printf("提醒 %d 条:\n", len(db.reminders))
	for _, r := range db.reminders {
		fmt.Printf("  • %s @ %s (%s)\n", r["title"], r["remind_at"], r["recurrence"])
	}
}

func splitEmotion(text string) (reply, emotion string) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "情绪:") || strings.HasPrefix(l, "情绪：") {
			emotion = strings.TrimSpace(strings.TrimLeft(l, "情绪:："))
			reply = strings.Join(lines[:i], "\n")
			return reply, emotion
		}
		break
	}
	return text, ""
}

func emoji(emotion string) string {
	switch {
	case strings.Contains(emotion, "开心"):
		return "😄"
	case strings.Contains(emotion, "思考"):
		return "🤔"
	case strings.Contains(emotion, "惊讶"):
		return "😮"
	case strings.Contains(emotion, "关心"):
		return "🥰"
	case strings.Contains(emotion, "抱歉"):
		return "🙇"
	default:
		return "🙂"
	}
}

func firstMemory(ms []*domain.MemoryWithScore) string {
	if len(ms) == 0 || ms[0] == nil || ms[0].Memory == nil {
		return ""
	}
	c := ms[0].Memory.Content
	if len(c) > 60 {
		c = c[:60] + "…"
	}
	return c
}

func weekdayCN(t time.Time) string {
	return "周" + []string{"日", "一", "二", "三", "四", "五", "六"}[int(t.Weekday())]
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
