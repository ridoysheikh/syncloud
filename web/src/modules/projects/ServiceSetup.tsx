import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Cog, Globe, Layers, Network, Plug, Plus, Shuffle, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { Field, IconButton, Input, Toggle, Button } from "@/ui/controls";
import { ChoiceCards, ChoiceField, Segmented } from "@/ui/choice";
import { Select } from "@/ui/select";
import { PortCheckHint, presets, usePortCheck, usePortsProblem } from "@/modules/compute/publicPorts";
import { useBaseDomain } from "@/modules/databases/Network";
import { DomainPicker } from "@/modules/domains/DomainPicker";
import { NodeLimitEditor } from "./NodeLimits";
import { ResourceFields, resourceError, resourceSummary, type ResourceForm } from "./Resources";

/* The new-service wizard's Network, Resources and Review parts (Phase 27):
   ports as rows (HTTP, public TCP/UDP, internal), the health check,
   resources with autoscaling and placement. */

const nameRE = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/;
const validPort = (v: string) => Number(v) >= 1 && Number(v) <= 65535;
const digits = (v: string) => v.replace(/\D/g, "");

// ── network ──────────────────────────────────────────────────────────────────

/** How a port is reached: HTTP through Traefik, a public TCP/UDP port, or privately. */
export type PortKind = "http" | "public" | "internal";

export interface PortRow {
  name: string;
  kind: PortKind;
  /** TCP or UDP (public and internal rows; HTTP rows are HTTP). */
  protocol: "tcp" | "udp";
  container: string;
  /** Public rows: the container's number, a chosen one, or an automatic one. */
  publicMode: "same" | "chosen" | "auto";
  publicPort: string;
  tls: boolean;
  proxyProtocol: boolean;
}

export type Exposure = "public" | "tcpudp" | "internal" | "none";

export interface HealthForm {
  type: "none" | "http" | "tcp" | "cmd";
  path: string;
  command: string;
}

export interface NetworkForm {
  exposure: Exposure;
  ports: PortRow[];
  health: HealthForm;
  /** An optional domain for the first HTTP port, added right after creation (Phase 28). */
  domain?: string;
}

const row = (p: Partial<PortRow>): PortRow => ({
  name: "http",
  kind: "http",
  protocol: "tcp",
  container: "8080",
  publicMode: "same",
  publicPort: "",
  tls: false,
  proxyProtocol: false,
  ...p,
});

/** The rows a network card starts with. */
const startRows: Record<Exposure, () => PortRow[]> = {
  public: () => [row({})],
  tcpudp: () => [row({ name: "tcp", kind: "public", container: "" })],
  internal: () => [row({ name: "tcp", kind: "internal" })],
  none: () => [],
};

/** A health check that fits the first port: a TCP connect, or none. */
const startHealth = (rows: PortRow[]): HealthForm => ({
  type: rows[0] && !(rows[0].kind !== "http" && rows[0].protocol === "udp") ? "tcp" : "none",
  path: "/",
  command: "",
});

export const networkForm = (exposure: Exposure = "public"): NetworkForm => {
  const ports = startRows[exposure]();
  return { exposure, ports, health: startHealth(ports) };
};

/** The public port a row asks for (0: an automatic one). */
export const publicOf = (r: PortRow) =>
  r.publicMode === "auto" ? 0 : Number(r.publicMode === "same" ? r.container : r.publicPort);

export function networkError(n: NetworkForm): string {
  if (n.exposure === "none") return "";
  if (n.ports.length === 0) return "Add a port, or choose Worker";
  if (n.ports.some((r) => !nameRE.test(r.name))) return "Port names: lowercase letters, digits and dashes";
  if (new Set(n.ports.map((r) => r.name)).size !== n.ports.length) return "Each port needs its own name";
  if (n.ports.some((r) => !validPort(r.container))) return "Container ports are 1–65535";
  const pub = n.ports.filter((r) => r.kind === "public");
  if (pub.some((r) => r.publicMode === "chosen" && !validPort(r.publicPort))) return "Public ports are 1–65535";
  const nums = pub.filter((r) => r.publicMode !== "auto").map((r) => `${r.protocol}/${publicOf(r)}`);
  if (new Set(nums).size !== nums.length) return "Two ports ask for the same public port";
  if (n.health.type === "http" && !n.health.path.startsWith("/")) return "The health check path starts with /";
  if (n.health.type === "cmd" && !n.health.command.trim()) return "Give the health check command";
  return "";
}

/** The spec's ports and health check. */
export function networkSpec(n: NetworkForm) {
  const ports = n.ports.map((r) => ({
    name: r.name,
    container: Number(r.container),
    protocol: r.kind === "http" ? ("http" as const) : r.protocol,
  }));
  const first = n.ports[0];
  const h = n.health;
  const health =
    !first || h.type === "none"
      ? undefined
      : h.type === "cmd"
        ? { type: "cmd" as const, command: ["sh", "-c", h.command.trim()] }
        : { type: h.type, port: first.name, ...(h.type === "http" ? { path: h.path } : {}) };
  return { ports, health };
}

/** The routing that opens the public rows (empty: none). */
export function networkRouting(n: NetworkForm): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const r of n.ports) {
    if (r.kind !== "public") continue;
    const num = publicOf(r);
    out[r.name] = {
      public: true,
      ...(num ? { publicPort: num } : {}),
      ...(r.protocol === "tcp" ? { proxyProtocol: r.proxyProtocol, tls: r.tls ? "terminate" : "none" } : {}),
    };
  }
  return out;
}

/** The Network step's problem: the form's own, else a public port that is taken or reserved. */
export function useNetworkProblem(n: NetworkForm, project: string): string {
  const portProblem = usePortsProblem(
    project,
    n.ports.filter((r) => r.kind === "public").map((r) => ({ name: r.name, protocol: r.protocol, port: publicOf(r) })),
  );
  return networkError(n) || portProblem;
}

export function NetworkStep({
  value: n,
  onChange,
  project,
}: {
  value: NetworkForm;
  onChange: (n: NetworkForm) => void;
  project: string;
}) {
  const setRows = (ports: PortRow[]) => onChange({ ...n, ports });
  const update = (i: number, p: Partial<PortRow>) => setRows(n.ports.map((r, j) => (j === i ? { ...r, ...p } : r)));
  const [preset, setPreset] = useState("");
  const choose = (exposure: Exposure) => {
    setPreset("");
    onChange(networkForm(exposure));
  };
  const usePreset = (id: string) => {
    setPreset(id);
    const ps = presets.find((x) => x.id === id);
    if (!ps) return;
    const ports = ps.ports.map((p) =>
      row({ name: p.name, kind: "public", protocol: p.protocol, container: String(p.port), tls: !!p.tls, proxyProtocol: !!p.proxyProtocol }),
    );
    onChange({ ...n, ports, health: startHealth(ports) });
  };
  const addRow = () => {
    const kind: PortKind = n.exposure === "public" ? "http" : n.exposure === "tcpudp" ? "public" : "internal";
    let i = n.ports.length + 1;
    while (n.ports.some((r) => r.name === `port${i}`)) i++;
    setRows([...n.ports, row({ name: `port${i}`, kind, container: "" })]);
  };
  return (
    <div className="flex flex-col gap-3">
      <ChoiceField label="Network">
        <ChoiceCards
          label="Network"
          value={n.exposure}
          onChange={choose}
          columns={4}
          options={[
            {
              value: "public",
              title: "Public HTTP",
              description: "Websites and APIs: a public HTTPS address through the load balancer on 80/443, no port of its own.",
              icon: Globe,
            },
            {
              value: "tcpudp",
              title: "Public TCP/UDP",
              description: "Clients reach the app on its own port: mail, MQTT, game servers.",
              icon: Plug,
            },
            {
              value: "internal",
              title: "Internal only",
              description: "Reachable by the project's services over the private network.",
              icon: Network,
            },
            { value: "none", title: "Worker", description: "No port: background jobs, queues, consumers.", icon: Cog },
          ]}
        />
      </ChoiceField>
      {n.exposure === "tcpudp" && (
        <Field
          label="Preset (optional)"
          hint={presets.find((x) => x.id === preset)?.hint ?? "Fills in the usual ports of a common server; change them as needed."}
        >
          <Select
            value={preset}
            onChange={usePreset}
            placeholder="None"
            options={presets.map((x) => ({ value: x.id, label: x.label }))}
            className="w-full sm:w-72"
          />
        </Field>
      )}
      {n.exposure !== "none" && (
        <div className="flex flex-col gap-2">
          {n.ports.map((r, i) => (
            <PortRowEditor
              key={i}
              r={r}
              project={project}
              onChange={(p) => update(i, p)}
              onRemove={n.ports.length > 1 ? () => setRows(n.ports.filter((_, j) => j !== i)) : undefined}
            />
          ))}
          <div>
            <Button onClick={addRow}>
              <Plus className="size-3.5" /> Add port
            </Button>
          </div>
          <p className="text-faint text-xs">
            A service can mix kinds: an HTTP admin page next to an MQTT port, for example. Labels, custom domains and
            allowed clients are set on the service's Networking tab once it exists.
          </p>
        </div>
      )}
      {n.exposure === "public" && n.ports.some((p) => p.kind === "http") && (
        <div className="flex flex-col gap-1.5">
          <span className="text-muted text-xs">Domain (optional)</span>
          <DomainPicker project={project} value={n.domain ?? ""} onChange={(domain) => domain !== (n.domain ?? "") && onChange({ ...n, domain })} />
          <span className="text-faint text-xs">
            The service also gets its generated address. Choose from your registered domains, or type a hostname; more domains, paths and redirects are set on the service's Networking tab.
          </span>
        </div>
      )}
      {n.ports.length > 0 && <HealthEditor value={n.health} first={n.ports[0]!} onChange={(health) => onChange({ ...n, health })} />}
    </div>
  );
}

function PortRowEditor({
  r,
  project,
  onChange,
  onRemove,
}: {
  r: PortRow;
  project: string;
  onChange: (p: Partial<PortRow>) => void;
  onRemove?: () => void;
}) {
  const num = publicOf(r);
  const check = usePortCheck(project, r.protocol, r.kind === "public" ? num : 0);
  const base = useBaseDomain();
  return (
    <div className="border-line flex flex-col gap-2 rounded-md border p-2">
      <div className="flex flex-wrap items-end gap-1.5">
        <Field label="Name">
          <Input
            value={r.name}
            onChange={(e) => onChange({ name: e.target.value.toLowerCase() })}
            className="w-28 font-mono"
            spellCheck={false}
          />
        </Field>
        <ChoiceField label="Reached">
          <Segmented
            label="Reached"
            value={r.kind}
            onChange={(kind) => onChange(kind === "public" ? { kind } : { kind, tls: false, proxyProtocol: false })}
            options={[
              { value: "http", label: "HTTP" },
              { value: "public", label: "Public port" },
              { value: "internal", label: "Internal" },
            ]}
          />
        </ChoiceField>
        {r.kind !== "http" && (
          <ChoiceField label="Protocol">
            <Segmented
              label="Protocol"
              value={r.protocol}
              onChange={(v) => onChange(v === "udp" ? { protocol: v, tls: false, proxyProtocol: false } : { protocol: v })}
              options={[
                { value: "tcp", label: "TCP" },
                { value: "udp", label: "UDP" },
              ]}
            />
          </ChoiceField>
        )}
        <Field label="Container port">
          <Input
            value={r.container}
            onChange={(e) => onChange({ container: digits(e.target.value) })}
            inputMode="numeric"
            placeholder="8080"
            className="w-24 font-mono"
          />
        </Field>
        {r.kind === "public" && (
          <ChoiceField label="Public port">
            <Segmented
              label="Public port"
              value={r.publicMode}
              onChange={(v) => onChange({ publicMode: v })}
              options={[
                { value: "same", label: "Same" },
                { value: "chosen", label: "Choose" },
                { value: "auto", label: "Automatic" },
              ]}
            />
          </ChoiceField>
        )}
        {r.kind === "public" && r.publicMode === "chosen" && (
          <Field label="Number">
            <Input
              value={r.publicPort}
              onChange={(e) => onChange({ publicPort: digits(e.target.value) })}
              inputMode="numeric"
              className="w-24 font-mono"
            />
          </Field>
        )}
        {onRemove && (
          <IconButton label="Remove port" onClick={onRemove}>
            <Trash2 className="size-3.5" />
          </IconButton>
        )}
      </div>
      <span className="text-faint text-xs">
        {r.kind === "http" ? (
          "Served over HTTPS at a generated address (shown on the review), through the load balancer's 80/443."
        ) : r.kind === "internal" ? (
          "Reached only by the project's services, at the service's private name."
        ) : r.publicMode === "auto" ? (
          "A free port from 20000–20999 is assigned."
        ) : (r.publicMode === "chosen" ? r.publicPort : r.container) ? (
          <PortCheckHint check={check} port={num} />
        ) : null}
      </span>
      {r.kind === "public" && r.protocol === "tcp" && (
        <div className="flex flex-col gap-1.5 sm:flex-row sm:gap-4">
          <Toggle
            checked={r.proxyProtocol}
            onChange={(v) => onChange({ proxyProtocol: v })}
            label="Real client IP (PROXY protocol)"
            hint="The app must support it (Postfix, Dovecot, nginx…)."
          />
          <Toggle
            checked={r.tls}
            disabled={!base && !r.tls}
            onChange={(v) => onChange({ tls: v })}
            label="TLS at the load balancer"
            hint={
              base
                ? "For implicit-TLS ports (465, 993, 8883); not for STARTTLS."
                : "Needs the platform's base domain for its certificate (Settings → Domain)."
            }
          />
        </div>
      )}
    </div>
  );
}

function HealthEditor({ value: h, first, onChange }: { value: HealthForm; first: PortRow; onChange: (h: HealthForm) => void }) {
  const udp = first.kind !== "http" && first.protocol === "udp";
  const options: { value: HealthForm["type"]; label: string }[] = [
    { value: "none", label: "None" },
    ...(udp ? [] : [{ value: "tcp" as const, label: "TCP connect" }]),
    ...(first.kind === "http" ? [{ value: "http" as const, label: "HTTP" }] : []),
    { value: "cmd", label: "Command" },
  ];
  const type = options.some((o) => o.value === h.type) ? h.type : "none";
  return (
    <div className="flex flex-wrap items-end gap-1.5">
      <ChoiceField
        label="Health check"
        hint={
          type === "none"
            ? "No check: a task counts as healthy while it runs."
            : `Tasks that fail it are replaced. It checks the first port (${first.name}).`
        }
      >
        <Segmented label="Health check" value={type} onChange={(t) => onChange({ ...h, type: t })} options={options} />
      </ChoiceField>
      {type === "http" && (
        <Field label="Path">
          <Input value={h.path} onChange={(e) => onChange({ ...h, path: e.target.value })} placeholder="/healthz" className="w-48 font-mono" />
        </Field>
      )}
      {type === "cmd" && (
        <Field label="Command (exit 0 = healthy)">
          <Input
            value={h.command}
            onChange={(e) => onChange({ ...h, command: e.target.value })}
            placeholder="pg_isready -q"
            className="w-72 max-w-full font-mono"
            spellCheck={false}
          />
        </Field>
      )}
    </div>
  );
}

// ── resources, scaling and placement ─────────────────────────────────────────

export type ScaleMetric = "cpu" | "memory" | "rps" | "latency";

export interface ScalingForm {
  enabled: boolean;
  min: string;
  max: string;
  metric: ScaleMetric;
  target: string;
}

export interface PlacementForm {
  /** null: any node the project allows. */
  nodes: string[] | null;
  strategy: "spread" | "binpack";
}

export const scalingForm = (): ScalingForm => ({ enabled: false, min: "1", max: "5", metric: "rps", target: "50" });
export const placementForm = (): PlacementForm => ({ nodes: null, strategy: "spread" });

const metricInfo: Record<ScaleMetric, { label: string; unit: string; target: string }> = {
  rps: { label: "Requests per task", unit: "requests/s", target: "50" },
  latency: { label: "p95 latency", unit: "ms", target: "250" },
  cpu: { label: "CPU", unit: "% of the reserved CPU", target: "60" },
  memory: { label: "Memory", unit: "% of the reserved memory", target: "70" },
};

/** The metrics a service can scale on: requests and latency need an HTTP port, CPU and memory a reservation. */
export const scaleMetrics = (n: NetworkForm, r: ResourceForm): ScaleMetric[] => [
  ...(n.ports.some((p) => p.kind === "http") ? (["rps", "latency"] as const) : []),
  ...(r.cpuMode === "reserved" ? (["cpu"] as const) : []),
  ...(r.memoryMode === "reserved" ? (["memory"] as const) : []),
];

export function resourcesError(r: ResourceForm, s: ScalingForm, p: PlacementForm, metrics: ScaleMetric[]): string {
  const e = resourceError(r);
  if (e) return e;
  if (s.enabled) {
    if (!metrics.includes(s.metric)) return "Pick what autoscaling follows";
    if (!(Number(s.min) >= 1 && Number(s.max) <= 100 && Number(s.min) <= Number(s.max)))
      return "Autoscaling: at least 1 task, at most 100, and the minimum no more than the maximum";
    if (!(Number(s.target) > 0)) return "Autoscaling: give a target above 0";
  }
  if (p.nodes !== null && p.nodes.length === 0) return "Pick at least one node, or allow any";
  return "";
}

/** The autoscaling policy to create (null: none). */
export const scalingPolicy = (s: ScalingForm) =>
  s.enabled
    ? {
        enabled: true,
        min: Number(s.min),
        max: Number(s.max),
        metric: s.metric,
        target: Number(s.target),
        scaleOutCooldown: 60,
        scaleInCooldown: 300,
        scaleInChecks: 4,
      }
    : null;

export function ResourcesStep({
  resources,
  scaling,
  placement,
  metrics,
  projectNodes,
  onResources,
  onScaling,
  onPlacement,
}: {
  resources: ResourceForm;
  scaling: ScalingForm;
  placement: PlacementForm;
  metrics: ScaleMetric[];
  projectNodes: string[];
  onResources: (r: ResourceForm) => void;
  onScaling: (s: ScalingForm) => void;
  onPlacement: (p: PlacementForm) => void;
}) {
  return (
    <div className="flex flex-col gap-4">
      <Section title="Resources per task">
        <ResourceFields value={resources} onChange={onResources} />
      </Section>
      <Section title="Autoscaling">
        <Toggle
          checked={scaling.enabled}
          disabled={metrics.length === 0}
          onChange={(enabled) => {
            const metric = metrics.includes(scaling.metric) ? scaling.metric : metrics[0]!;
            onScaling({ ...scaling, enabled, metric, target: metric === scaling.metric ? scaling.target : metricInfo[metric].target });
          }}
          label="Add and remove tasks with the load"
          hint={
            metrics.length === 0
              ? "Needs an HTTP port (to follow requests or latency) or reserved CPU or memory (to follow their use)."
              : "The task count of the Basics step is where it starts."
          }
        />
        {scaling.enabled && (
          <div className="flex flex-wrap items-end gap-1.5">
            <Field label="Fewest tasks">
              <Input value={scaling.min} onChange={(e) => onScaling({ ...scaling, min: digits(e.target.value) })} inputMode="numeric" className="w-20" />
            </Field>
            <Field label="Most tasks">
              <Input value={scaling.max} onChange={(e) => onScaling({ ...scaling, max: digits(e.target.value) })} inputMode="numeric" className="w-20" />
            </Field>
            <Field label="Follows">
              <Select
                value={scaling.metric}
                onChange={(v) => {
                  const m = v as ScaleMetric;
                  onScaling({ ...scaling, metric: m, target: metricInfo[m].target });
                }}
                options={metrics.map((m) => ({ value: m, label: metricInfo[m].label }))}
                className="w-48"
              />
            </Field>
            <Field label={`Target (${metricInfo[scaling.metric].unit})`}>
              <Input
                value={scaling.target}
                onChange={(e) => onScaling({ ...scaling, target: e.target.value.replace(/[^0-9.]/g, "") })}
                inputMode="decimal"
                className="w-24"
              />
            </Field>
          </div>
        )}
      </Section>
      <Section title="Placement">
        <NodeLimitEditor
          value={placement.nodes}
          onChange={(nodes) => onPlacement({ ...placement, nodes })}
          limit={projectNodes}
          anyLabel={projectNodes.length ? "Any node the project allows" : "Any schedulable node"}
        />
        <ChoiceCards
          label="Spreading"
          value={placement.strategy}
          onChange={(strategy) => onPlacement({ ...placement, strategy })}
          options={[
            {
              value: "spread",
              title: "Spread",
              description: "Tasks go to different nodes, so one node failing takes down as few as possible.",
              icon: Shuffle,
            },
            {
              value: "binpack",
              title: "Pack",
              description: "Tasks fill as few nodes as possible, leaving the others free.",
              icon: Layers,
            },
          ]}
        />
      </Section>
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-muted text-xs font-medium">{title}</h3>
      {children}
    </div>
  );
}

// ── review ───────────────────────────────────────────────────────────────────

interface Ingress {
  nodes: { name: string; address: string }[];
  base: string;
}

/** The addresses the service will have, by port. */
export function AddressesPreview({
  n,
  project,
  env,
  name,
}: {
  n: NetworkForm;
  project: string;
  env: string;
  name: string;
}) {
  const ingress = useQuery({
    queryKey: ["ingress", project],
    queryFn: () => api<Ingress>("GET", `/projects/${encodeURIComponent(project)}/ingress`),
    enabled: !!project,
    retry: false,
  });
  const fallback = useBaseDomain();
  const base = ingress.data?.base || fallback || "localhost";
  const lb = ingress.data?.nodes.length
    ? `the project's load balancer (${ingress.data.nodes.map((x) => x.name).join(", ")})`
    : "the shared load balancer (the controller and edge nodes)";
  if (n.exposure === "none") return <span className="text-faint">No port</span>;
  const firstHttp = n.ports.find((p) => p.kind === "http");
  const opensPorts = n.ports.some((p) => p.kind === "public");
  return (
    <span className="flex flex-col gap-0.5">
      {n.ports.map((r) => (
        <span key={r.name} className="font-mono break-all">
          {r.name}:{" "}
          {r.kind === "http"
            ? `https://${name || "NAME"}${r === firstHttp ? "" : `-${r.name}`}-${env}-${project}.${base}`
            : r.kind === "public"
              ? `${r.protocol}://${base}:${publicOf(r) || "<automatic port>"}${r.tls ? " (TLS at the load balancer)" : ""}${r.proxyProtocol ? " (PROXY protocol)" : ""}`
              : `${r.protocol}://${name || "NAME"}.${env}.${project}.syncloud.internal:${r.container} (private)`}
        </span>
      ))}
      {n.domain && (
        <span className="font-mono break-all">
          domain: https://{n.domain}
        </span>
      )}
      <span className="text-faint">
        Served by {lb}.
        {opensPorts && " Its Traefik restarts once to open the public ports, so HTTP traffic there pauses for a moment."}
      </span>
    </span>
  );
}

export function healthSummary(n: NetworkForm) {
  const h = n.health;
  const first = n.ports[0];
  if (!first || h.type === "none") return "none";
  if (h.type === "cmd") return `command: ${h.command.trim()}`;
  return h.type === "http" ? `HTTP ${h.path} on ${first.name}` : `TCP connect to ${first.name}`;
}

export function scalingSummary(s: ScalingForm) {
  if (!s.enabled) return "off";
  const m = metricInfo[s.metric];
  const label = s.metric === "cpu" ? "CPU" : m.label.toLowerCase();
  const target = m.unit.startsWith("%") ? `${s.target}${m.unit}` : `${s.target} ${m.unit}`;
  return `${s.min}–${s.max} tasks, following ${label} (target ${target})`;
}

export function placementSummary(p: PlacementForm) {
  const where = p.nodes === null ? "any allowed node" : `only ${p.nodes.join(", ")}`;
  return `${where}, ${p.strategy === "spread" ? "spread over nodes" : "packed onto few nodes"}`;
}

export { resourceSummary };
