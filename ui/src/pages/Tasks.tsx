import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, type TaskSummary } from "../lib/api";

const statusStyle: Record<string, string> = {
  completed: "bg-emerald-50 text-emerald-700 border-emerald-200",
  blocked:   "bg-orange-50  text-orange-700  border-orange-200",
  failed:    "bg-rose-50    text-rose-700    border-rose-200",
  running:   "bg-sky-50     text-sky-700     border-sky-200",
  pending:   "bg-slate-50   text-slate-600   border-slate-200",
  cancelled: "bg-slate-50   text-slate-500   border-slate-200",
  yielded:   "bg-amber-50   text-amber-700   border-amber-200",
};

function StatusBadge({ status }: { status?: string }) {
  const key = (status ?? "pending").toLowerCase();
  const cls = statusStyle[key] ?? statusStyle.pending;
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-medium ${cls}`}
    >
      {key}
    </span>
  );
}

function formatCost(usd?: number): string {
  if (!usd || usd <= 0) return "—";
  return `$${usd.toFixed(6)}`;
}

function formatDuration(ms?: number): string {
  if (!ms || ms <= 0) return "—";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

export function Tasks() {
  const [tasks, setTasks] = useState<TaskSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = async () => {
    setLoading(true);
    setError(null);
    try {
      const { tasks } = await api.listTasks(100);
      setTasks(tasks ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  return (
    <div className="flex flex-col gap-4">
      <header className="flex items-end justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight text-slate-900">
            Tasks
          </h2>
          <p className="text-sm text-slate-600">
            Every agent run is a task. Click into one to see checkpoints,
            replay, trace, and cost.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void load()}
          className="dashboard-secondary-button text-sm"
          data-testid="tasks-refresh"
        >
          Refresh
        </button>
      </header>

      {error && (
        <div className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700">
          {error}
        </div>
      )}

      <div className="overflow-hidden rounded-2xl border border-slate-100 bg-white shadow-sm">
        <table className="w-full text-sm" data-testid="tasks-table">
          <thead className="bg-slate-50 text-left text-xs uppercase tracking-wide text-slate-500">
            <tr>
              <th className="px-4 py-2">Status</th>
              <th className="px-4 py-2">Input</th>
              <th className="px-4 py-2">Agent</th>
              <th className="px-4 py-2">Rounds</th>
              <th className="px-4 py-2">Tools</th>
              <th className="px-4 py-2">Cost</th>
              <th className="px-4 py-2">Duration</th>
              <th className="px-4 py-2">Created</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {loading && (
              <tr>
                <td colSpan={8} className="px-4 py-8 text-center text-slate-400">
                  Loading…
                </td>
              </tr>
            )}
            {!loading && tasks.length === 0 && (
              <tr>
                <td colSpan={8} className="px-4 py-8 text-center text-slate-400">
                  No tasks yet. Kick one off in{" "}
                  <Link to="/live" className="text-sky-600 underline">
                    Live
                  </Link>{" "}
                  or{" "}
                  <Link to="/run" className="text-sky-600 underline">
                    Run
                  </Link>
                  .
                </td>
              </tr>
            )}
            {tasks.map((t) => (
              <tr
                key={t.id}
                className="hover:bg-slate-50"
                data-testid={`task-row-${t.id}`}
              >
                <td className="px-4 py-2">
                  <StatusBadge status={t.status} />
                </td>
                <td className="max-w-md truncate px-4 py-2">
                  <Link
                    to={`/tasks/${encodeURIComponent(t.id)}`}
                    className="text-sky-700 hover:underline"
                    title={t.input}
                  >
                    {t.input || t.id}
                  </Link>
                </td>
                <td className="px-4 py-2 text-slate-600">
                  {t.agent_name || t.team_name || "—"}
                </td>
                <td className="px-4 py-2 font-mono text-slate-600">
                  {t.stats?.rounds ?? "—"}
                </td>
                <td className="px-4 py-2 font-mono text-slate-600">
                  {t.stats?.tool_calls ?? "—"}
                </td>
                <td className="px-4 py-2 font-mono text-slate-600">
                  {formatCost(t.stats?.estimated_cost_usd)}
                </td>
                <td className="px-4 py-2 font-mono text-slate-600">
                  {formatDuration(t.stats?.duration_ms)}
                </td>
                <td className="px-4 py-2 font-mono text-xs text-slate-400">
                  {t.created_at?.slice(0, 19).replace("T", " ") || "—"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
