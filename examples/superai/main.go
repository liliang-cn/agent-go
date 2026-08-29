// Package main is a single-user backend for "SuperAI" — the AI assistant from
// the SuperLeo PRD — built entirely on the AgentGo framework. It exercises
// AgentGo end-to-end (NO multi-tenant / SaaS plumbing — just the brain):
//
//   - intent understanding + tool calling  (schedules / notes / people / reminders)
//   - built-in resolve_datetime so the model never miscomputes relative dates
//   - knowledge-graph–aware memory (WithGraphMemory + graph_recall)
//   - emotion tagging that would drive the 3D avatar  (the "情绪: <label>" tag)
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
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
	"github.com/liliang-cn/agent-go/v3/pkg/providers"
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
		WithLLM(brain)
	memMode := "graphflow"
	if embedder != nil {
		b = b.WithEmbedder(embedder).WithGraphMemory() // graph store + graph_recall
	} else {
		b = b.WithMemory(agent.WithMemoryStoreType("file")) // no embeddings -> file-backed memory
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
	return fmt.Sprintf(`You are SuperAI, a warm personal AI assistant for everyday life and work.
Current system time: %s %s (%s), timezone %s.
For anything relative (today / tomorrow / the day after / this Friday / next Monday / the Monday after next / the 3rd of next month / tonight at N ...), you MUST call the resolve_datetime tool first to turn it into an absolute time, then use the returned rfc3339 to create the schedule or reminder. Never work the date out in your head.

Responsibilities:
- Read the intent out of what the user says and call the matching tool: an appointment or meeting -> add_schedule; a person is mentioned -> upsert_person; work or a gotcha -> add_record(work, with project); life or mood -> add_record(diary); a note -> add_record(note); a check-in or habit -> add_record(habit); a reminder -> set_reminder.
- Whenever the user states something that happened, or asks you to record or remind, call the matching tool to store it BEFORE replying; never answer with something like "no memory found".
- For questions about who, with whom, how things relate, or related people and things, reach for graph_recall (knowledge-graph relation expansion) first, then combine it with the retrieval tools.
- For fresh or real-time information (news, stock prices, market data, facts not in memory or records), call web_search and answer from the results, citing the sources.
- To read, review or quote the real content of a specific URL, use fetch_url to fetch that page's body text (web_search only returns search summaries; reading a specific page needs fetch_url).
- Reply in Chinese: short, natural, warm. End every reply with the emotion tag alone on the last line, in exactly this form: 情绪: <中性|开心|思考|惊讶|关心|抱歉>.

Never reply in English, Japanese or Korean - always answer in Chinese.`,
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
// more natural-sounding message, but that blocks on an LLM round-trip — kept out of the hot
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
	if searchKey == "none" {
		searchKey = ""
	}
	agent.RegisterWebSearchTool(svc, agent.WebSearchConfig{
		BaseURL: envOr("SUPERAI_SEARCH_BASE", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:  searchKey,
		Model:   envOr("SUPERAI_SEARCH_MODEL", "qwen-plus"),
	})

	// fetch_url: framework built-in (read a specific page's text; SSRF-guarded).
	agent.RegisterFetchURLTool(svc)

	svc.AddToolWithMetadata("add_schedule", "Create a schedule entry or appointment. Give the time as an absolute RFC3339 timestamp (resolve it with resolve_datetime first).",
		obj(map[string]any{
			"title": s("Title of the entry"), "start_at": s("Start time, RFC3339"),
			"location": s("Location"), "participants": arr("Names of the participants"),
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

	svc.AddToolWithMetadata("list_schedules", "List every schedule entry.", obj(map[string]any{}),
		func(ctx context.Context, a map[string]any) (any, error) {
			db.mu.Lock()
			defer db.mu.Unlock()
			return ok(db.Schedules), nil
		}, read)

	svc.AddToolWithMetadata("add_record", "Record an entry: diary, work, note or habit.",
		obj(map[string]any{
			"type": s("Kind: diary|work|note|habit"), "title": s("Short title"),
			"body": s("Body text"), "tags": arr("Tags"), "project": s("Project it belongs to (for work records)"),
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

	svc.AddToolWithMetadata("search_records", "Search records by keyword, optionally filtered by type.",
		obj(map[string]any{"query": s("Keywords"), "type": s("Optional: diary|work|note|habit")}, "query"),
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

	svc.AddToolWithMetadata("upsert_person", "Create or update a person profile (relationship, preferences, recent news).",
		obj(map[string]any{
			"name": s("Name"), "relation": s("Relationship, e.g. colleague / friend / roommate"), "note": s("Preferences or recent news"),
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

	svc.AddToolWithMetadata("set_reminder", "Set a reminder, optionally recurring (SuperAI speaks up when it is due).",
		obj(map[string]any{
			"title": s("What to be reminded of"), "remind_at": s("RFC3339 for a one-off; HH:MM for a daily one"),
			"recurrence": s("Recurrence rule: daily or none"),
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

	svc.AddToolWithMetadata("list_reminders", "List every reminder.", obj(map[string]any{}),
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
