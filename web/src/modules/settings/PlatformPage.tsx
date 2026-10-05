import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Boxes } from "lucide-react";
import { api } from "@/lib/api";
import { subscribe } from "@/lib/stream";
import { since } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface SystemTask {
  taskId: string;
  name: string;
  description: string;
  image: string;
  node: string;
  state: "pending" | "pulling" | "starting" | "running" | "exited" | "failed" | "removed";
  health: string;
  error: string;
  containerId: string;
  startedAt: string | null;
  updatedAt: string;
}

const tone = { running: "ok", pulling: "info", starting: "info", pending: "neutral", exited: "bad", failed: "bad", removed: "neutral" } as const;
const key = ["system", "tasks"];

/** Platform components run by the controller as system tasks (D20, §5.0). */
export function PlatformPage() {
  const qc = useQueryClient();
  useEffect(
    () =>
      subscribe("system.task", (e) => {
        const t = e.data as SystemTask;
        qc.setQueryData<SystemTask[]>(key, (prev) => prev?.map((p) => (p.taskId === t.taskId ? t : p)));
      }),
    [qc],
  );
  const { data = [], isLoading } = useQuery({
    queryKey: key,
    queryFn: async () => (await api<{ items: SystemTask[] }>("GET", "/system/tasks")).items,
  });
  const running = data.filter((t) => t.state === "running").length;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Settings"]}
        title="Platform components"
        status={
          data.length > 0 && (
            <StatusBadge tone={running === data.length ? "ok" : "warn"}>
              {running}/{data.length} running
            </StatusBadge>
          )
        }
      />
      <Panel flush>
        <DataTable
          rows={data}
          rowKey={(t) => t.taskId}
          empty={!isLoading && <EmptyState icon={Boxes} title="No system tasks" />}
          columns={[
            {
              header: "Component",
              cell: (t) => (
                <div className="flex flex-col py-1">
                  <span className="font-medium">{t.name}</span>
                  <span className="text-faint">{t.description}</span>
                </div>
              ),
              className: "w-full",
            },
            {
              header: "State",
              cell: (t) => (
                <div className="flex flex-col items-start gap-0.5">
                  <StatusBadge tone={tone[t.state]}>{t.state}</StatusBadge>
                  {t.error && <span className="text-bad max-w-xs truncate" title={t.error}>{t.error}</span>}
                </div>
              ),
            },
            { header: "Image", cell: (t) => <span className="font-mono">{t.image}</span> },
            { header: "Node", cell: (t) => t.node },
            { header: "Container", cell: (t) => <span className="text-muted font-mono">{t.containerId ? t.containerId.slice(0, 12) : "—"}</span> },
            { header: "Started", cell: (t) => <span className="text-muted">{t.state === "running" ? since(t.startedAt) : "—"}</span> },
          ]}
        />
      </Panel>
      <p className="text-faint text-xs">
        Versions are pinned per SynCloud release and upgraded together with the controller. Components are re-applied every minute, so a
        container removed by hand comes back.
      </p>
    </div>
  );
}
