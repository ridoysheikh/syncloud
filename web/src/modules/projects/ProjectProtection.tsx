import { useEffect, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Cable, Database, Fingerprint, Network, Plus, Shield, ShieldOff, Waypoints } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { SourceList } from "@/ui/SourceList";
import { CopyBox } from "@/ui/CopyBox";
import { Alert, Button, Field, Input, StatusBadge, Toggle } from "@/ui/controls";
import { ChoiceCards, Segmented } from "@/ui/choice";
import { Select } from "@/ui/select";
import { confirmDialog } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";
import { ProjectIngressPanel } from "./ProjectIngress";

/* Project › Load balancer (Phase 25): who serves the project's traffic, and
   the Traefik-managed protection in front of it. */

interface Spec {
  enabled: boolean;
  rateLimit: { average: number; burst: number; period: "1s" | "1m" | "1h" };
  clientIp: { source: "connection" | "forwarded" | "header"; depth: number; header: string };
  maxConcurrent: number;
  maxBodyMb: number;
  block: string[];
  bypass: { ips: string[]; header: string; hasSecret: boolean; paths: string[] };
  redis: { databaseId: string };
  guard: { rate: number; burst: number; maxConnections: number; banMinutes: number };
}

interface Protection {
  project: string;
  spec: Spec;
  redis: { name?: string; health?: string; active: boolean; fallback: boolean };
  valkeys: { id: string; name: string; environment: string; health: string }[];
  ownNodes: boolean;
  stats?: { limited: number; blocked: number; total: number };
  secret?: string;
}

const errText = (e: unknown) => (e instanceof ApiError ? e.message : "Request failed");

const healthTone: Record<string, "ok" | "warn" | "bad" | "info" | "neutral"> = {
  healthy: "ok",
  degraded: "warn",
  starting: "info",
  down: "bad",
};

/** A whole number input (empty = 0). */
function Num({ value, onChange, min = 0, max, className }: { value: number; onChange: (v: number) => void; min?: number; max?: number; className?: string }) {
  return (
    <Input
      type="number"
      min={min}
      max={max}
      value={value}
      onChange={(e) => onChange(Math.max(min, Math.floor(Number(e.target.value) || 0)))}
      className={cn("w-24", className)}
    />
  );
}

function Section({ title, icon: Icon, children, hint }: { title: string; icon: typeof Shield; children: ReactNode; hint?: ReactNode }) {
  return (
    <div className="border-line flex flex-col gap-2 border-t pt-3 first:border-t-0 first:pt-0">
      <div className="flex flex-col gap-0.5">
        <span className="flex items-center gap-1.5 font-medium">
          <Icon className="text-muted size-3.5" />
          {title}
        </span>
        {hint && <p className="text-muted">{hint}</p>}
      </div>
      {children}
    </div>
  );
}

/** Path prefixes as a comma list. */
function PathsInput({ value, onChange }: { value: string[]; onChange: (v: string[]) => void }) {
  const [text, setText] = useState(value.join(", "));
  useEffect(() => setText(value.join(", ")), [value]);
  return (
    <Input
      value={text}
      onChange={(e) => setText(e.target.value)}
      onBlur={() =>
        onChange(
          text
            .split(/[\s,]+/)
            .map((s) => s.trim())
            .filter(Boolean),
        )
      }
      placeholder="/healthz, /webhooks/"
      className="w-full max-w-md font-mono"
      spellCheck={false}
    />
  );
}

export function ProtectionPanel({ project, environments }: { project: string; environments: string[] }) {
  const qc = useQueryClient();
  const key = ["protection", project];
  const q = useQuery({
    queryKey: key,
    queryFn: () => api<Protection>("GET", `/projects/${project}/protection`),
    refetchInterval: 30_000,
  });
  const [s, setS] = useState<Spec | null>(null);
  const [secret, setSecret] = useState("");
  const [valkeyEnv, setValkeyEnv] = useState(environments[0] ?? "");
  useEffect(() => {
    if (q.data && !s) setS(q.data.spec);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q.data]);
  const save = useMutation({
    mutationFn: (extra: Record<string, unknown>) => api<Protection>("PUT", `/projects/${project}/protection`, { ...s, ...extra }),
    onSuccess: (d) => {
      qc.setQueryData(key, d);
      setS(d.spec);
      if (d.secret) setSecret(d.secret);
    },
  });
  const createValkey = useMutation({
    mutationFn: () => api<Protection>("POST", `/projects/${project}/protection/valkey`, { environment: valkeyEnv }),
    onSuccess: (d) => {
      qc.setQueryData(key, d);
      setS((cur) => (cur ? { ...cur, redis: d.spec.redis } : d.spec));
      void qc.invalidateQueries({ queryKey: ["databases"] });
    },
  });
  const d = q.data;
  if (!s || !d) return <Panel title="Protection">{q.error ? <Alert tone="warn">{errText(q.error)}</Alert> : <span className="text-faint text-xs">Loading…</span>}</Panel>;
  const set = (patch: Partial<Spec>) => setS({ ...s, ...patch });
  const dirty = JSON.stringify(s) !== JSON.stringify(d.spec);
  const rl = s.rateLimit;
  const g = s.guard;
  const valkey = d.valkeys.find((v) => v.id === s.redis.databaseId);
  return (
    <Panel
      title="Protection"
      actions={
        <span className="flex items-center gap-2">
          {d.spec.enabled ? <StatusBadge tone="ok">on</StatusBadge> : <StatusBadge tone="neutral">off</StatusBadge>}
        </span>
      }
    >
      <div className="flex flex-col gap-3 text-xs">
        <div className={cn("grid grid-cols-1 sm:grid-cols-3", gap)}>
          <StatTile label="Requests (1h)" value={d.stats ? Math.round(d.stats.total).toLocaleString() : "—"} />
          <StatTile label="Rate limited (1h)" value={d.stats ? Math.round(d.stats.limited).toLocaleString() : "—"} hint="answered 429" tone={d.stats?.limited ? "warn" : undefined} />
          <StatTile label="Blocked (1h)" value={d.stats ? Math.round(d.stats.blocked).toLocaleString() : "—"} hint="answered 403" />
        </div>
        <ChoiceCards
          label="Protection"
          value={s.enabled ? "on" : "off"}
          onChange={(v) => set({ enabled: v === "on" })}
          options={[
            { value: "on", title: "On", description: "Applied to every route of the project, on whichever load balancer serves it.", icon: Shield },
            { value: "off", title: "Off (full bypass)", description: "Nothing below is applied; the settings are kept for later.", icon: ShieldOff },
          ]}
        />
        <div className={cn("flex flex-col gap-3", !s.enabled && "opacity-60")}>
          <Section title="Rate limiting" icon={Waypoints} hint="Traefik answers 429 to a client over its rate. Requests that bypass (below) are never limited.">
            <Toggle
              checked={rl.average > 0}
              onChange={(on) => set({ rateLimit: { ...rl, average: on ? 100 : 0, burst: on ? 200 : 0 } })}
              label="Limit requests per client"
            />
            {rl.average > 0 && (
              <div className="flex flex-wrap items-end gap-1.5">
                <Field label="Requests">
                  <Num value={rl.average} min={1} max={1000000} onChange={(average) => set({ rateLimit: { ...rl, average } })} />
                </Field>
                <Field label="Per">
                  <Segmented
                    label="Period"
                    value={rl.period}
                    onChange={(period) => set({ rateLimit: { ...rl, period } })}
                    options={[
                      { value: "1s", label: "second" },
                      { value: "1m", label: "minute" },
                      { value: "1h", label: "hour" },
                    ]}
                  />
                </Field>
                <Field label="Burst">
                  <Num value={rl.burst} min={1} max={1000000} onChange={(burst) => set({ rateLimit: { ...rl, burst } })} />
                </Field>
              </div>
            )}
            <div className="flex flex-wrap items-end gap-3">
              <Field label="Concurrent requests per client" hint="0 = no cap">
                <Num value={s.maxConcurrent} max={100000} onChange={(maxConcurrent) => set({ maxConcurrent })} />
              </Field>
              <Field label="Request body limit (MB)" hint="0 = no limit; larger bodies get 413">
                <Num value={s.maxBodyMb} max={10240} onChange={(maxBodyMb) => set({ maxBodyMb })} />
              </Field>
            </div>
          </Section>
          <Section title="Who is a client" icon={Fingerprint} hint="Behind a proxy or CDN every request comes from its addresses: count by the address it forwards instead.">
            <ChoiceCards
              label="Client identity"
              value={s.clientIp.source}
              onChange={(source) => set({ clientIp: { source, depth: source === "forwarded" ? Math.max(1, s.clientIp.depth) : 0, header: source === "header" ? s.clientIp.header || "CF-Connecting-IP" : "" } })}
              options={[
                { value: "connection", title: "Connection address", description: "Clients reach the load balancer directly.", icon: Cable },
                { value: "forwarded", title: "X-Forwarded-For", description: "Behind a proxy that appends the client's address.", icon: Network },
                { value: "header", title: "A header", description: "e.g. CF-Connecting-IP behind Cloudflare.", icon: Fingerprint },
              ]}
            />
            {s.clientIp.source === "forwarded" && (
              <Field label="Depth" hint="1 = the address the last proxy saw">
                <Num value={s.clientIp.depth} min={1} max={10} onChange={(depth) => set({ clientIp: { ...s.clientIp, depth } })} />
              </Field>
            )}
            {s.clientIp.source === "header" && (
              <Field label="Header">
                <Input value={s.clientIp.header} onChange={(e) => set({ clientIp: { ...s.clientIp, header: e.target.value } })} className="w-56 font-mono" />
              </Field>
            )}
            <p className="text-faint">The block and bypass address lists always match the connection's address.</p>
          </Section>
          <Section title="Shared counters" icon={Database} hint="Without Redis each load balancer counts on its own, so with 3 nodes a client gets about 3× the rate. If the Valkey goes down, the nodes count on their own until it is back (an alert fires).">
            <div className="flex flex-wrap items-end gap-1.5">
              <Field label="Valkey">
                <Select value={s.redis.databaseId} onChange={(databaseId) => set({ redis: { databaseId } })} className="w-64">
                  <option value="">none (per node)</option>
                  {d.valkeys.map((v) => (
                    <option key={v.id} value={v.id}>
                      {v.name} · {v.environment}
                    </option>
                  ))}
                </Select>
              </Field>
              {valkey && <StatusBadge tone={healthTone[valkey.health] ?? "neutral"}>{valkey.health}</StatusBadge>}
              {d.redis.fallback && <StatusBadge tone="warn">counting per node until it is back</StatusBadge>}
              {d.redis.active && !d.redis.fallback && <StatusBadge tone="ok">shared</StatusBadge>}
            </div>
            {!s.redis.databaseId && environments.length > 0 && (
              <div className="flex flex-wrap items-end gap-1.5">
                <Field label="Or create one in">
                  <Select value={valkeyEnv} onChange={setValkeyEnv} className="w-48">
                    {environments.map((e) => (
                      <option key={e} value={e}>
                        {e}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Button disabled={createValkey.isPending || !valkeyEnv} onClick={() => createValkey.mutate()}>
                  <Plus className="size-3.5" /> Create Valkey
                </Button>
                <span className="text-faint">64 MB, no persistence, listed under Databases.</span>
              </div>
            )}
            {createValkey.error && <Alert>{errText(createValkey.error)}</Alert>}
            {valkey && (
              <Link to={`/projects/${project}/${valkey.environment}` as string} className="text-accent w-fit hover:underline">
                Open it in Databases
              </Link>
            )}
          </Section>
          <Section title="Block list" icon={ShieldOff} hint="These clients get 403 on every route of the project, bypass or not.">
            <SourceList label="Blocked clients" value={s.block} onChange={(block) => set({ block })} />
          </Section>
          <Section title="Bypass" icon={Shield} hint="Requests that match any of these skip the rate and concurrency limits (the body limit and block list still apply).">
            <Field label="From these addresses" hint="Your office, monitoring, partners.">
              <SourceList label="Bypass addresses" value={s.bypass.ips} onChange={(ips) => set({ bypass: { ...s.bypass, ips } })} />
            </Field>
            <Field label="Under these paths" hint="Health checks, webhooks.">
              <PathsInput value={s.bypass.paths} onChange={(paths) => set({ bypass: { ...s.bypass, paths } })} />
            </Field>
            <Field label="With a secret header" hint="For your own backends, CI or load tests.">
              <div className="flex flex-wrap items-center gap-1.5">
                <Input value={s.bypass.header} onChange={(e) => set({ bypass: { ...s.bypass, header: e.target.value } })} className="w-56 font-mono" />
                {d.spec.bypass.hasSecret ? <StatusBadge tone="ok">secret set</StatusBadge> : <StatusBadge tone="neutral">no secret</StatusBadge>}
                <Button
                  disabled={save.isPending}
                  onClick={async () =>
                    (!d.spec.bypass.hasSecret ||
                      (await confirmDialog({ title: "Make a new secret?", message: "Clients using the current one get limited again.", confirmLabel: "New secret", tone: "warn" }))) &&
                    save.mutate({ generateSecret: true })
                  }
                >
                  {d.spec.bypass.hasSecret ? "New secret" : "Generate secret"}
                </Button>
                {d.spec.bypass.hasSecret && (
                  <Button variant="ghost" disabled={save.isPending} onClick={() => save.mutate({ clearSecret: true })}>
                    Remove
                  </Button>
                )}
              </div>
            </Field>
            {secret && (
              <Alert tone="info">
                <div className="flex flex-col gap-1">
                  <span>Send this header to bypass the limits. It is shown only now.</span>
                  <CopyBox value={`${s.bypass.header}: ${secret}`} />
                </div>
              </Alert>
            )}
          </Section>
          <Section
            title="Network guard"
            icon={Network}
            hint="Drops floods before they reach Traefik, in the host firewall of the project's own load-balancer nodes: new connections per second and concurrent connections per source on 80/443. Bypass addresses are exempt."
          >
            {!d.ownNodes && <Alert tone="info">Applies once the project has its own load-balancer nodes (above). The controller's firewall is shared, so it is not limited per project.</Alert>}
            <div className="flex flex-wrap items-end gap-3">
              <Field label="New connections / second" hint="0 = no limit">
                <Num value={g.rate} max={100000} onChange={(rate) => set({ guard: { ...g, rate } })} />
              </Field>
              <Field label="Burst">
                <Num value={g.burst} max={100000} onChange={(burst) => set({ guard: { ...g, burst } })} />
              </Field>
              <Field label="Connections at once" hint="0 = no cap">
                <Num value={g.maxConnections} max={100000} onChange={(maxConnections) => set({ guard: { ...g, maxConnections } })} />
              </Field>
              <Field label="Ban for (minutes)" hint="0 = drop only, no ban">
                <Num value={g.banMinutes} max={10080} onChange={(banMinutes) => set({ guard: { ...g, banMinutes } })} />
              </Field>
            </div>
            <p className="text-faint">When several projects share a node, it uses the most permissive guard among them. Bans show in the node's Firewall tab.</p>
          </Section>
        </div>
        {save.error && <Alert>{errText(save.error)}</Alert>}
        <div className="border-line bg-surface sticky bottom-0 flex items-center gap-2 border-t py-2">
          <Button variant="primary" disabled={!dirty || save.isPending} onClick={() => save.mutate({})}>
            Save
          </Button>
          {dirty ? <span className="text-muted">Unsaved changes</span> : save.isSuccess && <span className="text-ok">Saved. Load balancers pick it up within seconds.</span>}
          {dirty && (
            <Button variant="ghost" onClick={() => setS(d.spec)}>
              Discard
            </Button>
          )}
        </div>
      </div>
    </Panel>
  );
}

/** Project › Load balancer. */
export function LoadBalancerTab({ project, environments }: { project: string; environments: string[] }) {
  return (
    <div className={cn("flex flex-col", gap)}>
      <ProjectIngressPanel project={project} />
      <ProtectionPanel project={project} environments={environments} />
    </div>
  );
}
