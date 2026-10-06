import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Activity } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { HistoryChart, type HistorySeries } from "@/charts/HistoryChart";
import { Panel } from "@/ui/Panel";
import { EmptyState } from "@/ui/EmptyState";
import { Alert } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface MetricSeries {
  key: string;
  node: string;
  points: [number, number][];
}

interface Metrics {
  start: string;
  end: string;
  stepSeconds: number;
  charts: Record<
    "cpu" | "memory" | "netRx" | "netTx" | "diskRead" | "diskWrite",
    MetricSeries[]
  >;
}

const ranges = ["15m", "1h", "6h", "24h", "7d"] as const;
type Range = (typeof ranges)[number];

export function formatBytes(v: number) {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (Math.abs(v) >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 && v >= 10 ? Math.round(v) : +v.toFixed(1)} ${units[i]}`;
}
const fmtCPU = (v: number) => `${+v.toFixed(v < 10 ? 1 : 0)}%`;
const fmtRate = (v: number) => `${formatBytes(v)}/s`;
const identity = (k: string) => k;

/**
 * Resource charts for one service (a line per task) or a project environment
 * (a line per service). `path` is the API path without "/metrics".
 */
export function MetricsPanel({
  path,
  by,
  shorten = identity,
  memoryLimit,
}: {
  path: string;
  by: "task" | "service";
  shorten?: (key: string) => string;
  /** Per-task memory limit in bytes, drawn as a reference line. */
  memoryLimit?: number;
}) {
  const [range, setRange] = useState<Range>("1h");
  const { data, error, isLoading } = useQuery({
    queryKey: ["metrics", path, range],
    queryFn: () => api<Metrics>("GET", `${path}/metrics?range=${range}`),
    refetchInterval: range === "15m" || range === "1h" ? 15_000 : 60_000,
    retry: false,
  });
  const start = data ? Date.parse(data.start) : 0;
  const end = data ? Date.parse(data.end) : 0;

  const charts = useMemo(() => {
    if (!data) return null;
    const label = (s: MetricSeries) =>
      by === "task" && s.node
        ? `${shorten(s.key)} · ${s.node}`
        : shorten(s.key);
    const lines = (xs: MetricSeries[], suffix = ""): HistorySeries[] =>
      xs.map((s) => ({ name: label(s) + suffix, points: s.points }));
    return {
      cpu: lines(data.charts.cpu),
      memory: lines(data.charts.memory),
      net: [
        ...lines(data.charts.netRx, " in"),
        ...lines(data.charts.netTx, " out"),
      ],
      disk: [
        ...lines(data.charts.diskRead, " read"),
        ...lines(data.charts.diskWrite, " write"),
      ],
    };
  }, [data, by, shorten]);
  const empty = charts && charts.cpu.length === 0 && charts.memory.length === 0;

  return (
    <div className={cn("flex flex-col", gap)}>
      <div className="flex items-center justify-between gap-2">
        <span className="text-faint text-xs">
          {by === "task"
            ? "One line per task"
            : "One line per service (all its tasks added up)"}
          {data && ` · ${data.stepSeconds}s resolution`}
        </span>
        <div className="border-line flex rounded-sm border">
          {ranges.map((r) => (
            <button
              key={r}
              onClick={() => setRange(r)}
              className={cn(
                "px-2 py-0.5 text-xs",
                r === range ? "bg-raised text-fg" : "text-muted hover:text-fg",
              )}
            >
              {r}
            </button>
          ))}
        </div>
      </div>
      {error && (
        <Alert tone="warn">
          {error instanceof ApiError
            ? error.message
            : "Metrics are unavailable"}
        </Alert>
      )}
      {empty && (
        <Panel>
          <EmptyState icon={Activity} title="No samples in this range">
            Nodes sample every running task every 10 seconds. Charts fill in
            once tasks have run for a minute.
          </EmptyState>
        </Panel>
      )}
      {!isLoading && charts && !empty && (
        <div className={cn("grid grid-cols-1 xl:grid-cols-2", gap)}>
          <Panel title="CPU (100% = one core)">
            <HistoryChart
              series={charts.cpu}
              start={start}
              end={end}
              format={fmtCPU}
              area
            />
          </Panel>
          <Panel title="Memory">
            <HistoryChart
              series={charts.memory}
              start={start}
              end={end}
              format={formatBytes}
              area
              markLine={
                memoryLimit ? { value: memoryLimit, label: "limit" } : undefined
              }
            />
          </Panel>
          <Panel title="Network">
            <HistoryChart
              series={charts.net}
              start={start}
              end={end}
              format={fmtRate}
            />
          </Panel>
          <Panel title="Disk I/O">
            <HistoryChart
              series={charts.disk}
              start={start}
              end={end}
              format={fmtRate}
            />
          </Panel>
        </div>
      )}
    </div>
  );
}
