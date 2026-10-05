import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { statusQuery } from "@/lib/auth";
import { useStreamTopic, type StreamEvent } from "@/lib/stream";
import { bytes, pct, useNodes, type Node } from "@/lib/nodes";
import { TimeSeriesChart, type TimeSeriesHandle } from "@/charts/TimeSeriesChart";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { NodesTable } from "../compute/NodesTable";

// Module routes are registered at runtime, so links to them use plain strings.
const nodesPath: string = "/compute/nodes";

interface ControllerStats {
  goroutines: number;
  heapMB: number;
  uptimeSec: number;
}

function formatUptime(sec: number) {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m ${sec % 60}s`;
}

/** Cluster totals over nodes that are reporting metrics. */
function totals(nodes: Node[]) {
  let cores = 0,
    cpuWeighted = 0,
    memUsed = 0,
    memTotal = 0,
    diskUsed = 0,
    diskTotal = 0;
  for (const n of nodes) {
    const m = n.metrics;
    if (!m || n.status !== "ready") continue;
    cores += n.info.cpuCores;
    cpuWeighted += m.cpuPercent * n.info.cpuCores;
    memUsed += m.memoryUsedBytes;
    memTotal += m.memoryTotalBytes;
    diskUsed += m.diskUsedBytes;
    diskTotal += m.diskTotalBytes;
  }
  return { cores, cpu: cores ? cpuWeighted / cores : 0, memUsed, memTotal, diskUsed, diskTotal };
}

export function OverviewPage() {
  const { data: status } = useQuery(statusQuery);
  const { data: nodes = [], isLoading } = useNodes();
  const [stats, setStats] = useState<ControllerStats | null>(null);
  const cpuChart = useRef<TimeSeriesHandle>(null);
  const memChart = useRef<TimeSeriesHandle>(null);
  const heapChart = useRef<TimeSeriesHandle>(null);

  const onStats = useCallback((e: StreamEvent<ControllerStats>) => {
    heapChart.current?.append(Date.parse(e.at), [e.data.heapMB, e.data.goroutines]);
    setStats(e.data);
  }, []);
  useStreamTopic("controller.stats", onStats);

  // Sample cluster totals on a fixed cadence so the charts have evenly spaced points.
  const latest = useRef(nodes);
  latest.current = nodes;
  useEffect(() => {
    const id = setInterval(() => {
      const t = totals(latest.current);
      if (!t.cores) return;
      const now = Date.now();
      cpuChart.current?.append(now, [t.cpu]);
      memChart.current?.append(now, [pct(t.memUsed, t.memTotal)]);
    }, 5000);
    return () => clearInterval(id);
  }, []);

  const t = totals(nodes);
  const ready = nodes.filter((n) => n.status === "ready").length;
  const unhealthy = nodes.filter((n) => n.status === "suspect" || n.status === "not_ready").length;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Cluster"]}
        title="Overview"
        status={
          <StatusBadge tone={unhealthy ? "warn" : stats ? "ok" : "neutral"}>
            {unhealthy ? `${unhealthy} node${unhealthy > 1 ? "s" : ""} unhealthy` : stats ? "Healthy" : "Waiting for data"}
          </StatusBadge>
        }
      />

      <div className={cn("grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6", gap)}>
        <StatTile label="Nodes ready" value={`${ready}/${nodes.length}`} tone={unhealthy ? "warn" : ready ? "ok" : undefined} />
        <StatTile label="CPU" value={t.cores ? t.cpu.toFixed(0) : "—"} unit={t.cores ? "%" : undefined} hint={t.cores ? `${t.cores} cores` : "no node metrics yet"} />
        <StatTile label="Memory" value={t.memTotal ? pct(t.memUsed, t.memTotal).toFixed(0) : "—"} unit={t.memTotal ? "%" : undefined} hint={t.memTotal ? `${bytes(t.memUsed)} of ${bytes(t.memTotal)}` : undefined} />
        <StatTile label="Disk" value={t.diskTotal ? pct(t.diskUsed, t.diskTotal).toFixed(0) : "—"} unit={t.diskTotal ? "%" : undefined} hint={t.diskTotal ? `${bytes(t.diskUsed)} of ${bytes(t.diskTotal)}` : undefined} />
        <StatTile label="Services" value={0} hint="deploy in Phase 2" />
        <StatTile label="Controller" value={status?.version ?? "—"} hint={stats ? `up ${formatUptime(stats.uptimeSec)}` : undefined} />
      </div>

      <div className={cn("grid grid-cols-1 lg:grid-cols-3", gap)}>
        <Panel title="Cluster CPU" flush>
          <div className="p-1">
            <TimeSeriesChart ref={cpuChart} series={[{ name: "CPU", area: true }]} unit="%" />
          </div>
        </Panel>
        <Panel title="Cluster memory" flush>
          <div className="p-1">
            <TimeSeriesChart ref={memChart} series={[{ name: "Memory", area: true }]} unit="%" />
          </div>
        </Panel>
        <Panel title="Controller process" flush>
          <div className="p-1">
            <TimeSeriesChart ref={heapChart} series={[{ name: "Heap MB" }, { name: "Goroutines" }]} />
          </div>
        </Panel>
      </div>

      <Panel
        title="Nodes"
        flush
        actions={
          <Link to={nodesPath} className="text-accent text-xs hover:underline">
            Manage
          </Link>
        }
      >
        <NodesTable nodes={nodes} loading={isLoading} compact />
      </Panel>
    </div>
  );
}
