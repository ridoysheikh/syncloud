import { lazy, Suspense, useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Container, RotateCw, Search, SquareTerminal } from "lucide-react";
import { api } from "@/lib/api";
import { since } from "@/lib/nodes";
import { taskTone, type Task } from "@/lib/workloads";
import { DataTable, type Column } from "@/ui/DataTable";
import { Dialog } from "@/ui/Dialog";
import { EmptyState } from "@/ui/EmptyState";
import { IconButton, Input, StatusBadge } from "@/ui/controls";
import { cn } from "@/ui/cn";
import { NodeLink } from "./nodes";
import { ServiceLink } from "./services";

export { TaskDots, taskCounts } from "./TaskDots";

// xterm.js is only loaded when a shell is opened.
const Terminal = lazy(() =>
  import("@/ui/Terminal").then((m) => ({ default: m.Terminal })),
);

/* Tasks, drawn the same way everywhere (Phase 16). */

type Tone = "ok" | "warn" | "bad" | "info" | "neutral";

/** Tone of any container-like state (tasks, system tasks, members). */
export const stateTone = (state: string): Tone =>
  (taskTone as Record<string, Tone>)[state] ?? "neutral";

const healthTone: Record<string, Tone> = {
  healthy: "ok",
  starting: "info",
  unhealthy: "bad",
};

/**
 * A task's state: "stopping" while a running task is wanted stopped, then its
 * error or why the controller cannot reach it, when there is one.
 */
export function TaskState({
  state,
  desired = "running",
  error,
  central,
  centralError,
  compact,
}: {
  state: string;
  desired?: string;
  error?: string;
  central?: Task["central"];
  centralError?: string;
  /** The badge only; details in the tooltip. */
  compact?: boolean;
}) {
  const label =
    desired === "stopped" && state === "running" ? "stopping" : state;
  const badge = <StatusBadge tone={stateTone(state)}>{label}</StatusBadge>;
  if (compact || (!error && central !== "unreachable")) {
    return error ? <span title={error}>{badge}</span> : badge;
  }
  return (
    <div className="flex flex-col items-start gap-0.5 py-0.5">
      {badge}
      {error && (
        <span className="text-bad max-w-xs truncate" title={error}>
          {error}
        </span>
      )}
      {central === "unreachable" && (
        <span
          className="text-warn max-w-xs truncate"
          title={`The controller cannot reach this task over the private network (${centralError ?? ""}); Traefik routes around it.`}
        >
          unreachable from controller
        </span>
      )}
    </div>
  );
}

/** A task's health check result, or a dash without a check. */
export function TaskHealth({ health }: { health: string }) {
  if (!health) return <span className="text-faint">—</span>;
  return (
    <span
      className={cn(
        healthTone[health] === "ok" && "text-ok",
        healthTone[health] === "bad" && "text-bad",
        healthTone[health] === "info" && "text-info",
      )}
    >
      {health}
    </span>
  );
}

type Filter = "all" | "running" | "starting" | "problems" | "stopped";

const isProblem = (t: Task) =>
  ["exited", "failed", "lost"].includes(t.state) ||
  !!t.error ||
  t.health === "unhealthy" ||
  t.central === "unreachable";
const filters: Record<Filter, (t: Task) => boolean> = {
  all: () => true,
  running: (t) => t.state === "running" && t.desired === "running",
  starting: (t) => ["pending", "pulling", "starting"].includes(t.state),
  problems: isProblem,
  stopped: (t) => t.desired === "stopped",
};

const shortId = (id: string) => id.replace(/^task_/, "").slice(0, 10);

/**
 * The one task table (Tasks page, service, deployment and node pages). Filter
 * chips with counts and a search narrow the rows; service and node link to
 * their pages with a preview on hover; running tasks offer a shell and a
 * restart.
 */
export function TasksTable({
  tasks,
  loading,
  showService = true,
  showNode = true,
  toolbar = true,
}: {
  tasks: Task[];
  loading: boolean;
  showService?: boolean;
  showNode?: boolean;
  /** The filter chips and search (hidden for short embedded lists). */
  toolbar?: boolean;
}) {
  const restart = useMutation({
    mutationFn: (id: string) => api("POST", `/tasks/${id}/restart`),
  });
  const [shell, setShell] = useState<Task | null>(null);
  const [filter, setFilter] = useState<Filter>("all");
  const [q, setQ] = useState("");

  const counts = useMemo(() => {
    const out = {} as Record<Filter, number>;
    for (const f of Object.keys(filters) as Filter[])
      out[f] = tasks.filter(filters[f]).length;
    return out;
  }, [tasks]);
  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return tasks.filter(
      (t) =>
        filters[filter](t) &&
        (!needle ||
          [t.id, t.service, t.project, t.environment, t.node, t.ip]
            .join(" ")
            .toLowerCase()
            .includes(needle)),
    );
  }, [tasks, filter, q]);

  const columns: Column<Task>[] = [
    {
      header: "Task",
      cell: (t) => (
        <span
          className={cn("font-mono", t.desired === "stopped" && "text-faint")}
          title={t.id}
        >
          {shortId(t.id)}
        </span>
      ),
    },
  ];
  if (showService)
    columns.push({
      header: "Service",
      cell: (t) => (
        <ServiceLink
          project={t.project}
          environment={t.environment}
          name={t.service}
        />
      ),
    });
  columns.push(
    {
      header: "State",
      cell: (t) => (
        <TaskState
          state={t.state}
          desired={t.desired}
          error={t.error}
          central={t.central}
          centralError={t.centralError}
        />
      ),
    },
    { header: "Health", cell: (t) => <TaskHealth health={t.health} /> },
    {
      header: "Rev",
      cell: (t) => <span className="text-muted">{t.revision}</span>,
    },
  );
  if (showNode)
    columns.push({
      header: "Node",
      cell: (t) => <NodeLink id={t.nodeId} name={t.node} />,
    });
  columns.push(
    {
      header: "IP",
      cell: (t) => <span className="font-mono">{t.ip || "—"}</span>,
    },
    {
      header: "Started",
      className: "w-full",
      cell: (t) => (
        <span
          className="text-muted"
          title={t.startedAt ? new Date(t.startedAt).toLocaleString() : ""}
        >
          {t.startedAt ? since(t.startedAt) : "—"}
          {t.desired === "stopped" && t.exitCode !== 0 && t.finishedAt && (
            <span className="text-faint"> · exit {t.exitCode}</span>
          )}
        </span>
      ),
    },
    {
      header: "",
      cell: (t) =>
        t.desired === "running" && (
          <div className="flex">
            {t.state === "running" && (
              <IconButton label="Open a shell" onClick={() => setShell(t)}>
                <SquareTerminal className="size-3.5" />
              </IconButton>
            )}
            <IconButton
              label="Restart (replace) this task"
              disabled={restart.isPending && restart.variables === t.id}
              onClick={() => restart.mutate(t.id)}
            >
              <RotateCw
                className={cn(
                  "size-3.5",
                  restart.isPending &&
                    restart.variables === t.id &&
                    "animate-spin",
                )}
              />
            </IconButton>
          </div>
        ),
    },
  );

  // Chips only help when the tasks differ in state (or a filter is chosen).
  const useful =
    filter !== "all" ||
    (["running", "starting", "problems", "stopped"] as Filter[]).filter(
      (f) => counts[f] > 0,
    ).length > 1;
  const chips: [Filter, string][] = [
    ["all", "All"],
    ["running", "Running"],
    ["starting", "Starting"],
    ["problems", "Problems"],
    ["stopped", "Stopping"],
  ];

  return (
    <>
      {shell && (
        <Dialog
          open
          onClose={() => setShell(null)}
          title={`Shell: ${shell.service} · ${shortId(shell.id)} on ${shell.node}`}
          wide
        >
          <Suspense
            fallback={
              <span className="text-faint text-xs">Loading terminal…</span>
            }
          >
            <Terminal taskId={shell.id} />
          </Suspense>
        </Dialog>
      )}
      {toolbar && (useful || tasks.length > 8) && (
        <div className="border-line flex flex-wrap items-center gap-1.5 border-b px-2 py-1.5 md:px-3">
          <div role="tablist" className="flex flex-wrap items-center gap-1">
            {chips
              .filter(([f]) => f === "all" || counts[f] > 0 || filter === f)
              .map(([f, label]) => (
                <button
                  key={f}
                  role="tab"
                  aria-selected={filter === f}
                  onClick={() => setFilter(f)}
                  className={cn(
                    "inline-flex h-6 items-center gap-1 rounded-sm border px-1.5 text-xs transition-colors",
                    filter === f
                      ? "border-line-accent bg-hover text-fg"
                      : "text-muted hover:text-fg hover:bg-hover border-transparent",
                  )}
                >
                  {label}
                  <span
                    className={cn(
                      "font-mono tabular-nums",
                      f === "problems" && counts[f] > 0
                        ? "text-bad"
                        : "text-faint",
                    )}
                  >
                    {counts[f]}
                  </span>
                </button>
              ))}
          </div>
          {tasks.length > 8 && (
            <label className="relative ml-auto">
              <Search className="text-faint pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2" />
              <Input
                value={q}
                onChange={(e) => setQ(e.target.value)}
                placeholder="Filter by ID, service, node or IP"
                aria-label="Filter tasks"
                className="h-7 w-64 max-w-full pl-7 text-xs"
              />
            </label>
          )}
        </div>
      )}
      <DataTable
        rows={rows}
        rowKey={(t) => t.id}
        empty={
          !loading &&
          (tasks.length > 0 ? (
            <p className="text-faint px-2 py-3 text-xs md:px-3">
              No task matches.
            </p>
          ) : (
            <EmptyState icon={Container} title="No tasks" />
          ))
        }
        columns={columns}
      />
    </>
  );
}
