import { useCallback, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Server } from "lucide-react";
import { statusQuery } from "@/lib/auth";
import { useStreamTopic, type StreamEvent } from "@/lib/stream";
import { TimeSeriesChart, type TimeSeriesHandle } from "@/charts/TimeSeriesChart";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { EmptyState } from "@/ui/EmptyState";
import { StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

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

export function OverviewPage() {
  const { data: status } = useQuery(statusQuery);
  const [stats, setStats] = useState<ControllerStats | null>(null);
  const heap = useRef<TimeSeriesHandle>(null);
  const goroutines = useRef<TimeSeriesHandle>(null);

  const onStats = useCallback((e: StreamEvent<ControllerStats>) => {
    const t = Date.parse(e.at);
    heap.current?.append(t, [e.data.heapMB]);
    goroutines.current?.append(t, [e.data.goroutines]);
    setStats(e.data);
  }, []);
  useStreamTopic("controller.stats", onStats);

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Cluster"]}
        title="Overview"
        status={<StatusBadge tone={stats ? "ok" : "neutral"}>{stats ? "Controller healthy" : "Waiting for data"}</StatusBadge>}
      />

      <div className={cn("grid grid-cols-2 md:grid-cols-4 xl:grid-cols-6", gap)}>
        <StatTile label="Controller" value={status?.version ?? "—"} hint="version" />
        <StatTile label="Uptime" value={stats ? formatUptime(stats.uptimeSec) : "—"} />
        <StatTile label="Heap" value={stats ? stats.heapMB.toFixed(1) : "—"} unit="MB" />
        <StatTile label="Goroutines" value={stats?.goroutines ?? "—"} />
        <StatTile label="Nodes" value={0} hint="join workers in Phase 1" />
        <StatTile label="Services" value={0} hint="deploy in Phase 2" />
      </div>

      <div className={cn("grid grid-cols-1 lg:grid-cols-2", gap)}>
        <Panel title="Controller heap" flush>
          <div className="p-1">
            <TimeSeriesChart ref={heap} series={[{ name: "Heap", area: true }]} unit="MB" />
          </div>
        </Panel>
        <Panel title="Controller goroutines" flush>
          <div className="p-1">
            <TimeSeriesChart ref={goroutines} series={[{ name: "Goroutines", area: true }]} />
          </div>
        </Panel>
      </div>

      <Panel title="Nodes">
        <EmptyState icon={Server} title="No worker nodes yet">
          Workers join with a one-line command once the agent ships (Phase 1). Node CPU, memory, disk and network
          charts will appear here.
        </EmptyState>
      </Panel>
    </div>
  );
}
