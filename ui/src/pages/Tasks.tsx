import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
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

// Maps a task status to its i18n key, e.g. "completed" -> "taskStatusCompleted".
function statusKey(status: string): string {
  const s = (status || "pending").toLowerCase();
  return "taskStatus" + s.charAt(0).toUpperCase() + s.slice(1);
}

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
  const { t } = useTranslation();
  const [tasks, setTasks] = useState<TaskSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const [page, setPage] = useState(0);

  // Server-side pagination: each fetch pulls a single page (newest-first),
  // with status + search applied in SQL. `total` drives the page count.
  const load = async () => {
    setLoading(true);
    setError(null);
    try {
      const { tasks, total } = await api.listTasks({
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
        status: statusFilter === "all" ? "" : statusFilter,
        search: debouncedSearch.trim(),
      });
      setTasks(tasks ?? []);
      setTotal(total ?? 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  // Debounce the search box so we don't fetch on every keystroke.
  useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search), 300);
    return () => clearTimeout(t);
  }, [search]);

  // Reset to the first page whenever the filter/search changes.
  useEffect(() => {
    setPage(0);
  }, [statusFilter, debouncedSearch]);

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, statusFilter, debouncedSearch]);

  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const clampedPage = Math.min(page, pageCount - 1);
  const pageRows = tasks;

  return (
    <div className="flex flex-col gap-4">
      <header className="flex items-end justify-between">
        <div>
          <h2 className="text-2xl font-bold tracking-tight">{t("tasksTitle")}</h2>
          <p className="text-sm text-muted-foreground">{t("tasksDescription")}</p>
        </div>
        <Button variant="outline" size="sm" onClick={() => void load()}>
          <RefreshCw className="h-3.5 w-3.5" />
          {t("refresh")}
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
              {t(statusKey(s))}
            </Button>
          ))}
        </div>
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t("tasksSearchPlaceholder")}
          className="ml-auto w-64"
          data-testid="tasks-search"
        />
      </div>

      <Card className="overflow-hidden">
        <Table data-testid="tasks-table">
          <TableHeader>
            <TableRow>
              <TableHead>{t("status")}</TableHead>
              <TableHead>{t("input")}</TableHead>
              <TableHead>{t("agent")}</TableHead>
              <TableHead>{t("rounds")}</TableHead>
              <TableHead>{t("tools")}</TableHead>
              <TableHead>{t("cost")}</TableHead>
              <TableHead>{t("duration")}</TableHead>
              <TableHead>{t("created")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading && (
              <TableRow>
                <TableCell colSpan={8} className="py-8 text-center text-muted-foreground">
                  {t("loading")}
                </TableCell>
              </TableRow>
            )}
            {!loading && total === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="py-8 text-center text-muted-foreground">
                  {statusFilter === "all" && debouncedSearch.trim() === "" ? (
                    <>
                      {t("tasksEmptyPrefix")}
                      <Link to="/live" className="underline">{t("live")}</Link>.
                    </>
                  ) : (
                    t("tasksNoMatch")
                  )}
                </TableCell>
              </TableRow>
            )}
            {pageRows.map((task) => (
              <TableRow key={task.id} data-testid={`task-row-${task.id}`}>
                <TableCell>
                  <Badge variant={statusVariant(task.status)} className="capitalize">
                    {t(statusKey(task.status ?? "pending"))}
                  </Badge>
                </TableCell>
                <TableCell className="max-w-md truncate">
                  <Link
                    to={`/tasks/${encodeURIComponent(task.id)}`}
                    className="hover:underline"
                    title={task.input}
                  >
                    {task.input || task.id}
                  </Link>
                </TableCell>
                <TableCell className="text-muted-foreground">
                  {task.agent_name || task.team_name || "—"}
                </TableCell>
                <TableCell className="font-mono text-muted-foreground">
                  {task.stats?.rounds ?? "—"}
                </TableCell>
                <TableCell className="font-mono text-muted-foreground">
                  {task.stats?.tool_calls ?? "—"}
                </TableCell>
                <TableCell className="font-mono text-muted-foreground">
                  {formatCost(task.stats?.estimated_cost_usd)}
                </TableCell>
                <TableCell className="font-mono text-muted-foreground">
                  {formatDuration(task.stats?.duration_ms)}
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  {task.created_at?.slice(0, 19).replace("T", " ") || "—"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>

      {total > PAGE_SIZE && (
        <div className="flex items-center justify-between text-sm text-muted-foreground">
          <span>
            {t("tasksShowing", {
              from: clampedPage * PAGE_SIZE + 1,
              to: Math.min((clampedPage + 1) * PAGE_SIZE, total),
              total,
            })}
          </span>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => setPage((p) => Math.max(0, p - 1))}
              disabled={clampedPage === 0}
            >
              {t("prev")}
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
              {t("next")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
