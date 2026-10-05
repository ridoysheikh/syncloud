import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Shield, Trash2, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useNodes } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { Dialog } from "@/ui/Dialog";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface Rule {
  id?: string;
  protocol: "tcp" | "udp" | "icmp" | "any";
  ports: string;
  sources: string[];
  description: string;
}

interface Policy {
  id: string;
  name: string;
  description: string;
  targets: string[];
  rules: Rule[];
  updatedAt: string;
}

interface Effective {
  enabled: boolean;
  rules: Rule[];
  clusterSources: string[];
  ruleset: string;
}

const key = ["firewall", "policies"];

function ruleLabel(r: Rule) {
  return `${r.protocol}${r.ports ? ` ${r.ports}` : ""}`;
}

/** Host firewall policies and each node's effective rules (§8.3). */
export function FirewallPage() {
  const qc = useQueryClient();
  const { data: policies = [], isLoading } = useQuery({
    queryKey: key,
    queryFn: async () => (await api<{ items: Policy[] }>("GET", "/firewall/policies")).items,
  });
  const nodes = useNodes();
  const nodeName = (id: string) => (id === "*" ? "all nodes" : (nodes.data?.find((n) => n.id === id)?.name ?? id));
  const [editing, setEditing] = useState<Policy | "new" | null>(null);
  const del = useMutation({
    mutationFn: (id: string) => api("DELETE", `/firewall/policies/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Network"]}
        title="Firewall"
        actions={
          <Button variant="primary" onClick={() => setEditing("new")}>
            <Plus className="size-3.5" /> New policy
          </Button>
        }
      />
      <Alert tone="info">
        Inbound traffic on public interfaces is denied unless a rule allows it. The private network, established connections, WireGuard
        between nodes and essential ICMP are always allowed; the controller also keeps HTTP, HTTPS and the agent gateway open. Nodes roll a
        change back by themselves if they lose the controller within 60 seconds.
      </Alert>
      <Panel title="Host policies" flush>
        <DataTable
          rows={policies}
          rowKey={(p) => p.id}
          empty={!isLoading && <EmptyState icon={Shield} title="No policies" />}
          columns={[
            {
              header: "Policy",
              cell: (p) => (
                <div className="flex flex-col py-1">
                  <span className="font-medium">{p.name}</span>
                  {p.description && <span className="text-faint">{p.description}</span>}
                </div>
              ),
            },
            { header: "Applies to", cell: (p) => <span className="text-muted">{p.targets.map(nodeName).join(", ")}</span> },
            {
              header: "Allows",
              className: "w-full",
              cell: (p) => (
                <div className="flex flex-col gap-0.5 py-1">
                  {p.rules.length === 0 && <span className="text-faint">nothing</span>}
                  {p.rules.map((r, i) => (
                    <span key={i}>
                      <span className="font-mono">{ruleLabel(r)}</span>
                      <span className="text-muted"> from {r.sources.length ? r.sources.join(", ") : "anywhere"}</span>
                      {r.description && <span className="text-faint"> · {r.description}</span>}
                    </span>
                  ))}
                </div>
              ),
            },
            {
              header: "",
              cell: (p) => (
                <div className="flex">
                  <IconButton label="Edit" onClick={() => setEditing(p)}>
                    <Pencil className="size-3.5" />
                  </IconButton>
                  <IconButton label="Delete" onClick={() => confirm(`Delete policy ${p.name}? Its ports close on every node it targets.`) && del.mutate(p.id)}>
                    <Trash2 className="size-3.5" />
                  </IconButton>
                </div>
              ),
            },
          ]}
        />
      </Panel>
      <EffectiveRules nodes={nodes.data ?? []} />
      {editing && <PolicyDialog policy={editing === "new" ? null : editing} nodes={nodes.data ?? []} onClose={() => setEditing(null)} />}
    </div>
  );
}

function EffectiveRules({ nodes }: { nodes: { id: string; name: string }[] }) {
  const [node, setNode] = useState("");
  const id = node || nodes[0]?.id || "";
  const { data, error } = useQuery({
    queryKey: ["firewall", "effective", id],
    queryFn: () => api<Effective>("GET", `/firewall/nodes/${id}/effective`),
    enabled: !!id,
  });
  const [raw, setRaw] = useState(false);
  return (
    <Panel
      title="Effective rules"
      flush
      actions={
        <>
          <select value={id} onChange={(e) => setNode(e.target.value)} className="bg-bg border-line h-7 rounded-sm border px-1.5 text-xs">
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.name}
              </option>
            ))}
          </select>
          <Button variant="ghost" onClick={() => setRaw(!raw)}>
            {raw ? "Rules" : "nftables"}
          </Button>
        </>
      }
    >
      {error ? (
        <div className="p-3">
          <Alert tone="warn">{error instanceof ApiError ? error.message : "Unavailable"}</Alert>
        </div>
      ) : raw ? (
        <pre className="text-muted max-h-96 overflow-auto p-3 font-mono text-[11px] leading-relaxed">{data?.ruleset}</pre>
      ) : (
        <DataTable
          rows={data?.rules ?? []}
          rowKey={(r) => r.id ?? ruleLabel(r)}
          empty={data && !data.enabled && <EmptyState icon={Shield} title="Host firewall disabled on this controller" />}
          columns={[
            {
              header: "Source of rule",
              cell: (r) =>
                r.id?.startsWith("builtin:") ? <StatusBadge tone="neutral">built-in</StatusBadge> : <span className="font-mono text-muted">{r.id}</span>,
            },
            { header: "Allows", cell: (r) => <span className="font-mono">{ruleLabel(r)}</span> },
            { header: "From", className: "w-full", cell: (r) => <span className="text-muted">{r.sources.length ? r.sources.join(", ") : "anywhere"}</span> },
            { header: "Description", cell: (r) => <span className="text-faint">{r.description || "—"}</span> },
          ]}
        />
      )}
    </Panel>
  );
}

const emptyRule = (): Rule => ({ protocol: "tcp", ports: "", sources: [], description: "" });

function PolicyDialog({ policy, nodes, onClose }: { policy: Policy | null; nodes: { id: string; name: string }[]; onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(policy?.name ?? "");
  const [description, setDescription] = useState(policy?.description ?? "");
  const [targets, setTargets] = useState<string[]>(policy?.targets ?? ["*"]);
  const [rules, setRules] = useState<Rule[]>(policy?.rules.length ? policy.rules : [emptyRule()]);
  const save = useMutation({
    mutationFn: () => {
      const body = { name, description, targets, rules };
      return policy ? api("PUT", `/firewall/policies/${policy.id}`, body) : api("POST", "/firewall/policies", body);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["firewall"] });
      onClose();
    },
  });
  const setRule = (i: number, patch: Partial<Rule>) => setRules(rules.map((r, j) => (i === j ? { ...r, ...patch } : r)));
  const all = targets.includes("*");

  return (
    <Dialog
      open
      onClose={onClose}
      title={policy ? `Edit ${policy.name}` : "New firewall policy"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={save.isPending || !name} onClick={() => save.mutate()}>
            {save.isPending ? "Saving…" : "Save and apply"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-2.5">
        <div className="grid grid-cols-2 gap-2">
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="office-ssh" className="font-mono" />
          </Field>
          <Field label="Description">
            <Input value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
        </div>
        <Field label="Applies to">
          <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs">
            <label className="flex items-center gap-1.5">
              <input type="checkbox" checked={all} onChange={(e) => setTargets(e.target.checked ? ["*"] : [])} /> All nodes
            </label>
            {!all &&
              nodes.map((n) => (
                <label key={n.id} className="flex items-center gap-1.5">
                  <input
                    type="checkbox"
                    checked={targets.includes(n.id)}
                    onChange={(e) => setTargets(e.target.checked ? [...targets, n.id] : targets.filter((t) => t !== n.id))}
                  />
                  {n.name}
                </label>
              ))}
          </div>
        </Field>
        <div className="flex flex-col gap-1.5">
          <span className="text-muted text-xs font-medium">Allow inbound</span>
          {rules.map((r, i) => (
            <div key={i} className="grid grid-cols-[5.5rem_6.5rem_1fr_1fr_auto] items-center gap-1.5">
              <select value={r.protocol} onChange={(e) => setRule(i, { protocol: e.target.value as Rule["protocol"], ports: "" })} className="bg-bg border-line h-7 rounded-sm border px-1.5 text-xs">
                <option value="tcp">TCP</option>
                <option value="udp">UDP</option>
                <option value="icmp">ICMP</option>
                <option value="any">Any</option>
              </select>
              <Input
                value={r.ports}
                disabled={r.protocol === "icmp" || r.protocol === "any"}
                onChange={(e) => setRule(i, { ports: e.target.value })}
                placeholder="22 / 8000-8100"
                className="font-mono"
              />
              <Input
                value={r.sources.join(", ")}
                onChange={(e) => setRule(i, { sources: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })}
                placeholder="anywhere, or 203.0.113.0/24, cluster"
                className="font-mono"
              />
              <Input value={r.description} onChange={(e) => setRule(i, { description: e.target.value })} placeholder="description" />
              <IconButton label="Remove rule" onClick={() => setRules(rules.filter((_, j) => j !== i))}>
                <X className="size-3.5" />
              </IconButton>
            </div>
          ))}
          <div>
            <Button variant="ghost" onClick={() => setRules([...rules, emptyRule()])}>
              <Plus className="size-3.5" /> Add rule
            </Button>
          </div>
        </div>
        {save.error && <Alert>{save.error instanceof ApiError ? save.error.message : "Could not save"}</Alert>}
      </div>
    </Dialog>
  );
}
