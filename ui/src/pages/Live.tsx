import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { streamAgent, type StopReason, type StreamEvent } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";
import { Markdown } from "@/components/Markdown";

// Timeline entry — a normalized view of one streamed event.
type TimelineEntry = {
  id: number;
  type: string;
  round?: number;
  text?: string;
  toolName?: string;
  toolArgs?: Record<string, unknown>;
  toolResult?: unknown;
  analyticsName?: string;
  analyticsData?: Record<string, unknown>;
  timestamp?: string;
};

type RunSummary = {
  stopReason?: StopReason;
  finalContent?: string;
  estimatedCostUSD: number;
  totalTokens: number;
  toolCalls: number;
  toolsUsed: string[];
  taskId?: string;
};

const stopReasonStyle: Record<StopReason | "default", string> = {
  end_turn:               "bg-emerald-50 text-emerald-700 border-emerald-200",
  max_turns:              "bg-amber-50  text-amber-700  border-amber-200",
  max_budget_usd:         "bg-rose-50   text-rose-700   border-rose-200",
  refusal:                "bg-violet-50 text-violet-700 border-violet-200",
  max_tokens:             "bg-amber-50  text-amber-700  border-amber-200",
  lint_exhausted:         "bg-orange-50 text-orange-700 border-orange-200",
  stop_hook:              "bg-blue-50    text-blue-700    border-blue-200",
  error_during_execution: "bg-rose-50   text-rose-700   border-rose-200",
  default:                "bg-muted  text-muted-foreground  border-border",
};

function StopReasonBadge({ reason }: { reason?: StopReason }) {
  const cls = reason
    ? stopReasonStyle[reason] ?? stopReasonStyle.default
    : stopReasonStyle.default;
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-medium ${cls}`}
      data-testid="stop-reason-badge"
    >
      {reason ? reason.replace(/_/g, " ") : "等待中"}
    </span>
  );
}

function eventTypeStyle(type: string): string {
  if (type === "workflow_complete") return "border-emerald-200 bg-emerald-50/60";
  if (type === "workflow_blocked")  return "border-rose-200    bg-rose-50/60";
  if (type === "workflow_error")    return "border-rose-200    bg-rose-50/60";
  if (type === "tool_call")         return "border-blue-200     bg-blue-50/60";
  if (type === "tool_result")       return "border-emerald-100 bg-emerald-50/30";
  if (type === "thinking")          return "border-border   bg-muted/50";
  if (type === "partial")           return "border-indigo-100  bg-indigo-50/40";
  if (type === "analytics")         return "border-amber-100   bg-amber-50/40";
  if (type === "compact_boundary")  return "border-purple-200  bg-purple-50/60";
  if (type === "handoff")           return "border-violet-200  bg-violet-50/60";
  return "border-border bg-card";
}

function safeStringify(value: unknown, max = 240): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") {
    return value.length > max ? value.slice(0, max) + "…" : value;
  }
  try {
    const s = JSON.stringify(value);
    return s.length > max ? s.slice(0, max) + "…" : s;
  } catch {
    return String(value);
  }
}

function detectJSON(s: string): unknown | undefined {
  const trimmed = s.trim();
  if (!trimmed) return undefined;
  if (trimmed[0] !== "{" && trimmed[0] !== "[") return undefined;
  try {
    return JSON.parse(trimmed);
  } catch {
    return undefined;
  }
}

// A finished run kept in session-local history so the user can flip back
// to it without re-running.
type HistoryRun = {
  id: number;
  goal: string;
  timeline: TimelineEntry[];
  summary: RunSummary;
  at: string;
};

const emptySummary = (): RunSummary => ({
  estimatedCostUSD: 0,
  totalTokens: 0,
  toolCalls: 0,
  toolsUsed: [],
});

export function Live() {
  const [input, setInput] = useState("");
  const [debug, setDebug] = useState(false);
  const [running, setRunning] = useState(false);
  const [timeline, setTimeline] = useState<TimelineEntry[]>([]);
  const [summary, setSummary] = useState<RunSummary>(emptySummary);
  const [error, setError] = useState<string | null>(null);
  const [history, setHistory] = useState<HistoryRun[]>([]);
  const [viewingId, setViewingId] = useState<number | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const idCounterRef = useRef(0);
  const runSeqRef = useRef(0);
  const currentGoalRef = useRef("");
  const partialBufferRef = useRef<string>("");
  const partialEntryIdRef = useRef<number | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);

  // Auto-scroll timeline as new events arrive.
  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [timeline]);

  const appendEntry = (entry: Omit<TimelineEntry, "id">): number => {
    idCounterRef.current += 1;
    const id = idCounterRef.current;
    setTimeline((prev) => [...prev, { ...entry, id }]);
    return id;
  };

  const updateEntry = (id: number, patch: Partial<TimelineEntry>) => {
    setTimeline((prev) =>
      prev.map((e) => (e.id === id ? { ...e, ...patch } : e)),
    );
  };

  const handleStart = async () => {
    const goal = input.trim();
    if (!goal) return;
    setError(null);
    setRunning(true);
    setViewingId(null);
    setTimeline([]);
    setSummary(emptySummary());
    currentGoalRef.current = goal;
    partialBufferRef.current = "";
    partialEntryIdRef.current = null;
    idCounterRef.current = 0;

    const ctl = new AbortController();
    abortRef.current = ctl;

    try {
      for await (const evt of streamAgent(goal, { debug, signal: ctl.signal })) {
        handleEvent(evt);
      }
    } catch (e: unknown) {
      if (!ctl.signal.aborted) {
        setError(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setRunning(false);
      abortRef.current = null;
      // Snapshot the completed run into history. Read the latest state
      // via the setter callbacks so we capture the final values.
      runSeqRef.current += 1;
      const runId = runSeqRef.current;
      setTimeline((tl) => {
        setSummary((sm) => {
          setHistory((h) =>
            [
              {
                id: runId,
                goal,
                timeline: tl,
                summary: sm,
                at: new Date().toISOString(),
              },
              ...h,
            ].slice(0, 20),
          );
          return sm;
        });
        return tl;
      });
    }
  };

  const viewHistory = (run: HistoryRun) => {
    if (running) return;
    setViewingId(run.id);
    setTimeline(run.timeline);
    setSummary(run.summary);
  };

  const startNew = () => {
    if (running) return;
    setViewingId(null);
    setTimeline([]);
    setSummary(emptySummary());
    setError(null);
  };

  const handleCancel = () => {
    abortRef.current?.abort();
  };

  const handleEvent = (evt: StreamEvent) => {
    // Partial text deltas get coalesced into a single entry until the
    // next non-partial event arrives.
    if (evt.type === "partial") {
      partialBufferRef.current += evt.content ?? "";
      const text = partialBufferRef.current;
      if (partialEntryIdRef.current === null) {
        partialEntryIdRef.current = appendEntry({
          type: "partial",
          round: evt.round,
          text,
          timestamp: evt.timestamp,
        });
      } else {
        updateEntry(partialEntryIdRef.current, { text });
      }
      return;
    }
    // Reset the partial buffer when any non-partial event arrives.
    partialBufferRef.current = "";
    partialEntryIdRef.current = null;

    if (evt.type === "tool_call") {
      appendEntry({
        type: "tool_call",
        round: evt.round,
        toolName: evt.tool_name,
        toolArgs: evt.tool_args,
        timestamp: evt.timestamp,
      });
      setSummary((s) => ({
        ...s,
        toolCalls: s.toolCalls + 1,
        toolsUsed:
          evt.tool_name && !s.toolsUsed.includes(evt.tool_name)
            ? [...s.toolsUsed, evt.tool_name]
            : s.toolsUsed,
      }));
      return;
    }

    if (evt.type === "tool_result") {
      appendEntry({
        type: "tool_result",
        round: evt.round,
        toolName: evt.tool_name,
        toolResult: evt.tool_result,
        text: evt.content,
        timestamp: evt.timestamp,
      });
      return;
    }

    if (evt.type === "analytics" && evt.analytics) {
      // Pull cost / tokens out of latency analytics for the side panel.
      const { name, data } = evt.analytics;
      if (
        name === "tengu_llm_latency" &&
        typeof data.tokens === "number"
      ) {
        setSummary((s) => ({
          ...s,
          totalTokens: Math.max(s.totalTokens, data.tokens as number),
        }));
      }
      appendEntry({
        type: "analytics",
        round: evt.round,
        analyticsName: name,
        analyticsData: data,
        timestamp: evt.timestamp,
      });
      return;
    }

    if (
      evt.type === "workflow_complete" ||
      evt.type === "workflow_blocked"
    ) {
      setSummary((s) => ({
        ...s,
        stopReason: evt.stop_reason,
        finalContent: evt.content,
        estimatedCostUSD: evt.estimated_cost_usd ?? s.estimatedCostUSD,
      }));
    }
    if (evt.estimated_cost_usd && evt.estimated_cost_usd > 0) {
      setSummary((s) => ({ ...s, estimatedCostUSD: evt.estimated_cost_usd! }));
    }

    appendEntry({
      type: evt.type,
      round: evt.round,
      text: evt.content,
      timestamp: evt.timestamp,
    });
  };

  const parsedFinalJson = useMemo(() => {
    if (!summary.finalContent) return undefined;
    return detectJSON(summary.finalContent);
  }, [summary.finalContent]);

  return (
    <div className="flex flex-col gap-4">
      <header className="flex flex-col gap-1">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          实时运行
        </h2>
        <p className="text-sm text-muted-foreground">
          把任务交给智能体运行时流式执行,实时观察事件、工具调用、花费和停止原因。
        </p>
      </header>

      <Card className="p-4">
        <div className="flex flex-col gap-3">
          <Textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="目标 —— 例如:列出 AgentGo 工作区并挑三个文件。"
            disabled={running}
            className="min-h-[80px] resize-y"
            data-testid="live-input"
          />
          <div className="flex items-center justify-between gap-3">
            <label className="flex items-center gap-2 text-sm text-muted-foreground">
              <input
                type="checkbox"
                checked={debug}
                onChange={(e) => setDebug(e.target.checked)}
                disabled={running}
              />
              调试
            </label>
            <div className="flex gap-2">
              {running ? (
                <Button variant="outline" onClick={handleCancel} data-testid="live-cancel">
                  取消
                </Button>
              ) : (
                <Button
                  onClick={handleStart}
                  disabled={!input.trim()}
                  className="px-6"
                  data-testid="live-start"
                >
                  开始
                </Button>
              )}
            </div>
          </div>
        </div>
      </Card>

      {error && (
        <div className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700">
          {error}
        </div>
      )}

      {viewingId !== null && (
        <div className="flex items-center justify-between rounded-xl border border-amber-200 bg-amber-50/70 px-4 py-2 text-sm text-amber-800">
          <span>正在查看历史运行(只读)。</span>
          <button
            type="button"
            onClick={startNew}
            className="rounded-lg border border-amber-300 bg-card px-3 py-1 text-xs font-medium text-amber-700 hover:bg-amber-50"
          >
            新运行
          </button>
        </div>
      )}

      <section className="grid grid-cols-1 gap-4 lg:grid-cols-[200px_1fr_320px]">
        {/* History rail */}
        <aside
          className="flex max-h-[70vh] flex-col gap-1.5 overflow-auto rounded-[10px] border border-border bg-card p-3"
          data-testid="live-history"
        >
          <div className="px-1 pb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            历史
          </div>
          {history.length === 0 && (
            <p className="px-1 text-xs text-muted-foreground">
              已完成的运行会显示在这里。
            </p>
          )}
          {history.map((run) => (
            <button
              key={run.id}
              type="button"
              onClick={() => viewHistory(run)}
              disabled={running}
              className={`flex flex-col gap-1 rounded-lg border px-2.5 py-2 text-left text-xs transition disabled:opacity-50 ${
                viewingId === run.id
                  ? "border-primary bg-accent"
                  : "border-border hover:bg-muted"
              }`}
            >
              <span className="line-clamp-2 text-foreground">{run.goal}</span>
              <span className="flex items-center justify-between font-mono text-[10px] text-muted-foreground">
                <span>{run.summary.stopReason ?? "—"}</span>
                <span>${run.summary.estimatedCostUSD.toFixed(4)}</span>
              </span>
            </button>
          ))}
        </aside>

        {/* Event timeline */}
        <div
          ref={scrollRef}
          className="flex max-h-[70vh] flex-col gap-2 overflow-auto rounded-[10px] border border-border bg-muted/40 p-4"
          data-testid="live-timeline"
        >
          {timeline.length === 0 && !running && (
            <div className="rounded-xl border border-dashed border-border bg-card p-6 text-center text-sm text-muted-foreground">
              暂无事件 —— 输入目标后点「开始」。
            </div>
          )}
          {timeline.length === 0 && running && (
            <div className="rounded-xl border border-border bg-muted p-4 text-sm text-muted-foreground">
              等待运行时…
            </div>
          )}
          {timeline.map((e) => (
            <article
              key={e.id}
              className={`rounded-xl border px-3 py-2 text-sm ${eventTypeStyle(e.type)}`}
            >
              <div className="flex items-baseline justify-between gap-3 text-xs text-muted-foreground">
                <div className="flex items-center gap-2">
                  <span className="font-mono font-semibold text-foreground">
                    {e.type}
                  </span>
                  {e.round ? (
                    <span className="rounded-full bg-card px-1.5 py-0.5 font-medium text-muted-foreground">
                      r{e.round}
                    </span>
                  ) : null}
                  {e.toolName ? (
                    <span className="font-mono text-foreground">
                      {e.toolName}
                    </span>
                  ) : null}
                  {e.analyticsName ? (
                    <span className="font-mono text-amber-700">
                      {e.analyticsName}
                    </span>
                  ) : null}
                </div>
                {e.timestamp ? (
                  <time className="font-mono text-[10px] text-muted-foreground">
                    {e.timestamp.slice(11, 23)}
                  </time>
                ) : null}
              </div>
              {e.text && (
                <div className="mt-1">
                  <Markdown>{e.text}</Markdown>
                </div>
              )}
              {e.toolArgs && Object.keys(e.toolArgs).length > 0 && (
                <pre className="mt-1 overflow-x-auto rounded-md bg-background/70 p-2 font-mono text-xs text-muted-foreground">
                  {safeStringify(e.toolArgs, 400)}
                </pre>
              )}
              {e.toolResult !== undefined && e.toolResult !== null && (
                <pre className="mt-1 overflow-x-auto rounded-md bg-background/70 p-2 font-mono text-xs text-muted-foreground">
                  {safeStringify(e.toolResult, 400)}
                </pre>
              )}
              {e.analyticsData && (
                <pre className="mt-1 overflow-x-auto rounded-md bg-background/70 p-2 font-mono text-xs text-muted-foreground">
                  {safeStringify(e.analyticsData, 400)}
                </pre>
              )}
            </article>
          ))}
        </div>

        {/* Side panel */}
        <aside className="flex flex-col gap-3">
          <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
            <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
              运行摘要
            </h3>
            <dl className="mt-3 space-y-3 text-sm">
              <div className="flex items-center justify-between">
                <dt className="text-muted-foreground">停止原因</dt>
                <dd>
                  <StopReasonBadge reason={summary.stopReason} />
                </dd>
              </div>
              <div className="flex items-center justify-between">
                <dt className="text-muted-foreground">预估花费</dt>
                <dd className="font-mono text-foreground">
                  ${summary.estimatedCostUSD.toFixed(6)}
                </dd>
              </div>
              <div className="flex items-center justify-between">
                <dt className="text-muted-foreground">tokens(本轮)</dt>
                <dd className="font-mono text-foreground">
                  {summary.totalTokens.toLocaleString()}
                </dd>
              </div>
              <div className="flex items-center justify-between">
                <dt className="text-muted-foreground">工具调用</dt>
                <dd className="font-mono text-foreground">
                  {summary.toolCalls}
                </dd>
              </div>
              {summary.toolsUsed.length > 0 && (
                <div>
                  <dt className="text-muted-foreground">用到的工具</dt>
                  <dd className="mt-1 flex flex-wrap gap-1">
                    {summary.toolsUsed.map((t) => (
                      <span
                        key={t}
                        className="rounded-full bg-secondary px-2 py-0.5 font-mono text-[11px] text-secondary-foreground"
                      >
                        {t}
                      </span>
                    ))}
                  </dd>
                </div>
              )}
            </dl>
          </div>

          {summary.finalContent && (
            <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
              <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                最终答案
              </h3>
              {parsedFinalJson ? (
                <pre className="mt-2 max-h-72 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs text-foreground">
                  {JSON.stringify(parsedFinalJson, null, 2)}
                </pre>
              ) : (
                <div className="mt-2">
                  <Markdown>{summary.finalContent}</Markdown>
                </div>
              )}
            </div>
          )}

          <div className="rounded-[10px] border border-border bg-card p-4 text-xs text-muted-foreground shadow-sm">
            <p>
              提示:本视图流式来自{" "}
              <code className="rounded bg-muted px-1 py-0.5">
                /api/agent/stream
              </code>
              。已保存的运行可在{" "}
              <Link to="/tasks" className="text-foreground underline">
                /tasks
              </Link>
              {" "}查看。
            </p>
          </div>
        </aside>
      </section>
    </div>
  );
}
