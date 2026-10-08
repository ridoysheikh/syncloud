import { useMemo, useState, type FormEvent } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Globe, History, Search } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { HistoryChart, type HistorySeries } from "@/charts/HistoryChart";
import { formatBytes } from "@/modules/projects/MetricsPanel";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Meter } from "@/ui/Meter";
import { Alert, Button, Input, StatusBadge } from "@/ui/controls";
import { NodeLink } from "@/entities/nodes";
import { cn, gap } from "@/ui/cn";

interface Address {
  ip: string;
  owner: string;
  ownerId: string;
  kind: "task" | "run";
  since: string;
}

interface Ipam {
  meshCidr: string;
  containerCidr: string;
  serviceCidr: string;
  cooldownSeconds: number;
  nodes: { nodeId: string; node: string; meshIp: string; subnet: string; gateway: string; used: number; capacity: number; addresses: Address[] }[];
  vips: { serviceId: string; service: string; vip: string; dnsName: string; backends: number }[];
  released: { kind: string; address: string; releasedAt: string; reusableAt: string }[];
}

interface AddressRecord {
  ip: string;
  ownerId: string;
  owner: string;
  assignedAt: string;
  releasedAt: string | null;
}

const when = (t: string) => new Date(t).toLocaleString();

function serviceLink(path: string) {
  const [p, e, s] = path.split("/");
  if (!s) return <span>{path}</span>;
  return (
    <Link to={`/projects/${p}/${e}/services/${s}` as string} className="hover:text-accent">
      {path}
    </Link>
  );
}

/** Network › IPAM & DNS (§8.1, §8.2): the address plan and internal names. */
export function IpamPage() {
  const { data, isLoading } = useQuery({ queryKey: ["ipam"], queryFn: () => api<Ipam>("GET", "/network/ipam"), refetchInterval: 10_000 });
  const [historyIP, setHistoryIP] = useState("");
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Network"]} title="IPAM & DNS" />
      {data && (
        <div className="text-muted flex flex-wrap gap-x-4 gap-y-1 text-xs">
          <span>
            Nodes <span className="text-fg font-mono">{data.meshCidr}</span>
          </span>
          <span>
            Containers <span className="text-fg font-mono">{data.containerCidr}</span> (a /24 per node)
          </span>
          <span>
            Service VIPs <span className="text-fg font-mono">{data.serviceCidr}</span>
          </span>
          <span>Released addresses wait {Math.round((data.cooldownSeconds ?? 3600) / 60)} minutes before reuse</span>
        </div>
      )}
      <Panel title="Node subnets" flush>
        <DataTable
          rows={data?.nodes ?? []}
          rowKey={(n) => n.nodeId}
          empty={!isLoading && <EmptyState icon={Globe} title="No node has joined the private network yet" />}
          columns={[
            { header: "Node", cell: (n) => <NodeLink id={n.nodeId} name={n.node} className="font-medium" /> },
            { header: "Mesh address", cell: (n) => <span className="font-mono">{n.meshIp}</span> },
            { header: "Subnet", cell: (n) => <span className="font-mono">{n.subnet}</span> },
            { header: "Gateway / DNS", cell: (n) => <span className="text-muted font-mono">{n.gateway}</span> },
            {
              header: "In use",
              className: "w-full",
              cell: (n) => (
                <div className="flex items-center gap-2">
                  <Meter value={(100 * n.used) / n.capacity} label={`${n.used}/${n.capacity}`} className="w-48" />
                </div>
              ),
            },
          ]}
        />
      </Panel>
      <Panel title="Addresses in use" flush>
        <DataTable
          rows={(data?.nodes ?? []).flatMap((n) => n.addresses.map((a) => ({ ...a, node: n.node })))}
          rowKey={(a) => a.ip}
          empty={!isLoading && <EmptyState icon={Globe} title="No task has an address" />}
          columns={[
            { header: "Address", cell: (a) => <span className="font-mono">{a.ip}</span> },
            { header: "Owner", className: "w-full", cell: (a) => <span>{serviceLink(a.owner.replace(" (job run)", ""))}{a.kind === "run" && <span className="text-faint"> job run</span>}</span> },
            { header: "Task", cell: (a) => <span className="text-muted font-mono">{a.ownerId}</span> },
            { header: "Node", cell: (a) => <NodeLink name={a.node} className="text-muted" /> },
            { header: "Since", cell: (a) => <span className="text-muted whitespace-nowrap">{when(a.since)}</span> },
            {
              header: "",
              cell: (a) => (
                <Button variant="ghost" onClick={() => setHistoryIP(a.ip)}>
                  <History className="size-3.5" /> History
                </Button>
              ),
            },
          ]}
        />
      </Panel>
      <AddressHistory ip={historyIP} setIP={setHistoryIP} />
      <Panel title="Service VIPs" flush>
        <DataTable
          rows={data?.vips ?? []}
          rowKey={(v) => v.serviceId}
          empty={!isLoading && <EmptyState icon={Globe} title="No service with ports yet" />}
          columns={[
            { header: "VIP", cell: (v) => <span className="font-mono">{v.vip}</span> },
            { header: "Service", cell: (v) => serviceLink(v.service) },
            { header: "DNS name", className: "w-full", cell: (v) => <span className="text-muted font-mono">{v.dnsName}</span> },
            {
              header: "Backends",
              cell: (v) => <StatusBadge tone={v.backends > 0 ? "ok" : "warn"}>{v.backends > 0 ? `${v.backends} serving` : "none (connections refused)"}</StatusBadge>,
            },
          ]}
        />
      </Panel>
      {(data?.released.length ?? 0) > 0 && (
        <Panel title="Cooling down" flush>
          <DataTable
            rows={data!.released}
            rowKey={(r) => r.kind + r.address}
            columns={[
              { header: "Address", cell: (r) => <span className="font-mono">{r.address}</span> },
              { header: "Kind", cell: (r) => <span className="text-muted">{r.kind}</span> },
              { header: "Released", cell: (r) => <span className="text-muted">{when(r.releasedAt)}</span> },
              { header: "Reusable", className: "w-full", cell: (r) => <span className="text-muted">{when(r.reusableAt)}</span> },
            ]}
          />
        </Panel>
      )}
      <DnsPanel />
    </div>
  );
}

function AddressHistory({ ip, setIP }: { ip: string; setIP: (v: string) => void }) {
  const [draft, setDraft] = useState("");
  const { data, error } = useQuery({
    queryKey: ["ip-history", ip],
    queryFn: async () => (await api<{ items: AddressRecord[] }>("GET", `/network/ipam/history?ip=${encodeURIComponent(ip)}`)).items,
  });
  return (
    <Panel
      title={ip ? `History of ${ip}` : "Address history"}
      flush
      actions={
        <form
          onSubmit={(e) => {
            e.preventDefault();
            setIP(draft.trim());
          }}
          className="flex gap-1"
        >
          <Input value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="10.91.2.4" className="h-7 w-36 font-mono" />
          <Button type="submit" variant="ghost">
            <Search className="size-3.5" />
          </Button>
        </form>
      }
    >
      {error && (
        <div className="p-3">
          <Alert>{error instanceof ApiError ? error.message : "Unavailable"}</Alert>
        </div>
      )}
      <DataTable
        rows={(data ?? []).slice(0, 50)}
        rowKey={(r) => r.ip + r.ownerId + r.assignedAt}
        empty={<EmptyState icon={History} title="No assignments recorded" />}
        columns={[
          { header: "Address", cell: (r) => <span className="font-mono">{r.ip}</span> },
          { header: "Owner", className: "w-full", cell: (r) => serviceLink(r.owner) },
          { header: "Task", cell: (r) => <span className="text-muted font-mono">{r.ownerId}</span> },
          { header: "From", cell: (r) => <span className="text-muted whitespace-nowrap">{when(r.assignedAt)}</span> },
          { header: "Until", cell: (r) => <span className="text-muted whitespace-nowrap">{r.releasedAt ? when(r.releasedAt) : "now"}</span> },
        ]}
      />
    </Panel>
  );
}

interface DnsAnswer {
  name: string;
  found: boolean;
  source: "internal" | "upstream";
  kind?: string;
  ips: string[];
  error?: string;
}

function DnsPanel() {
  const { data } = useQuery({
    queryKey: ["dns"],
    queryFn: () => api<{ zone: string; items: { name: string; kind: string; ips: string[] }[] }>("GET", "/network/dns"),
    refetchInterval: 10_000,
  });
  const [filter, setFilter] = useState("");
  const [name, setName] = useState("");
  const lookup = useMutation({ mutationFn: () => api<DnsAnswer>("GET", `/network/dns/lookup?name=${encodeURIComponent(name)}`) });
  const rows = useMemo(() => (data?.items ?? []).filter((r) => r.name.includes(filter.toLowerCase())), [data, filter]);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    lookup.mutate();
  };
  const a = lookup.data;
  return (
    <>
      <Panel title="DNS lookup">
        <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="web.production.shop, tasks.web.production.shop or example.com" className="w-96 font-mono" />
          <Button type="submit" variant="primary" disabled={!name || lookup.isPending}>
            <Search className="size-3.5" /> Resolve
          </Button>
          {a && (
            <span className="text-xs">
              <span className="font-mono">{a.name}</span>{" "}
              {a.found ? (
                <>
                  → <span className="font-mono">{a.ips.join(", ") || "no addresses (no task serving)"}</span>{" "}
                  <span className="text-faint">
                    ({a.source === "internal" ? `internal, ${a.kind}` : "upstream resolvers"})
                  </span>
                </>
              ) : (
                <span className="text-bad">not found{a.source === "upstream" && a.error ? `: ${a.error}` : ""}</span>
              )}
            </span>
          )}
        </form>
        <p className="text-faint mt-1.5 text-xs">
          Tasks resolve <span className="font-mono">{data?.zone ?? "syncloud.internal"}</span> through their node's agent and can use short names within
          their environment (<span className="font-mono">http://api:8080</span>). Other names go to the node's upstream resolvers.
        </p>
      </Panel>
      <Panel
        title={`Internal zone · ${data?.items.length ?? 0} records`}
        flush
        actions={<Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="filter" className="h-7 w-48 font-mono" />}
      >
        <DataTable
          rows={rows}
          rowKey={(r) => r.name}
          columns={[
            { header: "Name", className: "w-full", cell: (r) => <span className="font-mono">{r.name}</span> },
            { header: "Kind", cell: (r) => <span className="text-muted">{r.kind === "tasks" ? "task IPs" : r.kind === "service" ? "service VIP" : "node"}</span> },
            { header: "Addresses", cell: (r) => <span className="font-mono">{r.ips.length ? r.ips.join(", ") : <span className="text-faint">none</span>}</span> },
          ]}
        />
      </Panel>
    </>
  );
}

interface Throughput {
  start: string;
  end: string;
  charts: Record<string, { key: string; points: [number, number][] }[]>;
  topServices: { key: string; rxBps: number; txBps: number }[];
  topTasks: { key: string; node: string; owner: string; rxBps: number; txBps: number }[];
}

const fmtRate = (v: number) => `${formatBytes(v)}/s`;

/** Node and mesh throughput, and the busiest services and tasks (§8.4). */
export function ThroughputPanels() {
  const [range, setRange] = useState("1h");
  const { data, error } = useQuery({
    queryKey: ["network", "throughput", range],
    queryFn: () => api<Throughput>("GET", `/network/throughput?range=${range}`),
    refetchInterval: 15_000,
  });
  const start = data ? Date.parse(data.start) : 0;
  const end = data ? Date.parse(data.end) : 0;
  const lines = (rx: string, tx: string): HistorySeries[] => [
    ...(data?.charts[rx] ?? []).map((s) => ({ name: `${s.key} in`, points: s.points })),
    ...(data?.charts[tx] ?? []).map((s) => ({ name: `${s.key} out`, points: s.points })),
  ];
  if (error) return <Alert tone="warn">{error instanceof ApiError ? error.message : "Throughput is unavailable"}</Alert>;
  return (
    <>
      <div className="flex justify-end">
        <div className="border-line flex rounded-sm border">
          {["15m", "1h", "6h", "24h", "7d"].map((r) => (
            <button key={r} onClick={() => setRange(r)} className={cn("px-2 py-0.5 text-xs", r === range ? "bg-raised text-fg" : "text-muted hover:text-fg")}>
              {r}
            </button>
          ))}
        </div>
      </div>
      <div className={cn("grid grid-cols-1 xl:grid-cols-2", gap)}>
        <Panel title="Node traffic (all interfaces)">
          <HistoryChart series={lines("nodeRx", "nodeTx")} start={start} end={end} format={fmtRate} />
        </Panel>
        <Panel title="Mesh traffic (WireGuard, between nodes)">
          <HistoryChart series={lines("meshRx", "meshTx")} start={start} end={end} format={fmtRate} />
        </Panel>
        <Panel title="Top services (last 5 minutes)" flush>
          <DataTable
            rows={data?.topServices ?? []}
            rowKey={(t) => t.key}
            columns={[
              { header: "Service", className: "w-full", cell: (t) => serviceLink(t.key) },
              { header: "In", cell: (t) => <span className="font-mono">{fmtRate(t.rxBps)}</span> },
              { header: "Out", cell: (t) => <span className="font-mono">{fmtRate(t.txBps)}</span> },
            ]}
          />
        </Panel>
        <Panel title="Top tasks (last 5 minutes)" flush>
          <DataTable
            rows={data?.topTasks ?? []}
            rowKey={(t) => t.key}
            columns={[
              { header: "Task", cell: (t) => <span className="font-mono">{t.key}</span> },
              { header: "Service", className: "w-full", cell: (t) => serviceLink(t.owner) },
              { header: "Node", cell: (t) => <NodeLink name={t.node} className="text-muted" /> },
              { header: "In", cell: (t) => <span className="font-mono">{fmtRate(t.rxBps)}</span> },
              { header: "Out", cell: (t) => <span className="font-mono">{fmtRate(t.txBps)}</span> },
            ]}
          />
        </Panel>
      </div>
    </>
  );
}
