import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Check, Copy, Eye, EyeOff, RefreshCw, Trash2 } from "lucide-react";
import { usePaged } from "@/lib/paged";
import { api, ApiError } from "@/lib/api";
import { bytes, since } from "@/lib/nodes";
import {
  dbPath,
  healthTone,
  useDatabases,
  type Database,
  type DatabaseMember,
} from "@/lib/databases";
import { HistoryChart, type HistorySeries } from "@/charts/HistoryChart";
import { formatBytes } from "@/modules/projects/MetricsPanel";
import { LogsView } from "@/modules/logs/LogsView";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable, type Column } from "@/ui/DataTable";
import { RangePicker, refetchFor, type Range } from "@/ui/RangePicker";
import { Tabs } from "@/ui/Tabs";
import { Alert, Button, IconButton, StatusBadge } from "@/ui/controls";
import { NodeLink } from "@/entities/nodes";
import { TaskState } from "@/entities/tasks";
import { cn, gap } from "@/ui/cn";
import { fmtOps } from "./DatabasesPage";
import { CapacityStep, DataStep, specOf } from "./NewDatabaseWizard";
import { Console, Explorer } from "./Explorer";
import { PgDataStep } from "./PostgresSteps";
import { PgBackupsTab } from "./pg/Backups";
import { PgConfigTab, PgReplicationTab } from "./pg/Config";
import { PgConsole } from "./pg/Console";
import { PgDatabases, PgSessions } from "./pg/Databases";
import { PgExplorer } from "./pg/Explorer";
import { PgRoles } from "./pg/Roles";
import { AccessEditor, allowList, PublicFields } from "./Network";
import { confirmAction, confirmDialog } from "@/ui/dialogs";

type Tab =
  | "overview"
  | "connectivity"
  | "metrics"
  | "databases"
  | "roles"
  | "explorer"
  | "console"
  | "sessions"
  | "backups"
  | "replication"
  | "configuration"
  | "autoscaling"
  | "logs"
  | "settings";

const valkeyTabs: Tab[] = [
  "overview",
  "connectivity",
  "metrics",
  "explorer",
  "console",
  "autoscaling",
  "logs",
  "settings",
];
// Metrics and autoscaling for PostgreSQL come with Phase 13d.
const postgresTabs: Tab[] = [
  "overview",
  "connectivity",
  "databases",
  "roles",
  "explorer",
  "console",
  "sessions",
  "replication",
  "backups",
  "configuration",
  "logs",
  "settings",
];

const errText = (e: unknown) =>
  e instanceof ApiError ? e.message : "Request failed";

function formatUptime(sec: number) {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}

/** One managed database (Phase 12). */
export function DatabasePage() {
  const { name } = useParams({ strict: false }) as { name: string };
  const { data: all, isLoading } = useDatabases();
  const d = all?.find((x) => x.name === name);
  const [tab, setTabState] = useState<Tab>(
    () =>
      (new URLSearchParams(window.location.search).get("tab") as Tab | null) ??
      "overview",
  );
  const setTab = (t: Tab) => {
    setTabState(t);
    // Keep the tab in the URL, so going back from a role page lands here.
    const u = new URL(window.location.href);
    u.searchParams.set("tab", t);
    window.history.replaceState(window.history.state, "", u);
  };
  const path = dbPath(name);
  // The database inside a PostgreSQL cluster the explorer and console use.
  const [pgDb, setPgDb] = useState(name.replace(/-/g, "_"));

  if (!d) {
    return isLoading ? null : (
      <Alert tone="warn">Database {name} not found.</Alert>
    );
  }
  const projectTo: string = `/projects/${d.project}/${d.environment}`;
  const pg = d.engine === "postgres";
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={[
          <Link to={"/databases" as string} className="hover:text-fg">
            Databases
          </Link>,
          d.standalone ? (
            <span>standalone</span>
          ) : (
            <Link to={projectTo} className="hover:text-fg">
              {d.project} / {d.environment}
            </Link>
          ),
        ]}
        title={d.name}
        status={
          <span className="flex items-center gap-1">
            <StatusBadge tone={healthTone[d.health]}>{d.health}</StatusBadge>
            <span className="text-faint text-xs">
              {d.engine === "valkey" ? "Valkey" : d.engine} {d.version}
            </span>
            {d.public.enabled && (
              <StatusBadge tone={d.public.available ? "info" : "warn"}>
                public
              </StatusBadge>
            )}
          </span>
        }
        actions={<FailoverButton d={d} path={path} />}
      />
      {d.status && <Alert tone="warn">{d.status}</Alert>}
      <Tabs
        tabs={pg ? postgresTabs : valkeyTabs}
        value={tab}
        onChange={setTab}
      />
      {tab === "overview" && <Overview d={d} path={path} />}
      {tab === "connectivity" && <Connectivity d={d} path={path} />}
      {tab === "metrics" && !pg && <Metrics path={path} />}
      {tab === "databases" && pg && (
        <PgDatabases
          path={path}
          onExplore={(db) => {
            setPgDb(db);
            setTab("explorer");
          }}
        />
      )}
      {tab === "roles" && pg && <PgRoles path={path} name={d.name} />}
      {tab === "explorer" &&
        (pg ? (
          <PgExplorer path={path} db={pgDb} setDb={setPgDb} />
        ) : (
          <Explorer path={path} />
        ))}
      {tab === "console" &&
        (pg ? (
          <PgConsole path={path} db={pgDb} setDb={setPgDb} />
        ) : (
          <Console path={path} />
        ))}
      {tab === "sessions" && pg && <PgSessions path={path} />}
      {tab === "backups" && pg && <PgBackupsTab d={d} path={path} />}
      {tab === "replication" && pg && <PgReplicationTab path={path} />}
      {tab === "configuration" && pg && <PgConfigTab d={d} path={path} />}
      {tab === "autoscaling" && !pg && <Autoscaling d={d} path={path} />}
      {tab === "logs" && <LogsView filter={{ database: d.name }} />}
      {tab === "settings" && <Settings d={d} path={path} />}
    </div>
  );
}

function FailoverButton({ d, path }: { d: Database; path: string }) {
  const fo = useMutation({ mutationFn: () => api("POST", `${path}/failover`) });
  if (d.state.replicas === 0) return null;
  return (
    <div className="flex items-center gap-2">
      {fo.error && (
        <span className="text-bad text-xs">{errText(fo.error)}</span>
      )}
      {fo.isSuccess && (
        <span className="text-ok text-xs">Failover requested</span>
      )}
      <Button
        disabled={fo.isPending}
        onClick={async () =>
          (await confirmAction(
            `Fail over ${d.name}? A replica becomes primary; clients reconnect within seconds.`,
          )) && fo.mutate()
        }
      >
        <RefreshCw className="size-3.5" /> Failover
      </Button>
    </div>
  );
}

function Overview({ d, path }: { d: Database; path: string }) {
  if (d.engine === "postgres") return <PgOverview d={d} path={path} />;
  const u = d.usage;
  const memPct = u.maxMemoryBytes
    ? (100 * u.usedMemoryBytes) / u.maxMemoryBytes
    : 0;
  return (
    <>
      <div
        className={cn("grid grid-cols-2 sm:grid-cols-3 xl:grid-cols-6", gap)}
      >
        <StatTile
          label="Memory"
          value={memPct.toFixed(0)}
          unit="%"
          tone={memPct > 90 ? "bad" : memPct > 75 ? "warn" : undefined}
          hint={`${bytes(u.usedMemoryBytes)} of ${d.state.memoryMiB} MiB`}
        />
        <StatTile label="Keys" value={u.keys.toLocaleString()} />
        <StatTile label="Operations" value={fmtOps(u.opsPerSec)} unit="/s" />
        <StatTile label="Clients" value={u.clients} />
        <StatTile
          label="Hit rate"
          value={u.hitRate < 0 ? "—" : u.hitRate.toFixed(1)}
          unit={u.hitRate < 0 ? undefined : "%"}
          hint={u.hitRate < 0 ? "no lookups yet" : "of key lookups"}
        />
        <StatTile
          label="Replicas"
          value={d.state.replicas}
          hint={`${d.spec.replicas.min}–${d.spec.replicas.max} · up ${formatUptime(u.uptimeSeconds)}`}
        />
      </div>
      <Connection d={d} path={path} />
      <Members d={d} />
    </>
  );
}

/** A PostgreSQL cluster's overview: topology and replication. */
function PgOverview({ d, path }: { d: Database; path: string }) {
  const data = d.members.filter((m) => m.kind === "data");
  const leader = data.find((m) => m.role === "primary");
  const maxLag = Math.max(0, ...data.map((m) => m.lagBytes));
  return (
    <>
      <div className={cn("grid grid-cols-2 sm:grid-cols-4", gap)}>
        <StatTile
          label="Leader"
          value={leader ? leader.name : "—"}
          tone={leader ? undefined : "bad"}
          hint={leader ? `on ${leader.node}` : "no leader"}
        />
        <StatTile
          label="Replicas"
          value={`${data.filter((m) => m.role === "replica" && m.linkUp).length}/${d.state.replicas}`}
          hint="streaming"
        />
        <StatTile
          label="Replication lag"
          value={bytes(maxLag)}
          tone={maxLag > 64 << 20 ? "warn" : undefined}
          hint={
            (d.spec.postgres?.replication?.mode ??
              (d.spec.postgres?.synchronous ? "sync" : "async")) === "async"
              ? "asynchronous"
              : "synchronous"
          }
        />
        <StatTile
          label="Size"
          value={`${d.state.memoryMiB} MiB`}
          hint={`${d.spec.cpu} CPU · ${d.spec.postgres?.maxConnections ?? 0} connections`}
        />
      </div>
      <Connection d={d} path={path} />
      <Members d={d} />
    </>
  );
}

function CopyButton({ value }: { value: string }) {
  const [done, setDone] = useState(false);
  return (
    <IconButton
      label="Copy"
      onClick={() =>
        navigator.clipboard?.writeText(value).then(() => {
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        })
      }
    >
      {done ? (
        <Check className="text-ok size-3.5" />
      ) : (
        <Copy className="size-3.5" />
      )}
    </IconButton>
  );
}

function Line({
  label,
  value,
  mono = true,
  copy = true,
}: {
  label: string;
  value: string;
  mono?: boolean;
  /** Off while a secret is hidden, so the dots are never copied. */
  copy?: boolean;
}) {
  return (
    <div className="flex min-w-0 items-center gap-2 text-xs">
      <span className="text-muted w-24 shrink-0">{label}</span>
      <code
        className={cn("min-w-0 flex-1 truncate", mono && "font-mono")}
        title={value}
      >
        {value}
      </code>
      {copy ? (
        <CopyButton value={value} />
      ) : (
        <span className="size-7 shrink-0" />
      )}
    </div>
  );
}

interface Credentials {
  host: string;
  readHost: string;
  port: number;
  username: string;
  password: string;
  url: string;
  readUrl: string;
  haUrl?: string;
  publicUrl?: string;
  publicReadUrl?: string;
}

function Connection({ d, path }: { d: Database; path: string }) {
  const [show, setShow] = useState(false);
  const creds = useQuery({
    queryKey: ["db-credentials", path],
    queryFn: () => api<Credentials>("GET", `${path}/credentials`),
    enabled: show,
    staleTime: Infinity,
  });
  const c = creds.data;
  const hidden = "•".repeat(16);
  const pg = d.engine === "postgres";
  const url = (host = "", port = d.port, tls = false) =>
    pg
      ? `postgresql://app:${hidden}@${host}:${port}/${d.name.replace(/-/g, "_")}${tls ? "?sslmode=require" : ""}`
      : `${tls ? "rediss" : "redis"}://default:${hidden}@${host}:${port}`;
  return (
    <Panel
      title="Connect"
      actions={
        <Button variant="ghost" onClick={() => setShow((s) => !s)}>
          {show ? (
            <EyeOff className="size-3.5" />
          ) : (
            <Eye className="size-3.5" />
          )}
          {show ? "Hide password" : "Show password"}
        </Button>
      }
    >
      <div className="flex flex-col gap-1.5">
        <Line label="Read-write" value={`${d.host}:${d.port}`} />
        <Line label="Read-only" value={`${d.readHost}:${d.port}`} />
        <Line label="Username" value={pg ? "app" : "default"} />
        {pg && <Line label="Database" value={d.name.replace(/-/g, "_")} />}
        <Line
          label="Password"
          value={show && c ? c.password : hidden}
          copy={show && !!c}
        />
        <Line
          label="URL"
          value={show && c ? c.url : url(d.host)}
          copy={show && !!c}
        />
        <Line
          label="Read URL"
          value={show && c ? c.readUrl : url(d.readHost)}
          copy={show && !!c}
        />
        {pg && (
          <Line
            label="HA URL"
            value={
              show && c?.haUrl
                ? c.haUrl
                : "postgresql://app:" +
                  hidden +
                  "@m0…,m1…/" +
                  d.name.replace(/-/g, "_") +
                  "?target_session_attrs=read-write"
            }
            copy={show && !!c?.haUrl}
          />
        )}
        {d.public.enabled && d.public.available && (
          <>
            <Line
              label="Public URL"
              value={
                show && c?.publicUrl
                  ? c.publicUrl
                  : url(d.public.host, d.public.port, true)
              }
              copy={show && !!c?.publicUrl}
            />
            <Line
              label="Public read"
              value={
                show && c?.publicReadUrl
                  ? c.publicReadUrl
                  : url(d.public.readHost, d.public.port, true)
              }
              copy={show && !!c?.publicReadUrl}
            />
          </>
        )}
        {creds.error && <Alert>{errText(creds.error)}</Alert>}
        <p className="text-faint text-xs">
          Inside the cluster, the services on the access list connect over the
          private network ({accessSummary(d)}).
          {d.public.enabled && d.public.available
            ? pg
              ? " From outside, use the public URL: TLS is required (sslmode=require)."
              : " From outside, use the public URL: TLS is required (rediss://, or redis-cli --tls)."
            : " Turn on the public endpoint under Connectivity to connect from outside the cluster."}{" "}
          {pg
            ? "Any PostgreSQL driver works. Send writes to the read-write host; the read-only host spreads reads over the replicas. The HA URL lists every member, so the driver finds the primary even while the controller is down."
            : "Any Redis client works. Send writes to the read-write host; the read-only host spreads reads over the replicas."}{" "}
          Revealing the password is recorded in the audit log.
        </p>
      </div>
    </Panel>
  );
}

const accessSummary = (d: Database) =>
  d.network.access.length ? d.network.access.join(", ") : "nobody is on it yet";

/** Who may connect: the internal access list and the public endpoint. */
function Connectivity({ d, path }: { d: Database; path: string }) {
  const qc = useQueryClient();
  const init = useMemo(
    () => ({
      access: d.network.access,
      public: d.network.public.enabled,
      allow: d.network.public.allow
        .filter((a) => a !== "0.0.0.0/0" && a !== "::/0")
        .join("\n"),
    }),
    [d.network],
  );
  const [f, setF] = useState(init);
  useEffect(() => setF(init), [init]);
  const save = useMutation({
    mutationFn: () =>
      api<Database>("PUT", `${path}/network`, {
        access: f.access,
        public: { enabled: f.public, allow: allowList(f.allow) },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["databases"] }),
  });
  const dirty = JSON.stringify(f) !== JSON.stringify(init);
  return (
    <div className={cn("grid grid-cols-1 items-start lg:grid-cols-2", gap)}>
      <Panel title="Endpoints">
        <div className="flex flex-col gap-1.5">
          <Line label="Internal" value={`${d.host}:${d.port}`} />
          <Line label="Internal read" value={`${d.readHost}:${d.port}`} />
          {d.public.enabled && d.public.available ? (
            <>
              <Line
                label="Public"
                value={`${d.public.host}:${d.public.port}`}
              />
              <Line
                label="Public read"
                value={`${d.public.readHost}:${d.public.port}`}
              />
            </>
          ) : (
            <p className="text-faint text-xs">
              {d.public.enabled
                ? `Public endpoint unavailable: ${d.public.reason}`
                : "No public endpoint: only the access list below can connect."}
            </p>
          )}
          <p className="text-faint pt-1 text-xs">
            The public endpoint is served by Traefik on the controller and edge
            nodes. It terminates TLS with the platform's certificate for the
            host name and forwards to the current primary (the replicas for the
            read-only host), following failovers within seconds.
          </p>
        </div>
      </Panel>
      <Panel title="Access">
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <h3 className="text-muted text-xs font-medium">
              Inside the cluster
            </h3>
            <AccessEditor
              value={f.access}
              onChange={(v) => setF((x) => ({ ...x, access: v }))}
            />
          </div>
          <div className="flex flex-col gap-2">
            <h3 className="text-muted text-xs font-medium">
              From outside the cluster
            </h3>
            <PublicFields
              name={d.name}
              enabled={f.public}
              allow={f.allow}
              port={d.public.port || d.port}
              engine={d.engine}
              onEnabled={(v) => setF((x) => ({ ...x, public: v }))}
              onAllow={(v) => setF((x) => ({ ...x, allow: v }))}
            />
          </div>
          {save.error && <Alert>{errText(save.error)}</Alert>}
          <div className="flex items-center gap-2">
            <Button
              variant="primary"
              disabled={!dirty || save.isPending}
              onClick={() => save.mutate()}
            >
              Save
            </Button>
            <span className="text-faint text-xs">
              Applies within seconds, without restarts.
            </span>
          </div>
        </div>
      </Panel>
    </div>
  );
}

function Members({ d }: { d: Database }) {
  const roleTone = {
    primary: "ok",
    replica: "info",
    sentinel: "neutral",
    "": "neutral",
  } as const;
  const columns: Column<DatabaseMember>[] = [
    {
      header: "Member",
      cell: (m) => <span className="font-mono">{m.name}</span>,
    },
    {
      header: "Role",
      cell: (m) =>
        m.role ? (
          <StatusBadge tone={roleTone[m.role]}>{m.role}</StatusBadge>
        ) : (
          "—"
        ),
    },
    {
      header: "Node",
      cell: (m) => <NodeLink name={m.node} />,
    },
    {
      header: "State",
      cell: (m) => <TaskState state={m.state} compact />,
    },
    // Live stats come from the Valkey probe; PostgreSQL metrics are Phase 13d.
    ...(d.engine === "valkey"
      ? ([
          {
            header: "Memory",
            cell: (m) => (m.kind === "data" ? bytes(m.usedMemoryBytes) : "—"),
          },
          {
            header: "Ops/s",
            cell: (m) => (m.kind === "data" ? fmtOps(m.opsPerSec) : "—"),
          },
          {
            header: "CPU",
            cell: (m) =>
              m.kind === "data" ? `${m.cpuPercent.toFixed(1)}%` : "—",
          },
          {
            header: "Clients",
            cell: (m) => (m.kind === "data" ? m.clients : "—"),
          },
        ] as Column<DatabaseMember>[])
      : []),
    {
      header: "Replication",
      cell: (m) =>
        m.role === "replica" ? (
          <span className={m.linkUp ? "text-muted" : "text-warn"}>
            {m.linkUp
              ? `${d.engine === "postgres" ? "streaming" : "in sync"} · lag ${bytes(m.lagBytes)}`
              : "link down"}
          </span>
        ) : (
          "—"
        ),
    },
    {
      header: "IP",
      cell: (m) => <span className="text-muted font-mono">{m.ip || "—"}</span>,
    },
    { header: "Created", cell: (m) => since(m.createdAt) },
  ];
  return (
    <Panel title={`Members (${d.members.length})`} flush>
      <DataTable
        columns={columns}
        rows={[...d.members].sort((a, b) =>
          a.kind === b.kind
            ? a.name.localeCompare(b.name)
            : a.kind === "data"
              ? -1
              : 1,
        )}
        rowKey={(m) => m.id}
      />
      {d.members.some((m) => m.error) && (
        <div className="border-line border-t px-2 py-1.5 text-xs md:px-3">
          {d.members
            .filter((m) => m.error)
            .map((m) => (
              <div key={m.id} className="text-warn">
                {m.name}: {m.error}
              </div>
            ))}
        </div>
      )}
    </Panel>
  );
}

interface History {
  start: string;
  end: string;
  stepSeconds: number;
  charts: Record<string, { key: string; points: [number, number][] }[]>;
}

const fmtPct = (v: number) => `${+v.toFixed(v < 10 ? 1 : 0)}%`;
const fmtRate = (v: number) => `${formatBytes(v)}/s`;

function Metrics({ path }: { path: string }) {
  const [range, setRange] = useState<Range>("1h");
  const { data, error } = useQuery({
    queryKey: ["db-metrics", path, range],
    queryFn: () => api<History>("GET", `${path}/metrics?range=${range}`),
    refetchInterval: refetchFor(range),
    retry: false,
  });
  const start = data ? Date.parse(data.start) : 0;
  const end = data ? Date.parse(data.end) : 0;
  const lines = (k: string): HistorySeries[] =>
    (data?.charts[k] ?? []).map((s) => ({ name: s.key, points: s.points }));
  const maxmem = data?.charts.maxmemory?.[0]?.points;
  const limit = maxmem?.length ? maxmem[maxmem.length - 1]![1] : undefined;
  const chart = (
    title: string,
    series: HistorySeries[],
    format: (v: number) => string,
    extra: {
      max?: number;
      integer?: boolean;
      markLine?: { value: number; label: string };
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
          {...extra}
        />
      )}
    </Panel>
  );
  return (
    <Panel
      title="History"
      actions={<RangePicker range={range} setRange={setRange} />}
    >
      {error ? (
        <Alert tone="warn">{errText(error)}</Alert>
      ) : (
        <div
          className={cn("grid grid-cols-1 md:grid-cols-2 2xl:grid-cols-3", gap)}
        >
          {chart("Operations per second", lines("ops"), (v) => fmtOps(v))}
          {chart("Memory used", lines("memory"), formatBytes, {
            markLine: limit
              ? { value: limit, label: `maxmemory ${formatBytes(limit)}` }
              : undefined,
          })}
          {chart("Keys", lines("keys"), (v) => Math.round(v).toLocaleString(), {
            integer: true,
          })}
          {chart("Clients", lines("clients"), (v) => `${Math.round(v)}`, {
            integer: true,
          })}
          {chart("Hit rate", lines("hitRate"), fmtPct, { max: 100 })}
          {chart(
            "Evicted and expired keys",
            lines("evictions"),
            (v) => `${v.toFixed(1)}/s`,
          )}
          {chart("Replication lag", lines("lag"), formatBytes)}
          {chart("CPU (% of one core)", lines("cpu"), fmtPct)}
          {chart("Network", lines("network"), fmtRate)}
        </div>
      )}
    </Panel>
  );
}

interface DbEvent {
  id: number;
  at: string;
  kind: string;
  from: string;
  to: string;
  reason: string;
  actor: string;
}

function Autoscaling({ d, path }: { d: Database; path: string }) {
  const qc = useQueryClient();
  const events = usePaged<DbEvent>(["db-events", path], `${path}/events`, {
    refetchInterval: 10_000,
  });
  const init = useMemo(
    () => ({
      memMin: d.spec.memory.min,
      memMax: d.spec.memory.max,
      repMin: d.spec.replicas.min,
      repMax: d.spec.replicas.max,
      cpu: String(d.spec.cpu),
      cpuTarget: String(d.spec.autoscaling.cpuTarget),
    }),
    [d.spec],
  );
  const [f, setF] = useState(init);
  useEffect(() => setF(init), [init]);
  const set = (k: string, v: unknown) => setF((x) => ({ ...x, [k]: v }));
  const save = useMutation({
    mutationFn: () =>
      api("PUT", path, {
        spec: specOf({
          ...f,
          persistence: d.spec.persistence,
          eviction: d.spec.evictionPolicy,
          nodes: d.spec.nodes ?? [],
        }),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["databases"] }),
  });
  const dirty = JSON.stringify(f) !== JSON.stringify(init);
  const a = d.autoscale;
  const columns: Column<DbEvent>[] = [
    {
      header: "When",
      cell: (e) => (
        <span title={new Date(e.at).toLocaleString()}>{since(e.at)}</span>
      ),
    },
    { header: "What", cell: (e) => e.kind },
    {
      header: "Change",
      cell: (e) => (e.from || e.to ? `${e.from || "—"} → ${e.to || "—"}` : "—"),
    },
    {
      header: "Why",
      cell: (e) => <span className="text-muted">{e.reason || "—"}</span>,
    },
    { header: "By", cell: (e) => e.actor },
  ];
  return (
    <>
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile
          label="Memory in use"
          value={a.memoryPercent.toFixed(0)}
          unit="%"
          hint={`grows above ${d.spec.autoscaling.memoryHigh}%, shrinks below 40%`}
        />
        <StatTile
          label="Memory now"
          value={d.state.memoryMiB}
          unit="MiB"
          hint={`${d.spec.memory.min}–${d.spec.memory.max} MiB`}
        />
        <StatTile
          label="Read CPU"
          value={a.readCpu.toFixed(0)}
          unit="%"
          hint={`target ${d.spec.autoscaling.cpuTarget}% of one core`}
        />
        <StatTile
          label="Replicas now"
          value={d.state.replicas}
          hint={`${d.spec.replicas.min}–${d.spec.replicas.max}`}
        />
      </div>
      {a.blocked && <Alert tone="warn">{a.blocked}</Alert>}
      <div
        className={cn(
          "grid grid-cols-1 items-start xl:grid-cols-[28rem_1fr]",
          gap,
        )}
      >
        <Panel title="Limits">
          <div className="flex flex-col gap-3">
            <CapacityStep f={f} set={set as never} />
            {save.error && <Alert>{errText(save.error)}</Alert>}
            <div className="flex items-center gap-2">
              <Button
                variant="primary"
                disabled={!dirty || save.isPending}
                onClick={() => save.mutate()}
              >
                Save
              </Button>
              <span className="text-faint text-xs">
                Memory and replicas change online. A maximum above the size the
                members were created for restarts them one at a time.
              </span>
            </div>
          </div>
        </Panel>
        <Panel title="Events" flush>
          <DataTable
            columns={columns}
            {...events.table}
            rows={events.items}
            rowKey={(e) => String(e.id)}
            empty={
              <p className="text-faint px-2 py-3 text-xs md:px-3">
                No events yet.
              </p>
            }
          />
        </Panel>
      </div>
    </>
  );
}

function Settings({ d, path }: { d: Database; path: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const init = useMemo(
    () => ({
      persistence: d.spec.persistence,
      eviction: d.spec.evictionPolicy,
      nodes: d.spec.nodes ?? [],
    }),
    [d.spec],
  );
  const [f, setF] = useState(init);
  useEffect(() => setF(init), [init]);
  const set = (k: string, v: unknown) => setF((x) => ({ ...x, [k]: v }));
  const save = useMutation({
    mutationFn: () =>
      api("PUT", path, {
        spec: specOf({
          memMin: d.spec.memory.min,
          memMax: d.spec.memory.max,
          repMin: d.spec.replicas.min,
          repMax: d.spec.replicas.max,
          cpu: String(d.spec.cpu),
          cpuTarget: String(d.spec.autoscaling.cpuTarget),
          engine: d.engine,
          postgres: d.spec.postgres,
          ...f,
        }),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["databases"] }),
  });
  const del = useMutation({
    mutationFn: () => api("DELETE", path),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["databases"] });
      void navigate({ to: "/databases" as string });
    },
  });
  const dirty = JSON.stringify(f) !== JSON.stringify(init);
  return (
    <div className={cn("grid grid-cols-1 items-start lg:grid-cols-2", gap)}>
      <Panel title="Data and placement">
        <div className="flex flex-col gap-3">
          {d.engine === "postgres" ? (
            <PgDataStep f={{ ...f, name: d.name }} set={set as never} />
          ) : (
            <DataStep f={f} set={set as never} />
          )}
          {save.error && <Alert>{errText(save.error)}</Alert>}
          <div className="flex items-center gap-2">
            <Button
              variant="primary"
              disabled={!dirty || save.isPending}
              onClick={() => save.mutate()}
            >
              Save
            </Button>
            <span className="text-faint text-xs">
              Restarts the members one at a time: replicas first, then the
              primary after a failover.
            </span>
          </div>
        </div>
      </Panel>
      <Panel title="Danger zone">
        <div className="flex flex-col gap-2 text-xs">
          <Detail
            k="Database ID"
            v={<span className="font-mono">{d.id}</span>}
          />
          <Detail k="Created" v={new Date(d.createdAt).toLocaleString()} />
          <div className="flex items-center justify-between gap-2 pt-2">
            <span className="text-muted">
              Deleting stops every member and deletes its data volumes. This
              cannot be undone.
            </span>
            <Button
              variant="danger"
              disabled={del.isPending}
              onClick={async () =>
                (await confirmDialog({
                  title: `Delete ${d.name}?`,
                  message:
                    "Every member stops and its data volumes are deleted. Backups in S3 stay. This cannot be undone.",
                  tone: "danger",
                  typeToConfirm: d.name,
                })) && del.mutate()
              }
            >
              <Trash2 className="size-3.5" /> Delete
            </Button>
          </div>
          {del.error && <Alert>{errText(del.error)}</Alert>}
        </div>
      </Panel>
    </div>
  );
}

function Detail({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div className="flex gap-2">
      <span className="text-muted w-24 shrink-0">{k}</span>
      <span className="min-w-0 break-words">{v}</span>
    </div>
  );
}
