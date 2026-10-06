import { Link } from "@tanstack/react-router";
import { Boxes, ExternalLink, Plus } from "lucide-react";
import { AWAITING_BUILD, useServices, type Service } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Button, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

export function serviceState(s: Service): {
  tone: "ok" | "warn" | "bad" | "info" | "neutral";
  label: string;
} {
  if (s.deleting) return { tone: "neutral", label: "deleting" };
  if (s.spec.image === AWAITING_BUILD)
    return { tone: "info", label: "awaiting build" };
  if (s.status) return { tone: "bad", label: "degraded" };
  if (s.desiredCount === 0) return { tone: "neutral", label: "stopped" };
  if (s.running >= s.desiredCount && s.pending === 0)
    return { tone: "ok", label: "healthy" };
  if (s.running === 0) return { tone: "warn", label: "starting" };
  return { tone: "info", label: "deploying" };
}

/** Every service in the cluster (§4). */
export function ServicesPage() {
  const { data: services = [], isLoading } = useServices();
  const newTo: string = "/compute/services/new";
  const running = services.reduce((n, s) => n + s.running, 0);
  const desired = services.reduce((n, s) => n + s.desiredCount, 0);
  const degraded = services.filter(
    (s) => serviceState(s).tone === "bad",
  ).length;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Compute"]}
        title="Services"
        actions={
          <Link to={newTo}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New service
            </Button>
          </Link>
        }
      />
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile label="Services" value={services.length} />
        <StatTile
          label="Tasks running"
          value={`${running}/${desired}`}
          tone={running < desired ? "warn" : running ? "ok" : undefined}
        />
        <StatTile
          label="Degraded"
          value={degraded}
          tone={degraded ? "bad" : undefined}
        />
        <StatTile
          label="Projects"
          value={new Set(services.map((s) => s.project)).size}
        />
      </div>
      <Panel title="Services" flush>
        <DataTable
          rows={services}
          rowKey={(s) => s.id}
          empty={
            !isLoading && (
              <EmptyState icon={Boxes} title="No services yet">
                A service keeps a number of containers running from one image,
                places them across nodes, and replaces them when they fail.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Service",
              cell: (s) => {
                const to: string = `/compute/services/${s.project}/${s.environment}/${s.name}`;
                return (
                  <Link
                    to={to}
                    className="hover:text-accent flex flex-col py-1"
                  >
                    <span className="font-medium">{s.name}</span>
                    <span className="text-faint">
                      {s.project} / {s.environment}
                    </span>
                  </Link>
                );
              },
            },
            {
              header: "State",
              cell: (s) => (
                <div className="flex flex-col items-start gap-0.5">
                  <StatusBadge tone={serviceState(s).tone}>
                    {serviceState(s).label}
                  </StatusBadge>
                  {s.status && (
                    <span
                      className="text-bad max-w-xs truncate"
                      title={s.status}
                    >
                      {s.status}
                    </span>
                  )}
                </div>
              ),
            },
            {
              header: "Tasks",
              cell: (s) => (
                <span className="font-mono">{`${s.running}/${s.desiredCount}`}</span>
              ),
            },
            {
              header: "Rev",
              cell: (s) => <span className="text-muted">{s.revision}</span>,
            },
            {
              header: "Image",
              cell: (s) => <span className="font-mono">{s.spec.image}</span>,
            },
            {
              header: "Endpoints",
              className: "w-full",
              cell: (s) =>
                s.endpoints.length ? (
                  <div className="flex flex-col">
                    {s.endpoints.map((e) => (
                      <a
                        key={e}
                        href={e}
                        target="_blank"
                        rel="noreferrer"
                        className="text-muted hover:text-accent inline-flex items-center gap-1 font-mono"
                      >
                        {e.replace(/^https?:\/\//, "")}{" "}
                        <ExternalLink className="size-3" />
                      </a>
                    ))}
                  </div>
                ) : (
                  <span className="text-faint">—</span>
                ),
            },
          ]}
        />
      </Panel>
    </div>
  );
}
