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
		default:
		}
	}
}

// ----------------------------------------------------------------------------
// web server
// ----------------------------------------------------------------------------

// runWeb serves the SuperAI web UI. If token != "", every /api/* call must
// present it (Authorization: Bearer <token>, X-Token header, or ?token= for SSE).
func runWeb(svc *agent.Service, db *store, h *hub, addr, token string) {
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

	// whether auth is required (UI shows a login gate accordingly)
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

	mux.HandleFunc("/api/state", auth(func(w http.ResponseWriter, r *http.Request) {
		db.mu.Lock()
		snapshot := map[string]any{
			"schedules": db.Schedules, "records": db.Records,
			"persons": db.Persons, "reminders": db.Reminders,
		}
		db.mu.Unlock()
		writeJSON(w, snapshot)
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

	authNote := "no token (open)"
	if token != "" {
		authNote = "token required"
	}
	fmt.Printf("\n🌐 SuperAI web UI: http://%s  (%s)\n", addr, authNote)
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
// embedded single-page UI (no build step; no backticks — Go raw string)
// ----------------------------------------------------------------------------

const indexHTML = `<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>SuperAI</title>
<style>
  :root{
    --bg:#0b0d12; --bg2:#0f121a; --panel:#12151e; --card:#171b26; --line:#232838;
    --txt:#e8ecf5; --mut:#8a93a8; --accent:#7c5cff; --accent2:#4da3ff;
    --me1:#7c5cff; --me2:#5a8bff; --ai:#1b2030; --ok:#34d399;
  }
  *{box-sizing:border-box}
  html,body{height:100%}
  body{margin:0;font:15px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",system-ui,sans-serif;
    color:var(--txt);background:
      radial-gradient(1100px 600px at 12% -10%,rgba(124,92,255,.16),transparent 60%),
      radial-gradient(900px 500px at 110% 10%,rgba(77,163,255,.12),transparent 55%),
      var(--bg);}
  .app{display:flex;height:100vh;overflow:hidden}

  /* left: chat */
  .chat{flex:1;display:flex;flex-direction:column;min-width:0}
  .topbar{display:flex;align-items:center;gap:12px;padding:16px 22px;border-bottom:1px solid var(--line);
    backdrop-filter:blur(8px)}
  .logo{width:38px;height:38px;border-radius:12px;display:grid;place-items:center;font-size:20px;
    background:linear-gradient(135deg,var(--accent),#4da3ff);box-shadow:0 6px 18px rgba(124,92,255,.35)}
  .ttl{font-weight:700;font-size:16px;letter-spacing:.2px}
  .sub{color:var(--mut);font-size:12.5px}
  .dot{width:8px;height:8px;border-radius:50%;background:var(--ok);box-shadow:0 0 0 3px rgba(52,211,153,.18);margin-left:auto}

  .log{flex:1;overflow:auto;padding:24px;display:flex;flex-direction:column;gap:16px}
  .row{display:flex;gap:10px;max-width:760px;width:100%;margin:0 auto}
  .row.me{flex-direction:row-reverse}
  .av{width:30px;height:30px;border-radius:9px;flex:none;display:grid;place-items:center;font-size:15px}
  .av.ai{background:linear-gradient(135deg,#2a2f45,#1a1e2e);border:1px solid var(--line)}
  .av.me{background:linear-gradient(135deg,var(--me1),var(--me2))}
  .bubble{padding:11px 15px;border-radius:16px;white-space:pre-wrap;word-break:break-word;max-width:78%}
  .me .bubble{background:linear-gradient(135deg,var(--me1),var(--me2));color:#fff;border-top-right-radius:5px}
  .ai .bubble{background:var(--ai);border:1px solid var(--line);border-top-left-radius:5px}
  .meta{display:flex;gap:6px;flex-wrap:wrap;margin-top:8px}
  .chip{font-size:11.5px;color:var(--mut);background:rgba(255,255,255,.04);border:1px solid var(--line);
    padding:2px 8px;border-radius:999px}
  .chip.emo{color:#ffd98a;border-color:#5a4a1f;background:rgba(255,200,80,.08)}
  .sys{align-self:center;color:var(--mut);font-size:13px}
  .typing{display:inline-flex;gap:4px;align-items:center}
  .typing i{width:6px;height:6px;border-radius:50%;background:var(--mut);animation:bp 1s infinite}
  .typing i:nth-child(2){animation-delay:.15s}.typing i:nth-child(3){animation-delay:.3s}
  @keyframes bp{0%,80%,100%{opacity:.3;transform:translateY(0)}40%{opacity:1;transform:translateY(-3px)}}

  .composer{padding:16px 22px;border-top:1px solid var(--line)}
  .inwrap{display:flex;gap:10px;align-items:flex-end;max-width:760px;margin:0 auto;
    background:var(--card);border:1px solid var(--line);border-radius:16px;padding:8px 8px 8px 16px}
  .inwrap:focus-within{border-color:var(--accent);box-shadow:0 0 0 3px rgba(124,92,255,.15)}
  textarea{flex:1;border:0;background:transparent;color:var(--txt);outline:none;resize:none;
    font:inherit;max-height:140px;padding:6px 0}
  .send{width:40px;height:40px;border:0;border-radius:12px;cursor:pointer;flex:none;
    background:linear-gradient(135deg,var(--accent),#4da3ff);color:#fff;font-size:17px;
    display:grid;place-items:center;transition:transform .1s}
  .send:active{transform:scale(.92)} .send:disabled{opacity:.45;cursor:default}

  /* right: panel */
  .panel{width:360px;border-left:1px solid var(--line);background:linear-gradient(180deg,var(--panel),var(--bg2));
    overflow:auto;padding:20px}
  .sec{margin-bottom:22px}
  .sec h3{display:flex;align-items:center;gap:8px;margin:0 0 10px;font-size:12px;color:var(--mut);
    text-transform:uppercase;letter-spacing:1px}
  .sec h3 .n{margin-left:auto;background:rgba(124,92,255,.16);color:#b7a6ff;border-radius:999px;
    padding:1px 8px;font-size:11px;letter-spacing:0}
  .card2{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:11px 13px;margin-bottom:9px}
  .card2 b{font-weight:600}
  .card2 .s{color:var(--mut);font-size:12.5px;margin-top:3px}
  .empty{color:var(--mut);font-size:13px;padding:6px 2px}

  /* toast */
  #toasts{position:fixed;top:18px;right:18px;display:flex;flex-direction:column;gap:10px;z-index:50}
  .toast{background:linear-gradient(135deg,#2a2410,#1c1809);border:1px solid #6b5320;color:#ffd98a;
    padding:12px 16px;border-radius:12px;box-shadow:0 12px 30px rgba(0,0,0,.4);animation:slin .3s ease}
  @keyframes slin{from{opacity:0;transform:translateX(20px)}to{opacity:1;transform:none}}

  /* login gate */
  #gate{position:fixed;inset:0;display:none;place-items:center;background:rgba(6,8,12,.72);backdrop-filter:blur(6px);z-index:100}
  #gate.show{display:grid}
  .gatecard{width:340px;background:var(--card);border:1px solid var(--line);border-radius:18px;padding:28px;text-align:center;
    box-shadow:0 20px 60px rgba(0,0,0,.5)}
  .gatecard .logo{margin:0 auto 14px;width:52px;height:52px;font-size:26px}
  .gatecard h2{margin:0 0 6px;font-size:18px} .gatecard p{margin:0 0 18px;color:var(--mut);font-size:13px}
  .gatecard input{width:100%;padding:12px 14px;border-radius:12px;border:1px solid var(--line);
    background:#0c0e14;color:var(--txt);outline:none;margin-bottom:12px}
  .gatecard input:focus{border-color:var(--accent)}
  .gatecard button{width:100%;padding:12px;border:0;border-radius:12px;cursor:pointer;font-weight:600;
    background:linear-gradient(135deg,var(--accent),#4da3ff);color:#fff}
  .gateerr{color:#ff8a8a;font-size:12.5px;height:16px;margin-top:8px}

  @media(max-width:820px){.panel{display:none}}
</style>
</head>
<body>
  <div class="app">
    <div class="chat">
      <div class="topbar">
        <div class="logo">🦁</div>
        <div><div class="ttl">SuperAI</div><div class="sub">有温度的 AI 生活 / 工作助手</div></div>
        <div class="dot" title="在线"></div>
      </div>
      <div class="log" id="log"></div>
      <div class="composer">
        <div class="inwrap">
          <textarea id="in" rows="1" placeholder="跟 SuperAI 说点什么…（如:明天下午三点和老王开会 / 提醒我每天22点喝水）"></textarea>
          <button class="send" id="send" title="发送">➤</button>
        </div>
      </div>
    </div>
    <div class="panel">
      <div class="sec"><h3>📅 日程 <span class="n" id="n-s">0</span></h3><div id="schedules"></div></div>
      <div class="sec"><h3>📝 记录 <span class="n" id="n-r">0</span></h3><div id="records"></div></div>
      <div class="sec"><h3>👤 人物 <span class="n" id="n-p">0</span></h3><div id="persons"></div></div>
      <div class="sec"><h3>🔔 提醒 <span class="n" id="n-m">0</span></h3><div id="reminders"></div></div>
    </div>
  </div>
  <div id="toasts"></div>
  <div id="gate"><div class="gatecard">
    <div class="logo">🦁</div><h2>SuperAI</h2><p>请输入访问令牌</p>
    <input id="tok" type="password" placeholder="access token" autocomplete="current-password"/>
    <button id="enter">进入</button>
    <div class="gateerr" id="gerr"></div>
  </div></div>
<script>
var TOKEN = localStorage.getItem('superai_token') || '';
var log=document.getElementById('log'), input=document.getElementById('in'), send=document.getElementById('send');
var gate=document.getElementById('gate'), tok=document.getElementById('tok'), gerr=document.getElementById('gerr');

function hdr(){var h={'Content-Type':'application/json'}; if(TOKEN) h['Authorization']='Bearer '+TOKEN; return h;}
function esc(s){return (s||'').replace(/[&<>]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;'}[c];});}
function el(cls,html){var d=document.createElement('div'); d.className=cls; d.innerHTML=html; log.appendChild(d); log.scrollTop=log.scrollHeight; return d;}
function rowAI(html){return el('row ai','<div class="av ai">🦁</div><div><div class="bubble">'+html+'</div></div>');}
function rowMe(text){return el('row me','<div class="av me">我</div><div class="bubble">'+esc(text)+'</div>');}

function card(title,sub,tag){return '<div class="card2"><b>'+esc(title)+'</b>'+(tag?' <span class="s" style="display:inline">·'+esc(tag)+'</span>':'')+(sub?'<div class="s">'+esc(sub)+'</div>':'')+'</div>';}
function fill(id,nid,items,render){
  var box=document.getElementById(id); var arr=items||[];
  document.getElementById(nid).textContent=arr.length;
  box.innerHTML=arr.length?arr.map(render).join(''):'<div class="empty">暂无</div>';
}
function refresh(){
  fetch('/api/state',{headers:hdr()}).then(function(r){ if(r.status===401){showGate();throw 0;} return r.json();}).then(function(s){
    fill('schedules','n-s',s.schedules,function(r){return card(r.title,(r.start_at||'')+' '+(r.location||''));});
    fill('records','n-r',s.records,function(r){return card(r.title||r.type,r.body,r.type);});
    fill('persons','n-p',Object.values(s.persons||{}),function(p){return card(p.name,p.note||'',p.relation||'');});
    fill('reminders','n-m',s.reminders,function(r){return card(r.title,(r.remind_at||'')+' · '+(r.recurrence||''));});
  }).catch(function(){});
}
function sendMsg(){
  var text=input.value.trim(); if(!text) return;
  rowMe(text); input.value=''; autosize(); input.disabled=send.disabled=true;
  var t=el('row ai','<div class="av ai">🦁</div><div class="bubble"><span class="typing"><i></i><i></i><i></i></span></div>');
  fetch('/api/chat',{method:'POST',headers:hdr(),body:JSON.stringify({message:text})})
    .then(function(r){ if(r.status===401){showGate();throw 0;} return r.json();})
    .then(function(r){
      t.remove();
      if(r.error){ rowAI('⚠️ '+esc(r.error)); return; }
      var m=rowAI(esc(r.reply));
      var meta='';
      if(r.emotion) meta+='<span class="chip emo">'+(r.emoji||'')+' '+esc(r.emotion)+'</span>';
      if(r.tools&&r.tools.length) meta+='<span class="chip">🔧 '+r.tools.map(esc).join(', ')+'</span>';
      if(meta) m.querySelector('div').insertAdjacentHTML('beforeend','<div class="meta">'+meta+'</div>');
      refresh();
    })
    .catch(function(){ t.remove(); })
    .finally(function(){ input.disabled=send.disabled=false; input.focus(); });
}
function autosize(){ input.style.height='auto'; input.style.height=Math.min(input.scrollHeight,140)+'px'; }
input.addEventListener('input',autosize);
input.addEventListener('keydown',function(e){ if(e.key==='Enter'&&!e.shiftKey){e.preventDefault();sendMsg();} });
send.onclick=sendMsg;

function toast(text){ var t=document.createElement('div'); t.className='toast'; t.textContent='🔔 '+text;
  document.getElementById('toasts').appendChild(t); setTimeout(function(){t.remove();},8000); }
function connectSSE(){
  var url='/api/events'+(TOKEN?('?token='+encodeURIComponent(TOKEN)):'');
  var es=new EventSource(url);
  es.onmessage=function(ev){ try{var d=JSON.parse(ev.data); if(d.type==='reminder'){toast(d.text);refresh();}}catch(e){} };
}

function start(){ gate.classList.remove('show'); rowAI('你好,我是 SuperAI 🦁 把日程、待办、灵感和要记住的事都交给我吧。'); refresh(); connectSSE(); input.focus(); }
function showGate(){ gate.classList.add('show'); tok.focus(); }
document.getElementById('enter').onclick=function(){
  var v=tok.value.trim(); if(!v){gerr.textContent='请输入令牌';return;}
  TOKEN=v; localStorage.setItem('superai_token',v); gerr.textContent='验证中…';
  fetch('/api/state',{headers:hdr()}).then(function(r){
    if(r.status===401){ gerr.textContent='令牌无效'; TOKEN=''; localStorage.removeItem('superai_token'); return; }
    gerr.textContent=''; start();
  }).catch(function(){ gerr.textContent='连接失败'; });
};
tok.addEventListener('keydown',function(e){ if(e.key==='Enter') document.getElementById('enter').click(); });

// boot: check whether auth is required
fetch('/api/config').then(function(r){return r.json();}).then(function(c){
  if(c.auth && !TOKEN){ showGate(); }
  else { fetch('/api/state',{headers:hdr()}).then(function(r){ if(r.status===401){showGate();} else {start();} }); }
}).catch(function(){ start(); });
</script>
</body>
</html>`
