import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Shield, Trash2, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useNodes } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { DropLogPanel } from "./SecurityGroups";

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
          <Link to={"/network/firewall/policies/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New policy
            </Button>
          </Link>
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
                  <Link to={`/network/firewall/policies/${p.id}` as string}>
                    <IconButton label="Edit">
                      <Pencil className="size-3.5" />
                    </IconButton>
                  </Link>
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
      <DropLogPanel />
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
  const nodeName = nodes.find((n) => n.id === id)?.name ?? "";
  const counters = useQuery({
    queryKey: ["firewall", "counters", nodeName],
    queryFn: async () => (await api<{ items: { id: string; packets: number }[] }>("GET", `/firewall/counters?node=${nodeName}`)).items,
    enabled: !!nodeName,
    refetchInterval: 15_000,
  });
  const hitsOf = (rid?: string) => counters.data?.find((c) => c.id === rid)?.packets ?? 0;
  const denied = hitsOf("default deny");
  return (
    <Panel
      title={`Effective rules${denied ? ` · ${denied.toLocaleString()} packets denied` : ""}`}
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
            { header: "Hits", cell: (r) => <span className="text-muted font-mono">{hitsOf(r.id).toLocaleString()}</span> },
          ]}
        />
      )}
    </Panel>
  );
}

const emptyRule = (): Rule => ({ protocol: "tcp", ports: "", sources: [], description: "" });

/** Create or edit a host firewall policy, as a full page. */
export function FirewallPolicyPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const isNew = !id;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: policies } = useQuery({
    queryKey: key,
    queryFn: async () => (await api<{ items: Policy[] }>("GET", "/firewall/policies")).items,
  });
  const nodes = useNodes().data ?? [];
  const policy = policies?.find((p) => p.id === id);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [targets, setTargets] = useState<string[]>(["*"]);
  const [rules, setRules] = useState<Rule[]>([emptyRule()]);
  const [loaded, setLoaded] = useState(false);
  useEffect(() => {
    if (policy && !loaded) {
      setName(policy.name);
      setDescription(policy.description);
      setTargets(policy.targets);
      setRules(policy.rules.length ? policy.rules : [emptyRule()]);
      setLoaded(true);
    }
  }, [policy, loaded]);
  const back = () => navigate({ to: "/network/firewall" as string });
  const save = useMutation({
    mutationFn: () => {
      const body = { name, description, targets, rules };
      return policy ? api("PUT", `/firewall/policies/${policy.id}`, body) : api("POST", "/firewall/policies", body);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["firewall"] });
      void back();
    },
  });
  const setRule = (i: number, patch: Partial<Rule>) => setRules(rules.map((r, j) => (i === j ? { ...r, ...patch } : r)));
  const all = targets.includes("*");
  if (!isNew && policies && !policy) {
    return (
      <div className={cn("flex flex-col", gap)}>
        <PageHeader crumbs={["Network", "Firewall"]} title="Policy not found" />
      </div>
    );
  }
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      className={cn("flex flex-col", gap)}
    >
      <PageHeader
        crumbs={["Network", "Firewall"]}
        title={isNew ? "New host policy" : policy?.name ?? "Host policy"}
        actions={
          <>
            <Button variant="ghost" onClick={back}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={save.isPending || !name}>
              {save.isPending ? "Saving…" : "Save and apply"}
            </Button>
          </>
        }
      />
      <Panel title="Policy">
        <div className="flex max-w-4xl flex-col gap-2.5">
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            <Field label="Name">
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="office-ssh" className="font-mono" autoFocus={isNew} />
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
        </div>
      </Panel>
      <Panel title="Allow inbound (on the nodes' public addresses)">
        <div className="flex flex-col gap-1.5">
          {rules.map((r, i) => (
            <div key={i} className="grid grid-cols-1 items-center gap-1.5 md:grid-cols-[5.5rem_7rem_1fr_1fr_auto]">
              <select
                value={r.protocol}
                onChange={(e) => setRule(i, { protocol: e.target.value as Rule["protocol"], ports: "" })}
                className="bg-bg border-line h-7 rounded-sm border px-1.5 text-xs"
              >
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
                className="h-7 font-mono"
              />
              <Input
                value={r.sources.join(", ")}
                onChange={(e) => setRule(i, { sources: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })}
                placeholder="anywhere, or 203.0.113.0/24, cluster"
                className="h-7 font-mono"
              />
              <Input value={r.description} onChange={(e) => setRule(i, { description: e.target.value })} placeholder="description" className="h-7" />
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
      </Panel>
      <p className="text-muted text-xs">
        Everything else on public interfaces is denied. Nodes apply a change at once and roll it back by themselves if they lose the
        controller within 60 seconds.
      </p>
      {save.error && <Alert>{save.error instanceof ApiError ? save.error.message : "Could not save"}</Alert>}
    </form>
  );
}
