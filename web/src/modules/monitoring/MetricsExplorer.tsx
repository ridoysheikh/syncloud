import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { LineChart as LineChartIcon, Play } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button } from "@/ui/controls";
import { HistoryChart } from "@/charts/HistoryChart";
import { cn, gap } from "@/ui/cn";

interface Series {
  labels: Record<string, string>;
  points: [number, number][];
}

const RANGES = { "15m": 15 * 60e3, "1h": 3600e3, "6h": 6 * 3600e3, "24h": 24 * 3600e3, "7d": 7 * 24 * 3600e3 } as const;
type Range = keyof typeof RANGES;

const EXAMPLES: { label: string; query: string }[] = [
  { label: "CPU % by service", query: "sum by (project, service) (syncloud_task_cpu_percent)" },
  { label: "Memory by service", query: "sum by (project, service) (syncloud_task_memory_bytes)" },
  { label: "Requests per second by service", query: "sum by (service) (rate(traefik_service_requests_total[5m]))" },
  { label: "5xx rate", query: 'sum by (service) (rate(traefik_service_requests_total{code=~"5.."}[5m]))' },
  { label: "p95 latency (s)", query: "histogram_quantile(0.95, sum by (service, le) (rate(traefik_service_request_duration_seconds_bucket[5m])))" },
  { label: "Node network in (B/s)", query: "sum by (node) (rate(syncloud_node_net_rx_bytes_total[5m]))" },
];

function seriesName(l: Record<string, string>): string {
  const name = l.__name__ ?? "";
  const rest = Object.entries(l)
    .filter(([k]) => k !== "__name__")
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}="${v}"`)
    .join(", ");
  return rest ? `${name}{${rest}}` : name || "{}";
}

function fmt(v: number): string {
  const a = Math.abs(v);
  if (a >= 1e9) return `${(v / 1e9).toFixed(2)}G`;
  if (a >= 1e6) return `${(v / 1e6).toFixed(2)}M`;
  if (a >= 1e3) return `${(v / 1e3).toFixed(2)}k`;
  if (a > 0 && a < 0.01) return v.toExponential(2);
  return String(Math.round(v * 1000) / 1000);
}

/** Monitoring › Metrics (§9.1): PromQL over everything VictoriaMetrics stores. */
export function MetricsExplorerPage() {
  const initial = new URLSearchParams(window.location.search).get("q") ?? EXAMPLES[0]!.query;
  const [text, setText] = useState(initial);
  const [query, setQuery] = useState(initial);
  const [range, setRange] = useState<Range>("1h");
  const run = () => {
    setQuery(text.trim());
    const u = new URL(window.location.href);
    u.searchParams.set("q", text.trim());
    window.history.replaceState(null, "", u);
  };
  const res = useQuery({
    queryKey: ["metrics-explore", query, range],
    queryFn: () => api<{ series: Series[]; truncated: boolean }>("GET", `/metrics/query?range=${range}&query=${encodeURIComponent(query)}`),
    enabled: query !== "",
    retry: false,
  });
  const names = useQuery({
    queryKey: ["metric-names"],
    queryFn: async () => (await api<{ items: string[] }>("GET", "/metrics/names")).items,
    staleTime: 5 * 60e3,
  });
  const end = res.dataUpdatedAt || Date.now();
  const series = useMemo(() => (res.data?.series ?? []).map((s) => ({ name: seriesName(s.labels), points: s.points })), [res.data]);

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Monitoring"]} title="Metrics" />
      <Panel title="Query">
        <div className="flex flex-col gap-2 text-xs">
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) run();
            }}
            rows={3}
            spellCheck={false}
            className="bg-bg border-line-strong focus:border-accent w-full rounded-sm border p-2 font-mono text-xs outline-none"
            placeholder="PromQL, e.g. sum by (service) (rate(traefik_service_requests_total[5m]))"
          />
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="primary" onClick={run} disabled={!text.trim()}>
              <Play className="size-3.5" /> Run
            </Button>
            <span className="text-faint">Ctrl+Enter</span>
            <div className="border-line ml-2 flex overflow-hidden rounded-sm border">
              {(Object.keys(RANGES) as Range[]).map((r) => (
                <button
                  key={r}
                  onClick={() => setRange(r)}
                  className={cn("px-2 py-1", r === range ? "bg-accent/20 text-fg" : "text-muted hover:text-fg")}
                >
                  {r}
                </button>
              ))}
            </div>
            <span className="text-muted ml-auto">Examples:</span>
            {EXAMPLES.map((e) => (
              <button
                key={e.label}
                className="text-accent hover:underline"
                onClick={() => {
                  setText(e.query);
                  setQuery(e.query);
                }}
              >
                {e.label}
              </button>
            ))}
          </div>
          {names.data && names.data.length > 0 && (
            <details>
              <summary className="text-muted cursor-pointer">{names.data.length} metric names</summary>
              <div className="mt-1 flex max-h-40 flex-wrap gap-x-3 gap-y-0.5 overflow-auto font-mono">
                {names.data.map((n) => (
                  <button key={n} className="text-muted hover:text-fg" onClick={() => setText(n)}>
                    {n}
                  </button>
                ))}
              </div>
            </details>
          )}
        </div>
      </Panel>
      {res.error && <Alert>{res.error instanceof ApiError ? res.error.message : "Query failed"}</Alert>}
      {res.data?.truncated && <Alert tone="warn">Only the first 200 series are shown; aggregate with sum by (…) to see fewer.</Alert>}
      <Panel title={res.isFetching ? "Result (running…)" : `Result · ${series.length} series`}>
        {series.length > 0 ? (
          <HistoryChart series={series} start={end - RANGES[range]} end={end} format={fmt} height={320} />
        ) : (
          !res.isFetching && <EmptyState icon={LineChartIcon} title="No data">The query returned no series for this range.</EmptyState>
        )}
      </Panel>
      {series.length > 0 && (
        <Panel title="Series" flush>
          <DataTable
            rows={res.data!.series}
            rowKey={(s) => seriesName(s.labels)}
            columns={[
              { header: "Labels", className: "w-full", cell: (s) => <span className="font-mono break-all">{seriesName(s.labels)}</span> },
              { header: "Latest", cell: (s) => <span className="font-mono tabular-nums">{s.points.length ? fmt(s.points[s.points.length - 1]![1]) : "—"}</span> },
              {
                header: "Max",
                cell: (s) => <span className="text-muted font-mono tabular-nums">{s.points.length ? fmt(Math.max(...s.points.map((p) => p[1]))) : "—"}</span>,
              },
            ]}
          />
        </Panel>
      )}
    </div>
  );
}
