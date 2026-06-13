package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

// ----------------------------------------------------------------------------
// hub: fan-out of proactive events (reminders) to connected web clients (SSE).
// ----------------------------------------------------------------------------

type hub struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func newHub() *hub { return &hub{subs: map[chan string]struct{}{}} }

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
		default: // drop if a slow client's buffer is full
		}
	}
}

// ----------------------------------------------------------------------------
// web server
// ----------------------------------------------------------------------------

func runWeb(svc *agent.Service, db *store, h *hub, addr string) {
	webSession := uuid.NewString()

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(indexHTML))
	})

	// POST /api/chat {message} -> {reply, emotion, tools}
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
			http.Error(w, "message required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		res, err := svc.Run(ctx, req.Message,
			agent.WithSessionID(webSession),
			agent.WithMemoryRecallShortcut(false),
		)
		if err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		reply, emotion := splitEmotion(res.Text())
		db.save()
		writeJSON(w, map[string]any{
			"reply":   reply,
			"emotion": emotion,
			"emoji":   emoji(emotion),
			"tools":   res.ToolsUsed,
		})
	})

	// GET /api/state -> the full store snapshot
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		db.mu.Lock()
		snapshot := map[string]any{
			"schedules": db.Schedules,
			"records":   db.Records,
			"persons":   db.Persons,
			"reminders": db.Reminders,
		}
		db.mu.Unlock()
		writeJSON(w, snapshot)
	})

	// GET /api/events -> SSE stream of proactive reminders
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
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
		fmt.Fprintf(w, ": connected\n\n")
		flusher.Flush()
		keepalive := time.NewTicker(20 * time.Second)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-keepalive.C:
				fmt.Fprintf(w, ": ping\n\n")
				flusher.Flush()
			case msg := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", msg)
				flusher.Flush()
			}
		}
	})

	fmt.Printf("\n🌐 SuperAI web UI: http://%s\n", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("web server: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// ----------------------------------------------------------------------------
// embedded single-page UI
// ----------------------------------------------------------------------------

const indexHTML = `<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>SuperAI</title>
<style>
  :root{--bg:#0f1115;--panel:#171a21;--line:#262b36;--me:#2b6cff;--ai:#222834;--txt:#e6e9ef;--mut:#8b93a7}
  *{box-sizing:border-box}
  body{margin:0;height:100vh;display:flex;font:15px/1.5 -apple-system,system-ui,"PingFang SC",sans-serif;background:var(--bg);color:var(--txt)}
  #left{flex:1;display:flex;flex-direction:column;min-width:0}
  header{padding:14px 18px;border-bottom:1px solid var(--line);font-weight:600;display:flex;align-items:center;gap:8px}
  #log{flex:1;overflow:auto;padding:18px;display:flex;flex-direction:column;gap:12px}
  .msg{max-width:78%;padding:10px 14px;border-radius:14px;white-space:pre-wrap;word-break:break-word}
  .me{align-self:flex-end;background:var(--me);color:#fff;border-bottom-right-radius:4px}
  .ai{align-self:flex-start;background:var(--ai);border-bottom-left-radius:4px}
  .meta{font-size:12px;color:var(--mut);margin-top:4px}
  .sys{align-self:center;color:var(--mut);font-size:13px}
  .toast{align-self:center;background:#3a2d12;border:1px solid #6b5320;color:#ffd98a;padding:8px 14px;border-radius:10px}
  #bar{display:flex;gap:8px;padding:14px;border-top:1px solid var(--line)}
  #in{flex:1;padding:11px 14px;border-radius:10px;border:1px solid var(--line);background:#0c0e12;color:var(--txt);outline:none}
  button{padding:11px 18px;border:0;border-radius:10px;background:var(--me);color:#fff;font-weight:600;cursor:pointer}
  button:disabled{opacity:.5;cursor:default}
  #right{width:340px;border-left:1px solid var(--line);background:var(--panel);overflow:auto;padding:16px}
  #right h3{margin:18px 0 8px;font-size:13px;color:var(--mut);text-transform:uppercase;letter-spacing:.5px}
  #right h3:first-child{margin-top:0}
  .card{background:#0c0e12;border:1px solid var(--line);border-radius:10px;padding:9px 11px;margin-bottom:7px;font-size:13px}
  .card b{font-weight:600}
  .card .sub{color:var(--mut);font-size:12px;margin-top:2px}
  .empty{color:var(--mut);font-size:13px}
</style>
</head>
<body>
  <div id="left">
    <header>🦁 SuperAI <span style="color:var(--mut);font-weight:400;font-size:13px">· 有温度的 AI 生活/工作助手</span></header>
    <div id="log"></div>
    <div id="bar">
      <input id="in" placeholder="跟 SuperAI 说点什么…（如：明天下午三点和老王开会）" autofocus/>
      <button id="send">发送</button>
    </div>
  </div>
  <div id="right">
    <h3>日程</h3><div id="schedules"></div>
    <h3>记录</h3><div id="records"></div>
    <h3>人物</h3><div id="persons"></div>
    <h3>提醒</h3><div id="reminders"></div>
  </div>
<script>
const log=document.getElementById('log'),input=document.getElementById('in'),send=document.getElementById('send');
function el(cls,html){const d=document.createElement('div');d.className=cls;d.innerHTML=html;log.appendChild(d);log.scrollTop=log.scrollHeight;return d;}
function esc(s){return (s||'').replace(/[&<>]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;'}[c]));}
function card(title,sub,tag){return '<div class="card"><b>'+esc(title)+'</b>'+(tag?' <span class="sub">'+esc(tag)+'</span>':'')+(sub?'<div class="sub">'+esc(sub)+'</div>':'')+'</div>';}
async function refresh(){
  const s=await (await fetch('/api/state')).json();
  const fill=(id,items,render)=>{const box=document.getElementById(id);box.innerHTML=items&&items.length?items.map(render).join(''):'<div class="empty">暂无</div>';};
  fill('schedules',s.schedules,r=>card(r.title,(r.start_at||'')+' '+(r.location||'')));
  fill('records',s.records,r=>card(r.title||r.type,r.body,'('+r.type+')'));
  fill('persons',Object.values(s.persons||{}),p=>card(p.name,p.note||'',p.relation||''));
  fill('reminders',s.reminders,r=>card(r.title,(r.remind_at||'')+' · '+(r.recurrence||'')));
}
async function sendMsg(){
  const text=input.value.trim(); if(!text) return;
  el('msg me',esc(text)); input.value=''; input.disabled=send.disabled=true;
  const thinking=el('sys','SuperAI 思考中…');
  try{
    const r=await (await fetch('/api/chat',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({message:text})})).json();
    thinking.remove();
    if(r.error){el('sys','⚠️ '+esc(r.error));}
    else{
      const m=el('msg ai',esc(r.reply));
      let meta=[]; if(r.emotion) meta.push((r.emoji||'')+' '+esc(r.emotion)); if(r.tools&&r.tools.length) meta.push('🔧 '+r.tools.map(esc).join(', '));
      if(meta.length) m.insertAdjacentHTML('beforeend','<div class="meta">'+meta.join(' · ')+'</div>');
    }
  }catch(e){thinking.remove();el('sys','⚠️ '+e);}
  input.disabled=send.disabled=false; input.focus(); refresh();
}
send.onclick=sendMsg; input.addEventListener('keydown',e=>{if(e.key==='Enter')sendMsg();});
// proactive reminders pushed from the server
new EventSource('/api/events').onmessage=ev=>{try{const d=JSON.parse(ev.data);if(d.type==='reminder'){el('toast','🔔 '+esc(d.text));refresh();}}catch(_){}};
refresh();
</script>
</body>
</html>`
