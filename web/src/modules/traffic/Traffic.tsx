import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Pause, Play, Waypoints } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { serviceUrl } from "@/lib/workloads";
import { HistoryChart, type HistorySeries } from "@/charts/HistoryChart";
import {
  TrafficSankey,
  type SankeyLink,
  type SankeyNode,
} from "@/charts/TrafficSankey";
import { formatBytes } from "@/modules/projects/MetricsPanel";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { RangePicker, refetchFor, type Range } from "@/ui/RangePicker";

interface Series {
  key: string;
  points: [number, number][];
}

export interface RouteTraffic {
  serviceId: string;
  project: string;
  environment: string;
  service: string;
  rps: number;
  errors4xx: number;
  errors5xx: number;
  p50Ms: number;
  p95Ms: number;
  bytesIn: number;
  bytesOut: number;
}

interface TrafficData {
  start: string;
  end: string;
  stepSeconds: number;
  charts: {
    requests: Series[];
    latency: Series[];
    bandwidth: Series[];
    services?: Series[];
  };
  routes: RouteTraffic[];
  windowSeconds: number;
}

const css = (n: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(n).trim();
export const statusColors = () => ({
  "2xx": css("--color-ok"),
  "3xx": css("--color-accent"),
  "4xx": css("--color-warn"),
  "5xx": css("--color-bad"),
});

export const fmtRps = (v: number) =>
  v === 0 ? "0" : v < 0.1 ? v.toFixed(3) : v < 10 ? v.toFixed(2) : v.toFixed(0);
export const fmtMs = (v: number) =>
  !v
    ? "—"
    : v >= 1000
      ? `${(v / 1000).toFixed(2)}s`
      : `${v.toFixed(v < 10 ? 1 : 0)}ms`;
const fmtPct = (part: number, total: number) =>
  total === 0
    ? "—"
    : `${((100 * part) / total).toFixed(part / total < 0.1 ? 1 : 0)}%`;
const fmtRate = (v: number) => `${formatBytes(v)}/s`;

function RouteLink({ r }: { r: RouteTraffic }) {
  if (!r.serviceId) {
    return (
      <span className="text-muted">
        {r.project}/{r.environment}/{r.service}
      </span>
    );
  }
  const to: string = serviceUrl({
    project: r.project,
    environment: r.environment,
    name: r.service,
  });
  return (
    <Link
      to={to}
      search={{ tab: "traffic" } as never}
      className="hover:text-accent whitespace-nowrap"
    >
      {r.project}/{r.environment}/{r.service}
    </Link>
  );
}

/**
 * Requests, errors, latency and bandwidth from Traefik (§5.7) for one
 * service, a project environment or every route. `path` is the API path of
 * the traffic endpoint.
 */
export function TrafficPanel({
  path,
  scope,
}: {
  path: string;
  scope: "service" | "environment" | "all";
}) {
  const [range, setRange] = useState<Range>("1h");
  const { data, error, isLoading } = useQuery({
    queryKey: ["traffic", path, range],
    queryFn: () => api<TrafficData>("GET", `${path}?range=${range}`),
    refetchInterval: refetchFor(range),
    retry: false,
  });
  const start = data ? Date.parse(data.start) : 0;
  const end = data ? Date.parse(data.end) : 0;
  const colors = useMemo(statusColors, []);
  const lines = (xs: Series[] | undefined): HistorySeries[] =>
    (xs ?? []).map((s) => ({ name: s.key, points: s.points }));
  const total = (data?.routes ?? []).reduce(
    (t, r) => ({
      rps: t.rps + r.rps,
      e4: t.e4 + r.errors4xx,
      e5: t.e5 + r.errors5xx,
      out: t.out + r.bytesOut,
      p95: Math.max(t.p95, r.p95Ms),
    }),
    { rps: 0, e4: 0, e5: 0, out: 0, p95: 0 },
  );
  const requests = useMemo(
    () =>
      lines(data?.charts.requests).sort((a, b) => a.name.localeCompare(b.name)),
    [data],
  );
  const empty =
    data &&
    requests.every((s) => s.points.every((p) => p[1] === 0)) &&
    total.rps === 0;
  const win = data ? `${data.windowSeconds / 60}m` : "5m";

  return (
    <div className={cn("flex flex-col", gap)}>
      <div className="flex items-center justify-between gap-2">
        <span className="text-faint text-xs">
          From Traefik's metrics{data && ` · ${data.stepSeconds}s resolution`} ·
          numbers are the last {win}
        </span>
        <RangePicker range={range} setRange={setRange} />
      </div>
      {error && (
        <Alert tone="warn">
          {error instanceof ApiError
            ? error.message
            : "Traffic metrics are unavailable"}
        </Alert>
      )}
      <div className={cn("grid grid-cols-2 md:grid-cols-5", gap)}>
        <StatTile label="Requests / s" value={fmtRps(total.rps)} />
        <StatTile
          label="4xx"
          value={fmtPct(total.e4, total.rps)}
          tone={total.e4 > 0 ? "warn" : undefined}
        />
        <StatTile
          label="5xx"
          value={fmtPct(total.e5, total.rps)}
          tone={total.e5 > 0 ? "bad" : undefined}
        />
        <StatTile
          label={scope === "service" ? "p95 latency" : "Slowest p95"}
          value={fmtMs(total.p95)}
        />
        <StatTile label="Bandwidth out" value={fmtRate(total.out)} />
      </div>
      {empty && (
        <Panel>
          <EmptyState icon={Waypoints} title="No requests in this range">
            Requests through Traefik show up here within about 20 seconds.
          </EmptyState>
        </Panel>
      )}
      {!isLoading && data && !empty && (
        <div className={cn("grid grid-cols-1 xl:grid-cols-2", gap)}>
          <Panel title="Requests by status">
            <HistoryChart
              series={requests}
              start={start}
              end={end}
              format={(v) => `${fmtRps(v)}/s`}
              colors={colors}
              stack
            />
          </Panel>
          <Panel title="Latency">
            <HistoryChart
              series={lines(data.charts.latency)}
              start={start}
              end={end}
              format={fmtMs}
            />
          </Panel>
          {scope !== "service" && (
            <Panel title="Requests per service">
              <HistoryChart
                series={lines(data.charts.services)}
                start={start}
                end={end}
                format={(v) => `${fmtRps(v)}/s`}
              />
            </Panel>
          )}
          <Panel title="Bandwidth">
            <HistoryChart
              series={lines(data.charts.bandwidth)}
              start={start}
              end={end}
              format={fmtRate}
            />
          </Panel>
        </div>
      )}
      {scope !== "service" && data && data.routes.length > 0 && (
        <Panel title={`Routes (last ${win})`} flush>
          <DataTable
            rows={data.routes}
            rowKey={(r) => `${r.project}/${r.environment}/${r.service}`}
            columns={[
              { header: "Service", cell: (r) => <RouteLink r={r} /> },
              {
                header: "Req/s",
                cell: (r) => <span className="font-mono">{fmtRps(r.rps)}</span>,
              },
              {
                header: "4xx",
                cell: (r) => (
                  <span
                    className={cn("font-mono", r.errors4xx > 0 && "text-warn")}
                  >
                    {fmtPct(r.errors4xx, r.rps)}
                  </span>
                ),
              },
              {
                header: "5xx",
                cell: (r) => (
                  <span
                    className={cn("font-mono", r.errors5xx > 0 && "text-bad")}
                  >
                    {fmtPct(r.errors5xx, r.rps)}
                  </span>
                ),
              },
              {
                header: "p50",
                cell: (r) => (
                  <span className="font-mono">{fmtMs(r.p50Ms)}</span>
                ),
              },
              {
                header: "p95",
                cell: (r) => (
                  <span className="font-mono">{fmtMs(r.p95Ms)}</span>
                ),
              },
              {
                header: "Out",
                className: "w-full",
                cell: (r) => (
                  <span className="text-muted font-mono">
                    {fmtRate(r.bytesOut)}
                  </span>
                ),
              },
            ]}
          />
        </Panel>
      )}
    </div>
  );
}

interface MapTask {
  id: string;
  node: string;
  ip: string;
  state: string;
  health: string;
  revision: number;
  rps: number;
}

interface MapRoute extends RouteTraffic {
  hosts: string[];
  tasks: MapTask[];
}

/** Live traffic map: hostname → service → task, sized by requests (§5.7). */
export function TrafficMap() {
  const navigate = useNavigate();
  const { data, error } = useQuery({
    queryKey: ["traffic", "map"],
    queryFn: () =>
      api<{ routes: MapRoute[]; windowSeconds: number }>("GET", "/traffic/map"),
    refetchInterval: 5_000,
    retry: false,
  });
  const graph = useMemo(() => {
    const nodes: SankeyNode[] = [];
    const links: SankeyLink[] = [];
    const targets: Record<string, string> = {};
    const routes = data?.routes ?? [];
    const ok = css("--color-ok"),
      warn = css("--color-warn"),
      bad = css("--color-bad"),
      accent = css("--color-accent"),
      neutral = css("--color-neutral");
    for (const r of routes) {
      const sid = `svc:${r.serviceId}`;
      const errRate = r.rps ? r.errors5xx / r.rps : 0;
      nodes.push({
        name: sid,
        label: `${r.project}/${r.environment}/${r.service}`,
        color: errRate > 0.05 ? bad : errRate > 0 ? warn : accent,
        detail: `<b>${r.project}/${r.environment}/${r.service}</b><br/>${fmtRps(r.rps)} req/s · 5xx ${fmtPct(r.errors5xx, r.rps)} · p95 ${fmtMs(r.p95Ms)}`,
      });
      targets[sid] = serviceUrl({
        project: r.project,
        environment: r.environment,
        name: r.service,
      });
      const perHost = r.rps / Math.max(r.hosts.length, 1);
      for (const h of r.hosts) {
        const hid = `host:${h}`;
        if (!nodes.some((n) => n.name === hid)) {
          nodes.push({ name: hid, label: h, color: neutral, detail: h });
        }
        links.push({ source: hid, target: sid, value: perHost });
      }
      for (const t of r.tasks) {
        const tid = `task:${t.id}`;
        const healthy =
          t.state === "running" && (t.health === "" || t.health === "healthy");
        nodes.push({
          name: tid,
          label: `${t.id.replace(/^task_/, "").slice(0, 8)} · ${t.node}`,
          color: healthy ? ok : t.state === "running" ? warn : bad,
          detail: `<b>${t.id}</b><br/>${t.node} · ${t.ip || "no IP"} · rev ${t.revision}<br/>${t.state}${t.health ? ` · ${t.health}` : ""} · ${fmtRps(t.rps)} req/s`,
        });
        targets[tid] = targets[sid]!;
        links.push({ source: sid, target: tid, value: t.rps });
      }
    }
    return {
      nodes,
      links,
      targets,
      rows:
        Math.max(
          ...routes.map((r) => Math.max(r.tasks.length, r.hosts.length)),
          1,
        ) * routes.length,
    };
  }, [data]);

  return (
    <Panel title="Traffic map" flush>
      {error && (
        <div className="p-2">
          <Alert tone="warn">
            {error instanceof ApiError
              ? error.message
              : "Traffic map unavailable"}
          </Alert>
        </div>
      )}
      {data && data.routes.length === 0 ? (
        <EmptyState icon={Waypoints} title="No routed services">
          Services with an HTTP port get a public address and appear here.
        </EmptyState>
      ) : (
        <div className="p-2">
          <div className="text-faint mb-1 flex gap-4 text-[11px]">
            <span>hostname</span>
            <span>→ service</span>
            <span>→ task (green healthy, amber unhealthy)</span>
            <span className="ml-auto">
              width: requests over the last {(data?.windowSeconds ?? 300) / 60}m
              · updates every 5s
            </span>
          </div>
          <TrafficSankey
            nodes={graph.nodes}
            links={graph.links}
            height={Math.min(Math.max(160, graph.rows * 28), 640)}
            onClick={(name) => {
              const to = graph.targets[name];
              if (to)
                void navigate({ to, search: { tab: "traffic" } as never });
            }}
          />
        </div>
      )}
    </Panel>
  );
}

interface RequestLine {
  time: string;
  project: string;
  environment: string;
  service: string;
  node: string;
  fields?: Record<string, string>;
}

const MAX = 1000;
const statusClass = (s = "") =>
  s.startsWith("5")
    ? "text-bad"
    : s.startsWith("4")
      ? "text-warn"
      : s.startsWith("3")
        ? "text-accent"
        : "text-ok";

/** Live request tail from Traefik's access log (§5.7). */
export function RequestsTail({
  filter,
}: {
  filter: { project?: string; environment?: string; service?: string };
}) {
  const [status, setStatus] = useState("");
  const [client, setClient] = useState("");
  const [applied, setApplied] = useState("");
  const [live, setLive] = useState(true);
  const [tail, setTail] = useState<RequestLine[]>([]);
  const box = useRef<HTMLDivElement>(null);
  const params = useMemo(() => {
    const p = new URLSearchParams({ stream: "access" });
    if (filter.project) p.set("project", filter.project);
    if (filter.environment) p.set("environment", filter.environment);
    if (filter.service) p.set("service", filter.service);
    if (status) p.set("status", status);
    if (applied) p.set("client", applied);
    return p;
  }, [filter.project, filter.environment, filter.service, status, applied]);
  const history = useQuery({
    queryKey: ["requests", params.toString()],
    queryFn: async () =>
      (
        await api<{ items: RequestLine[] }>(
          "GET",
          `/logs?${params}&since=15m&limit=300`,
        )
      ).items,
    retry: false,
  });
  useEffect(() => {
    setTail([]);
    if (!live) return;
    const es = new EventSource(`/api/v1/logs/tail?${params}`);
    es.onmessage = (e) => {
      const l = JSON.parse(e.data) as RequestLine;
      setTail((prev) =>
        prev.length >= MAX ? [...prev.slice(-MAX / 2), l] : [...prev, l],
      );
    };
    return () => es.close();
  }, [params, live]);
  const lines = useMemo(() => {
    const hist = history.data ?? [];
    const last = hist.at(-1)?.time ?? "";
    return [...hist, ...tail.filter((l) => l.time > last)].slice(-MAX);
  }, [history.data, tail]);
  const stick = useRef(true);
  useEffect(() => {
    const el = box.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [lines]);
  const sel = "bg-bg border-line-strong h-7 rounded-input border px-1.5 text-xs";
  const showService = !filter.service;

  return (
    <Panel
      flush
      title={
        <span className="flex items-center gap-2">
          Requests{" "}
          <span className="text-faint normal-case">{lines.length}</span>
        </span>
      }
      actions={
        <>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className={sel}
            aria-label="Status"
          >
            <option value="">all statuses</option>
            <option value="2xx">2xx</option>
            <option value="3xx">3xx</option>
            <option value="4xx">4xx</option>
            <option value="5xx">5xx</option>
          </select>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setApplied(client.trim());
            }}
          >
            <Input
              value={client}
              onChange={(e) => setClient(e.target.value)}
              placeholder="Client IP…"
              className="h-7 w-32"
            />
          </form>
          <Button
            variant={live ? "primary" : "default"}
            onClick={() => setLive(!live)}
            title="Live tail"
          >
            {live ? (
              <Pause className="size-3.5" />
            ) : (
              <Play className="size-3.5" />
            )}{" "}
            {live ? "Live" : "Paused"}
          </Button>
        </>
      }
    >
      <div
        ref={box}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
        className="h-[40vh] overflow-auto font-mono text-[11px] leading-[1.6]"
      >
        {lines.length === 0 && !history.isLoading ? (
          <EmptyState icon={Waypoints} title="No requests">
            Requests through Traefik appear here as they happen (the last 15
            minutes are loaded).
          </EmptyState>
        ) : (
          <table className="w-full">
            <tbody>
              {lines.map((l, i) => {
                const f = l.fields ?? {};
                return (
                  <tr
                    key={`${l.time}-${i}`}
                    className="hover:bg-hover align-top"
                  >
                    <td
                      className="text-faint px-2 whitespace-nowrap"
                      title={l.time}
                    >
                      {new Date(l.time).toLocaleTimeString()}
                    </td>
                    <td className={cn("px-1", statusClass(f.status))}>
                      {f.status}
                    </td>
                    <td className="text-muted px-1">{f.method}</td>
                    <td className="w-full px-1 break-all">
                      {showService && (
                        <span className="text-faint">{f.host}</span>
                      )}
                      {f.path}
                    </td>
                    <td className="text-muted px-1 text-right whitespace-nowrap">
                      {f.duration_ms}ms
                    </td>
                    <td className="text-faint px-1 whitespace-nowrap">
                      {f.client}
                    </td>
                    <td
                      className="text-faint px-2 whitespace-nowrap"
                      title={`answered by ${f.upstream ?? "—"} on ${l.node}`}
                    >
                      {showService
                        ? `${l.project}/${l.environment}/${l.service}`
                        : f.upstream}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </Panel>
  );
}

/** Network › Traffic: every route, the live map and the request tail (§5.7). */
export function TrafficPage() {
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Network"]} title="Traffic" />
      <TrafficMap />
      <TrafficPanel path="/traffic" scope="all" />
      <RequestsTail filter={{}} />
    </div>
  );
}
