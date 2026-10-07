import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { GitBranch, Plug } from "lucide-react";
import { usePaged } from "@/lib/paged";
import { api } from "@/lib/api";
import { since } from "@/lib/nodes";
import { serviceUrl } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { StatTile } from "@/ui/StatTile";
import { Button, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import {
  BuildsTable,
  buildsRefetch,
  type Build,
  type Builder,
  type GitSource,
} from "@/modules/compute/BuildsPanel";

interface SourceSummary extends GitSource {
  project: string;
  environment: string;
  service: string;
  lastWebhookAt: string | null;
}

const builderShort: Record<Builder, string> = {
  auto: "automatic",
  dockerfile: "Dockerfile",
  nixpacks: "Nixpacks",
  static: "static",
};

function ServiceLink({
  s,
}: {
  s: { project: string; environment: string; service: string };
}) {
  const to: string = serviceUrl({
    project: s.project,
    environment: s.environment,
    name: s.service,
  });
  return (
    <Link
      to={to}
      search={{ tab: "builds" } as never}
      className="hover:text-accent whitespace-nowrap"
    >
      {s.project}/{s.environment}/{s.service}
    </Link>
  );
}

/** Every service built from Git, with its watch rule and last check (§5.8). */
export function GitSourcesPage() {
  const { data = [], isLoading } = useQuery({
    queryKey: ["git", "sources"],
    queryFn: async () =>
      (await api<{ items: SourceSummary[] }>("GET", "/git/sources")).items,
    refetchInterval: 15_000,
  });
  const failing = data.filter((s) => s.lastError).length;
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Git & Builds"]}
        title="Sources"
        actions={
          <Link to={"/integrations" as string}>
            <Button>
              <Plug className="size-3.5" /> Git providers
            </Button>
          </Link>
        }
      />
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile label="Sources" value={data.length} />
        <StatTile
          label="Failing checks"
          value={failing}
          tone={failing ? "bad" : undefined}
        />
      </div>
      <Panel title="Git sources" flush>
        <DataTable
          rows={data}
          rowKey={(s) => `${s.project}/${s.environment}/${s.service}`}
          empty={
            !isLoading && (
              <EmptyState icon={GitBranch} title="No Git sources">
                Create a service from a Git repository in a project, or connect
                one on a service's Builds tab.
              </EmptyState>
            )
          }
          columns={[
            { header: "Service", cell: (s) => <ServiceLink s={s} /> },
            {
              header: "Repository",
              cell: (s) =>
                s.repo ? (
                  <span className="font-mono break-all">
                    {s.repo}{" "}
                    <span className="text-faint font-sans">
                      via {s.connection}
                    </span>
                  </span>
                ) : (
                  <span className="font-mono break-all">
                    {s.url.replace(/^https?:\/\//, "")}
                  </span>
                ),
            },
            {
              header: "Watching",
              cell: (s) => (
                <span className="font-mono whitespace-nowrap">
                  {s.branch}
                  {s.tags && <span className="text-muted"> +{s.tags}</span>}
                  {s.paths?.length > 0 && (
                    <span className="text-faint" title={s.paths.join("\n")}>
                      {" "}
                      · {s.paths.length} path{s.paths.length > 1 ? "s" : ""}
                    </span>
                  )}
                </span>
              ),
            },
            {
              header: "Builder",
              cell: (s) => (
                <span className="text-muted">
                  {builderShort[s.builder ?? "auto"]}
                </span>
              ),
            },
            {
              header: "Deploys",
              cell: (s) => (
                <span className="text-muted">
                  {s.autoDeploy ? "auto" : "manual"}
                </span>
              ),
            },
            {
              header: "Trigger",
              cell: (s) => (
                <span className="text-muted whitespace-nowrap">
                  {s.lastWebhookAt
                    ? `webhook ${since(s.lastWebhookAt)}`
                    : `poll every ${s.pollSeconds}s`}
                </span>
              ),
            },
            {
              header: "Checked",
              className: "w-full",
              cell: (s) =>
                s.lastError ? (
                  <StatusBadge tone="bad">{s.lastError}</StatusBadge>
                ) : (
                  <span className="text-muted">
                    {s.lastCheckedAt ? since(s.lastCheckedAt) : "never"}
                    {s.lastSha && (
                      <span className="text-faint font-mono">
                        {" "}
                        @ {s.lastSha.slice(0, 12)}
                      </span>
                    )}
                  </span>
                ),
            },
          ]}
        />
      </Panel>
    </div>
  );
}

/** Recent builds of every service: queue, history and logs (§5.8). */
export function BuildsPage() {
  const q = usePaged<Build>(["builds", "recent"], "/builds", {
    refetchInterval: buildsRefetch,
  });
  const { items: data, isLoading } = q;
  const count = (st: Build["status"]) =>
    data.filter((b) => b.status === st).length;
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Git & Builds"]} title="Builds" />
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile label="Building" value={count("building")} />
        <StatTile label="Queued" value={count("queued")} />
        <StatTile
          label="Failed (last 100)"
          value={count("failed")}
          tone={count("failed") ? "bad" : undefined}
        />
        <StatTile label="Succeeded (last 100)" value={count("succeeded")} />
      </div>
      <BuildsTable builds={data} loading={isLoading} showService />
    </div>
  );
}
