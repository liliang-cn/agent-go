import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { streamAgent, type StopReason, type StreamEvent } from "../lib/api";

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
  stop_hook:              "bg-sky-50    text-sky-700    border-sky-200",
  error_during_execution: "bg-rose-50   text-rose-700   border-rose-200",
  default:                "bg-slate-50  text-slate-600  border-slate-200",
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
      {reason ? reason.replace(/_/g, " ") : "pending"}
    </span>
  );
}

function eventTypeStyle(type: string): string {
  if (type === "workflow_complete") return "border-emerald-200 bg-emerald-50/60";
  if (type === "workflow_blocked")  return "border-rose-200    bg-rose-50/60";
  if (type === "workflow_error")    return "border-rose-200    bg-rose-50/60";
  if (type === "tool_call")         return "border-sky-200     bg-sky-50/60";
  if (type === "tool_result")       return "border-emerald-100 bg-emerald-50/30";
  if (type === "thinking")          return "border-slate-100   bg-slate-50/50";
  if (type === "partial")           return "border-indigo-100  bg-indigo-50/40";
  if (type === "analytics")         return "border-amber-100   bg-amber-50/40";
  if (type === "compact_boundary")  return "border-purple-200  bg-purple-50/60";
  if (type === "handoff")           return "border-violet-200  bg-violet-50/60";
  return "border-slate-100 bg-white";
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

export function Live() {
  const [input, setInput] = useState("");
  const [debug, setDebug] = useState(false);
  const [running, setRunning] = useState(false);
  const [timeline, setTimeline] = useState<TimelineEntry[]>([]);
  const [summary, setSummary] = useState<RunSummary>({
    estimatedCostUSD: 0,
    totalTokens: 0,
    toolCalls: 0,
    toolsUsed: [],
  });
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const idCounterRef = useRef(0);
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
    setTimeline([]);
    setSummary({
      estimatedCostUSD: 0,
      totalTokens: 0,
      toolCalls: 0,
      toolsUsed: [],
    });
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
    }
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
        <h2 className="text-2xl font-semibold tracking-tight text-slate-900">
          Live Run
        </h2>
        <p className="text-sm text-slate-600">
          Stream a task through the agent runtime and watch events,
          tool calls, cost, and stop reason in real time.
        </p>
      </header>

      <section className="rounded-2xl border border-slate-100 bg-white p-4 shadow-sm">
        <div className="flex flex-col gap-3">
          <textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="Goal — e.g. List the AgentGo workspace and pick three files."
            disabled={running}
            className="min-h-[80px] w-full resize-y rounded-xl border border-sky-100 bg-white px-4 py-2.5 text-sm text-slate-700 shadow-sm focus:border-sky-300 focus:outline-none focus:ring-2 focus:ring-sky-100 disabled:bg-slate-50"
            data-testid="live-input"
          />
          <div className="flex items-center justify-between gap-3">
            <label className="flex items-center gap-2 text-sm text-slate-600">
              <input
                type="checkbox"
                checked={debug}
                onChange={(e) => setDebug(e.target.checked)}
                disabled={running}
              />
              Debug
            </label>
            <div className="flex gap-2">
              {running ? (
                <button
                  type="button"
                  onClick={handleCancel}
                  className="dashboard-secondary-button text-sm"
                  data-testid="live-cancel"
                >
                  Cancel
                </button>
              ) : (
                <button
                  type="button"
                  onClick={handleStart}
                  disabled={!input.trim()}
                  className="dashboard-button px-6"
                  data-testid="live-start"
                >
                  Start
                </button>
              )}
            </div>
          </div>
        </div>
      </section>

      {error && (
        <div className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700">
          {error}
        </div>
      )}

      <section className="grid grid-cols-1 gap-4 lg:grid-cols-[1fr_320px]">
        {/* Event timeline */}
        <div
          ref={scrollRef}
          className="flex max-h-[70vh] flex-col gap-2 overflow-auto rounded-2xl border border-slate-100 bg-slate-50/40 p-4"
          data-testid="live-timeline"
        >
          {timeline.length === 0 && !running && (
            <div className="rounded-xl border border-dashed border-slate-200 bg-white p-6 text-center text-sm text-slate-400">
              No events yet — enter a goal and press Start.
            </div>
          )}
          {timeline.length === 0 && running && (
            <div className="rounded-xl border border-sky-100 bg-sky-50/60 p-4 text-sm text-sky-700">
              Waiting for the runtime…
            </div>
          )}
          {timeline.map((e) => (
            <article
              key={e.id}
              className={`rounded-xl border px-3 py-2 text-sm ${eventTypeStyle(e.type)}`}
            >
              <div className="flex items-baseline justify-between gap-3 text-xs text-slate-500">
                <div className="flex items-center gap-2">
                  <span className="font-mono font-semibold text-slate-700">
                    {e.type}
                  </span>
                  {e.round ? (
                    <span className="rounded-full bg-white px-1.5 py-0.5 font-medium text-slate-500">
                      r{e.round}
                    </span>
                  ) : null}
                  {e.toolName ? (
                    <span className="font-mono text-sky-700">
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
                  <time className="font-mono text-[10px] text-slate-400">
                    {e.timestamp.slice(11, 23)}
                  </time>
                ) : null}
              </div>
              {e.text && (
                <pre className="mt-1 whitespace-pre-wrap break-words font-sans text-slate-700">
                  {e.text}
                </pre>
              )}
              {e.toolArgs && Object.keys(e.toolArgs).length > 0 && (
                <pre className="mt-1 overflow-x-auto rounded-md bg-white/70 p-2 font-mono text-xs text-slate-600">
                  {safeStringify(e.toolArgs, 400)}
                </pre>
              )}
              {e.toolResult !== undefined && e.toolResult !== null && (
                <pre className="mt-1 overflow-x-auto rounded-md bg-white/70 p-2 font-mono text-xs text-slate-600">
                  {safeStringify(e.toolResult, 400)}
                </pre>
              )}
              {e.analyticsData && (
                <pre className="mt-1 overflow-x-auto rounded-md bg-white/70 p-2 font-mono text-xs text-slate-600">
                  {safeStringify(e.analyticsData, 400)}
                </pre>
              )}
            </article>
          ))}
        </div>

        {/* Side panel */}
        <aside className="flex flex-col gap-3">
          <div className="rounded-2xl border border-slate-100 bg-white p-4 shadow-sm">
            <h3 className="text-sm font-semibold uppercase tracking-wide text-slate-500">
              Run Summary
            </h3>
            <dl className="mt-3 space-y-3 text-sm">
              <div className="flex items-center justify-between">
                <dt className="text-slate-500">stop_reason</dt>
                <dd>
                  <StopReasonBadge reason={summary.stopReason} />
                </dd>
              </div>
              <div className="flex items-center justify-between">
                <dt className="text-slate-500">est. cost</dt>
                <dd className="font-mono text-slate-800">
                  ${summary.estimatedCostUSD.toFixed(6)}
                </dd>
              </div>
              <div className="flex items-center justify-between">
                <dt className="text-slate-500">tokens (turn)</dt>
                <dd className="font-mono text-slate-800">
                  {summary.totalTokens.toLocaleString()}
                </dd>
              </div>
              <div className="flex items-center justify-between">
                <dt className="text-slate-500">tool calls</dt>
                <dd className="font-mono text-slate-800">
                  {summary.toolCalls}
                </dd>
              </div>
              {summary.toolsUsed.length > 0 && (
                <div>
                  <dt className="text-slate-500">tools used</dt>
                  <dd className="mt-1 flex flex-wrap gap-1">
                    {summary.toolsUsed.map((t) => (
                      <span
                        key={t}
                        className="rounded-full bg-sky-50 px-2 py-0.5 font-mono text-[11px] text-sky-700"
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
            <div className="rounded-2xl border border-slate-100 bg-white p-4 shadow-sm">
              <h3 className="text-sm font-semibold uppercase tracking-wide text-slate-500">
                Final answer
              </h3>
              {parsedFinalJson ? (
                <pre className="mt-2 max-h-72 overflow-auto rounded-lg bg-slate-50 p-3 font-mono text-xs text-slate-800">
                  {JSON.stringify(parsedFinalJson, null, 2)}
                </pre>
              ) : (
                <p className="mt-2 whitespace-pre-wrap text-sm text-slate-700">
                  {summary.finalContent}
                </p>
              )}
            </div>
          )}

          <div className="rounded-2xl border border-slate-100 bg-white p-4 text-xs text-slate-500 shadow-sm">
            <p>
              Tip: this view streams from{" "}
              <code className="rounded bg-slate-100 px-1 py-0.5">
                /api/agent/stream
              </code>
              . Inspect saved runs at{" "}
              <Link to="/tasks" className="text-sky-600 underline">
                /tasks
              </Link>
              .
            </p>
          </div>
        </aside>
      </section>
    </div>
  );
}
