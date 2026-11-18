import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plug } from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Input, StatusBadge } from "@/ui/controls";
import { Select } from "@/ui/select";
import { NodeLink } from "@/entities/nodes";
import { cn, gap } from "@/ui/cn";

/* Network › Ports (Phase 26): the port table. Every public port of a
   service or database on every load-balancer node it is served from;
   a port has one holder per node. */

interface PortClaim {
  node: string;
  protocol: "tcp" | "udp";
  port: number;
  ownerKind: "service" | "database";
  ownerId: string;
  owner: string;
  project?: string;
  portName: string;
  mode: "auto" | "chosen";
  createdAt: string;
}

interface PortTable {
  items: PortClaim[];
  reserved: { protocol: string; ports: string; reason: string }[];
  conflicts: PortClaim[];
}

function OwnerLink({ c }: { c: PortClaim }) {
  if (c.ownerKind === "database") {
    return (
      <Link to={`/databases/${c.owner}` as string} className="hover:text-accent font-mono">
        {c.owner}
      </Link>
    );
  }
  const [p, e, s] = c.owner.split("/");
  if (!s) return <span className="font-mono">{c.owner}</span>;
  return (
    <Link to={`/projects/${p}/${e}/services/${s}` as string} className="hover:text-accent font-mono">
      {c.owner}
    </Link>
  );
}

export function PortsPage() {
  const { data, isLoading } = useQuery({
    queryKey: ["public-ports"],
    queryFn: () => api<PortTable>("GET", "/ports"),
    refetchInterval: 15_000,
  });
  const [node, setNode] = useState("");
  const [kind, setKind] = useState("");
  const [search, setSearch] = useState("");
  const items = data?.items ?? [];
  const nodes = useMemo(() => [...new Set(items.map((c) => c.node))].sort(), [items]);
  const rows = items.filter(
    (c) =>
      (!node || c.node === node) &&
      (!kind || c.ownerKind === kind) &&
      (!search ||
        String(c.port).includes(search) ||
        c.owner.toLowerCase().includes(search.toLowerCase())),
  );
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Network"]} title="Ports" />
      <p className="text-muted text-xs">
        Every public TCP/UDP port on every load-balancer node it is served
        from. A port has one holder per node, so the same number can be used
        again only on another load balancer (a project's own nodes). Websites
        and APIs need no port: they share 80 and 443 through Traefik.
      </p>
      {(data?.conflicts.length ?? 0) > 0 && (
        <Alert tone="warn">
          {data!.conflicts.map((c) => (
            <div key={`${c.node}/${c.protocol}/${c.port}/${c.ownerId}`}>
              {c.protocol.toUpperCase()} {c.port} of {c.owner} is not served on {c.node}: another owner holds it there
              (the node joined a load balancer that also uses it). Choose another port for one of them.
            </div>
          ))}
        </Alert>
      )}
      <Panel
        title="Port table"
        flush
        actions={
          <div className="flex flex-wrap items-center gap-1.5">
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Port or owner"
              className="w-40"
            />
            <Select
              value={node}
              onChange={setNode}
              size="sm"
              options={[{ value: "", label: "All nodes" }, ...nodes.map((n) => ({ value: n, label: n }))]}
            />
            <Select
              value={kind}
              onChange={setKind}
              size="sm"
              options={[
                { value: "", label: "Services and databases" },
                { value: "service", label: "Services" },
                { value: "database", label: "Databases" },
              ]}
            />
          </div>
        }
      >
        <DataTable
          rows={rows}
          rowKey={(c) => `${c.node}/${c.protocol}/${c.port}`}
          empty={
            !isLoading && (
              <EmptyState icon={Plug} title="No public ports">
                Public TCP/UDP ports of services and databases appear here.
              </EmptyState>
            )
          }
          columns={[
            { header: "Port", cell: (c) => <span className="font-mono">{c.port}</span> },
            { header: "Protocol", cell: (c) => c.protocol.toUpperCase() },
            { header: "Node", cell: (c) => <NodeLink name={c.node} /> },
            { header: "Owner", cell: (c) => <OwnerLink c={c} /> },
            {
              header: "Owner port",
              cell: (c) => (
                <span className="font-mono">
                  {c.ownerKind === "database" ? (c.portName === "ro" ? "read-only" : "read-write") : c.portName}
                </span>
              ),
            },
            {
              header: "Mode",
              cell: (c) => (
                <StatusBadge tone={c.mode === "chosen" ? "info" : "neutral"}>{c.mode === "chosen" ? "chosen" : "automatic"}</StatusBadge>
              ),
            },
          ]}
        />
      </Panel>
      <Panel title="Reserved" flush>
        <DataTable
          rows={data?.reserved ?? []}
          rowKey={(r) => `${r.protocol}/${r.ports}/${r.reason}`}
          columns={[
            { header: "Ports", cell: (r) => <span className="font-mono">{r.ports}</span> },
            { header: "Protocol", cell: (r) => r.protocol.toUpperCase() },
            { header: "Reserved for", cell: (r) => r.reason },
          ]}
        />
        <p className="text-faint border-line border-t px-2 py-1.5 text-xs md:px-3">
          These can never be chosen, nor can a port another program already listens on, on one of the load balancer's
          nodes.
        </p>
      </Panel>
    </div>
  );
}
