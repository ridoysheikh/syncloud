import { useCallback, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { statusQuery } from "@/lib/auth";
import { backupQuery } from "@/lib/backups";
import { readPref, writePref } from "@/lib/storage";
import { useStreamTopic, type StreamEvent } from "@/lib/stream";
import { bytes, pct, since, useNodes, type Node } from "@/lib/nodes";
import {
  serviceState,
  serviceUrl,
  useProjects,
  useServices,
  type Service,
} from "@/lib/workloads";
import { HistoryChart, type HistorySeries } from "@/charts/HistoryChart";
import { SERIES } from "@/charts/theme";
import { useActiveAlerts } from "@/modules/monitoring/AlertsPage";
import { formatBytes } from "@/modules/projects/MetricsPanel";
import { fmtMs, fmtRps, statusColors } from "@/modules/traffic/Traffic";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { RangePicker, refetchFor, type Range } from "@/ui/RangePicker";
import { Alert, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { NodesTable } from "@/entities/nodes";

// Module routes are registered at runtime, so links to them use plain strings.
const to = (p: string): string => p;

interface Series {
  key: string;
  node: string;
  points: [number, number][];
}

interface OverviewMetrics {
  start: string;
  end: string;
  stepSeconds: number;
  charts: Partial<
    Record<
      | "cpu"
      | "memory"
      | "disk"
      | "load"
      | "tasks"
      | "network"
      | "requests"
      | "latency"
      | "controllerHeap"
      | "controllerGoroutines",
      Series[]
    >
  >;
}

interface ControllerStats {
  goroutines: number;
  heapMB: number;
  uptimeSec: number;
}

interface Build {
  id: string;
  project: string;
  environment: string;
  service: string;
  sha: string;
  status: string;
  message: string;
  createdAt: string;
}

interface AuditEntry {
  id: number;
  at: string;
  actor: string;
  action: string;
  resource: string;
}

interface Certificate {
  host: string;
  status: string;
  notAfter: string | null;
  lastError: string;
}

interface Repository {
  name: string;
  tags: number;
  pulls: number;
  lastPushedAt: string | null;
}

interface ServiceHealth {
  serviceId: string;
  project: string;
  environment: string;
  service: string;
  state: string;
  uptime24h: number | null;
}

function formatUptime(sec: number) {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m ${sec % 60}s`;
}

const fmtPct = (v: number) => `${+v.toFixed(v < 10 ? 1 : 0)}%`;
const fmtCount = (v: number) => `${Math.round(v)}`;
const fmtLoad = (v: number) => v.toFixed(2);
const fmtRate = (v: number) => `${formatBytes(v)}/s`;
const fmtReq = (v: number) => `${fmtRps(v)}/s`;

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
  return {
    cores,
    cpu: cores ? cpuWeighted / cores : 0,
    memUsed,
    memTotal,
    diskUsed,
    diskTotal,
  };
}

/** The newest finite value of a series' points. */
function latest(points: [number, number][] | undefined) {
  for (let i = (points?.length ?? 0) - 1; i >= 0; i--) {
    const v = points![i]![1];
    if (Number.isFinite(v)) return v;
  }
  return undefined;
}

/** A small read-only list that survives missing permissions (403s). */
function useList<T>(key: string, path: string, refetchInterval = 60_000) {
  return useQuery({
    queryKey: ["overview", key],
    queryFn: async () => (await api<{ items: T[] }>("GET", path)).items,
    refetchInterval,
    retry: false,
  });
}

export function OverviewPage() {
  const [range, setRangeState] = useState<Range>(() =>
    readPref("overview.range", "1h"),
  );
  const setRange = (r: Range) => {
    setRangeState(r);
    writePref("overview.range", r);
  };

  const { data: nodes = [], isLoading } = useNodes();
  const { data: services = [] } = useServices();
  const { data: projects = [] } = useProjects();
  const { data: alerts = [] } = useActiveAlerts();
  const { data: backups } = useQuery(backupQuery);
  const { data: certs } = useList<Certificate>("certs", "/certificates");
  const { data: incidents } = useList<{ id: string; service: string }>(
    "incidents",
    "/health/incidents?open=true",
    30_000,
  );
  const { data: builds } = useList<Build>("builds", "/builds", 30_000);

  const metrics = useQuery({
    queryKey: ["overview", "metrics", range],
    queryFn: () =>
      api<OverviewMetrics>("GET", `/metrics/overview?range=${range}`),
    refetchInterval: refetchFor(range),
    retry: false,
  });

  const t = totals(nodes);
  const ready = nodes.filter((n) => n.status === "ready").length;
  const unhealthyNodes = nodes.filter(
    (n) => n.status === "suspect" || n.status === "not_ready",
  );
  const tasks = services.reduce((n, sv) => n + sv.running, 0);
  const desired = services.reduce((n, sv) => n + sv.desiredCount, 0);
  const states = services.map((sv) => ({ sv, st: serviceState(sv) }));
  const healthy = states.filter((x) => x.st.tone === "ok").length;
  const troubled = states.filter(
    (x) => x.st.tone === "bad" || x.st.tone === "warn",
  );

  // Latest traffic figures, from the newest point of each chart.
  const c = metrics.data?.charts;
  const rpsBy = (cls?: string) =>
    (c?.requests ?? [])
      .filter((s) => !cls || s.key === cls)
      .reduce((n, s) => n + (latest(s.points) ?? 0), 0);
  const rps = rpsBy();
  const errors = rpsBy("5xx");
  const p95 = latest(c?.latency?.find((s) => s.key === "p95")?.points);
  const p50 = latest(c?.latency?.find((s) => s.key === "p50")?.points);

  const firing = alerts.filter((a) => a.state === "firing");
  const certProblems = (certs ?? []).filter(
    (x) =>
      x.lastError !== "" ||
      (x.notAfter && Date.parse(x.notAfter) - Date.now() < 14 * 864e5),
  );
  const failedBuilds = (builds ?? []).filter(
    (b) =>
      b.status === "failed" && Date.now() - Date.parse(b.createdAt) < 864e5,
  );

  const attention: { tone: "bad" | "warn"; text: string; href: string }[] = [];
  for (const n of unhealthyNodes)
    attention.push({
      tone: n.status === "not_ready" ? "bad" : "warn",
      text: `Node ${n.name} is ${n.status === "not_ready" ? "not ready" : "suspect"}`,
      href: "/compute/nodes",
    });
  for (const a of firing)
    attention.push({
      tone: a.severity === "critical" ? "bad" : "warn",
      text: `Alert: ${a.label || a.rule}`,
      href: "/monitoring/alerts",
    });
  for (const { sv, st } of troubled)
    attention.push({
      tone: st.tone === "bad" ? "bad" : "warn",
      text: `${sv.project}/${sv.environment}/${sv.name} is ${st.label} (${sv.running}/${sv.desiredCount} running)`,
      href: serviceUrl(sv),
    });
  if (incidents?.length)
    attention.push({
      tone: "bad",
      text: `${incidents.length} open incident${incidents.length > 1 ? "s" : ""}`,
      href: "/health/incidents",
    });
  if (failedBuilds.length)
    attention.push({
      tone: "warn",
      text: `${failedBuilds.length} build${failedBuilds.length > 1 ? "s" : ""} failed in the last 24 hours`,
      href: "/git/builds",
    });
  if (certProblems.length)
    attention.push({
      tone: "warn",
      text: `${certProblems.length} certificate${certProblems.length > 1 ? "s" : ""} failing or expiring within 14 days`,
      href: "/settings/domains",
    });
  if (backups && (!backups.configured || backups.status.lastError))
    attention.push({
      tone: backups.configured ? "bad" : "warn",
      text: backups.configured
        ? "Backups are failing"
        : "The controller is not backed up",
      href: "/settings/backups",
    });

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Cluster"]}
        title="Overview"
        status={
          <StatusBadge
            tone={
              attention.some((a) => a.tone === "bad")
                ? "bad"
                : attention.length
                  ? "warn"
                  : nodes.length
                    ? "ok"
                    : "neutral"
            }
          >
            {attention.length
              ? `${attention.length} need${attention.length === 1 ? "s" : ""} attention`
              : nodes.length
                ? "Healthy"
                : "Waiting for data"}
          </StatusBadge>
        }
        actions={<RangePicker range={range} setRange={setRange} />}
      />

      <div
        className={cn(
          "grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-8",
          gap,
        )}
      >
        <StatTile
          label="Nodes ready"
          value={`${ready}/${nodes.length}`}
          tone={unhealthyNodes.length ? "warn" : ready ? "ok" : undefined}
          hint={t.cores ? `${t.cores} cores` : "no node metrics yet"}
        />
        <StatTile
          label="CPU"
          value={t.cores ? t.cpu.toFixed(0) : "—"}
          unit={t.cores ? "%" : undefined}
          tone={t.cpu > 85 ? "warn" : undefined}
          hint="across ready nodes"
        />
        <StatTile
          label="Memory"
          value={t.memTotal ? pct(t.memUsed, t.memTotal).toFixed(0) : "—"}
          unit={t.memTotal ? "%" : undefined}
          tone={pct(t.memUsed, t.memTotal) > 90 ? "warn" : undefined}
          hint={
            t.memTotal
              ? `${bytes(t.memUsed)} of ${bytes(t.memTotal)}`
              : undefined
          }
        />
        <StatTile
          label="Disk"
          value={t.diskTotal ? pct(t.diskUsed, t.diskTotal).toFixed(0) : "—"}
          unit={t.diskTotal ? "%" : undefined}
          tone={pct(t.diskUsed, t.diskTotal) > 85 ? "warn" : undefined}
          hint={
            t.diskTotal
              ? `${bytes(t.diskUsed)} of ${bytes(t.diskTotal)}`
              : undefined
          }
        />
        <StatTile
          label="Services"
          value={services.length ? `${healthy}/${services.length}` : "0"}
          tone={troubled.length ? "warn" : healthy ? "ok" : undefined}
          hint={
            services.length
              ? `healthy · ${projects.length} project${projects.length === 1 ? "" : "s"}`
              : "none yet"
          }
        />
        <StatTile
          label="Tasks"
          value={tasks}
          tone={tasks < desired ? "warn" : undefined}
          hint={`${desired} desired`}
        />
        <StatTile
          label="Requests"
          value={metrics.data ? fmtRps(rps) : "—"}
          unit={metrics.data ? "/s" : undefined}
          tone={errors > 0 ? "warn" : undefined}
          hint={
            metrics.data
              ? rps
                ? `${((100 * errors) / rps).toFixed(1)}% 5xx`
                : "no traffic"
              : undefined
          }
        />
        <StatTile
          label="Latency p95"
          value={p95 !== undefined ? fmtMs(p95) : "—"}
          tone={p95 !== undefined && p95 > 1000 ? "warn" : undefined}
          hint={p50 !== undefined ? `p50 ${fmtMs(p50)}` : "no traffic"}
        />
      </div>

      {attention.length > 0 && <AttentionList items={attention} />}

      <MetricsSection range={range} nodes={nodes} query={metrics} />

      {/* Columns pack the panels of different heights without holes. */}
      <div className="-mb-1.5 columns-1 gap-1.5 sm:-mb-2 sm:gap-2 md:-mb-3 md:gap-3 lg:-mb-4 lg:columns-2 lg:gap-4 2xl:columns-3">
        <Packed>
          <ProjectsPanel services={services} projects={projects} />
        </Packed>
        <Packed>
          <BuildsPanel builds={builds} />
        </Packed>
        <Packed>
          <HealthPanel />
        </Packed>
        <Packed>
          <ActivityPanel />
        </Packed>
        <Packed>
          <RegistryPanel />
        </Packed>
        <Packed>
          <CertificatesPanel certs={certs} />
        </Packed>
      </div>

      <Panel
        title={`Nodes (${nodes.length})`}
        flush
        actions={<MoreLink href="/compute/nodes">Manage</MoreLink>}
      >
        <NodesTable nodes={nodes} loading={isLoading} compact />
      </Panel>
    </div>
  );
}

/** Controller version and uptime; its own component so the 2s stream only re-renders this line. */
function ControllerHint() {
  const { data: status } = useQuery(statusQuery);
  const [stats, setStats] = useState<ControllerStats | null>(null);
  const onStats = useCallback(
    (e: StreamEvent<ControllerStats>) => setStats(e.data),
    [],
  );
  useStreamTopic("controller.stats", onStats);
  return (
    <Hint>
      controller {status?.version ?? "—"}
      {stats ? ` · up ${formatUptime(stats.uptimeSec)}` : ""}
    </Hint>
  );
}

/** One panel in a column layout: never split across columns. */
function Packed({ children }: { children: ReactNode }) {
  return (
    <div className="mb-1.5 break-inside-avoid empty:hidden sm:mb-2 md:mb-3 lg:mb-4">
      {children}
    </div>
  );
}

function MoreLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Link
      to={to(href)}
      className="text-accent flex items-center gap-0.5 text-xs hover:underline"
    >
      {children}
      <ChevronRight className="size-3" />
    </Link>
  );
}

function AttentionList({
  items,
}: {
  items: { tone: "bad" | "warn"; text: string; href: string }[];
}) {
  return (
    <Panel title={`Needs attention (${items.length})`} flush>
      <ul className="divide-line divide-y">
        {items.slice(0, 8).map((a, i) => (
          <li key={i}>
            <Link
              to={to(a.href)}
              className="hover:bg-hover/50 flex min-h-8 items-center gap-2 px-2 py-1 text-xs md:px-3"
            >
              <StatusBadge tone={a.tone}>
                {a.tone === "bad" ? "Problem" : "Warning"}
              </StatusBadge>
              <span className="min-w-0 flex-1 truncate">{a.text}</span>
              <ChevronRight className="text-faint size-3.5 shrink-0" />
            </Link>
          </li>
        ))}
      </ul>
    </Panel>
  );
}

/** Every history chart of the Overview, from one request. */
function MetricsSection({
  range,
  nodes,
  query,
}: {
  range: Range;
  nodes: Node[];
  query: {
    data?: OverviewMetrics;
    error: unknown;
    isLoading: boolean;
  };
}) {
  const { data, error } = query;
  const start = data ? Date.parse(data.start) : 0;
  const end = data ? Date.parse(data.end) : 0;

  // Colour follows the node, not its rank: every chart uses the same map.
  const nodeColors = useMemo(() => {
    const names = [...new Set(nodes.map((n) => n.name))].sort();
    return Object.fromEntries(
      names.map((n, i) => [n, SERIES[i % SERIES.length]!]),
    );
  }, [nodes]);

  const charts = useMemo(() => {
    const lines = (xs: Series[] | undefined): HistorySeries[] =>
      (xs ?? []).map((s) => ({ name: s.key, points: s.points }));
    const c = data?.charts;
    return {
      cpu: lines(c?.cpu),
      memory: lines(c?.memory),
      disk: lines(c?.disk),
      load: lines(c?.load),
      tasks: lines(c?.tasks),
      network: lines(c?.network),
      requests: lines(c?.requests),
      latency: lines(c?.latency),
      heap: lines(c?.controllerHeap),
      goroutines: lines(c?.controllerGoroutines),
    };
  }, [data]);
  const status = useMemo(statusColors, []);

  if (error) {
    return (
      <Alert tone="warn">
        Charts are unavailable:{" "}
        {error instanceof ApiError ? error.message : "metrics storage error"}
      </Alert>
    );
  }

  const chart = (
    title: string,
    series: HistorySeries[],
    format: (v: number) => string,
    opts: {
      colors?: Record<string, string>;
      max?: number;
      integer?: boolean;
      stack?: boolean;
      area?: boolean;
      hint?: string;
    } = {},
  ) => (
    <Panel title={title} actions={opts.hint && <Hint>{opts.hint}</Hint>}>
      {series.length === 0 ? (
        <div className="text-faint flex h-[160px] items-center justify-center text-xs">
          {data ? "No samples in this range yet" : "Loading…"}
        </div>
      ) : (
        <HistoryChart
          series={series}
          start={start}
          end={end}
          format={format}
          height={160}
          colors={opts.colors}
          max={opts.max}
          integer={opts.integer}
          stack={opts.stack}
          area={opts.area}
        />
      )}
    </Panel>
  );

  const grid = cn("grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3", gap);
  return (
    <>
      <Panel
        title="Nodes"
        actions={
          <Hint>
            {range} · {data ? `${data.stepSeconds}s resolution` : "loading"}
          </Hint>
        }
      >
        <div className={grid}>
          {chart("CPU", charts.cpu, fmtPct, { colors: nodeColors, max: 100 })}
          {chart("Memory", charts.memory, fmtPct, {
            colors: nodeColors,
            max: 100,
          })}
          {chart("Disk", charts.disk, fmtPct, { colors: nodeColors, max: 100 })}
          {chart("Load average (1m)", charts.load, fmtLoad, {
            colors: nodeColors,
          })}
          {chart("Running tasks", charts.tasks, fmtCount, {
            colors: nodeColors,
            integer: true,
          })}
          {chart("Network", charts.network, fmtRate, {
            hint: "all nodes",
          })}
        </div>
      </Panel>
      <Panel title="Traffic and controller" actions={<ControllerHint />}>
        <div
          className={cn("grid grid-cols-1 md:grid-cols-2 2xl:grid-cols-4", gap)}
        >
          {chart("Requests by status", charts.requests, fmtReq, {
            colors: status,
            stack: true,
            hint: "every route",
          })}
          {chart("Latency", charts.latency, fmtMs)}
          {chart("Controller heap", charts.heap, formatBytes)}
          {chart("Controller goroutines", charts.goroutines, fmtCount, {
            integer: true,
          })}
        </div>
      </Panel>
    </>
  );
}

function Hint({ children }: { children: ReactNode }) {
  return (
    <span className="text-faint truncate text-xs normal-case">{children}</span>
  );
}

function Row({
  href,
  left,
  right,
}: {
  href: string;
  left: ReactNode;
  right: ReactNode;
}) {
  return (
    <li>
      <Link
        to={to(href)}
        className="hover:bg-hover/50 flex h-8 items-center gap-2 px-2 text-xs md:px-3"
      >
        <span className="min-w-0 flex-1 truncate">{left}</span>
        <span className="text-muted flex shrink-0 items-center gap-2">
          {right}
        </span>
      </Link>
    </li>
  );
}

function ListPanel({
  title,
  href,
  empty,
  children,
  count,
}: {
  title: string;
  href: string;
  empty: string;
  children: ReactNode[];
  count?: number;
}) {
  return (
    <Panel
      title={count !== undefined ? `${title} (${count})` : title}
      flush
      actions={<MoreLink href={href}>All</MoreLink>}
    >
      {children.length ? (
        <ul className="divide-line divide-y">{children}</ul>
      ) : (
        <p className="text-faint px-2 py-3 text-xs md:px-3">{empty}</p>
      )}
    </Panel>
  );
}

function ProjectsPanel({
  services,
  projects,
}: {
  services: Service[];
  projects: { name: string; environments: string[] }[];
}) {
  const rows = projects.map((p) => {
    const svcs = services.filter((s) => s.project === p.name);
    const bad = svcs.filter((s) => {
      const t = serviceState(s).tone;
      return t === "bad" || t === "warn";
    }).length;
    return {
      name: p.name,
      envs: p.environments.length,
      services: svcs.length,
      tasks: svcs.reduce((n, s) => n + s.running, 0),
      bad,
    };
  });
  return (
    <ListPanel
      title="Projects"
      href="/projects"
      count={projects.length}
      empty="No projects yet."
    >
      {rows.slice(0, 8).map((r) => (
        <Row
          key={r.name}
          href={`/projects/${r.name}`}
          left={<span className="font-medium">{r.name}</span>}
          right={
            <>
              <span>
                {r.services} service{r.services === 1 ? "" : "s"} · {r.tasks}{" "}
                task{r.tasks === 1 ? "" : "s"} · {r.envs} env
                {r.envs === 1 ? "" : "s"}
              </span>
              <StatusBadge
                tone={r.bad ? "warn" : r.services ? "ok" : "neutral"}
              >
                {r.bad ? `${r.bad} degraded` : r.services ? "healthy" : "empty"}
              </StatusBadge>
            </>
          }
        />
      ))}
    </ListPanel>
  );
}

const buildTone = (s: string) =>
  s === "succeeded"
    ? "ok"
    : s === "failed"
      ? "bad"
      : s === "building"
        ? "info"
        : "neutral";

function BuildsPanel({ builds }: { builds: Build[] | undefined }) {
  if (!builds) return null;
  return (
    <ListPanel title="Recent builds" href="/git/builds" empty="No builds yet.">
      {builds.slice(0, 6).map((b) => (
        <Row
          key={b.id}
          href={serviceUrl({
            project: b.project,
            environment: b.environment,
            name: b.service,
          })}
          left={
            <>
              <span className="font-medium">
                {b.project}/{b.service}
              </span>{" "}
              <span className="text-faint font-mono">{b.sha.slice(0, 7)}</span>{" "}
              <span className="text-muted">{b.message.split("\n")[0]}</span>
            </>
          }
          right={
            <>
              <span>{since(b.createdAt)}</span>
              <StatusBadge tone={buildTone(b.status)}>{b.status}</StatusBadge>
            </>
          }
        />
      ))}
    </ListPanel>
  );
}

const healthTone: Record<string, "ok" | "warn" | "bad" | "info"> = {
  healthy: "ok",
  degraded: "warn",
  down: "bad",
  deploying: "info",
};

function HealthPanel() {
  const { data } = useList<ServiceHealth>("health", "/health/services", 30_000);
  if (!data) return null;
  // Worst uptime first: the services most worth a look.
  const rows = [...data].sort(
    (a, b) => (a.uptime24h ?? 101) - (b.uptime24h ?? 101),
  );
  return (
    <ListPanel
      title="Service health (24h uptime)"
      href="/health"
      count={data.length}
      empty="No health checks yet. Add one to a service's HTTP port."
    >
      {rows.slice(0, 6).map((h) => (
        <Row
          key={h.serviceId}
          href={serviceUrl({
            project: h.project,
            environment: h.environment,
            name: h.service,
          })}
          left={`${h.project}/${h.environment}/${h.service}`}
          right={
            <>
              <span className="font-mono tabular-nums">
                {h.uptime24h != null ? `${h.uptime24h.toFixed(2)}%` : "—"}
              </span>
              <StatusBadge tone={healthTone[h.state] ?? "neutral"}>
                {h.state || "unknown"}
              </StatusBadge>
            </>
          }
        />
      ))}
    </ListPanel>
  );
}

function ActivityPanel() {
  const { data } = useList<AuditEntry>("audit", "/audit?limit=8", 30_000);
  if (!data) return null;
  return (
    <ListPanel
      title="Recent activity"
      href="/iam/audit"
      empty="No activity yet."
    >
      {data.slice(0, 8).map((e) => (
        <Row
          key={e.id}
          href="/iam/audit"
          left={
            <>
              <span className="font-mono">{e.action}</span>{" "}
              <span className="text-faint">
                {e.resource.replace(/^srn:syncloud:/, "")}
              </span>
            </>
          }
          right={
            <>
              <span className="max-w-32 truncate">{e.actor}</span>
              <span>{since(e.at)}</span>
            </>
          }
        />
      ))}
    </ListPanel>
  );
}

function RegistryPanel() {
  const { data } = useList<Repository>("repos", "/registry/repositories");
  if (!data) return null;
  const rows = [...data].sort(
    (a, b) =>
      Date.parse(b.lastPushedAt ?? "0") - Date.parse(a.lastPushedAt ?? "0"),
  );
  return (
    <ListPanel
      title="Registry"
      href="/registry/repos"
      count={data.length}
      empty="No images pushed yet."
    >
      {rows.slice(0, 6).map((r) => (
        <Row
          key={r.name}
          href="/registry/repos"
          left={<span className="font-mono">{r.name}</span>}
          right={
            <>
              <span>
                {r.tags} tag{r.tags === 1 ? "" : "s"} · {r.pulls} pull
                {r.pulls === 1 ? "" : "s"}
              </span>
              <span>{r.lastPushedAt ? since(r.lastPushedAt) : "—"}</span>
            </>
          }
        />
      ))}
    </ListPanel>
  );
}

function CertificatesPanel({ certs }: { certs: Certificate[] | undefined }) {
  if (!certs) return null;
  const days = (c: Certificate) =>
    c.notAfter ? (Date.parse(c.notAfter) - Date.now()) / 864e5 : Infinity;
  const rows = [...certs].sort((a, b) => days(a) - days(b));
  return (
    <ListPanel
      title="Certificates"
      href="/settings/domains"
      count={certs.length}
      empty="No certificates yet."
    >
      {rows.slice(0, 6).map((c) => {
        const d = days(c);
        const tone = c.lastError
          ? "bad"
          : d < 14
            ? "warn"
            : c.status === "valid"
              ? "ok"
              : "neutral";
        return (
          <Row
            key={c.host}
            href="/settings/domains"
            left={<span className="font-mono">{c.host}</span>}
            right={
              <>
                <span>
                  {Number.isFinite(d) ? `${Math.floor(d)} days left` : "—"}
                </span>
                <StatusBadge tone={tone}>
                  {c.lastError
                    ? "failing"
                    : (c.status || "valid").replace("_", "-")}
                </StatusBadge>
              </>
            }
          />
        );
      })}
    </ListPanel>
  );
}
