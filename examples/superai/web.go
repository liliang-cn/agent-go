package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// ----------------------------------------------------------------------------
// hub: fan-out of proactive events (reminders) to web clients (SSE).
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

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(indexHTML))
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"auth": token != ""})
	})

	mux.HandleFunc("/api/chat", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
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
			"reply": reply, "emotion": emotion, "emoji": emoji(emotion), "tools": res.ToolsUsed,
		})
	}))

	// AI-generated overview/dashboard: the model decides the sections from the
	// current data. Only called on load / after chat / manual refresh.
	mux.HandleFunc("/api/overview", auth(func(w http.ResponseWriter, r *http.Request) {
		summary, empty := summarizeState(db)
		if empty {
			writeJSON(w, map[string]any{"sections": []map[string]any{{
				"icon": "🌱", "title": "还没有数据",
				"items": []string{"跟我说点什么吧,比如:明天下午三点和老王开个会"},
			}}})
			return
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
			writeJSON(w, parsed)
			return
		}
		// model didn't return clean JSON — show its text as one section
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// summarizeState renders a compact text view of the store for the overview
// prompt. empty=true when there's nothing yet (skip the LLM call).
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

// extractJSONObject pulls the outermost {...} from a possibly-fenced/explained
// model reply.
func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}

// ----------------------------------------------------------------------------
// embedded single-page UI (no build step; no backticks inside this raw string)
// ----------------------------------------------------------------------------

const indexHTML = `<!doctype html>
<html lang="zh" data-theme="dark">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>SuperAI</title>
<style>
  :root{
    --bg:#0b0d12; --bg2:#0f121a; --panel:#12151e; --card:#171b26; --line:#232838;
    --txt:#e8ecf5; --mut:#8a93a8; --accent:#7c5cff; --accent2:#4da3ff;
    --me1:#7c5cff; --me2:#5a8bff; --ai:#1b2030; --ok:#34d399; --shadow:rgba(0,0,0,.45);
    --g1:rgba(124,92,255,.16); --g2:rgba(77,163,255,.12);
  }
  html[data-theme="light"]{
    --bg:#f4f6fb; --bg2:#eaeef6; --panel:#ffffff; --card:#ffffff; --line:#e4e8f1;
    --txt:#1b2030; --mut:#6b7488; --accent:#6b4dff; --accent2:#2f8fff;
    --me1:#6b4dff; --me2:#3f7bff; --ai:#eef1f7; --ok:#16a34a; --shadow:rgba(60,70,100,.14);
    --g1:rgba(124,92,255,.10); --g2:rgba(77,163,255,.08);
  }
  *{box-sizing:border-box}
  html,body{height:100%}
  body{margin:0;font:15px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",system-ui,sans-serif;
    color:var(--txt);transition:background .25s,color .25s;
    background:radial-gradient(1100px 600px at 12% -10%,var(--g1),transparent 60%),
               radial-gradient(900px 500px at 110% 10%,var(--g2),transparent 55%),var(--bg);}
  .app{display:flex;height:100vh;overflow:hidden}
  .chat{flex:1;display:flex;flex-direction:column;min-width:0}
  .topbar{display:flex;align-items:center;gap:12px;padding:16px 22px;border-bottom:1px solid var(--line)}
  .logo{width:38px;height:38px;border-radius:12px;display:grid;place-items:center;font-size:20px;
    background:linear-gradient(135deg,var(--accent),var(--accent2));box-shadow:0 6px 18px var(--shadow)}
  .ttl{font-weight:700;font-size:16px}.sub{color:var(--mut);font-size:12.5px}
  .spacer{flex:1}
  .iconbtn{width:36px;height:36px;border-radius:10px;border:1px solid var(--line);background:var(--card);
    color:var(--txt);cursor:pointer;font-size:16px;display:grid;place-items:center}
  .iconbtn:hover{border-color:var(--accent)}
  .dot{width:8px;height:8px;border-radius:50%;background:var(--ok);box-shadow:0 0 0 3px rgba(52,211,153,.18)}

  .log{flex:1;overflow:auto;padding:24px;display:flex;flex-direction:column;gap:16px}
  .row{display:flex;gap:10px;max-width:760px;width:100%;margin:0 auto}
  .row.me{flex-direction:row-reverse}
  .av{width:30px;height:30px;border-radius:9px;flex:none;display:grid;place-items:center;font-size:15px}
  .av.ai{background:var(--ai);border:1px solid var(--line)}
  .av.me{background:linear-gradient(135deg,var(--me1),var(--me2));color:#fff}
  .bubble{padding:11px 15px;border-radius:16px;white-space:pre-wrap;word-break:break-word;max-width:78%}
  .me .bubble{background:linear-gradient(135deg,var(--me1),var(--me2));color:#fff;border-top-right-radius:5px}
  .ai .bubble{background:var(--ai);border:1px solid var(--line);border-top-left-radius:5px}
  .meta{display:flex;gap:6px;flex-wrap:wrap;margin-top:8px}
  .chip{font-size:11.5px;color:var(--mut);background:rgba(127,127,127,.10);border:1px solid var(--line);padding:2px 8px;border-radius:999px}
  .chip.emo{color:#caa23a;border-color:rgba(180,140,40,.4);background:rgba(200,160,60,.10)}
  .typing{display:inline-flex;gap:4px}.typing i{width:6px;height:6px;border-radius:50%;background:var(--mut);animation:bp 1s infinite}
  .typing i:nth-child(2){animation-delay:.15s}.typing i:nth-child(3){animation-delay:.3s}
  @keyframes bp{0%,80%,100%{opacity:.3;transform:translateY(0)}40%{opacity:1;transform:translateY(-3px)}}

  .composer{padding:16px 22px;border-top:1px solid var(--line)}
  .inwrap{display:flex;gap:10px;align-items:flex-end;max-width:760px;margin:0 auto;background:var(--card);
    border:1px solid var(--line);border-radius:16px;padding:8px 8px 8px 16px}
  .inwrap:focus-within{border-color:var(--accent);box-shadow:0 0 0 3px var(--g1)}
  textarea{flex:1;border:0;background:transparent;color:var(--txt);outline:none;resize:none;font:inherit;max-height:140px;padding:6px 0}
  .send{width:40px;height:40px;border:0;border-radius:12px;cursor:pointer;flex:none;
    background:linear-gradient(135deg,var(--accent),var(--accent2));color:#fff;font-size:17px;display:grid;place-items:center}
  .send:disabled{opacity:.45}

  .panel{width:360px;border-left:1px solid var(--line);background:linear-gradient(180deg,var(--panel),var(--bg2));overflow:auto;padding:20px}
  .phead{display:flex;align-items:center;gap:8px;margin-bottom:14px}
  .phead b{font-size:13px;color:var(--mut);text-transform:uppercase;letter-spacing:1px}
  .osec{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:14px;margin-bottom:12px;box-shadow:0 4px 14px var(--shadow)}
  .oh{font-weight:700;font-size:14px;margin-bottom:8px}
  .oitem{display:flex;gap:8px;color:var(--txt);font-size:13.5px;padding:4px 0;border-top:1px dashed var(--line)}
  .oitem:first-of-type{border-top:0}
  .oitem:before{content:"•";color:var(--accent)}
  .empty{color:var(--mut);font-size:13px;padding:8px 2px}
  .spin{display:inline-block;width:14px;height:14px;border:2px solid var(--line);border-top-color:var(--accent);border-radius:50%;animation:sp .7s linear infinite;vertical-align:-2px;margin-right:6px}
  @keyframes sp{to{transform:rotate(360deg)}}

  #toasts{position:fixed;top:18px;right:18px;display:flex;flex-direction:column;gap:10px;z-index:50}
  .toast{background:var(--card);border:1px solid var(--accent);color:var(--txt);padding:12px 16px;border-radius:12px;box-shadow:0 12px 30px var(--shadow);animation:slin .3s ease}
  @keyframes slin{from{opacity:0;transform:translateX(20px)}to{opacity:1;transform:none}}

  #gate{position:fixed;inset:0;display:none;place-items:center;background:rgba(6,8,12,.7);backdrop-filter:blur(6px);z-index:100}
  #gate.show{display:grid}
  .gatecard{width:340px;background:var(--card);border:1px solid var(--line);border-radius:18px;padding:28px;text-align:center;box-shadow:0 20px 60px var(--shadow)}
  .gatecard .logo{margin:0 auto 14px;width:52px;height:52px;font-size:26px}
  .gatecard h2{margin:0 0 6px;font-size:18px}.gatecard p{margin:0 0 18px;color:var(--mut);font-size:13px}
  .gatecard input{width:100%;padding:12px 14px;border-radius:12px;border:1px solid var(--line);background:var(--bg);color:var(--txt);outline:none;margin-bottom:12px}
  .gatecard button{width:100%;padding:12px;border:0;border-radius:12px;cursor:pointer;font-weight:600;background:linear-gradient(135deg,var(--accent),var(--accent2));color:#fff}
  .gateerr{color:#ff6b6b;font-size:12.5px;height:16px;margin-top:8px}
  @media(max-width:820px){.panel{display:none}}
</style>
</head>
<body>
  <div class="app">
    <div class="chat">
      <div class="topbar">
        <div class="logo">🦁</div>
        <div><div class="ttl">SuperAI</div><div class="sub">有温度的 AI 生活 / 工作助手</div></div>
        <div class="spacer"></div>
        <div class="dot" title="在线"></div>
        <button class="iconbtn" id="theme" title="切换主题">🌙</button>
      </div>
      <div class="log" id="log"></div>
      <div class="composer"><div class="inwrap">
        <textarea id="in" rows="1" placeholder="跟 SuperAI 说点什么…(如:明天下午三点和老王开会 / 提醒我每天22点喝水)"></textarea>
        <button class="send" id="send" title="发送">➤</button>
      </div></div>
    </div>
    <div class="panel">
      <div class="phead"><b>✨ 概览</b><div class="spacer" style="flex:1"></div><button class="iconbtn" id="refresh" title="刷新概览" style="width:30px;height:30px;font-size:14px">↻</button></div>
      <div id="overview"><div class="empty">加载中…</div></div>
    </div>
  </div>
  <div id="toasts"></div>
  <div id="gate"><div class="gatecard">
    <div class="logo">🦁</div><h2>SuperAI</h2><p>请输入访问令牌</p>
    <input id="tok" type="password" placeholder="access token"/>
    <button id="enter">进入</button><div class="gateerr" id="gerr"></div>
  </div></div>
<script>
var TOKEN=localStorage.getItem('superai_token')||'';
var log=document.getElementById('log'),input=document.getElementById('in'),send=document.getElementById('send');
var gate=document.getElementById('gate'),tok=document.getElementById('tok'),gerr=document.getElementById('gerr');

/* theme */
function applyTheme(t){document.documentElement.setAttribute('data-theme',t);document.getElementById('theme').textContent=(t==='dark'?'🌙':'☀️');localStorage.setItem('superai_theme',t);}
applyTheme(localStorage.getItem('superai_theme')|| (matchMedia&&matchMedia('(prefers-color-scheme: light)').matches?'light':'dark'));
document.getElementById('theme').onclick=function(){applyTheme(document.documentElement.getAttribute('data-theme')==='dark'?'light':'dark');};

function hdr(){var h={'Content-Type':'application/json'};if(TOKEN)h['Authorization']='Bearer '+TOKEN;return h;}
function esc(s){return (s||'').replace(/[&<>]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;'}[c];});}
function el(cls,html){var d=document.createElement('div');d.className=cls;d.innerHTML=html;log.appendChild(d);log.scrollTop=log.scrollHeight;return d;}
function rowAI(html){return el('row ai','<div class="av ai">🦁</div><div><div class="bubble">'+html+'</div></div>');}
function rowMe(t){return el('row me','<div class="av me">我</div><div class="bubble">'+esc(t)+'</div>');}

function renderOverview(data){
  var box=document.getElementById('overview');var secs=(data&&data.sections)||[];
  if(!secs.length){box.innerHTML='<div class="empty">暂无</div>';return;}
  box.innerHTML=secs.map(function(s){
    var items=(s.items||[]).map(function(it){return '<div class="oitem">'+esc(it)+'</div>';}).join('');
    return '<div class="osec"><div class="oh">'+esc(s.icon||'•')+' '+esc(s.title||'')+'</div>'+items+'</div>';
  }).join('');
}
function loadOverview(){
  var box=document.getElementById('overview');box.innerHTML='<div class="empty"><span class="spin"></span>SuperAI 正在生成概览…</div>';
  fetch('/api/overview',{headers:hdr()}).then(function(r){if(r.status===401){showGate();throw 0;}return r.json();})
    .then(renderOverview).catch(function(){box.innerHTML='<div class="empty">概览暂不可用</div>';});
}
document.getElementById('refresh').onclick=loadOverview;

function sendMsg(){
  var text=input.value.trim();if(!text)return;
  rowMe(text);input.value='';autosize();input.disabled=send.disabled=true;
  var t=el('row ai','<div class="av ai">🦁</div><div class="bubble"><span class="typing"><i></i><i></i><i></i></span></div>');
  fetch('/api/chat',{method:'POST',headers:hdr(),body:JSON.stringify({message:text})})
    .then(function(r){if(r.status===401){showGate();throw 0;}return r.json();})
    .then(function(r){
      t.remove();
      if(r.error){rowAI('⚠️ '+esc(r.error));return;}
      var m=rowAI(esc(r.reply));var meta='';
      if(r.emotion)meta+='<span class="chip emo">'+(r.emoji||'')+' '+esc(r.emotion)+'</span>';
      if(r.tools&&r.tools.length)meta+='<span class="chip">🔧 '+r.tools.map(esc).join(', ')+'</span>';
      if(meta)m.querySelector('div').insertAdjacentHTML('beforeend','<div class="meta">'+meta+'</div>');
      loadOverview();
    })
    .catch(function(){t.remove();})
    .finally(function(){input.disabled=send.disabled=false;input.focus();});
}
function autosize(){input.style.height='auto';input.style.height=Math.min(input.scrollHeight,140)+'px';}
input.addEventListener('input',autosize);
input.addEventListener('keydown',function(e){if(e.key==='Enter'&&!e.shiftKey){e.preventDefault();sendMsg();}});
send.onclick=sendMsg;

function toast(text){var t=document.createElement('div');t.className='toast';t.textContent='🔔 '+text;document.getElementById('toasts').appendChild(t);setTimeout(function(){t.remove();},8000);}
function connectSSE(){var url='/api/events'+(TOKEN?('?token='+encodeURIComponent(TOKEN)):'');new EventSource(url).onmessage=function(ev){try{var d=JSON.parse(ev.data);if(d.type==='reminder'){toast(d.text);loadOverview();}}catch(e){}};}

function start(){gate.classList.remove('show');rowAI('你好,我是 SuperAI 🦁 把日程、待办、灵感和要记住的事都交给我吧。');loadOverview();connectSSE();input.focus();}
function showGate(){gate.classList.add('show');tok.focus();}
document.getElementById('enter').onclick=function(){
  var v=tok.value.trim();if(!v){gerr.textContent='请输入令牌';return;}
  TOKEN=v;localStorage.setItem('superai_token',v);gerr.textContent='验证中…';
  fetch('/api/overview',{headers:hdr()}).then(function(r){
    if(r.status===401){gerr.textContent='令牌无效';TOKEN='';localStorage.removeItem('superai_token');return;}
    gerr.textContent='';start();
  }).catch(function(){gerr.textContent='连接失败';});
};
tok.addEventListener('keydown',function(e){if(e.key==='Enter')document.getElementById('enter').click();});

fetch('/api/config').then(function(r){return r.json();}).then(function(c){
  if(c.auth&&!TOKEN){showGate();}
  else{fetch('/api/overview',{headers:hdr()}).then(function(r){if(r.status===401){showGate();}else{start();}});}
}).catch(function(){start();});
</script>
</body>
</html>`
