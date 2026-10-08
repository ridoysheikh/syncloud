import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ArrowDownToLine, ArrowUpFromLine, Pencil, Plus, Route as RouteIcon, Server, ShieldCheck, ShieldX, Trash2, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useNodes } from "@/lib/nodes";
import { useProjects, useServices } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input, StatusBadge } from "@/ui/controls";
import { ChipSelect, ChoiceField, Segmented } from "@/ui/choice";
import { cn, gap } from "@/ui/cn";
import { confirmAction } from "@/ui/dialogs";

export interface SGRule {
  protocol: "tcp" | "udp" | "icmp" | "any";
  ports: string;
  peers: string[];
  description: string;
  packets?: number;
  bytes?: number;
}

export interface SecurityGroup {
  id: string;
  project: string;
  name: string;
  description: string;
  default: boolean;
  inbound: SGRule[];
  outbound: SGRule[];
  services: string[];
  members: string[];
  updatedAt: string;
}

const sel = "bg-bg border-line-strong focus:border-line-accent h-7 rounded-input border px-1.5 text-xs outline-none";

export function useSecurityGroups() {
  return useQuery({
    queryKey: ["security-groups"],
    queryFn: () => api<{ items: SecurityGroup[]; nodeErrors: Record<string, string> }>("GET", "/security-groups"),
    refetchInterval: 15_000,
  });
}

export function ruleWhat(r: SGRule) {
  if (r.protocol === "any") return "all traffic";
  return `${r.protocol}${r.ports ? ` ${r.ports}` : ""}`;
}

/** A peer as words: "service shop/production/web", "anywhere". */
export function peerLabel(p: string) {
  if (p === "any") return "anywhere";
  if (p === "cluster") return "cluster nodes";
  if (p === "environment:self") return "same environment";
  if (p === "project:self") return "same project";
  const [kind, rest] = p.split(/:(.*)/s);
  return rest ? `${kind} ${rest}` : p;
}

function hits(n?: number) {
  if (!n) return <span className="text-faint">0</span>;
  return n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(1)}k` : String(n);
}

function RuleList({ rules, dir }: { rules: SGRule[]; dir: "in" | "out" }) {
  if (rules.length === 0) return <span className="text-faint">{dir === "in" ? "nothing allowed in" : "nothing allowed out"}</span>;
  return (
    <div className="flex flex-col gap-0.5">
      {rules.map((r, i) => (
        <span key={i}>
          <span className="font-mono">{ruleWhat(r)}</span>
          <span className="text-muted">
            {" "}
            {dir === "in" ? "from" : "to"} {r.peers.map(peerLabel).join(", ")}
          </span>
          {r.description && <span className="text-faint"> · {r.description}</span>}
          <span className="text-faint ml-1.5 text-[10px]" title="packets matched since the agents started">
            {hits(r.packets)} {r.packets === 1 ? "hit" : "hits"}
          </span>
        </span>
      ))}
    </div>
  );
}

/** Network › Security groups: every project's groups (§8.3). */
export function SecurityGroupsPage() {
  const { data, isLoading } = useSecurityGroups();
  const qc = useQueryClient();
  const del = useMutation({
    mutationFn: (g: SecurityGroup) => api("DELETE", `/projects/${g.project}/security-groups/${g.name}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["security-groups"] }),
  });
  const groups = data?.items ?? [];
  const nodeErrors = Object.entries(data?.nodeErrors ?? {});
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Network"]}
        title="Security groups"
        actions={
          <Link to={"/network/security-groups/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New group
            </Button>
          </Link>
        }
      />
      <Alert tone="info">
        Groups only allow traffic; everything else between containers is dropped. A service uses the groups attached to it, or its project's{" "}
        <span className="font-mono">default</span> group (services in the same environment reach each other, all outbound traffic is allowed).
        Cluster nodes (Traefik, health checks) always reach every task.
      </Alert>
      {nodeErrors.map(([n, e]) => (
        <Alert key={n} tone="warn">
          Node {n} does not enforce security groups: {e}
        </Alert>
      ))}
      {del.error && <Alert>{del.error instanceof ApiError ? del.error.message : "Could not delete"}</Alert>}
      <Panel title="Groups" flush>
        <DataTable
          rows={groups}
          rowKey={(g) => g.id}
          empty={!isLoading && <EmptyState icon={ShieldCheck} title="No projects yet" />}
          columns={[
            {
              header: "Group",
              cell: (g) => (
                <div className="flex flex-col py-1">
                  <Link to={`/network/security-groups/${g.project}/${g.name}` as string} className="hover:text-accent font-medium">
                    {g.project}/{g.name}
                  </Link>
                  {g.default && <span className="text-faint">default</span>}
                </div>
              ),
            },
            { header: "Inbound", className: "w-1/3", cell: (g) => <RuleList rules={g.inbound} dir="in" /> },
            { header: "Outbound", className: "w-1/4", cell: (g) => <RuleList rules={g.outbound} dir="out" /> },
            {
              header: "Members",
              cell: (g) =>
                g.members.length ? (
                  <div className="flex flex-col">
                    {g.members.map((m) => (
                      <Link key={m} to={`/projects/${g.project}/${m.split("/")[0]}/services/${m.split("/")[1]}` as string} className="hover:text-accent">
                        {m}
                      </Link>
                    ))}
                  </div>
                ) : (
                  <span className="text-faint">none</span>
                ),
            },
            {
              header: "",
              cell: (g) => (
                <div className="flex">
                  <Link to={`/network/security-groups/${g.project}/${g.name}` as string}>
                    <IconButton label="Edit">
                      <Pencil className="size-3.5" />
                    </IconButton>
                  </Link>
                  {!g.default && (
                    <IconButton
                      label="Delete"
                      onClick={async () => (await confirmAction(`Delete ${g.project}/${g.name}? Its services fall back to the default group unless they have another.`)) && del.mutate(g)}
                    >
                      <Trash2 className="size-3.5" />
                    </IconButton>
                  )}
                </div>
              ),
            },
          ]}
        />
      </Panel>
      <ReachabilityPanel />
    </div>
  );
}

interface Verdict {
  from: string;
  to: string;
  protocol: string;
  port: number;
  verdict: {
    allowed: boolean;
    reason: string;
    egress?: { group: string; direction: string; index: number; rule: SGRule };
    ingress?: { group: string; direction: string; index: number; rule: SGRule };
  };
}

/** "Can A reach B?" (§8.3), answered with the deciding rule. */
export function ReachabilityPanel({ to: fixedTo }: { to?: string }) {
  const { data: services = [] } = useServices();
  const paths = useMemo(() => services.map((s) => `${s.project}/${s.environment}/${s.name}`).sort(), [services]);
  const [from, setFrom] = useState("");
  const [to, setTo] = useState(fixedTo ?? "");
  const [protocol, setProtocol] = useState("tcp");
  const [port, setPort] = useState("");
  const check = useMutation({
    mutationFn: () => api<Verdict>("POST", "/network/reachability", { from, to, protocol, port: Number(port) || 0 }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    check.mutate();
  };
  const v = check.data;
  return (
    <Panel title="Reachability check">
      <form onSubmit={submit} className="flex flex-wrap items-end gap-2">
        <Field label="From (a service, an IP address or platform)">
          <Input list="sg-paths" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="shop/production/web" className="w-64 font-mono" />
        </Field>
        {!fixedTo && (
          <Field label="To">
            <Input list="sg-paths" value={to} onChange={(e) => setTo(e.target.value)} placeholder="shop/production/db" className="w-64 font-mono" />
          </Field>
        )}
        <Field label="Protocol">
          <select value={protocol} onChange={(e) => setProtocol(e.target.value)} className={cn(sel, "h-8")}>
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
            <option value="icmp">ICMP</option>
          </select>
        </Field>
        <Field label="Port">
          <Input value={port} onChange={(e) => setPort(e.target.value)} disabled={protocol === "icmp"} placeholder="5432" className="w-24 font-mono" />
        </Field>
        <Button type="submit" variant="primary" disabled={!from || !to || check.isPending}>
          <RouteIcon className="size-3.5" /> Check
        </Button>
        <datalist id="sg-paths">
          {paths.map((p) => (
            <option key={p} value={p} />
          ))}
          <option value="platform" />
        </datalist>
      </form>
      {check.error && (
        <div className="mt-2">
          <Alert>{check.error instanceof ApiError ? check.error.message : "Check failed"}</Alert>
        </div>
      )}
      {v && (
        <div className={cn("mt-2 flex items-start gap-2 rounded-sm border p-2 text-xs", v.verdict.allowed ? "border-ok/20" : "border-bad/20")}>
          {v.verdict.allowed ? <ShieldCheck className="text-ok mt-0.5 size-4 shrink-0" /> : <ShieldX className="text-bad mt-0.5 size-4 shrink-0" />}
          <div className="flex flex-col gap-0.5">
            <span>
              <span className={cn("font-medium", v.verdict.allowed ? "text-ok" : "text-bad")}>{v.verdict.allowed ? "Allowed" : "Blocked"}</span>{" "}
              <span className="font-mono">
                {v.from} → {v.to} {v.protocol}
                {v.protocol !== "icmp" && `/${v.port}`}
              </span>
            </span>
            <span className="text-muted">{v.verdict.reason}</span>
            {[v.verdict.egress, v.verdict.ingress].map(
              (m) =>
                m && (
                  <span key={m.direction} className="text-muted">
                    {m.direction === "in" ? "Inbound" : "Outbound"} rule {m.index + 1} of{" "}
                    <Link to={`/network/security-groups/${m.group}` as string} className="hover:text-accent font-mono">
                      {m.group}
                    </Link>
                    : <span className="font-mono">{ruleWhat(m.rule)}</span> {m.direction === "in" ? "from" : "to"} {m.rule.peers.map(peerLabel).join(", ")}
                  </span>
                ),
            )}
          </div>
        </div>
      )}
    </Panel>
  );
}

const emptyRule = (): SGRule => ({ protocol: "tcp", ports: "", peers: [], description: "" });

/** Adds peers one at a time, with suggestions. */
function PeerPicker({ value, onChange, suggestions }: { value: string[]; onChange: (v: string[]) => void; suggestions: string[] }) {
  const [draft, setDraft] = useState("");
  const add = () => {
    const v = draft.trim();
    if (v && !value.includes(v)) onChange([...value, v]);
    setDraft("");
  };
  return (
    <div className="border-line-strong flex min-h-7 flex-wrap items-center gap-1 rounded-sm border px-1 py-0.5">
      {value.map((p) => (
        <span key={p} className="bg-hover flex items-center gap-1 rounded-sm px-1.5 py-0.5 font-mono text-[11px]">
          {p}
          <button type="button" onClick={() => onChange(value.filter((x) => x !== p))} className="text-faint hover:text-fg" aria-label={`Remove ${p}`}>
            <X className="size-3" />
          </button>
        </span>
      ))}
      <input
        list="sg-peers"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === ",") {
            e.preventDefault();
            add();
          }
        }}
        onBlur={add}
        placeholder={value.length ? "" : "add a peer…"}
        className="min-w-32 flex-1 bg-transparent px-1 font-mono text-xs outline-none"
      />
      <datalist id="sg-peers">
        {suggestions.map((s) => (
          <option key={s} value={s} />
        ))}
      </datalist>
    </div>
  );
}

function RulesEditor({ title, dir, rules, onChange, suggestions }: { title: string; dir: "in" | "out"; rules: SGRule[]; onChange: (r: SGRule[]) => void; suggestions: string[] }) {
  const set = (i: number, patch: Partial<SGRule>) => onChange(rules.map((r, j) => (i === j ? { ...r, ...patch } : r)));
  return (
    <Panel title={title}>
      <div className="flex flex-col gap-1.5">
        {rules.length === 0 && (
          <span className="text-faint text-xs">{dir === "in" ? "Nothing may connect to the members." : "The members may not open any connection."}</span>
        )}
        {rules.map((r, i) => (
          <div key={i} className="grid grid-cols-1 items-start gap-1.5 md:grid-cols-[5.5rem_7rem_minmax(0,2fr)_minmax(0,1fr)_auto]">
            <select value={r.protocol} onChange={(e) => set(i, { protocol: e.target.value as SGRule["protocol"], ports: "" })} className={sel}>
              <option value="tcp">TCP</option>
              <option value="udp">UDP</option>
              <option value="icmp">ICMP</option>
              <option value="any">All</option>
            </select>
            <Input
              value={r.ports}
              disabled={r.protocol === "icmp" || r.protocol === "any"}
              onChange={(e) => set(i, { ports: e.target.value })}
              placeholder="all ports"
              className="h-7 font-mono"
            />
            <PeerPicker value={r.peers} onChange={(peers) => set(i, { peers })} suggestions={suggestions} />
            <Input value={r.description} onChange={(e) => set(i, { description: e.target.value })} placeholder="description" className="h-7" />
            <IconButton label="Remove rule" onClick={() => onChange(rules.filter((_, j) => j !== i))}>
              <X className="size-3.5" />
            </IconButton>
          </div>
        ))}
        <div>
          <Button variant="ghost" onClick={() => onChange([...rules, emptyRule()])}>
            <Plus className="size-3.5" /> Add {dir === "in" ? "inbound" : "outbound"} rule
          </Button>
        </div>
      </div>
    </Panel>
  );
}

interface Change {
  service: string;
  lines: string[];
  tasks: number;
  nodes: string[];
}

/** Create or edit a security group, with a live preview of what changes. */
export function SecurityGroupPage() {
  const params = useParams({ strict: false }) as { project?: string; group?: string };
  const isNew = !params.group;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: projects = [] } = useProjects();
  const { data: services = [] } = useServices();
  const { data: all } = useSecurityGroups();
  const existing = all?.items.find((g) => g.project === params.project && g.name === params.group);
  const [project, setProject] = useState(params.project ?? new URLSearchParams(window.location.search).get("project") ?? "");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [inbound, setInbound] = useState<SGRule[]>([emptyRule()]);
  const [outbound, setOutbound] = useState<SGRule[]>([{ protocol: "any", ports: "", peers: ["any"], description: "All outbound traffic" }]);
  const [attached, setAttached] = useState<string[]>([]);
  const [loaded, setLoaded] = useState(false);
  useEffect(() => {
    if (existing && !loaded) {
      setProject(existing.project);
      setName(existing.name);
      setDescription(existing.description);
      setInbound(existing.inbound.map(({ packets: _p, bytes: _b, ...r }) => r));
      setOutbound(existing.outbound.map(({ packets: _p, bytes: _b, ...r }) => r));
      setAttached(existing.services);
      setLoaded(true);
    }
  }, [existing, loaded]);
  useEffect(() => {
    if (isNew && !project && projects[0]) setProject(projects[0].name);
  }, [isNew, project, projects]);

  const projectServices = services.filter((s) => s.project === project).map((s) => `${s.environment}/${s.name}`).sort();
  const suggestions = useMemo(() => {
    const envs = projects.find((p) => p.name === project)?.environments ?? [];
    return [
      "environment:self",
      "project:self",
      "any",
      "cluster",
      ...envs.map((e) => `environment:${e}`),
      ...projectServices.map((s) => `service:${s}`),
      ...(all?.items ?? []).filter((g) => g.project === project).map((g) => `group:${g.name}`),
      ...services.filter((s) => s.project !== project).map((s) => `service:${s.project}/${s.environment}/${s.name}`),
      ...projects.filter((p) => p.name !== project).map((p) => `project:${p.name}`),
    ];
  }, [projects, project, services, all, projectServices]);

  const body = { name, description, inbound, outbound, services: attached };
  // Preview what saving would change, a moment after each edit.
  const [preview, setPreview] = useState<{ changes: Change[] } | null>(null);
  const [previewError, setPreviewError] = useState("");
  const bodyKey = JSON.stringify(body);
  useEffect(() => {
    if (!project || !name || (!isNew && !loaded)) return;
    const t = setTimeout(() => {
      api<{ changes: Change[] }>("POST", `/projects/${project}/security-groups/preview`, { ...body, existing: params.group ?? "" })
        .then((p) => {
          setPreview(p);
          setPreviewError("");
        })
        .catch((e) => setPreviewError(e instanceof ApiError ? e.message : "Preview failed"));
    }, 400);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bodyKey, project, loaded]);

  const save = useMutation({
    mutationFn: () =>
      isNew ? api("POST", `/projects/${project}/security-groups`, body) : api("PUT", `/projects/${project}/security-groups/${params.group}`, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["security-groups"] });
      void navigate({ to: "/network/security-groups" as string });
    },
  });

  if (!isNew && all && !existing) {
    return (
      <div className={cn("flex flex-col", gap)}>
        <PageHeader crumbs={["Network", "Security groups"]} title="Group not found" />
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
        crumbs={["Network", "Security groups"]}
        title={isNew ? "New security group" : `${project}/${name}`}
        actions={
          <>
            <Button variant="ghost" onClick={() => navigate({ to: "/network/security-groups" as string })}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={!project || !name || save.isPending}>
              <ShieldCheck className="size-3.5" /> {isNew ? "Create group" : "Save and apply"}
            </Button>
          </>
        }
      />
      <Panel title="Group">
        <div className="grid max-w-4xl grid-cols-1 gap-2 sm:grid-cols-3">
          <Field label="Project">
            <select value={project} onChange={(e) => setProject(e.target.value)} disabled={!isNew} className={cn(sel, "h-8 w-full")}>
              {projects.map((p) => (
                <option key={p.name}>{p.name}</option>
              ))}
            </select>
          </Field>
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} disabled={existing?.default} placeholder="db" className="font-mono" autoFocus={isNew} />
          </Field>
          <Field label="Description">
            <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Postgres" />
          </Field>
        </div>
        <div className="mt-2">
          <ChoiceField
            label="Attached services"
            hint={
              existing?.default
                ? "Services with no group of their own use the default group too."
                : "A service with any attached group no longer uses the default group: attach default as well to keep it."
            }
          >
            <ChipSelect
              label="Attached services"
              mono
              empty="No services in this project yet."
              value={attached}
              onChange={setAttached}
              options={projectServices.map((s) => ({ value: s, label: s }))}
            />
          </ChoiceField>
        </div>
      </Panel>
      <RulesEditor title="Inbound rules (who may connect to the members)" dir="in" rules={inbound} onChange={setInbound} suggestions={suggestions} />
      <RulesEditor title="Outbound rules (where the members may connect)" dir="out" rules={outbound} onChange={setOutbound} suggestions={suggestions} />
      <Panel title="Preview">
        {previewError ? (
          <Alert tone="warn">{previewError}</Alert>
        ) : !preview ? (
          <span className="text-faint text-xs">Name the group to see what saving it would change.</span>
        ) : preview.changes.length === 0 ? (
          <span className="text-muted text-xs">No service's effective rules change.</span>
        ) : (
          <div className="flex flex-col gap-2">
            {preview.changes.map((c) => (
              <div key={c.service} className="flex flex-col gap-0.5">
                <span className="text-xs">
                  <span className="font-mono font-medium">{c.service}</span>
                  <span className="text-faint">
                    {" "}
                    · {c.tasks} task{c.tasks === 1 ? "" : "s"}
                    {c.nodes.length > 0 && ` on ${c.nodes.join(", ")}`}
                  </span>
                </span>
                <pre className="bg-bg border-line overflow-x-auto rounded-sm border p-1.5 font-mono text-[11px] leading-relaxed">
                  {c.lines.map((l, i) => (
                    <div key={i} className={cn(l.startsWith("+") ? "text-ok" : l.startsWith("-") ? "text-bad" : "text-muted")}>
                      {l}
                    </div>
                  ))}
                </pre>
              </div>
            ))}
          </div>
        )}
      </Panel>
      <p className="text-muted text-xs">
        Peers: <span className="font-mono">environment:self</span> (the member's own environment), <span className="font-mono">service:ENV/NAME</span>,{" "}
        <span className="font-mono">group:NAME</span>, <span className="font-mono">environment:ENV</span>, <span className="font-mono">project:NAME</span>, other
        projects as <span className="font-mono">service:PROJECT/ENV/NAME</span>, an IPv4 address or CIDR, <span className="font-mono">cluster</span> or{" "}
        <span className="font-mono">any</span>. Replies to allowed connections always flow.
      </p>
      {save.error && <Alert>{save.error instanceof ApiError ? save.error.message : "Could not save the group"}</Alert>}
    </form>
  );
}

/** The service page's Security tab: its groups and effective rules. */
export function ServiceSecurityPanel({ path, project, env, name }: { path: string; project: string; env: string; name: string }) {
  const { data, error } = useQuery({
    queryKey: ["service-security", path],
    queryFn: () => api<{ groups: SecurityGroup[]; lines: string[] }>("GET", `${path}/security`),
    refetchInterval: 15_000,
  });
  if (error) return <Alert tone="warn">{error instanceof ApiError ? error.message : "Unavailable"}</Alert>;
  return (
    <div className={cn("flex flex-col", gap)}>
      <Panel
        title="Security groups"
        flush
        actions={
          <Link to={"/network/security-groups" as string} className="text-muted hover:text-fg text-xs">
            All groups
          </Link>
        }
      >
        <DataTable
          rows={data?.groups ?? []}
          rowKey={(g) => g.id}
          columns={[
            {
              header: "Group",
              cell: (g) => (
                <Link to={`/network/security-groups/${g.project}/${g.name}` as string} className="hover:text-accent font-mono">
                  {g.name}
                  {g.default && <span className="text-faint font-sans"> (default)</span>}
                </Link>
              ),
            },
            { header: "Inbound", className: "w-1/2", cell: (g) => <RuleList rules={g.inbound} dir="in" /> },
            { header: "Outbound", className: "w-1/3", cell: (g) => <RuleList rules={g.outbound} dir="out" /> },
          ]}
        />
      </Panel>
      <ReachabilityPanel to={`${project}/${env}/${name}`} />
    </div>
  );
}

interface Drop {
  at: string;
  node: string;
  direction: "host" | "in" | "out";
  src: string;
  srcName?: string;
  dst?: string;
  dstName?: string;
  protocol: string;
  port: number;
  packets: number;
}

/** The drop log (§8.3): connection attempts a default deny dropped. */
export function DropLogPanel() {
  const nodes = useNodes();
  const [direction, setDirection] = useState("");
  const [node, setNode] = useState("");
  const [since, setSince] = useState("1h");
  const q = new URLSearchParams({ since, ...(direction && { direction }), ...(node && { node }) });
  const { data, isLoading, error } = useQuery({
    queryKey: ["firewall", "drops", q.toString()],
    queryFn: () => api<{ items: Drop[]; source: string }>("GET", `/firewall/drops?${q}`),
    refetchInterval: 10_000,
  });
  const who = (ip?: string, name?: string) =>
    ip ? (
      <span>
        <span className="font-mono">{ip}</span>
        {name && <span className="text-muted"> {name}</span>}
      </span>
    ) : null;
  return (
    <Panel
      title="Drop log"
      flush
      actions={
        <>
          <Segmented
            label="Direction"
            size="sm"
            value={direction}
            onChange={setDirection}
            options={[
              { value: "", label: "all" },
              { value: "in", label: "inbound", icon: ArrowDownToLine },
              { value: "out", label: "outbound", icon: ArrowUpFromLine },
              { value: "host", label: "host", icon: Server },
            ]}
          />
          <select value={node} onChange={(e) => setNode(e.target.value)} className={sel}>
            <option value="">every node</option>
            {(nodes.data ?? []).map((n) => (
              <option key={n.id} value={n.name}>
                {n.name}
              </option>
            ))}
          </select>
          <select value={since} onChange={(e) => setSince(e.target.value)} className={sel}>
            <option value="15m">15 min</option>
            <option value="1h">1 hour</option>
            <option value="24h">24 hours</option>
            <option value="168h">7 days</option>
          </select>
        </>
      }
    >
      {error && (
        <div className="p-3">
          <Alert tone="warn">{error instanceof ApiError ? error.message : "Unavailable"}</Alert>
        </div>
      )}
      <DataTable
        rows={data?.items ?? []}
        rowKey={(d) => `${d.at}-${d.node}-${d.src}-${d.dst}-${d.port}`}
        empty={!isLoading && <EmptyState icon={ShieldCheck} title="Nothing dropped" />}
        columns={[
          { header: "When", cell: (d) => <span className="text-muted whitespace-nowrap">{new Date(d.at).toLocaleTimeString()}</span> },
          {
            header: "Where",
            cell: (d) => (
              <StatusBadge tone={d.direction === "host" ? "warn" : "bad"}>
                {d.direction === "host" ? `host ${d.node}` : d.direction === "in" ? `in · ${d.node}` : `out · ${d.node}`}
              </StatusBadge>
            ),
          },
          { header: "Source", cell: (d) => who(d.src, d.srcName) },
          { header: "Destination", className: "w-full", cell: (d) => (d.direction === "host" ? <span className="text-muted">node {d.node}</span> : who(d.dst, d.dstName)) },
          { header: "Port", cell: (d) => <span className="font-mono">{`${d.protocol}/${d.port}`}</span> },
          { header: "Packets", cell: (d) => <span className="font-mono">{d.packets}</span> },
        ]}
      />
    </Panel>
  );
}
