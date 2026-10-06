import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Cloud, Copy, Globe, Layers, Pencil, Plus, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Meter } from "@/ui/Meter";
import { Alert, Button, Field, IconButton, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface PoolSpec {
  region?: string;
  type?: string;
  image?: string;
  sshKeys?: string[];
  nodeCpu?: number;
  nodeMemoryMiB?: number;
  autoscale: boolean;
  headroom?: number;
  scaleInBelow?: number;
  scaleInAfter?: number;
  pendingAfter?: number;
  maxStep?: number;
  joinTimeout?: number;
}
interface Pool {
  id: string;
  name: string;
  role: "worker" | "edge";
  provider: string;
  spec: PoolSpec;
  min: number;
  max: number;
  nodes: { id: string; name: string; status: string; reservedPercent: number; tasks: number; scaleInProtected: boolean; draining: boolean; address?: string }[];
  servers: { serverId: string; name: string; state: string; message?: string; createdAt: string }[];
  cpu: [number, number];
  memory: [number, number];
}
interface Provider {
  id: string;
  name: string;
  type: string;
  summary: string;
}

const sel = "bg-bg border-line-strong focus:border-accent h-8 w-full rounded-sm border px-2 text-sm outline-none";
const errText = (e: unknown, f: string) => (e instanceof ApiError ? e.message : f);
const usePools = () =>
  useQuery({ queryKey: ["node-pools"], queryFn: async () => (await api<{ items: Pool[] }>("GET", "/node-pools")).items, refetchInterval: 5000 });
const useProviders = () =>
  useQuery({ queryKey: ["cloud-providers"], queryFn: async () => (await api<{ items: Provider[] }>("GET", "/cloud-providers")).items });

/** Compute › Node pools (§6.5): manual and provider-backed pools, cluster autoscaling. */
export function NodePoolsPage() {
  const { data: pools = [], isLoading, error } = usePools();
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Compute"]}
        title="Node pools"
        actions={
          <Link to={"/compute/node-pools/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New pool
            </Button>
          </Link>
        }
      />
      {error && <Alert tone="warn">{errText(error, "Unavailable")}</Alert>}
      {!isLoading && pools.length === 0 && <EmptyState icon={Layers} title="No node pools" />}
      {pools.map((p) => (
        <PoolPanel key={p.id || "default"} pool={p} />
      ))}
      <ProvidersPanel />
    </div>
  );
}

function PoolPanel({ pool: p }: { pool: Pool }) {
  const qc = useQueryClient();
  const [join, setJoin] = useState("");
  const events = useQuery({
    queryKey: ["node-pools", p.name, "events"],
    queryFn: async () => (await api<{ items: { id: number; at: string; kind: string; message: string }[] }>("GET", `/node-pools/${p.name}/events`)).items,
    enabled: !!p.id,
    refetchInterval: 10_000,
  });
  const cmd = useMutation({ mutationFn: () => api<{ command: string }>("POST", `/node-pools/${p.name}/join-command`), onSuccess: (r) => setJoin(r.command) });
  const del = useMutation({ mutationFn: () => api("DELETE", `/node-pools/${p.name}`), onSuccess: () => qc.invalidateQueries({ queryKey: ["node-pools"] }) });
  const protect = useMutation({
    mutationFn: (n: Pool["nodes"][number]) => api("PUT", `/nodes/${n.id}/pool`, { pool: p.name, scaleInProtected: !n.scaleInProtected }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["node-pools"] }),
  });
  const cpuPct = p.cpu[1] ? (p.cpu[0] / p.cpu[1]) * 100 : 0;
  const memPct = p.memory[1] ? (p.memory[0] / p.memory[1]) * 100 : 0;
  return (
    <Panel
      title={p.name}
      actions={
        p.id && (
          <>
            {!p.provider && (
              <Button variant="ghost" onClick={() => cmd.mutate()}>
                <Plus className="size-3.5" /> Add node
              </Button>
            )}
            <Link to={`/compute/node-pools/${p.name}` as string}>
              <IconButton label="Edit">
                <Pencil className="size-3.5" />
              </IconButton>
            </Link>
            <IconButton label="Delete" onClick={() => confirm(`Delete pool ${p.name}? It must be empty.`) && del.mutate()}>
              <Trash2 className="size-3.5" />
            </IconButton>
          </>
        )
      }
    >
      <div className="flex flex-col gap-2 text-xs">
        <div className="flex flex-wrap items-center gap-x-6 gap-y-1">
          {p.role === "edge" && <StatusBadge tone="info">edge</StatusBadge>}
          <span className="text-muted">
            {p.id
              ? p.provider
                ? `${p.provider} · ${p.min}–${p.max} nodes${p.spec.autoscale ? ", autoscaled" : ""}`
                : "manual · nodes join by hand"
              : "nodes outside any pool"}
          </span>
          {p.provider && (
            <span className="text-faint font-mono">
              {p.spec.region} · {p.spec.type} · {p.spec.image}
            </span>
          )}
          <span className="flex items-center gap-1.5">
            CPU <Meter value={cpuPct} label={`${p.cpu[0].toFixed(2)}/${p.cpu[1].toFixed(1)}`} className="w-48" />
          </span>
          <span className="flex items-center gap-1.5">
            Memory <Meter value={memPct} label={`${(p.memory[0] / 1024).toFixed(1)}/${(p.memory[1] / 1024).toFixed(1)} GiB`} className="w-56" />
          </span>
        </div>
        {join && (
          <div className="bg-bg border-line flex items-center gap-2 rounded-sm border px-2 py-1 font-mono">
            <span className="flex-1 break-all">{join}</span>
            <button onClick={() => navigator.clipboard?.writeText(join)} className="text-muted hover:text-fg" aria-label="Copy">
              <Copy className="size-3.5" />
            </button>
          </div>
        )}
        <DataTable
          rows={p.nodes ?? []}
          rowKey={(n) => n.id}
          empty={<span className="text-faint p-2">No nodes.</span>}
          columns={[
            { header: "Node", cell: (n) => <span className="font-medium">{n.name}</span> },
            {
              header: "State",
              cell: (n) => (
                <div className="flex gap-1">
                  <StatusBadge tone={n.status === "ready" ? "ok" : n.status === "suspect" ? "warn" : "bad"}>{n.status}</StatusBadge>
                  {n.draining && <StatusBadge tone="warn">draining</StatusBadge>}
                </div>
              ),
            },
            { header: "Reserved", cell: (n) => <Meter value={n.reservedPercent} className="w-40" /> },
            { header: "Tasks", cell: (n) => <span className="text-muted">{n.tasks}</span> },
            { header: "Address", className: "w-full", cell: (n) => <span className="text-muted font-mono">{n.address || "—"}</span> },
            {
              header: "Scale-in",
              cell: (n) =>
                p.id ? (
                  <label className="flex items-center gap-1.5 whitespace-nowrap">
                    <input type="checkbox" checked={n.scaleInProtected} onChange={() => protect.mutate(n)} /> protected
                  </label>
                ) : null,
            },
          ]}
        />
        {(p.servers ?? []).length > 0 && (
          <div className="flex flex-col gap-0.5">
            {(p.servers ?? []).map((s) => (
              <span key={s.serverId} className={s.state === "failed" ? "text-bad" : "text-muted"}>
                server {s.name}: {s.state}
                {s.message ? ` (${s.message})` : ""}
              </span>
            ))}
          </div>
        )}
        {(events.data?.length ?? 0) > 0 && (
          <details>
            <summary className="text-muted cursor-pointer">Scaling history</summary>
            <div className="mt-1 flex flex-col gap-0.5">
              {events.data!.slice(0, 20).map((e) => (
                <span key={e.id}>
                  <span className="text-faint">{new Date(e.at).toLocaleString()}</span>{" "}
                  <StatusBadge tone={e.kind === "failed" ? "bad" : e.kind === "info" ? "neutral" : "info"}>{e.kind}</StatusBadge> {e.message}
                </span>
              ))}
            </div>
          </details>
        )}
        {(cmd.error || del.error || protect.error) && <Alert>{errText(cmd.error ?? del.error ?? protect.error, "Failed")}</Alert>}
      </div>
    </Panel>
  );
}

function ProvidersPanel() {
  const qc = useQueryClient();
  const { data: providers = [] } = useProviders();
  const [name, setName] = useState("");
  const [type, setType] = useState("hetzner");
  const [token, setToken] = useState("");
  const [url, setUrl] = useState("");
  const add = useMutation({
    mutationFn: () => api("POST", "/cloud-providers", { name, type, config: { token, url } }),
    onSuccess: () => {
      setName("");
      setToken("");
      setUrl("");
      void qc.invalidateQueries({ queryKey: ["cloud-providers"] });
    },
  });
  const del = useMutation({ mutationFn: (id: string) => api("DELETE", `/cloud-providers/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: ["cloud-providers"] }) });
  return (
    <Panel title="Cloud providers">
      <div className="flex flex-col gap-2 text-xs">
        {providers.length === 0 && <span className="text-faint">No provider yet: pools join nodes by hand.</span>}
        {providers.map((p) => (
          <div key={p.id} className="flex items-center gap-2">
            <Cloud className="text-muted size-3.5" />
            <span className="font-medium">{p.name}</span>
            <span className="text-muted">{p.type}</span>
            <span className="text-faint font-mono">{p.summary}</span>
            <IconButton label="Delete" onClick={() => confirm(`Delete provider ${p.name}?`) && del.mutate(p.id)}>
              <Trash2 className="size-3.5" />
            </IconButton>
          </div>
        ))}
        <div className="flex flex-wrap items-end gap-1.5">
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="hcloud" className="h-7 w-36" />
          </Field>
          <Field label="Type">
            <select value={type} onChange={(e) => setType(e.target.value)} className={cn(sel, "h-7 w-36")}>
              <option value="hetzner">Hetzner Cloud</option>
              <option value="digitalocean">DigitalOcean</option>
              <option value="webhook">Webhook</option>
            </select>
          </Field>
          {type === "webhook" ? (
            <Field label="URL">
              <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://provision.example.com/hook" className="h-7 w-80" />
            </Field>
          ) : (
            <Field label="API token">
              <Input type="password" value={token} onChange={(e) => setToken(e.target.value)} className="h-7 w-80" autoComplete="off" />
            </Field>
          )}
          <Button disabled={!name || add.isPending} onClick={() => add.mutate()}>
            <Plus className="size-3.5" /> Add
          </Button>
        </div>
        <span className="text-faint">Tokens are sealed with the master key and never shown again. Servers carry the label syncloud-pool.</span>
        {(add.error || del.error) && <Alert>{errText(add.error ?? del.error, "Failed")}</Alert>}
      </div>
    </Panel>
  );
}

/** Create or edit a node pool, as a full page. */
export function NodePoolPage() {
  const { name: ref } = useParams({ strict: false }) as { name?: string };
  const isNew = !ref;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: pools } = usePools();
  const { data: providers = [] } = useProviders();
  const existing = pools?.find((p) => p.name === ref);
  const [name, setName] = useState("");
  const [role, setRole] = useState<"worker" | "edge">("worker");
  const [provider, setProvider] = useState("");
  const [min, setMin] = useState(0);
  const [max, setMax] = useState(3);
  const [spec, setSpec] = useState<PoolSpec>({ autoscale: true, nodeCpu: 2, nodeMemoryMiB: 4096 });
  const [loaded, setLoaded] = useState(false);
  useEffect(() => {
    if (existing && !loaded) {
      setName(existing.name);
      setRole(existing.role);
      setProvider(existing.provider);
      setMin(existing.min);
      setMax(existing.max);
      setSpec(existing.spec);
      setLoaded(true);
    }
  }, [existing, loaded]);
  const patch = (p: Partial<PoolSpec>) => setSpec({ ...spec, ...p });
  const back = () => navigate({ to: "/compute/node-pools" as string });
  const save = useMutation({
    mutationFn: () => {
      const body = { name, role, provider, min, max, spec: provider ? spec : { autoscale: false } };
      return isNew ? api("POST", "/node-pools", body) : api("PUT", `/node-pools/${ref}`, body);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["node-pools"] });
      void back();
    },
  });
  const num = (v: string) => (v === "" ? undefined : Number(v));
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      className={cn("flex flex-col", gap)}
    >
      <PageHeader
        crumbs={["Compute", "Node pools"]}
        title={isNew ? "New node pool" : `Pool ${ref}`}
        actions={
          <>
            <Button variant="ghost" onClick={back}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={!name || save.isPending}>
              {isNew ? "Create pool" : "Save"}
            </Button>
          </>
        }
      />
      <Panel title="Pool">
        <div className="grid max-w-4xl grid-cols-1 gap-2 sm:grid-cols-3">
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} disabled={!isNew} placeholder="workers" autoFocus={isNew} />
          </Field>
          <Field label="Role" hint={role === "edge" ? "Runs a Traefik replica; takes no tasks" : "Runs tasks"}>
            <select value={role} onChange={(e) => setRole(e.target.value as "worker" | "edge")} className={sel}>
              <option value="worker">worker</option>
              <option value="edge">edge</option>
            </select>
          </Field>
          <Field label="Servers" hint={provider ? "created through the provider" : "nodes join with a command"}>
            <select value={provider} onChange={(e) => setProvider(e.target.value)} className={sel}>
              <option value="">manual (join by hand)</option>
              {providers.map((p) => (
                <option key={p.id} value={p.name}>
                  {p.name} ({p.type})
                </option>
              ))}
            </select>
          </Field>
        </div>
      </Panel>
      {provider && (
        <>
          <Panel title="Servers">
            <div className="grid max-w-4xl grid-cols-1 gap-2 sm:grid-cols-3">
              <Field label="Region">
                <Input value={spec.region ?? ""} onChange={(e) => patch({ region: e.target.value })} placeholder="fsn1, fra1" />
              </Field>
              <Field label="Type">
                <Input value={spec.type ?? ""} onChange={(e) => patch({ type: e.target.value })} placeholder="cx32, s-2vcpu-4gb" />
              </Field>
              <Field label="Image">
                <Input value={spec.image ?? ""} onChange={(e) => patch({ image: e.target.value })} placeholder="ubuntu-24.04" />
              </Field>
              <Field label="CPU cores per server" hint="for planning">
                <Input type="number" min={1} value={spec.nodeCpu ?? ""} onChange={(e) => patch({ nodeCpu: num(e.target.value) })} />
              </Field>
              <Field label="Memory per server (MiB)">
                <Input type="number" min={256} value={spec.nodeMemoryMiB ?? ""} onChange={(e) => patch({ nodeMemoryMiB: num(e.target.value) })} />
              </Field>
              <Field label="SSH keys" hint="provider key names or IDs, comma-separated">
                <Input
                  value={(spec.sshKeys ?? []).join(", ")}
                  onChange={(e) => patch({ sshKeys: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })}
                />
              </Field>
            </div>
          </Panel>
          <Panel title="Size and autoscaling">
            <div className="flex max-w-4xl flex-col gap-2">
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                <Field label="Minimum nodes">
                  <Input type="number" min={0} value={min} onChange={(e) => setMin(Number(e.target.value))} />
                </Field>
                <Field label="Maximum nodes">
                  <Input type="number" min={0} value={max} onChange={(e) => setMax(Number(e.target.value))} />
                </Field>
              </div>
              <label className="flex items-center gap-2 text-xs">
                <input type="checkbox" checked={spec.autoscale} onChange={(e) => patch({ autoscale: e.target.checked })} /> Add and remove servers automatically
              </label>
              {spec.autoscale && (
                <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
                  <Field label="Add when tasks wait (s)" hint="no node can take them">
                    <Input type="number" value={spec.pendingAfter ?? 60} onChange={(e) => patch({ pendingAfter: num(e.target.value) })} />
                  </Field>
                  <Field label="Add above (% reserved)">
                    <Input type="number" value={spec.headroom ?? 80} onChange={(e) => patch({ headroom: num(e.target.value) })} />
                  </Field>
                  <Field label="Remove below (% reserved)">
                    <Input type="number" value={spec.scaleInBelow ?? 40} onChange={(e) => patch({ scaleInBelow: num(e.target.value) })} />
                  </Field>
                  <Field label="…for (s)">
                    <Input type="number" value={spec.scaleInAfter ?? 600} onChange={(e) => patch({ scaleInAfter: num(e.target.value) })} />
                  </Field>
                  <Field label="Nodes per step">
                    <Input type="number" value={spec.maxStep ?? 2} onChange={(e) => patch({ maxStep: num(e.target.value) })} />
                  </Field>
                  <Field label="Join timeout (s)" hint="a server that does not join is deleted">
                    <Input type="number" value={spec.joinTimeout ?? 600} onChange={(e) => patch({ joinTimeout: num(e.target.value) })} />
                  </Field>
                </div>
              )}
              <p className="text-muted text-xs">
                New servers run cloud-init that joins the cluster with a single-use token. A node is removed only when its tasks fit on the pool's other nodes;
                it is drained first, and protected nodes are never removed.
              </p>
            </div>
          </Panel>
        </>
      )}
      {save.error && <Alert>{errText(save.error, "Could not save the pool")}</Alert>}
    </form>
  );
}

/** Network › Edge nodes (§8.5): Traefik replicas on edge pools. */
export function EdgeNodesPage() {
  const { data, isLoading } = useQuery({
    queryKey: ["edges"],
    queryFn: async () => (await api<{ items: { nodeId: string; node: string; meshIp: string; address: string; state: string; error?: string; container: string; checkedAt: string }[] }>("GET", "/edges")).items,
    refetchInterval: 5000,
  });
  const edges = data ?? [];
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Network"]} title="Edge nodes" />
      <Alert tone="info">
        Nodes in an <span className="font-mono">edge</span> pool run a Traefik replica with the same routes and certificates as the controller, fetched over
        the private network, and keep serving with the last configuration when the controller is down. Point your domain's A records at their addresses
        (sslip.io names point at the controller only).
      </Alert>
      <Panel flush>
        <DataTable
          rows={edges}
          rowKey={(e) => e.nodeId}
          empty={
            !isLoading && (
              <EmptyState icon={Globe} title="No edge nodes">
                Create a pool with the edge role (Compute › Node pools) and add nodes to it.
              </EmptyState>
            )
          }
          columns={[
            { header: "Node", cell: (e) => <span className="font-medium">{e.node}</span> },
            { header: "Health", cell: (e) => <StatusBadge tone={e.state === "healthy" ? "ok" : e.state === "starting" ? "info" : "bad"}>{e.state}</StatusBadge> },
            { header: "Public address", cell: (e) => <span className="font-mono">{e.address || "—"}</span> },
            { header: "Mesh address", cell: (e) => <span className="text-muted font-mono">{e.meshIp}</span> },
            { header: "Replica", cell: (e) => <span className="text-muted">{e.container || "—"}</span> },
            { header: "", className: "w-full", cell: (e) => <span className="text-bad">{e.error}</span> },
          ]}
        />
      </Panel>
    </div>
  );
}
