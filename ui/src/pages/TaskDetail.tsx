import { useEffect, useMemo, useState } from "react";
import { Link, useParams, useNavigate } from "react-router-dom";
import { ArrowLeft } from "lucide-react";
import {
  api,
  type Checkpoint,
  type TaskDetail as TaskDetailType,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

const statusStyle: Record<string, string> = {
  completed: "bg-emerald-50 text-emerald-700 border-emerald-200",
  blocked:   "bg-orange-50  text-orange-700  border-orange-200",
  failed:    "bg-rose-50    text-rose-700    border-rose-200",
  running:   "bg-blue-50     text-blue-700     border-blue-200",
  yielded:   "bg-amber-50   text-amber-700   border-amber-200",
  cancelled: "bg-muted   text-muted-foreground   border-border",
};

function StatusBadge({ status }: { status?: string }) {
  const key = (status ?? "pending").toLowerCase();
  const cls = statusStyle[key] ?? "bg-muted text-muted-foreground border-border";
  return (
    <span
      className={`inline-flex items-center rounded-full border px-3 py-1 text-xs font-medium ${cls}`}
    >
      {key}
    </span>
  );
}

function formatCost(usd?: number): string {
  if (!usd || usd <= 0) return "—";
  return `$${usd.toFixed(6)}`;
}

function eventStyle(type: string): string {
  if (type === "workflow_complete")  return "border-emerald-200 bg-emerald-50/50";
  if (type === "workflow_blocked")   return "border-orange-200  bg-orange-50/50";
  if (type === "workflow_error")     return "border-rose-200    bg-rose-50/50";
  if (type === "tool_call")          return "border-blue-200     bg-blue-50/50";
  if (type === "tool_result")        return "border-emerald-100 bg-emerald-50/30";
  if (type === "compact_boundary")   return "border-purple-200  bg-purple-50/60";
  if (type === "handoff")            return "border-violet-200  bg-violet-50/60";
  if (type === "analytics")          return "border-amber-100   bg-amber-50/30";
  return "border-border bg-card";
}

export function TaskDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [task, setTask] = useState<TaskDetailType | null>(null);
  const [checkpoints, setCheckpoints] = useState<Checkpoint[]>([]);
  const [loading, setLoading] = useState(true);
  const [replaying, setReplaying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [followUp, setFollowUp] = useState("");
  const [selectedCheckpoint, setSelectedCheckpoint] = useState<string>("");

  // load fetches the trace + checkpoints. Pass silent=true for poll
  // refreshes so the page doesn't flash the "Loading…" placeholder.
  const load = async (silent = false) => {
    if (!id) return;
    if (!silent) setLoading(true);
    setError(null);
    try {
      const [taskResp, cpResp] = await Promise.all([
        api.getTaskTrace(id),
        api.listTaskCheckpoints(id),
      ]);
      setTask(taskResp.task);
      setCheckpoints(cpResp.checkpoints ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!silent) setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, [id]);

  // Auto-poll while the task is still in flight so stats / events /
  // checkpoints fill in live. Stops as soon as the task reaches a
  // terminal status.
  const isLive =
    task?.status === "running" || task?.status === "pending";
  useEffect(() => {
    if (!isLive || !id) return;
    const handle = window.setInterval(() => void load(true), 2000);
    return () => window.clearInterval(handle);
  }, [isLive, id]);

  const handleReplay = async () => {
    if (!id) return;
    setReplaying(true);
    setError(null);
    try {
      await api.replayTask(id, {
        checkpoint_id: selectedCheckpoint || undefined,
        follow_up: followUp.trim() || undefined,
      });
      setFollowUp("");
      // Re-fetch to surface the resumed run's new state.
      setTimeout(() => void load(), 600);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setReplaying(false);
    }
  };

  const stopReason = useMemo(() => {
    // Pick the most-recent terminal event's stop_reason from the events
    // log. The store doesn't currently surface it as a top-level field.
    const terminals = (task?.events ?? []).filter(
      (e) =>
        e.type === "workflow_complete" ||
        e.type === "workflow_blocked" ||
        e.type === "workflow_error",
    );
    return terminals.length > 0 ? terminals[terminals.length - 1] : undefined;
  }, [task]);

  if (!id) return null;

  return (
    <div className="flex flex-col gap-4">
      <header className="flex items-center justify-between">
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-3">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => navigate("/tasks")}
              className="h-7 px-2 text-muted-foreground"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              Tasks
            </Button>
            <h2 className="text-xl font-semibold tracking-tight text-foreground">
              Task
            </h2>
            <code className="rounded bg-muted px-2 py-0.5 font-mono text-xs text-muted-foreground">
              {id}
            </code>
          </div>
          {task && (
            <p className="max-w-2xl truncate text-sm text-muted-foreground">
              {task.input || "(no input)"}
            </p>
          )}
        </div>
        {task && (
          <div className="flex items-center gap-2">
            {isLive && (
              <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <span className="h-2 w-2 animate-pulse rounded-full bg-foreground" />
                live
              </span>
            )}
            <StatusBadge status={task.status} />
          </div>
        )}
      </header>

      {error && (
        <div className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700">
          {error}
        </div>
      )}

      {loading && (
        <div className="rounded-xl border border-border bg-card px-4 py-6 text-center text-sm text-muted-foreground">
          Loading…
        </div>
      )}

      {task && !loading && (
        <>
          {/* Summary cards */}
          <section className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
              <div className="text-xs uppercase tracking-wide text-muted-foreground">
                Rounds
              </div>
              <div className="mt-1 font-mono text-2xl font-semibold text-foreground">
                {task.stats?.rounds ?? "—"}
              </div>
            </div>
            <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
              <div className="text-xs uppercase tracking-wide text-muted-foreground">
                Tool calls
              </div>
              <div className="mt-1 font-mono text-2xl font-semibold text-foreground">
                {task.stats?.tool_calls ?? "—"}
              </div>
            </div>
            <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
              <div className="text-xs uppercase tracking-wide text-muted-foreground">
                Tokens
              </div>
              <div className="mt-1 font-mono text-2xl font-semibold text-foreground">
                {(task.stats?.total_tokens ?? 0).toLocaleString()}
              </div>
            </div>
            <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
              <div className="text-xs uppercase tracking-wide text-muted-foreground">
                Cost
              </div>
              <div className="mt-1 font-mono text-2xl font-semibold text-foreground">
                {formatCost(task.stats?.estimated_cost_usd)}
              </div>
            </div>
          </section>

          <section className="grid grid-cols-1 gap-4 lg:grid-cols-[1fr_360px]">
            {/* Output + Events */}
            <div className="flex flex-col gap-3">
              {task.output && (
                <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
                  <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                    Output
                  </h3>
                  <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-muted p-3 text-sm text-foreground">
                    {task.output}
                  </pre>
                </div>
              )}
              {stopReason && (
                <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
                  <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                    Terminal event
                  </h3>
                  <div className="mt-2 flex flex-col gap-1 text-sm">
                    <div>
                      <span className="text-muted-foreground">type: </span>
                      <span className="font-mono text-foreground">
                        {stopReason.type}
                      </span>
                    </div>
                    {stopReason.message && (
                      <pre className="whitespace-pre-wrap break-words text-foreground">
                        {stopReason.message}
                      </pre>
                    )}
                  </div>
                </div>
              )}
              <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
                <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                  Event trace ({task.events?.length ?? 0})
                </h3>
                <div
                  className="mt-2 flex max-h-[500px] flex-col gap-1.5 overflow-auto"
                  data-testid="task-events"
                >
                  {(task.events ?? []).map((e) => (
                    <div
                      key={e.id}
                      className={`rounded-lg border px-3 py-1.5 text-xs ${eventStyle(e.type)}`}
                    >
                      <div className="flex items-baseline justify-between gap-2">
                        <span className="font-mono font-semibold text-foreground">
                          {e.type}
                        </span>
                        <span className="font-mono text-[10px] text-muted-foreground">
                          {e.timestamp?.slice(11, 19)}
                        </span>
                      </div>
                      {e.message && (
                        <pre className="mt-0.5 whitespace-pre-wrap break-words font-sans text-foreground">
                          {e.message.length > 240
                            ? e.message.slice(0, 240) + "…"
                            : e.message}
                        </pre>
                      )}
                    </div>
                  ))}
                  {(task.events?.length ?? 0) === 0 && (
                    <div className="rounded-md border border-dashed border-border p-3 text-center text-xs text-muted-foreground">
                      No events recorded.
                    </div>
                  )}
                </div>
              </div>
            </div>

            {/* Right: checkpoints + replay */}
            <aside className="flex flex-col gap-3">
              <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
                <div className="flex items-center justify-between">
                  <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                    Checkpoints
                  </h3>
                  <span className="font-mono text-xs text-muted-foreground">
                    {checkpoints.length}
                  </span>
                </div>
                <div className="mt-2 flex max-h-72 flex-col gap-1.5 overflow-auto">
                  {checkpoints.length === 0 && (
                    <div className="rounded-md border border-dashed border-border p-3 text-center text-xs text-muted-foreground">
                      No checkpoints yet.
                    </div>
                  )}
                  {checkpoints.map((cp) => (
                    <button
                      key={cp.id}
                      type="button"
                      onClick={() =>
                        setSelectedCheckpoint(
                          cp.id === selectedCheckpoint ? "" : cp.id,
                        )
                      }
                      className={`flex flex-col gap-0.5 rounded-lg border px-3 py-2 text-left text-xs ${
                        cp.id === selectedCheckpoint
                          ? "border-primary bg-accent"
                          : "border-border hover:bg-muted"
                      }`}
                      data-testid={`checkpoint-${cp.seq}`}
                    >
                      <div className="flex items-center justify-between">
                        <span className="font-mono font-semibold text-foreground">
                          #{cp.seq}
                        </span>
                        <span className="font-mono text-[10px] text-muted-foreground">
                          r{cp.round} · {cp.message_count}msg
                        </span>
                      </div>
                      <div className="font-mono text-[10px] text-muted-foreground">
                        {cp.created_at?.slice(0, 19).replace("T", " ")}
                      </div>
                      {cp.final_text && (
                        <div className="truncate text-muted-foreground">
                          {cp.final_text}
                        </div>
                      )}
                    </button>
                  ))}
                </div>
              </div>

              <div className="rounded-[10px] border border-border bg-card p-4 shadow-sm">
                <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                  Replay
                </h3>
                <p className="mt-1 text-xs text-muted-foreground">
                  Re-runs the task starting from{" "}
                  {selectedCheckpoint
                    ? "the selected checkpoint"
                    : "the latest checkpoint"}
                  . Add an optional follow-up to steer the resumed run.
                </p>
                <Textarea
                  value={followUp}
                  onChange={(e) => setFollowUp(e.target.value)}
                  placeholder="Follow-up (optional)"
                  className="mt-2 min-h-[64px] resize-y"
                  data-testid="replay-follow-up"
                />
                <Button
                  onClick={() => void handleReplay()}
                  disabled={replaying || checkpoints.length === 0}
                  className="mt-2 w-full"
                  data-testid="replay-button"
                >
                  {replaying ? "Replaying…" : "Replay"}
                </Button>
              </div>
            </aside>
          </section>
        </>
      )}
    </div>
  );
}
