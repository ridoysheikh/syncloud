import { Link } from "@tanstack/react-router";
import { Database as DatabaseIcon, Globe, Plus } from "lucide-react";
import { bytes } from "@/lib/nodes";
import {
  dbUrl,
  healthTone,
  useDatabases,
  type Database,
} from "@/lib/databases";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable, type Column } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, StatusBadge } from "@/ui/controls";
import { Meter } from "@/ui/Meter";
import { cn, gap } from "@/ui/cn";
import { ApiError } from "@/lib/api";

export const fmtOps = (v: number) =>
  v >= 1000 ? `${(v / 1000).toFixed(1)}k` : v.toFixed(0);

/** Table of databases, used by the Databases page and a project's tab. */
export function DatabasesTable({
  items,
  loading,
  showProject = true,
}: {
  items: Database[];
  loading?: boolean;
  showProject?: boolean;
}) {
  const columns: Column<Database>[] = [
    {
      header: "Database",
      cell: (d) => (
        <Link to={dbUrl(d) as string} className="hover:text-accent font-medium">
          {d.name}
        </Link>
      ),
    },
    ...(showProject
      ? [
          {
            header: "Owner",
            cell: (d: Database) =>
              d.standalone ? (
                <span className="text-muted">standalone</span>
              ) : (
                <Link
                  to={`/projects/${d.project}/${d.environment}` as string}
                  className="hover:text-accent"
                >
                  {d.project} / {d.environment}
                </Link>
              ),
          },
        ]
      : []),
    {
      header: "Health",
      cell: (d) => (
        <StatusBadge tone={healthTone[d.health]}>{d.health}</StatusBadge>
      ),
    },
    {
      header: "Memory",
      cell: (d) => (
        <Meter
          className="min-w-48"
          value={
            d.usage.maxMemoryBytes
              ? (100 * d.usage.usedMemoryBytes) / d.usage.maxMemoryBytes
              : 0
          }
          label={`${bytes(d.usage.usedMemoryBytes)} / ${d.state.memoryMiB} MiB`}
        />
      ),
    },
    {
      header: "Replicas",
      cell: (d) => (
        <span className="tabular-nums">
          {d.state.replicas}
          <span className="text-faint">
            {" "}
            ({d.spec.replicas.min}–{d.spec.replicas.max})
          </span>
        </span>
      ),
    },
    { header: "Keys", cell: (d) => d.usage.keys.toLocaleString() },
    { header: "Ops/s", cell: (d) => fmtOps(d.usage.opsPerSec) },
    { header: "Clients", cell: (d) => d.usage.clients },
    {
      header: "Endpoint",
      cell: (d) => (
        <span className="text-muted flex items-center gap-1.5 font-mono">
          {d.public.enabled && d.public.available && (
            <span title={`Public: ${d.public.host}:${d.public.port} (TLS)`}>
              <Globe className="text-accent size-3.5" />
            </span>
          )}
          {d.public.enabled && d.public.available
            ? `${d.public.host}:${d.public.port}`
            : `${d.host}:${d.port}`}
        </span>
      ),
    },
  ];
  return (
    <DataTable
      columns={columns}
      rows={items}
      rowKey={(d) => d.id}
      empty={
        loading ? null : (
          <EmptyState icon={DatabaseIcon} title="No databases yet">
            Managed databases, standalone or in a project: Valkey
            (Redis-compatible) with failover, autoscaled memory and read
            replicas, and an optional public TLS endpoint.
          </EmptyState>
        )
      }
    />
  );
}

/** Every managed database, standalone and in projects (Phase 12). */
export function DatabasesPage() {
  const { data = [], isLoading, error } = useDatabases();
  const newTo: string = "/databases/new";
  const publicCount = data.filter((d) => d.public.enabled).length;
  const healthy = data.filter((d) => d.health === "healthy").length;
  const mem = data.reduce((n, d) => n + d.usage.usedMemoryBytes, 0);
  const ops = data.reduce((n, d) => n + d.usage.opsPerSec, 0);
  const keys = data.reduce((n, d) => n + d.usage.keys, 0);
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Databases"]}
        title="Databases"
        actions={
          <Link to={newTo}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New database
            </Button>
          </Link>
        }
      />
      {error && (
        <Alert tone="warn">
          {error instanceof ApiError
            ? error.message
            : "Could not load databases"}
        </Alert>
      )}
      <div className={cn("grid grid-cols-2 md:grid-cols-5", gap)}>
        <StatTile
          label="Databases"
          value={data.length ? `${healthy}/${data.length}` : 0}
          tone={
            data.length && healthy < data.length
              ? "warn"
              : healthy
                ? "ok"
                : undefined
          }
          hint="healthy"
        />
        <StatTile label="Memory used" value={bytes(mem)} />
        <StatTile label="Operations" value={fmtOps(ops)} unit="/s" />
        <StatTile label="Keys" value={keys.toLocaleString()} />
        <StatTile
          label="Public"
          value={publicCount}
          hint="with a TLS endpoint"
        />
      </div>
      <Panel title={`Databases (${data.length})`} flush>
        <DatabasesTable items={data} loading={isLoading} />
      </Panel>
    </div>
  );
}
