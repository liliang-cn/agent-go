package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Built React/Vite SPA (Vercel AI SDK). Build with: (cd web && npm i && npm run build)
//
//go:embed all:web/dist
var distFS embed.FS

// ----------------------------------------------------------------------------
// hub: fan-out of proactive events (reminders) to web clients (SSE).
// ----------------------------------------------------------------------------

type hub struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func newHub() *hub { return &hub{subs: map[chan string]struct{}{}} }

// overview is generated at most once per day and cached in-process.
var (
	overviewMu    sync.Mutex
	overviewCache map[string]any
	overviewDay   string
)

func (h *hub) subscribe() (chan string, func()) {
	ch := make(chan string, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		close(ch)
		h.mu.Unlock()
	}
}

func (h *hub) publish(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

// ----------------------------------------------------------------------------
// web server
// ----------------------------------------------------------------------------

func runWeb(svc *agent.Service, db *store, gen domain.Generator, h *hub, addr, token string) {
	webSession := uuid.NewString()

	tokenOK := func(r *http.Request) bool {
		if token == "" {
			return true
		}
		if v := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); v == token {
			return true
		}
		if r.Header.Get("X-Token") == token {
			return true
		}
		return r.URL.Query().Get("token") == token
	}
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !tokenOK(r) {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}

	mux := http.NewServeMux()

	// Serve the embedded SPA (index.html fallback for the single route).
	sub, _ := fs.Sub(distFS, "web/dist")
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA: always serve index.html for the app shell
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "ui not built (run: cd examples/superai/web && npm i && npm run build)", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"auth": token != ""})
	})

	// AI SDK UI message stream protocol. useChat POSTs {messages}; we run the
	// agent and stream the (emotion-stripped) reply back as text-delta parts.
	mux.HandleFunc("/api/chat", auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role  string `json:"role"`
				Parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// last user message text
		userText := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				var b strings.Builder
				for _, p := range req.Messages[i].Parts {
					if p.Type == "text" {
						b.WriteString(p.Text)
					}
				}
				userText = strings.TrimSpace(b.String())
				break
			}
		}
		sid := strings.TrimSpace(r.Header.Get("X-Session"))
		if sid == "" {
			sid = webSession
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("x-vercel-ai-ui-message-stream", "v1")
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		send(map[string]any{"type": "start"})

		reply, emotion := "", ""
		var tools []string
		if userText == "" {
			reply = "(空消息)"
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
			res, err := svc.Run(ctx, userText, agent.WithSessionID(sid), agent.WithMemoryRecallShortcut(false))
			cancel()
			if err != nil {
				log.Printf("chat error (session=%s): %v", sid, err)
				reply = "⚠️ " + err.Error()
			} else {
				reply, emotion = splitEmotion(res.Text())
				reply = strings.TrimSpace(reply)
				tools = res.ToolsUsed
				db.save()
			}
		}

		const id = "0"
		send(map[string]any{"type": "text-start", "id": id})
		for _, chunk := range chunkRunes(reply, 3) {
			send(map[string]any{"type": "text-delta", "id": id, "delta": chunk})
			time.Sleep(10 * time.Millisecond) // typewriter feel
		}
		send(map[string]any{"type": "text-end", "id": id})
		// AI SDK custom data part: tools used + emotion → rendered as chips.
		if emotion != "" || len(tools) > 0 {
			send(map[string]any{"type": "data-meta", "data": map[string]any{
				"emotion": emotion, "emoji": emoji(emotion), "tools": tools,
			}})
		}
		send(map[string]any{"type": "finish"})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))

	mux.HandleFunc("/api/overview", auth(func(w http.ResponseWriter, r *http.Request) {
		summary, empty := summarizeState(db)
		if empty {
			writeJSON(w, map[string]any{"sections": []map[string]any{{
				"icon": "🌱", "title": "还没有数据",
				"items": []string{"跟我说点什么吧,比如:明天下午三点和老王开个会"},
			}}})
			return
		}
		today := time.Now().Format("2006-01-02")
		force := r.URL.Query().Get("force") != ""
		if !force {
			overviewMu.Lock()
			cached, day := overviewCache, overviewDay
			overviewMu.Unlock()
			if day == today && cached != nil {
				writeJSON(w, cached)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		out, err := gen.Generate(ctx, overviewPrompt(summary), &domain.GenerationOptions{Temperature: 0.4, MaxTokens: 600})
		if err != nil {
			writeJSON(w, map[string]any{"sections": []map[string]any{{
				"icon": "⚠️", "title": "概览生成失败", "items": []string{err.Error()},
			}}})
			return
		}
		var parsed map[string]any
		if json.Unmarshal([]byte(extractJSONObject(out)), &parsed) == nil && parsed["sections"] != nil {
			overviewMu.Lock()
			overviewCache, overviewDay = parsed, today
			overviewMu.Unlock()
			writeJSON(w, parsed)
			return
		}
		writeJSON(w, map[string]any{"sections": []map[string]any{{
			"icon": "🦁", "title": "概览", "items": []string{strings.TrimSpace(out)},
		}}})
	}))

	mux.HandleFunc("/api/events", auth(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		ch, unsub := h.subscribe()
		defer unsub()
		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()
		keepalive := time.NewTicker(20 * time.Second)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-keepalive.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			case msg := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", msg)
				flusher.Flush()
			}
		}
	}))

	note := "no token (open)"
	if token != "" {
		note = "token required"
	}
	fmt.Printf("\n🌐 SuperAI web UI: http://%s  (%s)\n", addr, note)
	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("web server: %v", err)
	}
}

// chunkRunes splits s into groups of n runes for a streamed/typewriter effect.
func chunkRunes(s string, n int) []string {
	if s == "" {
		return nil
	}
	r := []rune(s)
	var out []string
	for i := 0; i < len(r); i += n {
		j := i + n
		if j > len(r) {
			j = len(r)
		}
		out = append(out, string(r[i:j]))
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func summarizeState(db *store) (string, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.Schedules) == 0 && len(db.Records) == 0 && len(db.Persons) == 0 && len(db.Reminders) == 0 {
		return "", true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "现在时间: %s\n", time.Now().Format("2006-01-02 15:04 周一"))
	b.WriteString("日程:\n")
	for _, r := range db.Schedules {
		fmt.Fprintf(&b, "- %v @ %v %v 参与:%v\n", r["title"], r["start_at"], r["location"], r["participants"])
	}
	b.WriteString("记录:\n")
	for _, r := range db.Records {
		fmt.Fprintf(&b, "- (%v) %v: %v\n", r["type"], r["title"], r["body"])
	}
	b.WriteString("人物:\n")
	for _, p := range db.Persons {
		fmt.Fprintf(&b, "- %v(%v) %v\n", p["name"], p["relation"], p["note"])
	}
	b.WriteString("提醒:\n")
	for _, r := range db.Reminders {
		fmt.Fprintf(&b, "- %v @ %v (%v)\n", r["title"], r["remind_at"], r["recurrence"])
	}
	return b.String(), false
}

func overviewPrompt(state string) string {
	return "你是 SuperAI,一个有温度的助手。根据下面用户的数据,生成一个简洁、有用、有人情味的中文「概览面板」。\n" +
		"自行决定要展示哪些区块(例如:今日重点、即将到来、待办提醒、最近动态、关系洞察、贴心建议),只保留真正有信息量的。\n" +
		"严格只输出 JSON,不要任何额外文字、不要 markdown 代码块,格式:\n" +
		`{"sections":[{"icon":"一个emoji","title":"短标题","items":["一行要点","..."]}]}` + "\n" +
		"每个区块 1-4 条要点,精炼。只用中文。\n\n数据:\n" + state
}

func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}
