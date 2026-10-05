import { Server, Trash2 } from "lucide-react";
import { bytes, pct, since, statusLabel, statusTone, type Node } from "@/lib/nodes";
import { DataTable, type Column } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { IconButton, StatusBadge } from "@/ui/controls";
import { Meter } from "@/ui/Meter";

export function NodesTable({
  nodes,
  loading,
  compact,
  onDelete,
}: {
  nodes: Node[];
  loading?: boolean;
  compact?: boolean;
  onDelete?: (n: Node) => void;
}) {
  const columns: Column<Node>[] = [
    {
      header: "Name",
      cell: (n) => (
        <div className="flex items-center gap-2">
          <span className="font-medium">{n.name}</span>
          {n.name === "ctl-0" && <span className="text-faint text-[10px] uppercase">controller</span>}
        </div>
      ),
    },
    { header: "Status", cell: (n) => <StatusBadge tone={statusTone[n.status]}>{statusLabel[n.status]}</StatusBadge> },
    {
      header: "CPU",
      cell: (n) => (n.metrics ? <Meter value={n.metrics.cpuPercent} label={`${n.metrics.cpuPercent.toFixed(0)}% · ${n.info.cpuCores}c`} /> : "—"),
      className: "w-1/5",
    },
    {
      header: "Memory",
      cell: (n) =>
        n.metrics ? (
          <Meter value={pct(n.metrics.memoryUsedBytes, n.metrics.memoryTotalBytes)} label={`${bytes(n.metrics.memoryUsedBytes)}`} />
        ) : (
          "—"
        ),
      className: "w-1/5",
    },
    {
      header: "Disk",
      cell: (n) => (n.metrics ? <Meter value={pct(n.metrics.diskUsedBytes, n.metrics.diskTotalBytes)} /> : "—"),
      className: "w-1/6",
    },
  ];
  if (!compact) {
    columns.push(
      { header: "Load", cell: (n) => <span className="font-mono">{n.metrics ? n.metrics.load1.toFixed(2) : "—"}</span> },
      { header: "OS", cell: (n) => <span className="text-muted">{n.info.os || "—"}</span> },
      { header: "Docker", cell: (n) => <span className="text-muted font-mono">{n.info.dockerVersion || "—"}</span> },
      { header: "Agent", cell: (n) => <span className="text-muted font-mono">{n.info.agentVersion || "—"}</span> },
    );
  }
  columns.push({ header: "Last seen", cell: (n) => <span className="text-muted">{since(n.lastSeenAt)}</span> });
  if (onDelete) {
    columns.push({
      header: "",
      cell: (n) => (
        <IconButton label={`Remove ${n.name}`} onClick={() => onDelete(n)}>
          <Trash2 className="size-3.5" />
        </IconButton>
      ),
    });
  }

  return (
    <DataTable
      rows={nodes}
      rowKey={(n) => n.id}
      columns={columns}
      empty={
        !loading && (
          <EmptyState icon={Server} title="No nodes yet">
            Add a node with a join token. The controller's own agent appears as <code className="font-mono">ctl-0</code>.
          </EmptyState>
        )
      }
    />
  );
}
