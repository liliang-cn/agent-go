// Package main is a single-user backend for "SuperAI" — the AI assistant from
// the SuperLeo PRD — built entirely on the AgentGo framework. It exercises
// AgentGo end-to-end (NO multi-tenant / SaaS plumbing — just the brain):
//
//   - intent understanding + tool calling  (建日程 / 记事 / 记人物 / 设提醒)
//   - built-in resolve_datetime so the model never miscomputes relative dates
//   - knowledge-graph–aware memory (WithGraphMemory + graph_recall)
//   - emotion tagging that would drive the 3D avatar  (情绪: 标签)
//   - persistence across restarts (graph memory + a JSON-backed store)
//   - proactive reminders: a scheduler that makes SuperAI "speak" when due (S2)
//   - an interactive chat mode
//
// Brain is any OpenAI-compatible endpoint (default DashScope Qwen). Embeddings
// are optional: with one, SuperAI uses graph memory; without (SUPERAI_EMBED_KEY=none)
// it falls back to file memory — so it runs against chat-only proxies too.
//
// Usage:
//
//	DASHSCOPE_API_KEY=sk-...  go run ./examples/superai            # scripted demo
//	DASHSCOPE_API_KEY=sk-...  go run ./examples/superai -i         # interactive chat
//	DASHSCOPE_API_KEY=sk-...  go run ./examples/superai -web       # web UI
//	SUPERAI_HOME=~/.superai   go run ./examples/superai -i         # custom data dir
//
//	# any OpenAI-compatible brain (no embeddings → file memory):
//	SUPERAI_LLM_BASE=https://host/v1 SUPERAI_LLM_KEY=sk-... SUPERAI_LLM_MODEL=gpt-5.4 \
//	SUPERAI_EMBED_KEY=none  go run ./examples/superai -i
//
// State persists under SUPERAI_HOME (default ./.superai-data), so a second run
// remembers everything from the first.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
)

// ----------------------------------------------------------------------------
// Persistent in-process store (stands in for the PRD's Record/Schedule/... tables)
// ----------------------------------------------------------------------------

type store struct {
	mu        sync.Mutex
	path      string
	Schedules []map[string]any          `json:"schedules"`
	Records   []map[string]any          `json:"records"`
	Persons   map[string]map[string]any `json:"persons"`
	Reminders []map[string]any          `json:"reminders"`
}

func newStore(path string) *store {
	return &store{path: path, Persons: map[string]map[string]any{}}
}

func ok(data any) map[string]any { return map[string]any{"ok": true, "data": data} }

// load reads the JSON snapshot if it exists (persistence across restarts).
func (db *store) load() {
	raw, err := os.ReadFile(db.path)
	if err != nil {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	_ = json.Unmarshal(raw, db)
	if db.Persons == nil {
		db.Persons = map[string]map[string]any{}
	}
}

// save writes the JSON snapshot. Callers must NOT hold db.mu.
func (db *store) save() {
	db.mu.Lock()
	raw, err := json.MarshalIndent(db, "", "  ")
	db.mu.Unlock()
	if err != nil {
		return
	}
	_ = os.WriteFile(db.path, raw, 0o644)
}

// dueReminders returns the titles of reminders that should fire now, updating
// their fired/last-fired state. One-time reminders use an RFC3339 remind_at;
// daily ones use HH:MM and fire once per day.
func (db *store) dueReminders(now time.Time) []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	today := now.Format("2006-01-02")
	hm := now.Format("15:04")
	var due []string
	for _, r := range db.Reminders {
		title, _ := r["title"].(string)
		remindAt, _ := r["remind_at"].(string)
		recur, _ := r["recurrence"].(string)
		if recur == "daily" || (strings.Contains(remindAt, ":") && !strings.Contains(remindAt, "T")) {
			target := remindAt
			if strings.Contains(remindAt, "T") {
				if t, err := time.Parse(time.RFC3339, remindAt); err == nil {
					target = t.Format("15:04")
				}
			}
			if target == hm && r["last_fired"] != today {
				r["last_fired"] = today
				due = append(due, title)
			}
			continue
		}
		// one-time
		if fired, _ := r["fired"].(bool); fired {
			continue
		}
		if t, err := time.Parse(time.RFC3339, remindAt); err == nil && !now.Before(t) {
			r["fired"] = true
			due = append(due, title)
		}
	}
	return due
}

// ----------------------------------------------------------------------------
// main
// ----------------------------------------------------------------------------

func main() {
	interactive, web := false, false
	for _, a := range os.Args[1:] {
		switch a {
		case "-i", "--interactive", "-chat", "--chat":
			interactive = true
		case "-web", "--web":
			web = true
		}
	}

	// --- Brain (LLM): any OpenAI-compatible endpoint. Default DashScope Qwen. ---
	// Override with SUPERAI_LLM_BASE / SUPERAI_LLM_KEY / SUPERAI_LLM_MODEL to use
	// e.g. an OpenAI-compatible proxy. Falls back to DASHSCOPE_API_KEY.
	const dashBase = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	llmBase := envOr("SUPERAI_LLM_BASE", dashBase)
	llmKey := envOr("SUPERAI_LLM_KEY", os.Getenv("DASHSCOPE_API_KEY"))
	model := envOr("SUPERAI_LLM_MODEL", envOr("DASHSCOPE_MODEL", "qwen-plus"))
	if llmKey == "" {
		log.Fatal("need SUPERAI_LLM_KEY (or DASHSCOPE_API_KEY)")
	}
	brain, err := pool.NewPool(pool.PoolConfig{
		Enabled:  true,
		Strategy: pool.StrategyRoundRobin,
		Providers: []pool.Provider{{
			Name: "brain", BaseURL: llmBase, Key: llmKey,
			ModelName: model, MaxConcurrency: 5, Capability: 8,
		}},
	})
	if err != nil {
		log.Fatalf("build brain: %v", err)
	}

	// --- Embedder (optional): needed only for vector/graph memory. Default
	// DashScope text-embedding-v4. Set SUPERAI_EMBED_KEY=none to disable, in
	// which case SuperAI falls back to file memory (works with any brain,
	// including chat-only proxies that have no /v1/embeddings). ---
	embModel := envOr("SUPERAI_EMBED_MODEL", "text-embedding-v4")
	embBase := envOr("SUPERAI_EMBED_BASE", dashBase)
	embKey := envOr("SUPERAI_EMBED_KEY", os.Getenv("DASHSCOPE_API_KEY"))
	var embedder domain.Embedder
	if embKey != "" && embKey != "none" {
		embedder, err = providers.NewOpenAIEmbedderProvider(&domain.OpenAIProviderConfig{
			BaseURL: embBase, APIKey: embKey, EmbeddingModel: embModel,
		})
		if err != nil {
			log.Fatalf("build embedder: %v", err)
		}
	}

	// Persistent home: graph memory (cortex.db) + the JSON store live here and
	// survive restarts.
	home := envOr("SUPERAI_HOME", "./.superai-data")
	if strings.HasPrefix(home, "~/") {
		if h, e := os.UserHomeDir(); e == nil {
			home = filepath.Join(h, home[2:])
		}
	}
	cfg := &config.Config{Home: home}
	if embedder != nil {
		// Make config's store type match the builder's graph memory, so the
		// memory path resolves to cortex.db (a file) rather than the file-memory
		// directory — otherwise cortexdb tries to open a dir and CANTOPENs.
		cfg.Memory.StoreType = config.MemoryStoreTypeGraphFlow
	}
	cfg.ApplyHomeLayout()
	_ = os.MkdirAll(cfg.DataDir(), 0o755)

	db := newStore(filepath.Join(cfg.DataDir(), "superai-store.json"))
	db.load()

	b := agent.New("SuperAI").
		WithPrompt(buildPersona(time.Now())).
		WithConfig(cfg).
		WithLLM(brain).
		WithPTC(false) // simple one-tool-per-intent turns: direct tool-calling
	memMode := "graphflow"
	if embedder != nil {
		b = b.WithEmbedder(embedder).WithGraphMemory() // 图存储 + graph_recall
	} else {
		b = b.WithMemory(agent.WithMemoryStoreType("file")) // 无 embedding → 文件记忆
		memMode = "file (no embeddings)"
	}
	svc, err := b.Build()
	if err != nil {
		log.Fatalf("build SuperAI: %v", err)
	}
	defer svc.Close()
	registerTools(svc, db)

	fmt.Printf("=== SuperAI (AgentGo) ===\nbrain=%s @ %s  memory=%s  home=%s\n", model, llmBase, memMode, home)
	fmt.Printf("已加载: 日程 %d / 记录 %d / 人物 %d / 提醒 %d\n",
		len(db.Schedules), len(db.Records), len(db.Persons), len(db.Reminders))

	// Proactive reminder scheduler (PRD S2): fires due reminders out-of-band.
	// In web mode it pushes to connected browsers (SSE); otherwise it prints.
	events := newHub()
	onDue := announceReminder
	if web {
		onDue = func(title string) {
			log.Printf("proactive reminder due: %s", title)
			events.publish(fmt.Sprintf(`{"type":"reminder","text":%q}`, title))
		}
	}
	stopReminders := startReminderScheduler(db, !interactive && !web, onDue)
	defer stopReminders()

	switch {
	case web:
		runWeb(svc, db, brain, events, envOr("SUPERAI_ADDR", "127.0.0.1:43517"), os.Getenv("SUPERAI_TOKEN"))
	case interactive:
		runInteractive(svc, db)
	default:
		runScriptedDemo(svc, db)
	}
	db.save()
}

func buildPersona(now time.Time) string {
	return fmt.Sprintf(`你是 SuperAI，一个有温度的随身 AI 生活/工作助手。
当前系统时间：%s %s（%s），时区 %s。
凡涉及相对时间（今天/明天/后天/大后天/这周五/下周一/下下周一/下个月3号/今晚N点…），都【必须先调用 resolve_datetime 工具】换算成绝对时间，再用返回的 rfc3339 去建日程/设提醒。绝不要自己心算日期。

职责：
- 从用户的话里识别意图，主动调用工具记录：约定/会面→add_schedule；提到人→upsert_person；工作/踩坑→add_record(work,挂 project)；生活/心情→add_record(diary)；笔记→add_record(note)；打卡/习惯→add_record(habit)；要提醒→set_reminder。
- 只要用户在陈述发生的事或要求记录/提醒，必须先调用对应工具存下来再回复；不要回答"没找到记忆"之类的话。
- 提问且涉及"谁/和谁/什么关系/相关的人或事"时，优先用 graph_recall（知识图谱关系扩展），再结合检索工具作答。
- 需要最新/实时信息(新闻、股价、行情、不在记忆和记录里的事实)时，调用 web_search 联网查,再据结果回答并带上来源。
- 回答用中文，简短、自然、有人情味。每条回复最后单独一行输出情绪标签，格式严格为：情绪: <中性|开心|思考|惊讶|关心|抱歉>。

严禁输出英文、日文或韩文，一律用中文回复。`,
		now.Format("2006-01-02"), now.Format("15:04:05"), weekdayCN(now), now.Format("-07:00"))
}

// ----------------------------------------------------------------------------
// run modes
// ----------------------------------------------------------------------------

func runInteractive(svc *agent.Service, db *store) {
	fmt.Println("\n进入交互模式。直接说话即可；命令: /state 看落库, /quit 退出。")
	session := uuid.NewString()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Save on Ctrl-C.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\n(已保存，再见)")
		db.save()
		os.Exit(0)
	}()

	fmt.Print("\n🧑 ")
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case line == "/quit" || line == "/exit":
			return
		case line == "/state":
			dumpState(db)
		case line == "/help":
			fmt.Println("命令: /state 看落库 · /quit 退出。其它输入都当作对话。")
		default:
			turn(svc, session, line)
			db.save()
		}
		fmt.Print("\n🧑 ")
	}
}

func runScriptedDemo(svc *agent.Service, db *store) {
	if os.Getenv("SUPERAI_PROACTIVE_TEST") != "" {
		proactiveDemo(db)
		return
	}
	sessionA := uuid.NewString()
	fmt.Printf("\n########## 会话 A（记录阶段）session=%s ##########\n", short(sessionA))
	for _, msg := range []string{
		"我刚跟老王约了这周五下午三点在楼下星巴克喝咖啡，他最近在看 AI 创业。",
		"今天把登录模块做完了，遇到一个 token 过期没刷新的坑，记到「SuperLeo」项目里。",
		"今天有点累，不过晚上和大学室友吃了顿火锅，挺开心的。",
		"以后每天晚上 22:00 提醒我喝杯水。",
		"下下周一上午十点提醒我交季度报告。",
	} {
		turn(svc, sessionA, msg)
	}
	db.save()

	sessionB := uuid.NewString()
	fmt.Printf("\n########## 会话 B（全新 session，验证记忆与图谱）session=%s ##########\n", short(sessionB))
	for _, msg := range []string{
		"我这周五是不是有约？跟谁、在哪？",
		"跟 AI 创业有关的人或事都有哪些？",
		"我设过哪些提醒？",
	} {
		turn(svc, sessionB, msg)
	}

	proactiveDemo(db)
	dumpState(db)
}

// proactiveDemo seeds a one-time reminder a few seconds out and waits for the
// scheduler to fire it (demonstrates PRD S2 without waiting for a real clock).
func proactiveDemo(db *store) {
	fmt.Println("\n########## 主动提醒演示（约 3 秒后触发）##########")
	db.mu.Lock()
	db.Reminders = append(db.Reminders, map[string]any{
		"id": short(uuid.NewString()), "title": "起来活动一下，喝口水",
		"remind_at": time.Now().Add(3 * time.Second).Format(time.RFC3339), "recurrence": "none",
	})
	db.mu.Unlock()
	time.Sleep(8 * time.Second)
}

// turn runs one conversational turn and prints intent → tools → reply → emotion.
func turn(svc *agent.Service, sessionID, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := svc.Run(ctx, msg,
		agent.WithSessionID(sessionID),
		agent.WithMemoryRecallShortcut(false), // action assistant: tools must fire
	)
	if err != nil {
		fmt.Printf("   ⚠️  错误：%v\n", err)
		return
	}
	if len(res.ToolsUsed) > 0 {
		fmt.Printf("   🔧 %s（%d 次）\n", strings.Join(res.ToolsUsed, ", "), res.ToolCalls)
	}
	reply, emotion := splitEmotion(res.Text())
	fmt.Printf("🦁 %s\n", strings.TrimSpace(reply))
	if emotion != "" {
		fmt.Printf("   %s 情绪=%s\n", emoji(emotion), emotion)
	}
}

// ----------------------------------------------------------------------------
// proactive reminder scheduler (PRD S2 / F-SCH-3)
// ----------------------------------------------------------------------------

func startReminderScheduler(db *store, fastTick bool, onDue func(title string)) func() {
	tick := 30 * time.Second
	if fastTick {
		tick = 1 * time.Second // demo wants a quick, reliable fire
	}
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				for _, title := range db.dueReminders(now) {
					onDue(title)
					db.save()
				}
			}
		}
	}()
	return func() { close(stop) }
}

// announceReminder makes SuperAI proactively "speak" a due reminder (PRD S2 /
// F-SCH-3). It prints immediately with a warm template so a reminder is never
// dropped or delayed. (You could polish the wording via svc.Ask for a more
// "语义化" message, but that blocks on an LLM round-trip — kept out of the hot
// path here so the reminder always surfaces instantly.)
func announceReminder(title string) {
	fmt.Printf("\n🔔 SuperAI（主动）：到点啦～ %s\n🧑 ", title)
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

	// resolve_datetime: framework built-in (model understands, Go computes).
	agent.RegisterDateTimeTool(svc)

	// web_search: real-time info via DashScope's enable_search (news/finance/facts).
	// Registered only when a search key is configured (defaults to the embedding
	// key, which is a DashScope key). The model calls it when the answer isn't in
	// memory/records and needs to be fresh.
	searchKey := envOr("SUPERAI_SEARCH_KEY", os.Getenv("SUPERAI_EMBED_KEY"))
	searchBase := envOr("SUPERAI_SEARCH_BASE", "https://dashscope.aliyuncs.com/compatible-mode/v1")
	searchModel := envOr("SUPERAI_SEARCH_MODEL", "qwen-plus")
	if searchKey != "" && searchKey != "none" {
		svc.AddToolWithMetadata("web_search",
			"联网搜索实时信息(新闻/财经/股价/事实等),返回简要答案与来源。当用户要查最新、实时、或不在记忆与记录里的信息时调用。",
			obj(map[string]any{"query": s("搜索关键词或问题")}, "query"),
			func(ctx context.Context, a map[string]any) (any, error) {
				q := str(a, "query")
				if q == "" {
					return map[string]any{"ok": false, "error": "query required"}, nil
				}
				ans, err := dashscopeSearch(ctx, searchBase, searchKey, searchModel, q)
				if err != nil {
					return map[string]any{"ok": false, "error": err.Error()}, nil
				}
				return ok(map[string]any{"query": q, "answer": ans}), nil
			}, read)
	}

	svc.AddToolWithMetadata("add_schedule", "新建一条日程/约会。时间请用 RFC3339 绝对时间（先用 resolve_datetime 换算）。",
		obj(map[string]any{
			"title": s("日程标题"), "start_at": s("开始时间 RFC3339"),
			"location": s("地点"), "participants": arr("参与人姓名"),
		}, "title", "start_at"),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			rec := map[string]any{
				"id": short(uuid.NewString()), "title": str(a, "title"), "start_at": str(a, "start_at"),
				"location": str(a, "location"), "participants": strSlice(a, "participants"),
			}
			db.Schedules = append(db.Schedules, rec)
			db.mu.Unlock()
			db.save()
			return ok(rec), nil
		}, write)

	svc.AddToolWithMetadata("list_schedules", "列出全部日程。", obj(map[string]any{}),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			return ok(db.Schedules), nil
		}, read)

	svc.AddToolWithMetadata("add_record", "记录一条内容：日记/工作/笔记/习惯。",
		obj(map[string]any{
			"type": s("类型：diary|work|note|habit"), "title": s("简短标题"),
			"body": s("正文内容"), "tags": arr("标签"), "project": s("所属项目（工作记录用）"),
		}, "type", "body"),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			rec := map[string]any{
				"id": short(uuid.NewString()), "type": str(a, "type"), "title": str(a, "title"),
				"body": str(a, "body"), "tags": strSlice(a, "tags"), "project": str(a, "project"),
				"occurred_at": time.Now().Format(time.RFC3339),
			}
			db.Records = append(db.Records, rec)
			db.mu.Unlock()
			db.save()
			return ok(rec), nil
		}, write)

	svc.AddToolWithMetadata("search_records", "按关键词检索记录，可选按 type 过滤。",
		obj(map[string]any{"query": s("关键词"), "type": s("可选：diary|work|note|habit")}, "query"),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			q, typ := strings.ToLower(str(a, "query")), str(a, "type")
			hits := []map[string]any{}
			for _, r := range db.Records {
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
			name := str(a, "name")
			p := db.Persons[name]
			if p == nil {
				p = map[string]any{"name": name}
			}
			if v := str(a, "relation"); v != "" {
				p["relation"] = v
			}
			if v := str(a, "note"); v != "" {
				p["note"] = v
			}
			db.Persons[name] = p
			db.mu.Unlock()
			db.save()
			return ok(p), nil
		}, write)

	svc.AddToolWithMetadata("set_reminder", "设置提醒，可周期重复（到点 SuperAI 会主动提醒）。",
		obj(map[string]any{
			"title": s("提醒内容"), "remind_at": s("一次性用 RFC3339；每日用 HH:MM"),
			"recurrence": s("重复规则：daily 或 none"),
		}, "title", "remind_at"),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			rec := map[string]any{
				"id": short(uuid.NewString()), "title": str(a, "title"),
				"remind_at": str(a, "remind_at"), "recurrence": orDefault(str(a, "recurrence"), "none"),
			}
			db.Reminders = append(db.Reminders, rec)
			db.mu.Unlock()
			db.save()
			return ok(rec), nil
		}, write)

	svc.AddToolWithMetadata("list_reminders", "列出全部提醒。", obj(map[string]any{}),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			return ok(db.Reminders), nil
		}, read)
}

// ----------------------------------------------------------------------------
// pretty printing / helpers
// ----------------------------------------------------------------------------

func dumpState(db *store) {
	db.mu.Lock()
	defer db.mu.Unlock()
	fmt.Printf("\n========== 落库状态（持久化于 %s）==========\n", db.path)
	fmt.Printf("日程 %d 条:\n", len(db.Schedules))
	for _, r := range db.Schedules {
		fmt.Printf("  • %s @ %s %v 参与:%v\n", r["title"], r["start_at"], r["location"], r["participants"])
	}
	fmt.Printf("记录 %d 条:\n", len(db.Records))
	for _, r := range db.Records {
		proj := ""
		if p, _ := r["project"].(string); p != "" {
			proj = " [" + p + "]"
		}
		fmt.Printf("  • (%s)%s %s — %s\n", r["type"], proj, r["title"], r["body"])
	}
	names := make([]string, 0, len(db.Persons))
	for n := range db.Persons {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("人物 %d 个:\n", len(names))
	for _, n := range names {
		p := db.Persons[n]
		fmt.Printf("  • %s（%v）%v\n", p["name"], p["relation"], p["note"])
	}
	fmt.Printf("提醒 %d 条:\n", len(db.Reminders))
	for _, r := range db.Reminders {
		fmt.Printf("  • %s @ %s (%s)\n", r["title"], r["remind_at"], r["recurrence"])
	}
}

// splitEmotion peels the trailing "情绪: X" tag off a reply. It is robust to the
// tag sitting on a real newline, on a literal "\n" the model emitted as text, or
// just appended — and returns the cleaned reply plus the emotion.
func splitEmotion(text string) (reply, emotion string) {
	text = strings.TrimRight(text, " \t\r\n")
	for _, marker := range []string{"情绪:", "情绪："} {
		if i := strings.LastIndex(text, marker); i >= 0 {
			emotion = strings.TrimSpace(text[i+len(marker):])
			if nl := strings.IndexAny(emotion, "\r\n"); nl >= 0 {
				emotion = strings.TrimSpace(emotion[:nl])
			}
			reply = text[:i]
			reply = strings.TrimRight(reply, " \t\r\n")
			reply = strings.TrimSuffix(reply, "\\n") // literal backslash-n the model emitted
			reply = strings.TrimRight(reply, " \t\r\n")
			return reply, emotion
		}
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

// dashscopeSearch performs a grounded web search via DashScope's enable_search
// extension (the OpenAI-style web_search_options is ignored by DashScope; the
// non-standard enable_search:true is what actually triggers retrieval).
func dashscopeSearch(ctx context.Context, base, key, model, query string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "联网搜索后用中文简要回答下面的问题,并在末尾附 1-3 个来源链接:\n" + query,
		}},
		"enable_search": true,
		"max_tokens":    700,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("no search result")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
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
