import { serviceUrl } from "@/lib/workloads";
import { useEffect } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { HeartPulse, Siren } from "lucide-react";
import { api } from "@/lib/api";
import { subscribe } from "@/lib/stream";
import { since } from "@/lib/nodes";
import { Sparkline } from "@/charts/Sparkline";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface Incident {
  id: string;
  serviceId: string;
  project: string;
  environment: string;
  service: string;
  state: "degraded" | "down";
  cause: string;
  openedAt: string;
  closedAt: string | null;
}

interface ServiceHealth {
  serviceId: string;
  project: string;
  environment: string;
  service: string;
  state: "healthy" | "degraded" | "down" | "deploying" | "stopped";
  reason: string;
  serving: number;
  desired: number;
  uptime24h: number | null;
  uptime7d: number | null;
  uptime30d: number | null;
  lastCheck: {
    ok: boolean;
    status: number;
    latencyMs: number;
    error?: string;
    url: string;
  } | null;
  latency: { t: number; ms: number; ok: boolean }[];
  incident: Incident | null;
}

const tone = {
  healthy: "ok",
  degraded: "warn",
  down: "bad",
  deploying: "info",
  stopped: "neutral",
} as const;
const pct = (v: number | null) =>
  v == null ? "—" : `${v >= 99.995 ? "100" : v.toFixed(2)}%`;

function useHealth() {
  const qc = useQueryClient();
  useEffect(
    () =>
      subscribe("service.health", () =>
        qc.invalidateQueries({ queryKey: ["health"] }),
      ),
    [qc],
  );
  return useQuery({
    queryKey: ["health", "services"],
    queryFn: async () =>
      (await api<{ items: ServiceHealth[] }>("GET", "/health/services")).items,
    refetchInterval: 15000,
  });
}

/** Health and uptime of every service (§5.6). */
export function ServiceHealthPage() {
  const { data = [], isLoading } = useHealth();
  const count = (s: ServiceHealth["state"]) =>
    data.filter((x) => x.state === s).length;
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Health"]} title="Service health" />
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile
          label="Healthy"
          value={count("healthy")}
          tone={count("healthy") ? "ok" : undefined}
        />
        <StatTile
          label="Degraded"
          value={count("degraded")}
          tone={count("degraded") ? "warn" : undefined}
        />
        <StatTile
          label="Down"
          value={count("down")}
          tone={count("down") ? "bad" : undefined}
        />
        <StatTile
          label="Open incidents"
          value={data.filter((x) => x.incident).length}
          tone={data.some((x) => x.incident) ? "bad" : undefined}
        />
      </div>
      <Panel title="Services" flush>
        <DataTable
          rows={data}
          rowKey={(h) => h.serviceId}
          empty={
            !isLoading && (
              <EmptyState icon={HeartPulse} title="No services yet" />
            )
          }
          columns={[
            {
              header: "Service",
              cell: (h) => {
                const to: string = serviceUrl({
                  project: h.project,
                  environment: h.environment,
                  name: h.service,
                });
                return (
                  <Link
                    to={to}
                    className="hover:text-accent flex flex-col py-1"
                  >
                    <span className="font-medium">{h.service}</span>
                    <span className="text-faint">
                      {h.project} / {h.environment}
                    </span>
                  </Link>
                );
              },
            },
            {
              header: "State",
              cell: (h) => (
                <div className="flex flex-col items-start gap-0.5">
                  <StatusBadge tone={tone[h.state]}>{h.state}</StatusBadge>
                  {h.reason && (
                    <span
                      className="text-muted max-w-xs truncate"
                      title={h.reason}
                    >
                      {h.reason}
                    </span>
                  )}
                </div>
              ),
            },
            {
              header: "Serving",
              cell: (h) => (
                <span className="font-mono">{`${h.serving}/${h.desired}`}</span>
              ),
            },
            {
              header: "24h",
              cell: (h) => (
                <span className="font-mono">{pct(h.uptime24h)}</span>
              ),
            },
            {
              header: "7d",
              cell: (h) => (
                <span className="text-muted font-mono">{pct(h.uptime7d)}</span>
              ),
            },
            {
              header: "30d",
              cell: (h) => (
                <span className="text-muted font-mono">{pct(h.uptime30d)}</span>
              ),
            },
            {
              header: "Response time (1h)",
              className: "w-full",
              cell: (h) => (
                <div className="flex items-center gap-2">
                  <Sparkline points={h.latency} />
                  {h.lastCheck && (
                    <span
                      className={h.lastCheck.ok ? "text-muted" : "text-bad"}
                      title={h.lastCheck.url}
                    >
                      {h.lastCheck.ok
                        ? `${h.lastCheck.latencyMs.toFixed(0)} ms`
                        : h.lastCheck.error}
                    </span>
                  )}
                </div>
              ),
            },
          ]}
        />
      </Panel>
      <p className="text-faint text-xs">
        Every routed service is checked end to end through Traefik every 15
        seconds, with no setup. Only healthy tasks receive traffic.
      </p>
    </div>
  );
}

/** Incident timeline (§5.6). */
export function IncidentsPage() {
  const qc = useQueryClient();
  useEffect(
    () =>
      subscribe("service.health", () =>
        qc.invalidateQueries({ queryKey: ["incidents"] }),
      ),
    [qc],
  );
  const { data = [], isLoading } = useQuery({
    queryKey: ["incidents"],
    queryFn: async () =>
      (await api<{ items: Incident[] }>("GET", "/health/incidents")).items,
    refetchInterval: 30000,
  });
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Health"]} title="Incidents" />
      <Panel flush>
        <DataTable
          rows={data}
          rowKey={(i) => i.id}
          empty={
            !isLoading && (
              <EmptyState icon={Siren} title="No incidents">
                Incidents open when a service is degraded or down for 30
                seconds, and close on recovery.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Service",
              cell: (i) => (
                <span className="font-medium">{`${i.project}/${i.environment}/${i.service}`}</span>
              ),
            },
            {
              header: "State",
              cell: (i) => (
                <StatusBadge tone={i.state === "down" ? "bad" : "warn"}>
                  {i.state}
                </StatusBadge>
              ),
            },
            {
              header: "Opened",
              cell: (i) => (
                <span
                  className="text-muted"
                  title={new Date(i.openedAt).toLocaleString()}
                >
                  {since(i.openedAt)}
                </span>
              ),
            },
            {
              header: "Duration",
              cell: (i) =>
                i.closedAt ? (
                  <span className="text-muted">
                    {Math.max(
                      1,
                      Math.round(
                        (new Date(i.closedAt).getTime() -
                          new Date(i.openedAt).getTime()) /
                          60000,
                      ),
                    )}{" "}
                    min
                  </span>
                ) : (
                  <StatusBadge tone="bad">open</StatusBadge>
                ),
            },
            {
              header: "Cause",
              className: "w-full",
              cell: (i) => <span className="text-muted">{i.cause}</span>,
            },
          ]}
        />
      </Panel>
    </div>
  );
}
