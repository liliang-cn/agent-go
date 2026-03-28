import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { AgentModel, Team, TeamTask } from "../lib/api";

type ViewMode = "normal" | "debug";
type ChatStatus = "idle" | "sending";

type UIMessagePart = {
  type: string;
  text?: string;
  data?: Record<string, unknown>;
};

type UIMessage = {
  id: string;
  role: "user" | "assistant" | "system";
  metadata?: Record<string, unknown>;
  parts?: UIMessagePart[];
};

type MultiAgentChatPanelProps = {
  team: Team;
  leadAgent: AgentModel;
};

function stringify(value: unknown) {
  if (value == null) return "";
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function getTextParts(message: UIMessage) {
  return (message.parts ?? []).filter(
    (part) => part.type === "text" && typeof part.text === "string",
  );
}

function extractMentionedAgents(text: string, allowedNames: string[]) {
  const allowed = new Map(
    allowedNames.map((name) => [name.toLowerCase(), name]),
  );
  const matches = text.matchAll(/(?:^|\s)@([A-Za-z0-9_-]+)/g);
  const seen = new Set<string>();
  const names: string[] = [];
  for (const match of matches) {
    const raw = (match[1] ?? "").trim();
    if (!raw) continue;
    const normalized = raw.toLowerCase();
    const canonical = allowed.get(normalized);
    if (!canonical || seen.has(normalized)) continue;
    seen.add(normalized);
    names.push(canonical);
  }
  return names;
}

export function MultiAgentChatPanel({
  team,
  leadAgent,
}: MultiAgentChatPanelProps) {
  const { t } = useTranslation();
  const [messages, setMessages] = useState<UIMessage[]>([]);
  const [input, setInput] = useState("");
  const [viewMode, setViewMode] = useState<ViewMode>("normal");
  const [status, setStatus] = useState<ChatStatus>("idle");
  const [error, setError] = useState<string | null>(null);
  const [taskSnapshots, setTaskSnapshots] = useState<Record<string, TeamTask>>(
    {},
  );
  const [mentionQuery, setMentionQuery] = useState("");
  const [mentionRange, setMentionRange] = useState<{
    start: number;
    end: number;
  } | null>(null);
  const [activeMentionIndex, setActiveMentionIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const seenFinishedTaskIds = useRef<Set<string>>(new Set());
  const lastPollAtRef = useRef<number>(0);

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  useEffect(() => {
    setMessages([]);
    setTaskSnapshots({});
    setInput("");
    setError(null);
    setStatus("idle");
    setMentionQuery("");
    setMentionRange(null);
    setActiveMentionIndex(0);
    seenFinishedTaskIds.current = new Set();
    lastPollAtRef.current = 0;
  }, [leadAgent.name]);

  useEffect(() => {
    let cancelled = false;

    const pollTasks = async () => {
      try {
        const query = new URLSearchParams({
          team_id: team.id,
          lead_agent_name: leadAgent.name,
          limit: "50",
          after: String(lastPollAtRef.current),
        });
        const response = await fetch(`/api/teams/tasks?${query.toString()}`);
        if (!response.ok) {
          return;
        }

        const payload = (await response.json()) as { tasks?: TeamTask[] };
        const tasks = payload.tasks ?? [];
        if (cancelled || tasks.length === 0) {
          return;
        }

        lastPollAtRef.current = Date.now();

        setTaskSnapshots((current) => {
          const next = { ...current };
          for (const task of tasks) {
            next[task.id] = task;
          }
          return next;
        });

        setMessages((current) => {
          const next = [...current];
          for (const task of tasks) {
            if (
              (task.status === "completed" || task.status === "failed") &&
              !seenFinishedTaskIds.current.has(task.id)
            ) {
              seenFinishedTaskIds.current.add(task.id);
              next.push({
                id: `result-${task.id}`,
                role: "assistant",
                metadata: {
                  taskId: task.id,
                  status: task.status,
                  leadAgent: task.lead_agent_name ?? task.captain_name,
                  agentNames: task.agent_names,
                },
                parts: [
                  {
                    type: "text",
                    text:
                      task.result_text ||
                      (task.status === "failed"
                        ? "Task failed."
                        : "Task completed."),
                  },
                ],
              });
            }
          }
          return next;
        });
      } catch {
        // Ignore intermittent polling failures and recover on the next tick.
      }
    };

    void pollTasks();
    const timer = window.setInterval(() => {
      void pollTasks();
    }, 1500);

    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [leadAgent.name, team.id]);

  const totalParts = useMemo(
    () =>
      messages.reduce(
        (sum, message) => sum + ((message as UIMessage).parts?.length ?? 0),
        0,
      ),
    [messages],
  );
  const lastAssistant = [...messages]
    .reverse()
    .find((message) => message.role === "assistant");
  const mentionSuggestions = useMemo(() => {
    if (mentionRange == null) {
      return [];
    }
    const needle = mentionQuery.trim().toLowerCase();
    return team.members.filter(
      (agent) =>
        agent.kind === "specialist" &&
        agent.name.toLowerCase().includes(needle),
    );
  }, [mentionQuery, mentionRange, team.members]);
  const pendingTasks = useMemo(
    () =>
      Object.values(taskSnapshots).filter(
        (task) => task.status === "queued" || task.status === "running",
      ),
    [taskSnapshots],
  );

  useEffect(() => {
    setActiveMentionIndex(0);
  }, [mentionSuggestions.length]);

  const updateMentionState = (value: string, caret: number | null) => {
    if (caret == null) {
      setMentionRange(null);
      setMentionQuery("");
      return;
    }

    const beforeCaret = value.slice(0, caret);
    const match = /(?:^|\s)@([A-Za-z0-9_-]*)$/.exec(beforeCaret);
    if (!match) {
      setMentionRange(null);
      setMentionQuery("");
      return;
    }

    const query = match[1] ?? "";
    setMentionQuery(query);
    setMentionRange({
      start: caret - query.length - 1,
      end: caret,
    });
  };

  const closeMentionMenu = () => {
    setMentionRange(null);
    setMentionQuery("");
    setActiveMentionIndex(0);
  };

  const applyMention = (agentName: string) => {
    if (mentionRange == null) {
      return;
    }

    const nextValue = `${input.slice(0, mentionRange.start)}@${agentName} ${input.slice(mentionRange.end)}`;
    const nextCaret = mentionRange.start + agentName.length + 2;
    setInput(nextValue);
    closeMentionMenu();

    requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.setSelectionRange(nextCaret, nextCaret);
    });
  };

  const handleSend = async (event: FormEvent) => {
    event.preventDefault();
    if (!input.trim()) return;

    const text = input.trim();
    const specialistNames = team.members
      .filter((agent) => agent.kind === "specialist")
      .map((agent) => agent.name);
    const agentNames = extractMentionedAgents(text, specialistNames);

    setMessages((current) => [
      ...current,
      {
        id: `user-${Date.now()}`,
        role: "user",
        parts: [{ type: "text", text }],
      },
    ]);
    setStatus("sending");
    setError(null);
    setInput("");
    closeMentionMenu();

    try {
      const response = await fetch("/api/teams/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          team_id: team.id,
          lead_agent_name: leadAgent.name,
          message: text,
          agent_names: agentNames,
        }),
      });
      const payload = (await response.json()) as {
        task?: TeamTask;
        ack_message?: string;
        error?: string;
      };
      if (!response.ok) {
        throw new Error(payload.error || "Request failed");
      }

      if (payload.task) {
        setTaskSnapshots((current) => ({
          ...current,
          [payload.task!.id]: payload.task!,
        }));
      }

      setMessages((current) => [
        ...current,
        {
          id: `ack-${payload.task?.id ?? Date.now()}`,
          role: "assistant",
          metadata: payload.task
            ? {
                taskId: payload.task.id,
                status: payload.task.status,
                queuedAhead: payload.task.queued_ahead,
              }
            : undefined,
          parts: [
            {
              type: "text",
              text: payload.ack_message || `${leadAgent.name} received that.`,
            },
          ],
        },
      ]);
    } catch (requestError) {
      setError(
        requestError instanceof Error ? requestError.message : "Request failed",
      );
    } finally {
      setStatus("idle");
    }
  };

  return (
    <section
      className="glass-panel rounded-[32px] p-6"
      data-testid={`agent-multi-chat-${team.id}`}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-xs uppercase tracking-[0.28em] text-slate-500">
            {t("captains")}
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <h3 className="text-2xl font-semibold text-slate-900">
              {leadAgent.name}
            </h3>
            <span className="rounded-full bg-blue-100 px-3 py-1 text-xs text-blue-800">
              {t("kindCaptain")}
            </span>
          </div>
          <p className="mt-2 text-sm font-medium text-slate-700">{team.name}</p>
          <p className="mt-2 text-sm text-slate-600">{leadAgent.description}</p>
          <p className="mt-2 text-sm text-slate-500">
            {t("chatMultiAgentHint")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="inline-flex rounded-xl border border-sky-100 bg-white p-1">
            <button
              type="button"
              onClick={() => setViewMode("normal")}
              className={`rounded-lg px-3 py-1.5 text-sm ${viewMode === "normal" ? "bg-blue-600 text-white" : "text-slate-600"}`}
              data-testid="agent-chat-view-normal"
            >
              {t("chatViewNormal")}
            </button>
            <button
              type="button"
              onClick={() => setViewMode("debug")}
              className={`rounded-lg px-3 py-1.5 text-sm ${viewMode === "debug" ? "bg-blue-600 text-white" : "text-slate-600"}`}
              data-testid="agent-chat-view-debug"
            >
              {t("chatViewDebug")}
            </button>
          </div>
          <button
            type="button"
            onClick={() => {
              setMessages([]);
              setTaskSnapshots({});
              setInput("");
              setError(null);
              closeMentionMenu();
              seenFinishedTaskIds.current = new Set();
              lastPollAtRef.current = 0;
            }}
            className="dashboard-secondary-button px-3 py-2 text-sm"
            data-testid="agent-chat-clear"
          >
            {t("clear")}
          </button>
        </div>
      </div>

      <div className="mt-5 grid gap-4 xl:grid-cols-[minmax(0,1fr)_340px]">
        <div
          className="dashboard-muted-card rounded-[28px] p-4"
          data-testid="agent-chat-console"
        >
          <div className="mb-4 flex flex-wrap items-center gap-2 text-xs text-slate-600">
            <span className="rounded-full bg-sky-100 px-2 py-1 text-sky-800">
              {t("chatModeMultiAgent")}
            </span>
            <span className="rounded-full bg-slate-100 px-2 py-1 text-slate-700">
              {viewMode === "debug" ? t("chatViewDebug") : t("chatViewNormal")}
            </span>
            <span className="rounded-full bg-white px-2 py-1 text-slate-500">
              {t("chatStatusLine", {
                id: lastAssistant?.id ?? "-",
                status,
                messages: messages.length,
                parts: totalParts,
              })}
            </span>
            <span className="rounded-full bg-white px-2 py-1 text-slate-500">
              {t("chatQueueDepth", { count: pendingTasks.length })}
            </span>
          </div>

          <div
            className="max-h-[520px] space-y-4 overflow-y-auto pr-1"
            data-testid="agent-chat-messages"
          >
            {messages.length === 0 && (
              <div className="py-10 text-center text-slate-500">
                {t("chatMultiAgentHint")}
              </div>
            )}

            {messages.map((message) => {
              const textParts = getTextParts(message);
              const isUser = message.role === "user";

              return (
                <div
                  key={message.id}
                  className={isUser ? "text-right" : "text-left"}
                >
                  <div
                    className={`inline-block max-w-[88%] rounded-2xl px-4 py-3 ${
                      isUser
                        ? "bg-blue-600 text-white"
                        : "border border-sky-100 bg-white text-slate-900"
                    }`}
                  >
                    <div className="space-y-3">
                      {textParts.map((part, index) => (
                        <p
                          key={`${message.id}-text-${index}`}
                          className="whitespace-pre-wrap leading-7"
                        >
                          {part.text}
                        </p>
                      ))}
                    </div>
                  </div>
                </div>
              );
            })}

            {error && (
              <div className="rounded-[20px] border border-rose-200 bg-rose-50 p-3 text-sm text-rose-700">
                {t("error")}: {error}
              </div>
            )}
            <div ref={messagesEndRef} />
          </div>

          <form
            onSubmit={handleSend}
            className="mt-4 flex gap-2"
            data-testid="agent-chat-form"
          >
            <div className="relative flex-1">
              <input
                ref={inputRef}
                type="text"
                value={input}
                onChange={(event) => {
                  const nextValue = event.target.value;
                  setInput(nextValue);
                  updateMentionState(nextValue, event.target.selectionStart);
                }}
                onClick={(event) =>
                  updateMentionState(
                    event.currentTarget.value,
                    event.currentTarget.selectionStart,
                  )
                }
                onKeyUp={(event) =>
                  updateMentionState(
                    event.currentTarget.value,
                    event.currentTarget.selectionStart,
                  )
                }
                onBlur={() => {
                  window.setTimeout(() => {
                    closeMentionMenu();
                  }, 120);
                }}
                onKeyDown={(event) => {
                  if (mentionSuggestions.length === 0 || mentionRange == null) {
                    return;
                  }
                  if (event.key === "ArrowDown") {
                    event.preventDefault();
                    setActiveMentionIndex(
                      (current) => (current + 1) % mentionSuggestions.length,
                    );
                    return;
                  }
                  if (event.key === "ArrowUp") {
                    event.preventDefault();
                    setActiveMentionIndex(
                      (current) =>
                        (current - 1 + mentionSuggestions.length) %
                        mentionSuggestions.length,
                    );
                    return;
                  }
                  if (event.key === "Enter") {
                    event.preventDefault();
                    applyMention(
                      mentionSuggestions[activeMentionIndex]?.name ??
                        mentionSuggestions[0].name,
                    );
                    return;
                  }
                  if (event.key === "Escape") {
                    event.preventDefault();
                    closeMentionMenu();
                  }
                }}
                placeholder={t("chatPromptMultiAgent")}
                className="dashboard-input flex-1"
                data-testid="agent-chat-input"
              />
              {mentionSuggestions.length > 0 && mentionRange != null && (
                <div
                  className="absolute bottom-[calc(100%+8px)] left-0 z-20 w-full rounded-2xl border border-sky-100 bg-white p-2 shadow-sm"
                  data-testid="agent-chat-mention-menu"
                >
                  <div className="mb-1 px-2 text-[11px] font-medium uppercase tracking-[0.12em] text-slate-400">
                    Agents
                  </div>
                  <div className="space-y-1">
                    {mentionSuggestions.map((agent, index) => (
                      <button
                        key={agent.id}
                        type="button"
                        onMouseDown={(event) => {
                          event.preventDefault();
                          applyMention(agent.name);
                        }}
                        className={`flex w-full items-center justify-between rounded-xl px-3 py-2 text-left text-sm ${
                          index === activeMentionIndex
                            ? "bg-blue-600 text-white"
                            : "bg-slate-50 text-slate-700 hover:bg-sky-50"
                        }`}
                        data-testid={`agent-chat-mention-option-${agent.name}`}
                      >
                        <span className="font-medium">@{agent.name}</span>
                        <span
                          className={`text-xs ${index === activeMentionIndex ? "text-blue-100" : "text-slate-400"}`}
                        >
                          {t("kindSpecialist")}
                        </span>
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </div>
            <button
              type="submit"
              disabled={status === "sending" || !input.trim()}
              className="dashboard-button px-6 py-2 disabled:cursor-not-allowed"
              data-testid="agent-chat-send"
            >
              {status === "sending" ? t("sending") : t("sendMessage")}
            </button>
          </form>
        </div>

        <aside className="space-y-3" data-testid="agent-chat-debug-panel">
          <div className="dashboard-muted-card rounded-[24px] p-4">
            <div className="mb-2 text-sm font-semibold text-slate-900">
              {t("chatViewDebug")}
            </div>
            <p className="text-sm text-slate-600">{t("chatDebugHint")}</p>
          </div>
          <div className="dashboard-muted-card rounded-[24px] p-4">
            <div className="mb-3 text-sm font-semibold text-slate-900">
              {t("chatProtocolSummary")}
            </div>
            <div className="space-y-2 text-xs text-slate-600">
              <div>{t("chatMessagesCount", { count: messages.length })}</div>
              <div>{t("chatPartsCount", { count: totalParts })}</div>
              <div>{t("chatCurrentStatus", { status })}</div>
              <div>{t("chatQueueDepth", { count: pendingTasks.length })}</div>
            </div>
          </div>
          <div className="dashboard-muted-card rounded-[24px] p-4">
            <div className="mb-3 text-sm font-semibold text-slate-900">
              {t("chatLastAssistantMessage")}
            </div>
            {lastAssistant ? (
              <pre className="max-h-64 overflow-auto rounded-xl bg-slate-950/95 p-3 text-[11px] text-sky-100">
                {stringify({
                  id: lastAssistant.id,
                  metadata: lastAssistant.metadata ?? null,
                  parts: lastAssistant.parts ?? [],
                })}
              </pre>
            ) : (
              <div className="text-sm text-slate-500">
                {t("chatNoStructuredData")}
              </div>
            )}
          </div>
        </aside>
      </div>
    </section>
  );
}
