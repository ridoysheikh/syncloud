import { Link } from "@tanstack/react-router";
import { useMutation } from "@tanstack/react-query";
import { lazy, Suspense, useState } from "react";
import { Container, RotateCw, SquareTerminal } from "lucide-react";
import { Dialog } from "@/ui/Dialog";
// xterm.js is only loaded when a shell is opened.
const Terminal = lazy(() =>
  import("@/ui/Terminal").then((m) => ({ default: m.Terminal })),
);
import { api } from "@/lib/api";
import { since } from "@/lib/nodes";
import { taskTone, useTasks, type Task } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { IconButton, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

export function TasksTable({
  tasks,
  loading,
  showService = true,
}: {
  tasks: Task[];
  loading: boolean;
  showService?: boolean;
}) {
  const restart = useMutation({
    mutationFn: (id: string) => api("POST", `/tasks/${id}/restart`),
  });
  const [shell, setShell] = useState<Task | null>(null);
  return (
    <>
      {shell && (
        <Dialog
          open
          onClose={() => setShell(null)}
          title={`Shell: ${shell.service} · ${shell.id} on ${shell.node}`}
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
      <DataTable
        rows={tasks}
        rowKey={(t) => t.id}
        empty={!loading && <EmptyState icon={Container} title="No tasks" />}
        columns={[
          {
            header: "Task",
            cell: (t) => (
              <span
                className={cn(
                  "font-mono",
                  t.desired === "stopped" && "text-faint",
                )}
              >
                {t.id}
              </span>
            ),
          },
          ...(showService
            ? [
                {
                  header: "Service",
                  cell: (t: Task) => {
                    const to: string = `/compute/services/${t.project}/${t.environment}/${t.service}`;
                    return (
                      <Link to={to} className="hover:text-accent">
                        {t.project}/{t.environment}/{t.service}
                      </Link>
                    );
                  },
                },
              ]
            : []),
          {
            header: "State",
            cell: (t) => (
              <div className="flex flex-col items-start gap-0.5">
                <StatusBadge tone={taskTone[t.state]}>
                  {t.desired === "stopped" && t.state === "running"
                    ? "stopping"
                    : t.state}
                </StatusBadge>
                {t.error && (
                  <span className="text-bad max-w-xs truncate" title={t.error}>
                    {t.error}
                  </span>
                )}
              </div>
            ),
          },
          {
            header: "Rev",
            cell: (t) => <span className="text-muted">{t.revision}</span>,
          },
          {
            header: "Node",
            cell: (t) => t.node || <span className="text-faint">—</span>,
          },
          {
            header: "IP",
            cell: (t) => <span className="font-mono">{t.ip || "—"}</span>,
          },
          {
            header: "Started",
            className: "w-full",
            cell: (t) => (
              <span className="text-muted">
                {t.startedAt ? since(t.startedAt) : "—"}
              </span>
            ),
          },
          {
            header: "",
            cell: (t) =>
              t.desired === "running" && (
                <div className="flex">
                  {t.state === "running" && (
                    <IconButton
                      label="Open a shell"
                      onClick={() => setShell(t)}
                    >
                      <SquareTerminal className="size-3.5" />
                    </IconButton>
                  )}
                  <IconButton
                    label="Restart (replace) this task"
                    onClick={() => restart.mutate(t.id)}
                  >
                    <RotateCw className="size-3.5" />
                  </IconButton>
                </div>
              ),
          },
        ]}
      />
    </>
  );
}

/** Every active task across all nodes. */
export function TasksPage() {
  const { data = [], isLoading } = useTasks();
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Compute"]} title="Tasks" />
      <Panel title={`Active tasks (${data.length})`} flush>
        <TasksTable tasks={data} loading={isLoading} />
      </Panel>
    </div>
  );
}
