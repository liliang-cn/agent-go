import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { RefreshCw } from "lucide-react";
import { api, type TaskSummary } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

const PAGE_SIZE = 25;

const STATUS_FILTERS = [
  "all",
  "completed",
  "running",
  "blocked",
  "failed",
] as const;
type StatusFilter = (typeof STATUS_FILTERS)[number];

type BadgeVariant = "default" | "secondary" | "destructive" | "outline";

function statusVariant(status?: string): BadgeVariant {
  switch ((status ?? "").toLowerCase()) {
    case "completed":
      return "default";
    case "failed":
    case "blocked":
      return "destructive";
    case "running":
    case "pending":
    case "yielded":
      return "secondary";
    default:
      return "outline";
  }
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
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);

  const load = async () => {
    setLoading(true);
    setError(null);
    try {
      const { tasks } = await api.listTasks(500);
      setTasks((tasks ?? []).slice().reverse());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  useEffect(() => {
    setPage(0);
  }, [statusFilter, search]);

  const statusCounts = useMemo(() => {
    const counts: Record<string, number> = { all: tasks.length };
    for (const t of tasks) {
      const k = (t.status ?? "pending").toLowerCase();
      counts[k] = (counts[k] ?? 0) + 1;
    }
    return counts;
  }, [tasks]);

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return tasks.filter((t) => {
      if (
        statusFilter !== "all" &&
        (t.status ?? "").toLowerCase() !== statusFilter
      ) {
        return false;
      }
      if (q) {
        const hay =
          `${t.input ?? ""} ${t.agent_name ?? ""} ${t.team_name ?? ""} ${t.id}`.toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  }, [tasks, statusFilter, search]);

  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const clampedPage = Math.min(page, pageCount - 1);
  const pageRows = filtered.slice(
    clampedPage * PAGE_SIZE,
    clampedPage * PAGE_SIZE + PAGE_SIZE,
  );

  return (
    <div className="flex flex-col gap-4">
      <header className="flex items-end justify-between">
        <div>
          <h2 className="text-2xl font-bold tracking-tight">Tasks</h2>
          <p className="text-sm text-muted-foreground">
            Every agent run is a task. Click into one to see checkpoints,
            replay, trace, and cost.
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => void load()}>
          <RefreshCw className="h-3.5 w-3.5" />
          Refresh
        </Button>
      </header>

      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive">
          {error}
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <div className="flex flex-wrap gap-1" data-testid="tasks-status-filter">
          {STATUS_FILTERS.map((s) => (
            <Button
              key={s}
              variant={statusFilter === s ? "default" : "outline"}
              size="sm"
              onClick={() => setStatusFilter(s)}
              className="h-7 capitalize"
            >
              {s}
              <span className="ml-1 font-mono text-[11px] opacity-70">
                {statusCounts[s] ?? 0}
              </span>
            </Button>
          ))}
        </div>
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search input / agent / id…"
          className="ml-auto w-64"
          data-testid="tasks-search"
        />
      </div>

      <Card className="overflow-hidden">
        <Table data-testid="tasks-table">
          <TableHeader>
            <TableRow>
              <TableHead>Status</TableHead>
              <TableHead>Input</TableHead>
              <TableHead>Agent</TableHead>
              <TableHead>Rounds</TableHead>
              <TableHead>Tools</TableHead>
              <TableHead>Cost</TableHead>
              <TableHead>Duration</TableHead>
              <TableHead>Created</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading && (
              <TableRow>
                <TableCell colSpan={8} className="py-8 text-center text-muted-foreground">
                  Loading…
                </TableCell>
              </TableRow>
            )}
            {!loading && tasks.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="py-8 text-center text-muted-foreground">
                  No tasks yet. Kick one off in{" "}
                  <Link to="/live" className="underline">Live</Link> or{" "}
                  <Link to="/run" className="underline">Run</Link>.
                </TableCell>
              </TableRow>
            )}
            {!loading && tasks.length > 0 && filtered.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="py-8 text-center text-muted-foreground">
                  No tasks match the current filter.
                </TableCell>
              </TableRow>
            )}
            {pageRows.map((t) => (
              <TableRow key={t.id} data-testid={`task-row-${t.id}`}>
                <TableCell>
                  <Badge variant={statusVariant(t.status)} className="capitalize">
                    {(t.status ?? "pending").toLowerCase()}
                  </Badge>
                </TableCell>
                <TableCell className="max-w-md truncate">
                  <Link
                    to={`/tasks/${encodeURIComponent(t.id)}`}
                    className="hover:underline"
                    title={t.input}
                  >
                    {t.input || t.id}
                  </Link>
                </TableCell>
                <TableCell className="text-muted-foreground">
                  {t.agent_name || t.team_name || "—"}
                </TableCell>
                <TableCell className="font-mono text-muted-foreground">
                  {t.stats?.rounds ?? "—"}
                </TableCell>
                <TableCell className="font-mono text-muted-foreground">
                  {t.stats?.tool_calls ?? "—"}
                </TableCell>
                <TableCell className="font-mono text-muted-foreground">
                  {formatCost(t.stats?.estimated_cost_usd)}
                </TableCell>
                <TableCell className="font-mono text-muted-foreground">
                  {formatDuration(t.stats?.duration_ms)}
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  {t.created_at?.slice(0, 19).replace("T", " ") || "—"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>

      {filtered.length > PAGE_SIZE && (
        <div className="flex items-center justify-between text-sm text-muted-foreground">
          <span>
            Showing {clampedPage * PAGE_SIZE + 1}–
            {Math.min((clampedPage + 1) * PAGE_SIZE, filtered.length)} of{" "}
            {filtered.length}
          </span>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => setPage((p) => Math.max(0, p - 1))}
              disabled={clampedPage === 0}
            >
              Prev
            </Button>
            <span className="font-mono text-xs">
              {clampedPage + 1} / {pageCount}
            </span>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setPage((p) => Math.min(pageCount - 1, p + 1))}
              disabled={clampedPage >= pageCount - 1}
            >
              Next
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
