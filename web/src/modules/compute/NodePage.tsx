import { useMemo, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { TerminalSquare, TriangleAlert } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import {
  bytes,
  CONTROLLER_NODE,
  pct,
  since,
  useNodes,
  type Node,
} from "@/lib/nodes";
import { useTasks, type Task } from "@/lib/workloads";
import { ControllerTag, NodeStatus, tasksOn } from "@/entities/nodes";
import { TasksTable } from "@/entities/tasks";
import { HistoryChart, type HistorySeries } from "@/charts/HistoryChart";
import { formatBytes } from "@/modules/projects/MetricsPanel";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { RangePicker, refetchFor, type Range } from "@/ui/RangePicker";
import { Tabs } from "@/ui/Tabs";
import { Terminal } from "@/ui/Terminal";
import { Alert, Button } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { confirmAction } from "@/ui/dialogs";
import { ErrorBoundary } from "@/ui/ErrorBoundary";

const nodesPath: string = "/compute/nodes";

interface Series {
  key: string;
  points: [number, number][];
}

interface NodeMetricsHistory {
  start: string;
  end: string;
  stepSeconds: number;
  charts: Record<
    | "cpu"
    | "memory"
    | "disk"
    | "load"
    | "network"
    | "mesh"
    | "tasks"
    | "serviceCpu"
    | "serviceMemory",
    Series[]
  >;
}

type Tab = "overview" | "tasks" | "shell";

function formatUptime(sec: number) {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}

const fmtPct = (v: number) => `${+v.toFixed(v < 10 ? 1 : 0)}%`;
const fmtRate = (v: number) => `${formatBytes(v)}/s`;
const fmtCount = (v: number) => `${Math.round(v)}`;

/** One node's own page (§6.4): live usage, history, its tasks and a shell. */
export function NodePage() {
  const { name } = useParams({ strict: false }) as { name: string };
  const { data: nodes, isLoading } = useNodes();
  const { data: allTasks = [], isLoading: tasksLoading } = useTasks();
  const [tab, setTab] = useState<Tab>("overview");
  const node = nodes?.find((n) => n.name === name);
  const tasks = useMemo(
    () => (node ? tasksOn(allTasks, node) : []),
    [allTasks, node],
  );

  if (!node) {
    return (
      <div className={cn("flex flex-col", gap)}>
        <PageHeader
          crumbs={["Compute", <Link to={nodesPath}>Nodes</Link>]}
          title={name}
        />
        {!isLoading && <Alert>No node named {name}.</Alert>}
      </div>
    );
  }

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={[
          "Compute",
          <Link to={nodesPath} className="hover:text-fg">
            Nodes
          </Link>,
        ]}
        title={
          <span className="flex items-center gap-2">
            {node.name}
            <ControllerTag name={node.name} />
          </span>
        }
        status={<NodeStatus node={node} />}
        actions={<ScheduleActions node={node} />}
      />
      <Tabs
        tabs={["overview", "tasks", "shell"] as Tab[]}
        value={tab}
        onChange={setTab}
        label={(t) =>
          t === "tasks"
            ? `Tasks (${tasks.length})`
            : t === "shell"
              ? "Shell"
              : t
        }
      />
      <ErrorBoundary resetKey={tab}>
        {tab === "overview" && <Overview node={node} tasks={tasks} />}
        {tab === "tasks" && (
          <Panel title={`Tasks on this node (${tasks.length})`} flush>
            <TasksTable tasks={tasks} loading={tasksLoading} showNode={false} />
          </Panel>
        )}
        {tab === "shell" && <NodeShell node={node} />}
      </ErrorBoundary>
    </div>
  );
}

function ScheduleActions({ node }: { node: Node }) {
  const qc = useQueryClient();
  const act = useMutation({
    mutationFn: (action: "cordon" | "drain" | "uncordon") =>
      action === "drain"
        ? api("POST", `/nodes/${node.id}/drain`)
        : api("PUT", `/nodes/${node.id}/schedulable`, {
            schedulable: action === "uncordon",
          }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["nodes"] }),
  });
  if (!node.schedulable || node.draining) {
    return (
      <Button onClick={() => act.mutate("uncordon")} disabled={act.isPending}>
        Allow new tasks
      </Button>
    );
  }
  return (
    <>
      <Button onClick={() => act.mutate("cordon")} disabled={act.isPending}>
        Cordon
      </Button>
      <Button
        onClick={async () =>
          (await confirmAction(
            `Drain ${node.name}? Its tasks move to other nodes as replacements become healthy.`,
          )) && act.mutate("drain")
        }
        disabled={act.isPending}
      >
        Drain
      </Button>
    </>
  );
}

function Overview({ node, tasks }: { node: Node; tasks: Task[] }) {
  const m = node.metrics;
  const running = tasks.filter((t) => t.state === "running").length;
  return (
    <>
      <div
        className={cn("grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6", gap)}
      >
        <StatTile
          label="CPU"
          value={m ? m.cpuPercent.toFixed(0) : "—"}
          unit={m ? "%" : undefined}
          tone={m && m.cpuPercent > 85 ? "warn" : undefined}
          hint={`${node.info.cpuCores} cores · ${node.info.arch}`}
        />
        <StatTile
          label="Memory"
          value={
            m ? pct(m.memoryUsedBytes, m.memoryTotalBytes).toFixed(0) : "—"
          }
          unit={m ? "%" : undefined}
          tone={
            m && pct(m.memoryUsedBytes, m.memoryTotalBytes) > 90
              ? "warn"
              : undefined
          }
          hint={
            m
              ? `${bytes(m.memoryUsedBytes)} of ${bytes(m.memoryTotalBytes)}`
              : undefined
          }
        />
        <StatTile
          label="Disk"
          value={m ? pct(m.diskUsedBytes, m.diskTotalBytes).toFixed(0) : "—"}
          unit={m ? "%" : undefined}
          tone={
            m && pct(m.diskUsedBytes, m.diskTotalBytes) > 85
              ? "warn"
              : undefined
          }
          hint={
            m
              ? `${bytes(m.diskUsedBytes)} of ${bytes(m.diskTotalBytes)}`
              : undefined
          }
        />
        <StatTile
          label="Load"
          value={m ? m.load1.toFixed(2) : "—"}
          tone={m && m.load1 > node.info.cpuCores ? "warn" : undefined}
          hint={
            m
              ? `5m ${m.load5.toFixed(2)} · 15m ${m.load15.toFixed(2)}`
              : undefined
          }
        />
        <StatTile
          label="Tasks"
          value={running}
          hint={
            tasks.length > running
              ? `${tasks.length - running} starting`
              : "running"
          }
        />
        <StatTile
          label="Uptime"
          value={m ? formatUptime(m.uptimeSeconds) : "—"}
          hint={`seen ${since(node.lastSeenAt)}`}
        />
      </div>
      <div className={cn("grid grid-cols-1 xl:grid-cols-[1fr_20rem]", gap)}>
        <NodeCharts node={node} />
        <Details node={node} />
      </div>
    </>
  );
}

function Details({ node }: { node: Node }) {
  const rows: [string, ReactNode][] = [
    ["Hostname", node.info.hostname || "—"],
    ["OS", node.info.os || "—"],
    ["Kernel", node.info.kernel || "—"],
    ["Architecture", node.info.arch || "—"],
    ["CPU cores", node.info.cpuCores || "—"],
    ["Memory", node.info.memoryBytes ? bytes(node.info.memoryBytes) : "—"],
    ["Disk", node.info.diskBytes ? bytes(node.info.diskBytes) : "—"],
    ["Docker", node.info.dockerVersion || "not installed"],
    ["Agent", node.info.agentVersion || "—"],
    [
      "Takes tasks",
      node.draining
        ? "no (draining)"
        : node.schedulable
          ? "yes"
          : node.name === CONTROLLER_NODE
            ? "only projects or services that name it"
            : "no (cordoned)",
    ],
    ["Joined", new Date(node.createdAt).toLocaleString()],
    ["Node ID", <span className="font-mono">{node.id}</span>],
  ];
  return (
    <Panel title="Details" flush>
      <dl className="divide-line divide-y text-xs">
        {rows.map(([k, v]) => (
          <div key={k} className="flex gap-2 px-2 py-1.5 md:px-3">
            <dt className="text-muted w-24 shrink-0">{k}</dt>
            <dd className="min-w-0 break-words">{v}</dd>
          </div>
        ))}
      </dl>
    </Panel>
  );
}

function NodeCharts({ node }: { node: Node }) {
  const [range, setRange] = useState<Range>("1h");
  const { data, error } = useQuery({
    queryKey: ["node-metrics", node.id, range],
    queryFn: () =>
      api<NodeMetricsHistory>(
        "GET",
        `/nodes/${node.id}/metrics?range=${range}`,
      ),
    refetchInterval: refetchFor(range),
    retry: false,
  });
  const start = data ? Date.parse(data.start) : 0;
  const end = data ? Date.parse(data.end) : 0;
  const c = data?.charts;
  const lines = (xs: Series[] | undefined): HistorySeries[] =>
    (xs ?? []).map((s) => ({ name: s.key, points: s.points }));
  const pick = (xs: Series[] | undefined, key: string) =>
    lines(xs?.filter((s) => s.key === key));
  const total = (xs: Series[] | undefined) => {
    const p = xs?.find((s) => s.key === "total")?.points;
    const v = p?.length ? p[p.length - 1]![1] : undefined;
    return v ? { value: v, label: `total ${formatBytes(v)}` } : undefined;
  };

  const chart = (
    title: string,
    series: HistorySeries[],
    format: (v: number) => string,
    opts: {
      max?: number;
      markLine?: { value: number; label: string };
      integer?: boolean;
    } = {},
  ) => (
    <Panel title={title}>
      {series.length === 0 ? (
        <div className="text-faint flex h-[150px] items-center justify-center text-xs">
          {data ? "No samples in this range yet" : "Loading…"}
        </div>
      ) : (
        <HistoryChart
          series={series}
          start={start}
          end={end}
          format={format}
          height={150}
          {...opts}
        />
      )}
    </Panel>
  );

  return (
    <Panel
      title="Usage"
      actions={<RangePicker range={range} setRange={setRange} />}
    >
      {error ? (
        <Alert tone="warn">
          {error instanceof ApiError
            ? error.message
            : "Metrics are unavailable"}
        </Alert>
      ) : (
        <div
          className={cn("grid grid-cols-1 md:grid-cols-2 2xl:grid-cols-3", gap)}
        >
          {chart("CPU", lines(c?.cpu), fmtPct, { max: 100 })}
          {chart("Memory", pick(c?.memory, "used"), formatBytes, {
            markLine: total(c?.memory),
          })}
          {chart("Disk", pick(c?.disk, "used"), formatBytes, {
            markLine: total(c?.disk),
          })}
          {chart("Load average (1m)", lines(c?.load), (v) => v.toFixed(2))}
          {chart("Network", lines(c?.network), fmtRate)}
          {chart("Private network (mesh)", lines(c?.mesh), fmtRate)}
          {chart("Running tasks", lines(c?.tasks), fmtCount, { integer: true })}
          {chart("CPU by service", lines(c?.serviceCpu), fmtPct)}
          {chart("Memory by service", lines(c?.serviceMemory), formatBytes)}
        </div>
      )}
    </Panel>
  );
}

function NodeShell({ node }: { node: Node }) {
  const [open, setOpen] = useState(false);
  if (!node.connected) {
    return <Alert tone="warn">The node is not connected.</Alert>;
  }
  return (
    <Panel
      title={`Shell on ${node.name}`}
      actions={
        open && (
          <Button variant="ghost" onClick={() => setOpen(false)}>
            End session
          </Button>
        )
      }
    >
      {open ? (
        <Terminal
          taskId={node.id}
          url={`/api/v1/nodes/${node.id}/shell`}
          command={[]}
          height="h-[65vh]"
        />
      ) : (
        <div className="flex flex-col items-start gap-2 text-xs">
          <p className="text-muted flex items-start gap-1.5">
            <TriangleAlert className="text-warn mt-0.5 size-3.5 shrink-0" />
            <span>
              A login shell on the node itself, as the user the agent runs as
              (root on installed nodes). Every session is recorded in the audit
              log. Nodes started with{" "}
              <code className="font-mono">--no-host-shell</code> refuse it.
            </span>
          </p>
          <Button variant="primary" onClick={() => setOpen(true)}>
            <TerminalSquare className="size-3.5" /> Open shell
          </Button>
        </div>
      )}
    </Panel>
  );
}
