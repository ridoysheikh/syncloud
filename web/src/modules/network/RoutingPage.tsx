import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Check, CornerUpRight, FileArchive, FileCode, Gauge, Globe, KeyRound, Layers, Pencil, Plus, RotateCw, Shield, ShieldCheck, Trash2, X, Zap } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useProjects, useServices } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input, Toggle } from "@/ui/controls";
import { ChipSelect, ChoiceCards } from "@/ui/choice";
import { cn, gap } from "@/ui/cn";
import { confirmAction } from "@/ui/dialogs";
import { Select } from "@/ui/select";

type PresetType =
  | "ip-allowlist"
  | "rate-limit"
  | "basic-auth"
  | "redirect-www"
  | "cors"
  | "security-headers"
  | "circuit-breaker"
  | "compress"
  | "retry";

interface MiddlewareView {
  id: string;
  project: string;
  name: string;
  type: PresetType;
  config: Record<string, unknown>;
  services: string[];
}


function useMiddlewares() {
  return useQuery({
    queryKey: ["middlewares"],
    queryFn: () => api<{ items: MiddlewareView[]; types: { type: PresetType; description: string }[] }>("GET", "/middlewares"),
  });
}

/** One line describing a preset's settings. */
function summary(m: MiddlewareView) {
  const c = m.config as Record<string, any>;
  switch (m.type) {
    case "rate-limit":
      return `${c.average}/s per client, burst ${c.burst}`;
    case "basic-auth":
      return `users: ${(c.users ?? []).map((u: { username: string }) => u.username).join(", ")}`;
    case "ip-allowlist":
      return (c.sourceRange ?? []).join(", ");
    case "cors":
      return `origins: ${(c.origins ?? []).join(", ")}`;
    case "security-headers":
      return [c.hsts && "HSTS", c.frameDeny && "frame-deny", c.noSniff && "nosniff", c.referrerPolicy].filter(Boolean).join(", ") || "—";
    case "circuit-breaker":
      return c.expression;
    case "retry":
      return `${c.attempts} attempts`;
    case "redirect-www":
      return "www.host → host";
    default:
      return "";
  }
}

/** Network › Routing (§5.7): middleware presets, custom configuration and the served config. */
export function RoutingPage() {
  const { data, isLoading } = useMiddlewares();
  const qc = useQueryClient();
  const del = useMutation({
    mutationFn: (m: MiddlewareView) => api("DELETE", `/projects/${m.project}/middlewares/${m.name}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["middlewares"] }),
  });
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Network"]}
        title="Routing"
        actions={
          <Link to={"/network/routing/middlewares/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New middleware
            </Button>
          </Link>
        }
      />
      <Alert tone="info">
        Every HTTP route of a service applies its middlewares in a fixed order (allow-list, rate limit, auth, redirects, CORS, headers, circuit
        breaker, compression), after the global defaults, then retries a failed request on another task unless a retry preset says otherwise.
        Global options (TLS, timeouts, trusted proxies, retries) are under{" "}
        <Link to={"/network/traefik" as string} className="underline">
          Network › Traefik
        </Link>
        .
      </Alert>
      {del.error && <Alert>{del.error instanceof ApiError ? del.error.message : "Could not delete"}</Alert>}
      <Panel title="Middleware presets" flush>
        <DataTable
          rows={data?.items ?? []}
          rowKey={(m) => m.id}
          empty={!isLoading && <EmptyState icon={Layers} title="No middlewares yet">Add rate limits, passwords, allow-lists and more to any service.</EmptyState>}
          columns={[
            {
              header: "Middleware",
              cell: (m) => (
                <Link to={`/network/routing/middlewares/${m.project}/${m.name}` as string} className="hover:text-accent font-medium">
                  {m.project}/{m.name}
                </Link>
              ),
            },
            { header: "Type", cell: (m) => <span className="font-mono">{m.type}</span> },
            { header: "Settings", className: "w-full", cell: (m) => <span className="text-muted">{summary(m)}</span> },
            {
              header: "Services",
              cell: (m) =>
                m.services.length ? (
                  <div className="flex flex-col">
                    {m.services.map((s) => (
                      <Link key={s} to={`/projects/${m.project}/${s.split("/")[0]}/services/${s.split("/")[1]}` as string} className="hover:text-accent whitespace-nowrap">
                        {s}
                      </Link>
                    ))}
                  </div>
                ) : (
                  <span className="text-faint">none</span>
                ),
            },
            {
              header: "",
              cell: (m) => (
                <div className="flex">
                  <Link to={`/network/routing/middlewares/${m.project}/${m.name}` as string}>
                    <IconButton label="Edit">
                      <Pencil className="size-3.5" />
                    </IconButton>
                  </Link>
                  <IconButton label="Delete" onClick={async () => (await confirmAction(`Delete middleware ${m.project}/${m.name}?`)) && del.mutate(m)}>
                    <Trash2 className="size-3.5" />
                  </IconButton>
                </div>
              ),
            },
          ]}
        />
      </Panel>
      <CustomConfigPanel />
      <RawConfigPanel />
    </div>
  );
}

function CustomConfigPanel() {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ["traefik", "custom"], queryFn: () => api<{ yaml: string }>("GET", "/traefik/custom") });
  const [text, setText] = useState<string | null>(null);
  const value = text ?? data?.yaml ?? "";
  const [checked, setChecked] = useState<"" | "ok" | string>("");
  const validate = useMutation({
    mutationFn: () => api("POST", "/traefik/custom/validate", { yaml: value }),
    onSuccess: () => setChecked("ok"),
    onError: (e) => setChecked(e instanceof ApiError ? e.message : "Invalid"),
  });
  const save = useMutation({
    mutationFn: () => api("PUT", "/traefik/custom", { yaml: value }),
    onSuccess: () => {
      setChecked("");
      setText(null);
      void qc.invalidateQueries({ queryKey: ["traefik"] });
    },
  });
  const dirty = text !== null && text !== (data?.yaml ?? "");
  return (
    <Panel
      title="Custom configuration (advanced)"
      actions={
        <>
          <Button variant="ghost" disabled={validate.isPending} onClick={() => validate.mutate()}>
            <Check className="size-3.5" /> Validate
          </Button>
          <Button variant="primary" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
            {save.isPending ? "Applying…" : "Validate and apply"}
          </Button>
        </>
      }
    >
      <p className="text-muted mb-1.5 text-xs">
        Traefik dynamic configuration (YAML) merged into the generated one, for what the dashboard does not cover: extra routers, services and
        middlewares under <span className="font-mono">http</span>, <span className="font-mono">tcp</span> or <span className="font-mono">udp</span>.
        Names starting with <span className="font-mono">svc-</span>, <span className="font-mono">dom-</span>, <span className="font-mono">mw-</span> or{" "}
        <span className="font-mono">syncloud-</span> are SynCloud's; routers may use them. It is checked before it is applied, so a mistake never
        stops routing.
      </p>
      <textarea
        value={value}
        onChange={(e) => {
          setText(e.target.value);
          setChecked("");
        }}
        spellCheck={false}
        rows={12}
        placeholder={"http:\n  routers:\n    legacy:\n      rule: Host(`legacy.example.com`)\n      service: legacy\n  services:\n    legacy:\n      loadBalancer:\n        servers:\n          - url: http://10.0.0.5:8080"}
        className="bg-bg border-line-strong focus:border-line-accent w-full rounded-input border p-2 font-mono text-xs outline-none"
      />
      {checked === "ok" && <p className="text-ok mt-1 text-xs">Valid.</p>}
      {checked && checked !== "ok" && (
        <div className="mt-1">
          <Alert>{checked}</Alert>
        </div>
      )}
      {save.error && (
        <div className="mt-1">
          <Alert>{save.error instanceof ApiError ? save.error.message : "Could not apply"}</Alert>
        </div>
      )}
    </Panel>
  );
}

function RawConfigPanel() {
  const [open, setOpen] = useState(false);
  const { data } = useQuery({
    queryKey: ["traefik", "config"],
    queryFn: () => api<Record<string, unknown>>("GET", "/traefik/config"),
    enabled: open,
    refetchInterval: open ? 5000 : false,
  });
  return (
    <Panel
      title="Served configuration"
      actions={
        <Button variant="ghost" onClick={() => setOpen(!open)}>
          <FileCode className="size-3.5" /> {open ? "Hide" : "Show"}
        </Button>
      }
    >
      {open ? (
        <pre className="text-muted max-h-[32rem] overflow-auto font-mono text-[11px] leading-relaxed">{JSON.stringify(data ?? {}, null, 2)}</pre>
      ) : (
        <p className="text-muted text-xs">Exactly what Traefik polls every 2 seconds, read-only. Certificates, keys and password hashes are redacted.</p>
      )}
    </Panel>
  );
}

// ── editor ───────────────────────────────────────────────────────────────────

const presetIcons: Record<PresetType, typeof Layers> = {
  "ip-allowlist": ShieldCheck,
  "rate-limit": Gauge,
  "basic-auth": KeyRound,
  "redirect-www": CornerUpRight,
  cors: Globe,
  "security-headers": Shield,
  "circuit-breaker": Zap,
  compress: FileArchive,
  retry: RotateCw,
};

const presetLabels: Record<PresetType, string> = {
  "ip-allowlist": "IP allow-list",
  "rate-limit": "Rate limit",
  "basic-auth": "Basic auth",
  "redirect-www": "Redirect www",
  cors: "CORS",
  "security-headers": "Security headers",
  "circuit-breaker": "Circuit breaker",
  compress: "Compress",
  retry: "Retry",
};

const defaults: Record<PresetType, Record<string, unknown>> = {
  "ip-allowlist": { sourceRange: [] },
  "rate-limit": { average: 20, burst: 40 },
  "basic-auth": { users: [{ username: "", password: "" }], realm: "" },
  "redirect-www": {},
  cors: { origins: ["https://"], methods: [], headers: [], credentials: false, maxAge: 600 },
  "security-headers": { hsts: true, frameDeny: true, noSniff: true, referrerPolicy: "strict-origin-when-cross-origin" },
  "circuit-breaker": { expression: "NetworkErrorRatio() > 0.30 || ResponseCodeRatio(500, 600, 0, 600) > 0.25" },
  compress: {},
  retry: { attempts: 3 },
};

const list = (v: unknown) => (Array.isArray(v) ? (v as string[]).join(", ") : "");
const split = (s: string) =>
  s
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);

function PresetFields({ type, config, set }: { type: PresetType; config: Record<string, any>; set: (c: Record<string, any>) => void }) {
  const patch = (p: Record<string, unknown>) => set({ ...config, ...p });
  switch (type) {
    case "rate-limit":
      return (
        <div className="grid grid-cols-2 gap-2">
          <Field label="Requests per second" hint="per client IP, on average">
            <Input type="number" min={1} value={config.average ?? ""} onChange={(e) => patch({ average: Number(e.target.value) })} />
          </Field>
          <Field label="Burst" hint="requests allowed at once">
            <Input type="number" min={1} value={config.burst ?? ""} onChange={(e) => patch({ burst: Number(e.target.value) })} />
          </Field>
        </div>
      );
    case "ip-allowlist":
      return (
        <Field label="Allowed addresses" hint="IPs or CIDRs, comma-separated; everyone else gets 403">
          <Input value={list(config.sourceRange)} onChange={(e) => patch({ sourceRange: split(e.target.value) })} placeholder="203.0.113.0/24, 198.51.100.7" className="font-mono" />
        </Field>
      );
    case "basic-auth": {
      const users = (config.users ?? []) as { username: string; password?: string }[];
      const setUser = (i: number, u: Partial<{ username: string; password: string }>) => patch({ users: users.map((x, j) => (i === j ? { ...x, ...u } : x)) });
      return (
        <div className="flex flex-col gap-1.5">
          <span className="text-muted text-xs">Users (leave a password empty to keep the current one)</span>
          {users.map((u, i) => (
            <div key={i} className="grid grid-cols-[1fr_1fr_auto] gap-1.5">
              <Input value={u.username} onChange={(e) => setUser(i, { username: e.target.value })} placeholder="username" />
              <Input type="password" value={u.password ?? ""} onChange={(e) => setUser(i, { password: e.target.value })} placeholder="password (8+ characters)" autoComplete="new-password" />
              <IconButton label="Remove user" onClick={() => patch({ users: users.filter((_, j) => j !== i) })}>
                <X className="size-3.5" />
              </IconButton>
            </div>
          ))}
          <div>
            <Button variant="ghost" onClick={() => patch({ users: [...users, { username: "", password: "" }] })}>
              <Plus className="size-3.5" /> Add user
            </Button>
          </div>
          <Field label="Realm" hint="shown in the browser's prompt">
            <Input value={config.realm ?? ""} onChange={(e) => patch({ realm: e.target.value })} />
          </Field>
        </div>
      );
    }
    case "cors":
      return (
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Field label="Origins" hint="comma-separated, or *">
            <Input value={list(config.origins)} onChange={(e) => patch({ origins: split(e.target.value) })} className="font-mono" />
          </Field>
          <Field label="Methods" hint="empty: GET, POST, PUT, PATCH, DELETE, OPTIONS">
            <Input value={list(config.methods)} onChange={(e) => patch({ methods: split(e.target.value.toUpperCase()) })} className="font-mono" />
          </Field>
          <Field label="Allowed headers">
            <Input value={list(config.headers)} onChange={(e) => patch({ headers: split(e.target.value) })} className="font-mono" placeholder="Authorization, Content-Type" />
          </Field>
          <Field label="Max age" hint="seconds browsers cache the answer">
            <Input type="number" value={config.maxAge ?? 0} onChange={(e) => patch({ maxAge: Number(e.target.value) })} />
          </Field>
          <Toggle checked={!!config.credentials} onChange={(v) => patch({ credentials: v })} label="Allow credentials (cookies)" />
        </div>
      );
    case "security-headers":
      return (
        <div className="flex flex-col gap-1.5 text-xs">
          <Toggle checked={!!config.hsts} onChange={(v) => patch({ hsts: v })} label="HSTS" hint="Browsers only use HTTPS for a year." />
          <Toggle checked={!!config.frameDeny} onChange={(v) => patch({ frameDeny: v })} label="Deny framing" hint="X-Frame-Options: DENY" />
          <Toggle checked={!!config.noSniff} onChange={(v) => patch({ noSniff: v })} label="No MIME sniffing" hint="X-Content-Type-Options: nosniff" />
          <Field label="Referrer policy">
            <Select value={config.referrerPolicy ?? ""} onChange={(v) => patch({ referrerPolicy: v })} className="w-full">
              {["", "no-referrer", "no-referrer-when-downgrade", "origin", "origin-when-cross-origin", "same-origin", "strict-origin", "strict-origin-when-cross-origin", "unsafe-url"].map((p) => (
                <option key={p} value={p}>
                  {p || "not set"}
                </option>
              ))}
            </Select>
          </Field>
        </div>
      );
    case "circuit-breaker":
      return (
        <Field label="Trips when" hint="NetworkErrorRatio(), ResponseCodeRatio(from, to, from, to), LatencyAtQuantileMS(q), && and ||">
          <Input value={config.expression ?? ""} onChange={(e) => patch({ expression: e.target.value })} className="font-mono" />
        </Field>
      );
    case "retry":
      return (
        <Field label="Attempts" hint="1 turns retries off">
          <Input type="number" min={1} max={10} value={config.attempts ?? 2} onChange={(e) => patch({ attempts: Number(e.target.value) })} />
        </Field>
      );
    case "redirect-www":
      return <p className="text-muted text-xs">Requests to www.example.com are redirected permanently to example.com. Add both domains to the service.</p>;
    case "compress":
      return <p className="text-muted text-xs">Responses over 1 KiB are compressed with gzip, Brotli or Zstandard when the client accepts it.</p>;
  }
}

/** Create or edit a middleware preset, as a full page. */
export function MiddlewarePage() {
  const params = useParams({ strict: false }) as { project?: string; name?: string };
  const isNew = !params.name;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data } = useMiddlewares();
  const { data: projects = [] } = useProjects();
  const { data: services = [] } = useServices();
  const existing = data?.items.find((m) => m.project === params.project && m.name === params.name);
  const [project, setProject] = useState(params.project ?? "");
  const [name, setName] = useState("");
  const [type, setType] = useState<PresetType>("rate-limit");
  const [config, setConfig] = useState<Record<string, any>>(defaults["rate-limit"]);
  const [attached, setAttached] = useState<string[]>([]);
  const [loaded, setLoaded] = useState(false);
  useEffect(() => {
    if (existing && !loaded) {
      setProject(existing.project);
      setName(existing.name);
      setType(existing.type);
      setConfig(existing.config);
      setAttached(existing.services);
      setLoaded(true);
    }
  }, [existing, loaded]);
  useEffect(() => {
    if (isNew && !project && projects[0]) setProject(projects[0].name);
  }, [isNew, project, projects]);
  const projectServices = services.filter((s) => s.project === project).map((s) => `${s.environment}/${s.name}`).sort();
  const save = useMutation({
    mutationFn: () => {
      const body = { name, type, config, services: attached };
      return isNew ? api("POST", `/projects/${project}/middlewares`, body) : api("PUT", `/projects/${project}/middlewares/${params.name}`, body);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["middlewares"] });
      void navigate({ to: "/network/routing" as string });
    },
  });
  if (!isNew && data && !existing) {
    return (
      <div className={cn("flex flex-col", gap)}>
        <PageHeader crumbs={["Network", "Routing"]} title="Middleware not found" />
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
        crumbs={["Network", "Routing"]}
        title={isNew ? "New middleware" : `${project}/${name}`}
        actions={
          <>
            <Button variant="ghost" onClick={() => navigate({ to: "/network/routing" as string })}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={!project || !name || save.isPending}>
              {isNew ? "Create middleware" : "Save and apply"}
            </Button>
          </>
        }
      />
      {isNew && (
        <Panel title="Type">
          <ChoiceCards
            label="Type"
            value={type}
            onChange={(v) => {
              setType(v);
              setConfig(defaults[v]);
            }}
            columns={4}
            options={(data?.types ?? []).map((t) => ({
              value: t.type,
              title: presetLabels[t.type],
              description: t.description,
              icon: presetIcons[t.type],
            }))}
          />
        </Panel>
      )}
      <Panel title={presetLabels[type]}>
        <div className="flex max-w-3xl flex-col gap-2">
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            <Field label="Project">
              <Select value={project} onChange={setProject} disabled={!isNew} className="w-full">
                {projects.map((p) => (
                  <option key={p.name}>{p.name}</option>
                ))}
              </Select>
            </Field>
            <Field label="Name">
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="api-limit" className="font-mono" autoFocus={isNew} />
            </Field>
          </div>
          <PresetFields type={type} config={config} set={setConfig} />
        </div>
      </Panel>
      <Panel title="Attached services">
        <ChipSelect
          label="Attached services"
          mono
          empty="No services in this project yet."
          value={attached}
          onChange={setAttached}
          options={projectServices.map((s) => ({ value: s, label: s }))}
        />
      </Panel>
      {save.error && <Alert>{save.error instanceof ApiError ? save.error.message : "Could not save"}</Alert>}
    </form>
  );
}
