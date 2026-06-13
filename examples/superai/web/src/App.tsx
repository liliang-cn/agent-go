import { useChat } from "@ai-sdk/react";
import { DefaultChatTransport } from "ai";
import { useEffect, useMemo, useRef, useState } from "react";

// ---- session + token persistence -------------------------------------------
function uuid() {
  return crypto.randomUUID
    ? crypto.randomUUID()
    : "s" + Date.now() + Math.random().toString(16).slice(2);
}
function getToken() {
  return localStorage.getItem("superai_token") || "";
}
function getSession() {
  let s = localStorage.getItem("superai_session");
  if (!s) {
    s = uuid();
    localStorage.setItem("superai_session", s);
  }
  return s;
}

type Section = { icon?: string; title?: string; items?: string[] };

export default function App() {
  const [token, setToken] = useState(getToken());
  const [session, setSession] = useState(getSession());
  const [needAuth, setNeedAuth] = useState(false);
  const [gateOpen, setGateOpen] = useState(false);
  const [tokenInput, setTokenInput] = useState("");
  const [gateErr, setGateErr] = useState("");
  const [theme, setTheme] = useState(
    localStorage.getItem("superai_theme") ||
      (matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark")
  );
  const [overview, setOverview] = useState<Section[]>([]);
  const [ovLoading, setOvLoading] = useState(false);
  const [input, setInput] = useState("");
  const logRef = useRef<HTMLDivElement>(null);

  // transport reads the current token/session each request
  const transport = useMemo(
    () =>
      new DefaultChatTransport({
        api: "/api/chat",
        headers: () => {
          const h: Record<string, string> = { "X-Session": getSession() };
          const t = getToken();
          if (t) h["Authorization"] = "Bearer " + t;
          return h;
        },
      }),
    []
  );

  const { messages, sendMessage, status, setMessages } = useChat({ transport });

  useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
    localStorage.setItem("superai_theme", theme);
  }, [theme]);

  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight });
  }, [messages, status]);

  // boot: check auth, then load overview
  useEffect(() => {
    fetch("/api/config")
      .then((r) => r.json())
      .then((c) => {
        setNeedAuth(!!c.auth);
        if (c.auth && !getToken()) setGateOpen(true);
        else loadOverview(false);
      })
      .catch(() => loadOverview(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function authHeaders(): Record<string, string> {
    const t = getToken();
    return t ? { Authorization: "Bearer " + t } : {};
  }

  function loadOverview(force: boolean) {
    setOvLoading(true);
    fetch("/api/overview" + (force ? "?force=1" : ""), { headers: authHeaders() })
      .then((r) => {
        if (r.status === 401) {
          setGateOpen(true);
          throw new Error("401");
        }
        return r.json();
      })
      .then((d) => setOverview(d.sections || []))
      .catch(() => {})
      .finally(() => setOvLoading(false));
  }

  function submit() {
    const text = input.trim();
    if (!text || status === "streaming" || status === "submitted") return;
    setInput("");
    sendMessage({ text });
  }

  function newSession() {
    const s = uuid();
    localStorage.setItem("superai_session", s);
    setSession(s);
    setMessages([]);
  }

  function enterToken() {
    const v = tokenInput.trim();
    if (!v) {
      setGateErr("请输入令牌");
      return;
    }
    localStorage.setItem("superai_token", v);
    setToken(v);
    setGateErr("验证中…");
    fetch("/api/overview", { headers: { Authorization: "Bearer " + v } }).then((r) => {
      if (r.status === 401) {
        setGateErr("令牌无效");
        localStorage.removeItem("superai_token");
        setToken("");
        return;
      }
      setGateErr("");
      setGateOpen(false);
      loadOverview(false);
    });
  }

  const busy = status === "streaming" || status === "submitted";

  return (
    <div className="app">
      <div className="chat">
        <div className="topbar">
          <div className="logo">🦁</div>
          <div>
            <div className="ttl">SuperAI</div>
            <div className="sub">有温度的 AI 生活 / 工作助手 · AI SDK</div>
          </div>
          <div className="spacer" />
          <div className="dot" title="在线" />
          <button className="iconbtn" title="新开会话" onClick={newSession}>
            ＋
          </button>
          <button
            className="iconbtn"
            title="切换主题"
            onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          >
            {theme === "dark" ? "🌙" : "☀️"}
          </button>
        </div>

        <div className="log" ref={logRef}>
          {messages.length === 0 && (
            <div className="row ai">
              <div className="av ai">🦁</div>
              <div className="aiwrap">
                <div className="bubble">
                  你好,我是 SuperAI 🦁 把日程、待办、灵感和要记住的事都交给我吧。
                </div>
              </div>
            </div>
          )}
          {messages.map((m) => {
            const text = m.parts
              .filter((p: any) => p.type === "text")
              .map((p: any) => p.text)
              .join("");
            if (m.role === "user")
              return (
                <div className="row me" key={m.id}>
                  <div className="av me">我</div>
                  <div className="bubble">{text}</div>
                </div>
              );
            return (
              <div className="row ai" key={m.id}>
                <div className="av ai">🦁</div>
                <div className="aiwrap">
                  <div className="bubble">{text || "…"}</div>
                </div>
              </div>
            );
          })}
          {busy && messages[messages.length - 1]?.role === "user" && (
            <div className="row ai">
              <div className="av ai">🦁</div>
              <div className="aiwrap">
                <div className="bubble">
                  <span className="typing">
                    <i />
                    <i />
                    <i />
                  </span>
                </div>
              </div>
            </div>
          )}
        </div>

        <div className="composer">
          <div className="inwrap">
            <textarea
              rows={1}
              value={input}
              placeholder="跟 SuperAI 说点什么…（如：明天下午三点和老王开会 / 搜一下 SpaceX 新闻）"
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  submit();
                }
              }}
            />
            <button className="send" title="发送" onClick={submit} disabled={busy}>
              ➤
            </button>
          </div>
        </div>
      </div>

      <div className="panel">
        <div className="phead">
          <b>✨ 概览</b>
          <div className="spacer" />
          <button
            className="iconbtn small"
            title="刷新概览"
            onClick={() => loadOverview(true)}
          >
            ↻
          </button>
        </div>
        {ovLoading ? (
          <div className="empty">
            <span className="spin" /> 生成概览…
          </div>
        ) : overview.length === 0 ? (
          <div className="empty">暂无</div>
        ) : (
          overview.map((s, i) => (
            <div className="osec" key={i}>
              <div className="oh">
                {s.icon || "•"} {s.title}
              </div>
              {(s.items || []).map((it, j) => (
                <div className="oitem" key={j}>
                  {it}
                </div>
              ))}
            </div>
          ))
        )}
      </div>

      {gateOpen && (
        <div className="gate show">
          <div className="gatecard">
            <div className="logo">🦁</div>
            <h2>SuperAI</h2>
            <p>请输入访问令牌</p>
            <input
              type="password"
              value={tokenInput}
              placeholder="access token"
              onChange={(e) => setTokenInput(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && enterToken()}
            />
            <button onClick={enterToken}>进入</button>
            <div className="gateerr">{gateErr}</div>
          </div>
        </div>
      )}
    </div>
  );
}
