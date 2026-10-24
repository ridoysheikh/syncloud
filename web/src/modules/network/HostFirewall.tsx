import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, Globe, KeyRound, ListChecks, ShieldCheck, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { since } from "@/lib/nodes";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Dialog } from "@/ui/Dialog";
import { StatTile } from "@/ui/StatTile";
import { SourceList } from "@/ui/SourceList";
import { Alert, Button, Field, IconButton, Input, StatusBadge, Toggle } from "@/ui/controls";
import { ChoiceCards, Segmented } from "@/ui/choice";
import { confirmAction } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";

/* The locked-down host firewall (Phase 24): SSH access, the block list and
   each node's firewall view. */

export interface FirewallBlock {
  id: string;
  source: string;
  node: string;
  reason: string;
  createdBy: string;
  auto: boolean;
  createdAt: string;
  expiresAt: string | null;
}

interface SSHSettings {
  ports: number[];
  sources: string[];
  rate: number;
  burst: number;
  exempt: string[];
  autoBan: boolean;
  banMinutes: number;
}

const errText = (e: unknown) => (e instanceof ApiError ? e.message : "Request failed");

export function until(iso: string | null) {
  if (!iso) return "until removed";
  const s = (Date.parse(iso) - Date.now()) / 1000;
  if (s <= 0) return "expiring";
  if (s < 3600) return `${Math.ceil(s / 60)}m left`;
  if (s < 86400) return `${Math.round(s / 3600)}h left`;
  return `${Math.round(s / 86400)}d left`;
}

const durations = [
  { value: "10", label: "10 min" },
  { value: "60", label: "1 hour" },
  { value: "1440", label: "1 day" },
  { value: "0", label: "always" },
];

/** The block dialog, opened with block(source, node). */
export function useBlocker() {
  const qc = useQueryClient();
  const [target, setTarget] = useState<{ source: string; node?: string } | null>(null);
  const [source, setSource] = useState("");
  const [scope, setScope] = useState<"node" | "all">("all");
  const [minutes, setMinutes] = useState("60");
  const [reason, setReason] = useState("");
  const [own, setOwn] = useState(false);
  useEffect(() => {
    if (!target) return;
    setSource(target.source);
    setScope(target.node ? "node" : "all");
    setReason("");
    setOwn(false);
  }, [target]);
  const save = useMutation({
    mutationFn: (force: boolean) =>
      api<FirewallBlock>("POST", "/firewall/blocks", {
        source: source.trim(),
        node: scope === "node" ? target?.node : "",
        minutes: Number(minutes),
        reason,
        force,
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["firewall"] });
      void qc.invalidateQueries({ queryKey: ["node-firewall"] });
      setTarget(null);
    },
    onError: (e) => setOwn(e instanceof ApiError && e.message.includes("your own address")),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (source.trim()) save.mutate(false);
  };
  const dialog = (
    <Dialog
      open={target !== null}
      onClose={() => setTarget(null)}
      title="Block a source"
      description="Its packets are dropped before anything else on the node, even on SSH."
      footer={
        <>
          <Button onClick={() => setTarget(null)}>Cancel</Button>
          {own ? (
            <Button variant="danger" disabled={save.isPending} onClick={() => save.mutate(true)}>
              Block my address anyway
            </Button>
          ) : (
            <Button variant="danger" type="submit" form="block-source" disabled={!source.trim() || save.isPending}>
              Block
            </Button>
          )}
        </>
      }
    >
      <form id="block-source" onSubmit={submit} className="flex flex-col gap-3 text-xs">
        <Field label="IP or CIDR">
          <Input value={source} onChange={(e) => setSource(e.target.value)} className="font-mono" spellCheck={false} autoFocus />
        </Field>
        {target?.node && (
          <Field label="Where">
            <Segmented
              label="Where"
              value={scope}
              onChange={setScope}
              options={[
                { value: "node", label: `only ${target.node}` },
                { value: "all", label: "every node" },
              ]}
            />
          </Field>
        )}
        <Field label="For">
          <Segmented label="For" value={minutes} onChange={setMinutes} options={durations} />
        </Field>
        <Field label="Reason (optional)">
          <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="port scan" maxLength={200} />
        </Field>
        {save.error && <Alert>{errText(save.error)}</Alert>}
      </form>
    </Dialog>
  );
  return { block: (source: string, node?: string) => setTarget({ source, node }), dialog };
}

/** Network › Firewall › SSH access: always open, who and how often. */
export function SSHAccessPanel() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["firewall", "ssh"], queryFn: () => api<SSHSettings>("GET", "/firewall/ssh") });
  const [v, setV] = useState<SSHSettings | null>(null);
  const [ports, setPorts] = useState("");
  const [restricted, setRestricted] = useState(false);
  useEffect(() => {
    if (!q.data) return;
    setV(q.data);
    setPorts(q.data.ports.join(", "));
    setRestricted(q.data.sources.length > 0);
  }, [q.data]);
  const save = useMutation({
    mutationFn: (s: SSHSettings) => api<SSHSettings>("PUT", "/firewall/ssh", s),
    onSuccess: (d) => qc.setQueryData(["firewall", "ssh"], d),
  });
  if (!v) return <Panel title="SSH access">{q.error ? <Alert>{errText(q.error)}</Alert> : <span className="text-faint text-xs">Loading…</span>}</Panel>;
  const parsedPorts = ports
    .split(/[\s,]+/)
    .filter(Boolean)
    .map(Number);
  const portsBad = parsedPorts.some((p) => !Number.isInteger(p) || p < 1 || p > 65535);
  const next: SSHSettings = { ...v, ports: portsBad ? v.ports : parsedPorts, sources: restricted ? v.sources : [] };
  const dirty = JSON.stringify(next) !== JSON.stringify(q.data);
  const invalid = portsBad || (restricted && v.sources.length === 0);
  return (
    <Panel title="SSH access">
      <div className="flex flex-col gap-3 text-xs">
        <p className="text-muted">
          SSH is always open on port 22 and on every port <span className="font-mono">sshd</span> listens on, on every node, so you can get in when
          something goes wrong. Here you decide who may connect and how often.
        </p>
        <ChoiceCards
          label="Who may connect"
          value={restricted ? "only" : "anywhere"}
          onChange={(x) => setRestricted(x === "only")}
          options={[
            { value: "anywhere", title: "Anyone", description: "With the rate limit below in front.", icon: Globe },
            { value: "only", title: "Only these addresses", description: "Your office, VPN or bastion. Everyone else is dropped.", icon: ListChecks },
          ]}
        />
        {restricted && (
          <Field label="Allowed sources">
            <SourceList label="Allowed SSH sources" value={v.sources} onChange={(sources) => setV({ ...v, sources })} empty="add at least one address" />
          </Field>
        )}
        <div className={cn("grid grid-cols-1 sm:grid-cols-2", gap)}>
          <div className="flex flex-col gap-2">
            <Toggle
              checked={v.rate > 0}
              onChange={(on) => setV({ ...v, rate: on ? 10 : 0, burst: on ? 5 : 0, autoBan: on && v.autoBan })}
              label="Rate limit new connections"
              hint="Per source address; slows down password guessing."
            />
            {v.rate > 0 && (
              <div className="flex flex-wrap items-end gap-1.5">
                <Field label="Per minute">
                  <Input type="number" min={1} max={10000} value={v.rate} onChange={(e) => setV({ ...v, rate: Number(e.target.value) })} className="w-24" />
                </Field>
                <Field label="Burst">
                  <Input type="number" min={1} max={10000} value={v.burst} onChange={(e) => setV({ ...v, burst: Number(e.target.value) })} className="w-24" />
                </Field>
              </div>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <Toggle
              checked={v.autoBan}
              disabled={v.rate === 0}
              onChange={(autoBan) => setV({ ...v, autoBan })}
              label="Auto-ban sources over the limit"
              hint="Blocked on that node; listed under Blocked, where you can lift it."
            />
            {v.autoBan && (
              <Field label="Ban for (minutes)">
                <Input type="number" min={1} max={10080} value={v.banMinutes} onChange={(e) => setV({ ...v, banMinutes: Number(e.target.value) })} className="w-24" />
              </Field>
            )}
          </div>
        </div>
        <Field label="Never rate limited" hint="Cluster nodes always are exempt.">
          <SourceList label="Exempt sources" value={v.exempt} onChange={(exempt) => setV({ ...v, exempt })} />
        </Field>
        <Field label="Extra SSH ports" hint="Besides 22 and the ports sshd is seen listening on.">
          <Input value={ports} onChange={(e) => setPorts(e.target.value)} placeholder="2222" className="w-48 font-mono" />
        </Field>
        {portsBad && <Alert tone="warn">Ports must be numbers from 1 to 65535.</Alert>}
        {save.error && <Alert>{errText(save.error)}</Alert>}
        <div className="flex items-center gap-2">
          <Button variant="primary" disabled={!dirty || invalid || save.isPending} onClick={() => save.mutate(next)}>
            Save
          </Button>
          {save.isSuccess && !dirty && <span className="text-ok">Saved. Every node's firewall is updating.</span>}
        </div>
      </div>
    </Panel>
  );
}

/** The block list, of every node or one. */
export function BlocksPanel({ node, title = "Blocked" }: { node?: string; title?: string }) {
  const qc = useQueryClient();
  const { block, dialog } = useBlocker();
  const q = useQuery({
    queryKey: ["firewall", "blocks", node ?? ""],
    queryFn: async () => (await api<{ items: FirewallBlock[] }>("GET", `/firewall/blocks${node ? `?node=${encodeURIComponent(node)}` : ""}`)).items,
    refetchInterval: 15_000,
  });
  const del = useMutation({
    mutationFn: (id: string) => api("DELETE", `/firewall/blocks/${id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["firewall"] });
      void qc.invalidateQueries({ queryKey: ["node-firewall"] });
    },
  });
  return (
    <Panel
      title={title}
      flush
      actions={
        <Button onClick={() => block("", node)}>
          <Ban className="size-3.5" /> Block an address
        </Button>
      }
    >
      {dialog}
      {del.error && (
        <div className="p-2">
          <Alert>{errText(del.error)}</Alert>
        </div>
      )}
      <DataTable
        rows={q.data ?? []}
        rowKey={(b) => b.id}
        empty={!q.isLoading && <EmptyState icon={ShieldCheck} title="Nothing blocked" />}
        columns={[
          { header: "Source", cell: (b) => <span className="font-mono">{b.source}</span> },
          { header: "Where", cell: (b) => (b.node ? <span>{b.node}</span> : <span className="text-muted">every node</span>) },
          { header: "Expires", cell: (b) => <span className="text-muted whitespace-nowrap">{until(b.expiresAt)}</span> },
          {
            header: "Why",
            className: "w-full",
            cell: (b) => (
              <span className="flex flex-wrap items-center gap-1">
                {b.auto ? <StatusBadge tone="warn">auto-ban</StatusBadge> : <span className="text-muted">{b.createdBy || "—"}</span>}
                {b.reason && <span>{b.reason}</span>}
                <span className="text-faint">{since(b.createdAt)}</span>
              </span>
            ),
          },
          {
            header: "",
            cell: (b) => (
              <IconButton label="Unblock" onClick={async () => (await confirmAction(`Unblock ${b.source}?`)) && del.mutate(b.id)}>
                <Trash2 className="size-3.5" />
              </IconButton>
            ),
          },
        ]}
      />
    </Panel>
  );
}

interface NodeFirewall {
  enabled: boolean;
  node: string;
  state?: { generation: number; applied: number; pending: boolean; error: string; driftCorrections: number; updatedAt: string | null };
  rules: { id: string; kind: string; protocol: string; ports: string; sources: string[]; description: string; packets: number }[];
  guards: { id: string; ports: string; rate: number; unit: string; burst: number; maxConnections: number; dropped: number }[];
  blocked: number;
  denied: number;
  sshPorts?: number[];
  listeners: { protocol: string; address: string; port: number; process: string; verdict: string; rule?: string }[];
  listenersAt?: string;
  drops: { at: string; direction: string; src: string; srcName?: string; protocol: string; port: number; packets: number }[];
}

const kindLabel: Record<string, string> = {
  syncloud: "SynCloud",
  ssh: "SSH",
  service: "service port",
  database: "database",
  "load-balancer": "load balancer",
  policy: "policy",
};

const verdictTone: Record<string, "ok" | "warn" | "info" | "neutral" | "bad"> = {
  open: "warn",
  restricted: "info",
  blocked: "ok",
  local: "neutral",
  private: "neutral",
};

const verdictText: Record<string, string> = {
  open: "open to anyone",
  restricted: "some sources",
  blocked: "closed by the firewall",
  local: "this host only",
  private: "private network only",
};

function hostPort(a: string, p: number) {
  return a.includes(":") ? `[${a}]:${p}` : `${a}:${p}`;
}

/** Compute › Nodes › a node › Firewall. */
export function NodeFirewallTab({ nodeId }: { nodeId: string }) {
  const { block, dialog } = useBlocker();
  const q = useQuery({
    queryKey: ["node-firewall", nodeId],
    queryFn: () => api<NodeFirewall>("GET", `/nodes/${nodeId}/firewall`),
    refetchInterval: 10_000,
  });
  if (q.error) return <Alert tone="warn">{errText(q.error)}</Alert>;
  const f = q.data;
  if (!f) return <span className="text-faint text-xs">Loading…</span>;
  if (!f.enabled)
    return (
      <Alert tone="warn">
        The host firewall is off on this cluster (controller flag <span className="font-mono">--firewall=0</span>): every port that listens is
        reachable.
      </Alert>
    );
  const st = f.state;
  const state: ReactNode = !st ? (
    <StatusBadge tone="neutral">no report yet</StatusBadge>
  ) : st.error ? (
    <StatusBadge tone="bad">error: {st.error}</StatusBadge>
  ) : st.pending ? (
    <StatusBadge tone="info">applying, waiting for confirmation</StatusBadge>
  ) : st.applied < st.generation ? (
    <StatusBadge tone="info">updating</StatusBadge>
  ) : (
    <StatusBadge tone="ok">enforced</StatusBadge>
  );
  const exposed = f.listeners.filter((l) => l.verdict === "open");
  const guarded = f.guards.reduce((n, g) => n + g.dropped, 0);
  return (
    <div className={cn("flex flex-col", gap)}>
      {dialog}
      <div className={cn("grid grid-cols-2 lg:grid-cols-4", gap)}>
        <StatTile label="Firewall" value={state} hint={st && st.driftCorrections > 0 ? `${st.driftCorrections} outside changes undone` : "default deny"} />
        <StatTile label="Denied" value={f.denied.toLocaleString()} unit="packets" hint="closed ports and forwarded traffic" />
        <StatTile label="Rate limited" value={guarded.toLocaleString()} unit="packets" hint="over a per-source limit" />
        <StatTile label="Blocked sources" value={f.blocked.toLocaleString()} unit="packets" hint="dropped before anything else" />
      </div>
      <Panel title="Open ports" flush>
        <DataTable
          rows={f.rules}
          rowKey={(r) => `${r.id}-${r.protocol}-${r.ports}-${r.sources.join(",")}`}
          columns={[
            { header: "Port", cell: (r) => <span className="font-mono whitespace-nowrap">{`${r.protocol}${r.ports ? "/" + r.ports : ""}`}</span> },
            { header: "Opened for", cell: (r) => <StatusBadge tone={r.kind === "policy" ? "info" : "neutral"}>{kindLabel[r.kind] ?? r.kind}</StatusBadge> },
            {
              header: "From",
              cell: (r) =>
                r.sources.length === 0 ? <span>anyone</span> : <span className="font-mono">{r.sources.join(", ")}</span>,
            },
            { header: "", className: "w-full", cell: (r) => <span className="text-muted">{r.description}</span> },
            { header: "Packets", cell: (r) => <span className="font-mono">{r.packets.toLocaleString()}</span> },
          ]}
        />
        <p className="text-muted border-line border-t p-2 text-xs">
          Everything else is dropped, including ports opened by other tools (ufw, Docker's <span className="font-mono">-p</span>): changes made outside
          SynCloud are undone.
          {f.sshPorts && f.sshPorts.length > 0 && <> SSH is on {f.sshPorts.join(", ")}.</>}
        </p>
      </Panel>
      <Panel
        title="Listening"
        flush
        actions={f.listenersAt && <span className="text-faint text-xs">checked {since(f.listenersAt)}</span>}
      >
        {exposed.length > 0 && (
          <div className="p-2">
            <Alert tone="warn">
              Reachable by anyone: {exposed.map((l) => `${l.protocol}/${l.port}${l.process ? ` (${l.process})` : ""}`).join(", ")}.
            </Alert>
          </div>
        )}
        <DataTable
          rows={f.listeners}
          rowKey={(l) => `${l.protocol}-${l.address}-${l.port}`}
          empty={!q.isLoading && <EmptyState icon={KeyRound} title="Not reported yet">The agent reports listening ports once a minute.</EmptyState>}
          columns={[
            { header: "Address", cell: (l) => <span className="font-mono whitespace-nowrap">{hostPort(l.address, l.port)}</span> },
            { header: "Protocol", cell: (l) => <span className="font-mono">{l.protocol}</span> },
            { header: "Process", cell: (l) => <span className="font-mono">{l.process || "—"}</span> },
            {
              header: "Reachable",
              className: "w-full",
              cell: (l) => <StatusBadge tone={verdictTone[l.verdict] ?? "neutral"}>{verdictText[l.verdict] ?? l.verdict}</StatusBadge>,
            },
          ]}
        />
      </Panel>
      <Panel title="Unauthorized attempts" flush>
        <DataTable
          rows={f.drops}
          rowKey={(d) => `${d.at}-${d.src}-${d.port}-${d.direction}`}
          empty={!q.isLoading && <EmptyState icon={ShieldCheck} title="No attempts recorded" />}
          columns={[
            { header: "When", cell: (d) => <span className="text-muted whitespace-nowrap">{new Date(d.at).toLocaleTimeString()}</span> },
            {
              header: "Source",
              cell: (d) => (
                <span>
                  <span className="font-mono">{d.src}</span>
                  {d.srcName && <span className="text-muted"> {d.srcName}</span>}
                </span>
              ),
            },
            { header: "Port", cell: (d) => <span className="font-mono">{`${d.protocol}/${d.port}`}</span> },
            {
              header: "What",
              className: "w-full",
              cell: (d) =>
                d.direction === "limit" ? (
                  <StatusBadge tone="warn">over a rate limit</StatusBadge>
                ) : d.direction === "host" ? (
                  <span className="text-muted">closed port</span>
                ) : (
                  <span className="text-muted">security group ({d.direction})</span>
                ),
            },
            { header: "Packets", cell: (d) => <span className="font-mono">{d.packets}</span> },
            {
              header: "",
              cell: (d) => (
                <Button onClick={() => block(d.src, f.node)}>
                  <Ban className="size-3.5" /> Block
                </Button>
              ),
            },
          ]}
        />
      </Panel>
      <BlocksPanel node={f.node} title="Blocked on this node" />
    </div>
  );
}
