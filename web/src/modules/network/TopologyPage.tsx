import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Network } from "lucide-react";
import { api } from "@/lib/api";
import { since } from "@/lib/nodes";
import { MeshGraph, type GraphLink, type GraphNode } from "@/charts/MeshGraph";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface MeshPeer {
  nodeId: string;
  name: string;
  endpoint: string;
  lastHandshake: string | null;
  rxBytes: number;
  txBytes: number;
  rttMs: number;
}

interface MeshNode {
  nodeId: string;
  name: string;
  address: string;
  subnet: string;
  endpoint: string;
  publicKey: string;
  mode: string;
  generation: number;
  appliedGeneration: number;
  error: string;
  statusUpdatedAt: string | null;
  peers: MeshPeer[];
}

interface Mesh {
  meshCidr: string;
  containerCidr: string;
  serviceCidr: string;
  items: MeshNode[];
}

// WireGuard re-handshakes every 2 minutes on active links.
const fresh = (p: MeshPeer) => !!p.lastHandshake && Date.now() - new Date(p.lastHandshake).getTime() < 3 * 60_000;

function state(n: MeshNode): { tone: "ok" | "warn" | "bad" | "neutral"; label: string } {
  if (!n.publicKey) return { tone: "neutral", label: "not in mesh" };
  if (n.error) return { tone: "bad", label: "error" };
  if (!n.appliedGeneration) return { tone: "warn", label: "pending" };
  const down = n.peers.filter((p) => !fresh(p)).length;
  if (down > 0) return { tone: "warn", label: `${down} link${down > 1 ? "s" : ""} down` };
  return { tone: "ok", label: "connected" };
}

function bytes(n: number) {
  if (n < 1 << 20) return `${(n / 1024).toFixed(0)} KiB`;
  if (n < 1 << 30) return `${(n / (1 << 20)).toFixed(1)} MiB`;
  return `${(n / (1 << 30)).toFixed(2)} GiB`;
}

/** WireGuard mesh topology and per-node state (§8, §8.4). */
export function TopologyPage() {
  const { data, isLoading } = useQuery({
    queryKey: ["network", "mesh"],
    queryFn: () => api<Mesh>("GET", "/network/mesh"),
    refetchInterval: 5000,
  });
  const items = data?.items ?? [];

  const graph = useMemo(() => {
    const nodes: GraphNode[] = items.map((n) => ({ id: n.nodeId, label: n.name, sub: `${n.address} · ${n.subnet}`, tone: state(n).tone }));
    const links: GraphLink[] = [];
    const seen = new Set<string>();
    for (const n of items) {
      for (const p of n.peers) {
        if (!p.nodeId) continue;
        const key = [n.nodeId, p.nodeId].sort().join("|");
        if (seen.has(key)) continue;
        seen.add(key);
        links.push({
          source: n.nodeId,
          target: p.nodeId,
          tone: fresh(p) ? "ok" : p.lastHandshake ? "warn" : "bad",
          label: p.rttMs ? `${p.rttMs.toFixed(1)} ms` : fresh(p) ? "up" : "down",
        });
      }
    }
    return { nodes, links };
  }, [items]);

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Network"]} title="Topology" />
      {data && (
        <div className="text-muted flex flex-wrap gap-x-4 gap-y-1 text-xs">
          <span>
            Nodes <span className="text-fg font-mono">{data.meshCidr}</span>
          </span>
          <span>
            Containers <span className="text-fg font-mono">{data.containerCidr}</span>
          </span>
          <span>
            Service VIPs <span className="text-fg font-mono">{data.serviceCidr}</span>
          </span>
        </div>
      )}
      <Panel title="WireGuard mesh">
        {items.length > 0 ? (
          <MeshGraph nodes={graph.nodes} links={graph.links} />
        ) : (
          !isLoading && (
            <EmptyState icon={Network} title="No mesh members yet">
              Nodes join the mesh when their agent runs as root (or with --network on).
            </EmptyState>
          )
        )}
      </Panel>
      <Panel title="Members" flush>
        <DataTable
          rows={items}
          rowKey={(n) => n.nodeId}
          columns={[
            { header: "Node", cell: (n) => <span className="font-medium">{n.name}</span> },
            { header: "State", cell: (n) => <StatusBadge tone={state(n).tone}>{state(n).label}</StatusBadge> },
            { header: "Address", cell: (n) => <span className="font-mono">{n.address}</span> },
            { header: "Subnet", cell: (n) => <span className="font-mono">{n.subnet}</span> },
            { header: "Endpoint", cell: (n) => <span className="text-muted font-mono">{n.endpoint || "—"}</span> },
            { header: "WireGuard", cell: (n) => <span className="text-muted">{n.mode || "—"}</span> },
            {
              header: "Links",
              className: "w-full",
              cell: (n) => (
                <div className="flex flex-wrap gap-x-3 gap-y-0.5">
                  {n.peers.map((p) => (
                    <span key={p.nodeId || p.endpoint} className={fresh(p) ? "text-muted" : "text-warn"} title={`rx ${bytes(p.rxBytes)} · tx ${bytes(p.txBytes)} · handshake ${since(p.lastHandshake)}`}>
                      {p.name || p.endpoint}
                      {p.rttMs ? ` ${p.rttMs.toFixed(1)}ms` : ""}
                    </span>
                  ))}
                  {n.error && <span className="text-bad">{n.error}</span>}
                </div>
              ),
            },
          ]}
        />
      </Panel>
    </div>
  );
}
