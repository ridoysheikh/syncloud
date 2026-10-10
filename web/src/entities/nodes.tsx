import { useMemo } from "react";
import { Link } from "@tanstack/react-router";
import { Cpu, Server, Trash2 } from "lucide-react";
import {
  bytes,
  CONTROLLER_NODE,
  pct,
  since,
  statusLabel,
  statusTone,
  useNodeIndex,
  useNodes,
  type Node,
} from "@/lib/nodes";
import { useTaskIndex, type Task } from "@/lib/workloads";
import { DataTable, type Column } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { CardRow, HoverCard } from "@/ui/HoverCard";
import { IconButton, StatusBadge } from "@/ui/controls";
import { Meter } from "@/ui/Meter";
import { ChoiceCard } from "@/ui/choice";
import { cn, pad } from "@/ui/cn";
import { Select } from "@/ui/select";

/* Nodes, drawn the same way everywhere (Phase 16). */

export const nodeUrl = (name: string): string => `/compute/nodes/${name}`;

const dotTone = {
  ok: "bg-ok",
  warn: "bg-warn",
  bad: "bg-bad",
  neutral: "bg-neutral",
} as const;

/** A status dot for a node. */
export function NodeDot({ node }: { node: Node }) {
  return (
    <span
      aria-hidden
      className={cn(
        "size-1.5 shrink-0 rounded-full",
        dotTone[statusTone[node.status]],
      )}
    />
  );
}

/** The "controller" tag next to the controller's own agent. */
export function ControllerTag({ name }: { name: string }) {
  if (name !== CONTROLLER_NODE) return null;
  return (
    <span className="text-faint text-[10px] font-normal uppercase">
      controller
    </span>
  );
}

/** Status plus scheduling: draining, or not taking new tasks. */
export function NodeStatus({ node }: { node: Node }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <StatusBadge tone={statusTone[node.status]}>
        {statusLabel[node.status]}
      </StatusBadge>
      {node.draining ? (
        <StatusBadge tone="warn">draining</StatusBadge>
      ) : (
        !node.schedulable && (
          <StatusBadge tone="neutral">no new tasks</StatusBadge>
        )
      )}
    </span>
  );
}

/** CPU, memory and disk meters; "no metrics" until the agent reports. */
export function NodeUsage({
  node,
  className,
}: {
  node: Node;
  className?: string;
}) {
  const m = node.metrics;
  if (!m) return <span className="text-faint">no metrics yet</span>;
  const rows: [string, number, string][] = [
    [
      "CPU",
      m.cpuPercent,
      `${m.cpuPercent.toFixed(0)}% · ${node.info.cpuCores}c`,
    ],
    [
      "Memory",
      pct(m.memoryUsedBytes, m.memoryTotalBytes),
      `${bytes(m.memoryUsedBytes)} / ${bytes(m.memoryTotalBytes)}`,
    ],
    [
      "Disk",
      pct(m.diskUsedBytes, m.diskTotalBytes),
      `${bytes(m.diskUsedBytes)} / ${bytes(m.diskTotalBytes)}`,
    ],
  ];
  return (
    <div className={cn("flex flex-col gap-1", className)}>
      {rows.map(([k, v, label]) => (
        <div key={k} className="flex items-center gap-2">
          <span className="text-muted w-12 shrink-0">{k}</span>
          <Meter value={v} label={label} className="min-w-0 flex-1" />
        </div>
      ))}
    </div>
  );
}

/** Active tasks per node ID, from the shared task cache. */
export function useTasksPerNode() {
  const { data: tasks = [] } = useTaskIndex();
  return useMemo(() => {
    const out = new Map<string, { running: number; total: number }>();
    for (const t of tasks) {
      if (t.desired !== "running" || !t.nodeId) continue;
      const c = out.get(t.nodeId) ?? { running: 0, total: 0 };
      c.total++;
      if (t.state === "running") c.running++;
      out.set(t.nodeId, c);
    }
    return out;
  }, [tasks]);
}

function NodeCardBody({ node }: { node: Node }) {
  const tasks = useTasksPerNode().get(node.id);
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-1.5">
          <Server className="text-muted size-3.5 shrink-0" />
          <span className="truncate font-semibold">{node.name}</span>
          <ControllerTag name={node.name} />
        </span>
        <NodeStatus node={node} />
      </div>
      <NodeUsage node={node} />
      <div className="flex flex-col gap-0.5">
        <CardRow k="Tasks">
          {tasks ? `${tasks.running} running` : "none"}
          {tasks && tasks.total > tasks.running
            ? `, ${tasks.total - tasks.running} starting`
            : ""}
        </CardRow>
        <CardRow k="System">
          {[node.info.os, node.info.arch].filter(Boolean).join(" · ") || "—"}
        </CardRow>
        <CardRow k="Agent">
          <span className="font-mono">{node.info.agentVersion || "—"}</span>
        </CardRow>
        <CardRow k="Seen">
          {node.connected ? "connected" : "disconnected"},{" "}
          {since(node.lastSeenAt)}
        </CardRow>
      </div>
    </div>
  );
}

/**
 * A node reference: status dot, name and a link to its page, with a preview
 * card on hover. Pass the name or the ID; an unknown node renders as text.
 */
export function NodeLink({
  name,
  id,
  className,
  plain,
}: {
  name?: string;
  id?: string;
  className?: string;
  /** No dot (for headers that show the status already). */
  plain?: boolean;
}) {
  const { data: nodes } = useNodeIndex();
  const node = nodes?.find((n) => (id ? n.id === id : n.name === name));
  const label = node?.name ?? name ?? id ?? "";
  if (!label) return <span className="text-faint">—</span>;
  if (!node) return <span className={cn("truncate", className)}>{label}</span>;
  return (
    <HoverCard
      trigger={
        <Link
          to={nodeUrl(node.name)}
          className={cn(
            "hover:text-accent inline-flex min-w-0 items-center gap-1.5",
            className,
          )}
        >
          {!plain && <NodeDot node={node} />}
          <span className="truncate">{node.name}</span>
        </Link>
      }
    >
      {() => <NodeCardBody node={node} />}
    </HoverCard>
  );
}

/** A node as a card, for grid views. */
export function NodeCard({
  node,
  tasks,
}: {
  node: Node;
  tasks?: { running: number; total: number };
}) {
  return (
    <Link
      to={nodeUrl(node.name)}
      className={cn(
        "bg-surface border-line hover:border-line-strong flex min-w-0 flex-col gap-2 rounded-md border text-xs transition-colors",
        pad,
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <span className="flex min-w-0 flex-col">
          <span className="flex min-w-0 items-center gap-1.5">
            <NodeDot node={node} />
            <span className="truncate text-sm font-semibold">{node.name}</span>
            <ControllerTag name={node.name} />
          </span>
          <span className="text-faint truncate">
            {node.info.hostname || "—"}
          </span>
        </span>
        <NodeStatus node={node} />
      </div>
      <NodeUsage node={node} />
      <div className="text-muted flex flex-wrap items-center justify-between gap-x-3 gap-y-0.5">
        <span className="inline-flex items-center gap-1">
          <Cpu className="size-3" />
          {node.info.cpuCores}c · {bytes(node.info.memoryBytes)}
        </span>
        <span>
          <span className="text-fg font-mono">{tasks?.running ?? 0}</span>{" "}
          {tasks?.running === 1 ? "task" : "tasks"}
          {tasks && tasks.total > tasks.running && (
            <span className="text-info"> +{tasks.total - tasks.running}</span>
          )}
        </span>
        <span className="text-faint">
          {node.metrics ? `load ${node.metrics.load1.toFixed(2)}` : ""}{" "}
          {since(node.lastSeenAt)}
        </span>
      </div>
    </Link>
  );
}

/** Nodes as cards in a responsive grid. */
export function NodeGrid({ nodes }: { nodes: Node[] }) {
  const perNode = useTasksPerNode();
  return (
    <div
      className={cn(
        "grid grid-cols-1 gap-1.5 sm:grid-cols-2 sm:gap-2 md:gap-3 xl:grid-cols-3 2xl:grid-cols-4",
      )}
    >
      {nodes.map((n) => (
        <NodeCard key={n.id} node={n} tasks={perNode.get(n.id)} />
      ))}
    </div>
  );
}

export type ScheduleAction = "cordon" | "drain" | "uncordon";

/** The one node table: overview (compact) and the nodes page. */
export function NodesTable({
  nodes,
  loading,
  compact,
  onDelete,
  onSchedule,
}: {
  nodes: Node[];
  loading?: boolean;
  compact?: boolean;
  onDelete?: (n: Node) => void;
  /** Cordon ("cordon"), drain or allow tasks again ("uncordon"). */
  onSchedule?: (n: Node, action: ScheduleAction) => void;
}) {
  const perNode = useTasksPerNode();
  const columns: Column<Node>[] = [
    {
      header: "Name",
      cell: (n) => (
        <div className="flex items-center gap-2">
          <NodeLink name={n.name} className="font-medium" />
          <ControllerTag name={n.name} />
        </div>
      ),
    },
    { header: "Status", cell: (n) => <NodeStatus node={n} /> },
    {
      header: "CPU",
      cell: (n) =>
        n.metrics ? (
          <Meter
            value={n.metrics.cpuPercent}
            label={`${n.metrics.cpuPercent.toFixed(0)}% · ${n.info.cpuCores}c`}
          />
        ) : (
          "—"
        ),
      className: "w-1/5",
    },
    {
      header: "Memory",
      cell: (n) =>
        n.metrics ? (
          <Meter
            value={pct(n.metrics.memoryUsedBytes, n.metrics.memoryTotalBytes)}
            label={`${bytes(n.metrics.memoryUsedBytes)}`}
          />
        ) : (
          "—"
        ),
      className: "w-1/5",
    },
    {
      header: "Disk",
      cell: (n) =>
        n.metrics ? (
          <Meter
            value={pct(n.metrics.diskUsedBytes, n.metrics.diskTotalBytes)}
          />
        ) : (
          "—"
        ),
      className: "w-1/6",
    },
    {
      header: "Tasks",
      cell: (n) => {
        const t = perNode.get(n.id);
        return (
          <span className="font-mono tabular-nums">
            {t?.running ?? 0}
            {t && t.total > t.running && (
              <span className="text-info"> +{t.total - t.running}</span>
            )}
          </span>
        );
      },
    },
  ];
  if (!compact) {
    columns.push(
      {
        header: "Load",
        cell: (n) => (
          <span className="font-mono">
            {n.metrics ? n.metrics.load1.toFixed(2) : "—"}
          </span>
        ),
      },
      {
        header: "OS",
        cell: (n) => <span className="text-muted">{n.info.os || "—"}</span>,
      },
      {
        header: "Docker",
        cell: (n) => (
          <span className="text-muted font-mono">
            {n.info.dockerVersion || "—"}
          </span>
        ),
      },
      {
        header: "Agent",
        cell: (n) => (
          <span className="text-muted font-mono">
            {n.info.agentVersion || "—"}
          </span>
        ),
      },
    );
  }
  columns.push({
    header: "Last seen",
    cell: (n) => <span className="text-muted">{since(n.lastSeenAt)}</span>,
  });
  if (onSchedule) {
    columns.push({
      header: "Scheduling",
      cell: (n) => (
        <Select
          value={n.draining ? "drain" : n.schedulable ? "uncordon" : "cordon"}
          onChange={(v) => onSchedule(n, v as ScheduleAction)}
          title="Whether this node runs services"
          size="sm"
        >
          <option value="uncordon">run tasks</option>
          <option value="cordon">no new tasks</option>
          <option value="drain">drain</option>
        </Select>
      ),
    });
  }
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
            Add a node with a join token. The controller's own agent appears as{" "}
            <code className="font-mono">{CONTROLLER_NODE}</code>.
          </EmptyState>
        )
      }
    />
  );
}

/** Tasks of one node, in the order the node page lists them. */
export const tasksOn = (tasks: Task[], node: Node) =>
  tasks.filter((t) => t.nodeId === node.id && t.desired === "running");

/** A compact two-bar usage summary (CPU and memory) for picker cards. */
function MiniUsage({ node }: { node: Node }) {
  const m = node.metrics;
  if (!m) return <span className="text-faint">no metrics yet</span>;
  return (
    <span className="flex flex-col gap-1">
      <span className="flex items-center gap-2">
        <span className="text-muted w-8 shrink-0">CPU</span>
        <Meter
          value={m.cpuPercent}
          label={`${m.cpuPercent.toFixed(0)}% · ${node.info.cpuCores}c`}
          className="min-w-0 flex-1"
        />
      </span>
      <span className="flex items-center gap-2">
        <span className="text-muted w-8 shrink-0">Mem</span>
        <Meter
          value={pct(m.memoryUsedBytes, m.memoryTotalBytes)}
          label={`${bytes(m.memoryUsedBytes)} / ${bytes(m.memoryTotalBytes)}`}
          className="min-w-0 flex-1"
        />
      </span>
    </span>
  );
}

const byRole = (a: string, b: string) =>
  a === CONTROLLER_NODE ? -1 : b === CONTROLLER_NODE ? 1 : a.localeCompare(b);

/**
 * Nodes as selectable cards (Phase 16b): status, controller tag, CPU and
 * memory, and tasks. `value` holds node names; `limit` narrows the choice
 * (a project's allowed nodes). Chosen names that are not (or no longer)
 * nodes stay listed so they can be removed.
 */
export function NodePicker({
  value,
  onChange,
  limit,
  counts,
  label = "Nodes",
}: {
  value: string[];
  onChange: (v: string[]) => void;
  limit?: string[];
  /** Tasks per node name to show; by default the node's active tasks. */
  counts?: Record<string, number>;
  label?: string;
}) {
  const { data: nodes = [], isLoading } = useNodes();
  const perNode = useTasksPerNode();
  const names = useMemo(() => {
    const all = new Set(nodes.map((n) => n.name));
    for (const v of value) all.add(v);
    return [...all]
      .filter((n) => !limit?.length || limit.includes(n))
      .sort(byRole);
  }, [nodes, value, limit]);
  const toggle = (n: string) =>
    onChange(
      value.includes(n) ? value.filter((x) => x !== n) : [...value, n].sort(),
    );
  if (!isLoading && names.length === 0)
    return (
      <span className="text-faint text-xs">No nodes have joined yet.</span>
    );
  return (
    <div
      role="group"
      aria-label={label}
      className="grid grid-cols-1 gap-1.5 sm:grid-cols-2 sm:gap-2 xl:grid-cols-3"
    >
      {names.map((name) => {
        const n = nodes.find((x) => x.name === name);
        const tasks = counts
          ? (counts[name] ?? 0)
          : n
            ? (perNode.get(n.id)?.running ?? 0)
            : 0;
        return (
          <ChoiceCard
            key={name}
            multi
            selected={value.includes(name)}
            onSelect={() => toggle(name)}
            icon={Server}
            title={
              <span className="flex items-center gap-1.5">
                {name}
                <ControllerTag name={name} />
              </span>
            }
            description={
              n ? (
                <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                  <span className="inline-flex items-center gap-1">
                    <NodeDot node={n} />
                    {statusLabel[n.status]}
                    {n.draining
                      ? " · draining"
                      : !n.schedulable
                        ? " · no new tasks"
                        : ""}
                  </span>
                  <span className="text-faint">
                    {tasks} {tasks === 1 ? "task" : "tasks"}
                  </span>
                </span>
              ) : (
                "not joined (anymore)"
              )
            }
          >
            {n && <MiniUsage node={n} />}
          </ChoiceCard>
        );
      })}
    </div>
  );
}
