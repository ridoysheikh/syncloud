import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import type { Port, Spec } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import {
  Alert,
  Button,
  Field,
  IconButton,
  Input,
  StatusBadge,
  Toggle,
} from "@/ui/controls";
import { confirmDialog } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";
import { DomainsPanel } from "./DomainsPanel";
import { Select } from "@/ui/select";

/* Service › Networking (Phase 15c): ports, addresses and custom domains. */

export interface PortRoute {
  port: string;
  container: number;
  protocol: "http" | "tcp" | "udp";
  generated: boolean;
  label?: string;
  host?: string;
  public: boolean;
  publicPort?: number;
  address?: string;
  allow: string[];
}

const errText = (e: unknown, fallback: string) =>
  e instanceof ApiError ? e.message : fallback;


const nameRE = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/;

/** The platform-set fields are not part of what the user edits. */
function ownSpec(spec: Spec) {
  const {
    sharedEnv: _,
    s3: _s3,
    redeployedAt: _r,
    ...own
  } = spec as Spec & { s3?: unknown };
  return own;
}

/** The container ports and the health check (saving makes a revision). */
function PortsPanel({ path, spec }: { path: string; spec: Spec }) {
  const qc = useQueryClient();
  const [ports, setPorts] = useState<Required<Port>[]>(() =>
    (spec.ports ?? []).map((p, i) => ({
      name: p.name ?? (i === 0 ? "http" : `port${i + 1}`),
      container: p.container,
      protocol: p.protocol ?? "http",
    })),
  );
  const h = spec.health;
  const [health, setHealth] = useState({
    type: (h?.type ?? "none") as "none" | "http" | "tcp" | "cmd",
    path: h?.path ?? "/",
    port: h?.port ?? "",
    command: h?.command?.join(" ") ?? "",
    interval: String(h?.interval ?? 10),
  });
  const [dirty, setDirty] = useState(false);
  const edit = (fn: () => void) => {
    setDirty(true);
    fn();
  };
  const names = ports.map((p) => p.name);
  const problem = ports.some((p) => !nameRE.test(p.name))
    ? "Port names: lowercase letters, digits and dashes."
    : new Set(names).size !== names.length
      ? "Each port needs its own name."
      : ports.some((p) => !(p.container >= 1 && p.container <= 65535))
        ? "Container ports are 1–65535."
        : health.type === "http" && !health.path.startsWith("/")
          ? "The health check path starts with /."
          : health.type === "cmd" && !health.command.trim()
            ? "Give the health check command."
            : !(Number(health.interval) >= 1)
              ? "The health check interval is at least 1 second."
              : "";
  const save = useMutation({
    mutationFn: () =>
      api("PUT", path, {
        ...ownSpec(spec),
        ports,
        health:
          health.type === "none"
            ? undefined
            : {
                type: health.type,
                path: health.type === "http" ? health.path : undefined,
                port:
                  health.type !== "cmd" && health.port
                    ? health.port
                    : undefined,
                command:
                  health.type === "cmd"
                    ? ["sh", "-c", health.command.trim()]
                    : undefined,
                interval: Number(health.interval),
              },
      }),
    onSuccess: () => {
      setDirty(false);
      void qc.invalidateQueries({ queryKey: ["services"] });
      void qc.invalidateQueries({ queryKey: ["routing", path] });
    },
  });
  const update = (i: number, p: Partial<Port>) =>
    setPorts(ports.map((x, j) => (j === i ? { ...x, ...p } : x)));
  return (
    <Panel
      title="Ports"
      actions={
        <Button
          variant="primary"
          disabled={!dirty || !!problem || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Deploying…" : "Save and deploy"}
        </Button>
      }
    >
      <div className={cn("flex flex-col", gap)}>
        <p className="text-faint text-xs">
          The ports the container listens on. HTTP ports get addresses and
          custom domains; TCP and UDP ports are reached privately, or publicly
          below. Saving rolls out a new revision.
        </p>
        {ports.length === 0 && (
          <p className="text-muted text-xs">
            No ports: the service is reached by nothing (a worker).
          </p>
        )}
        {ports.map((p, i) => (
          <div key={i} className="flex flex-wrap items-end gap-1.5">
            <label className="flex flex-col gap-0.5">
              <span className="text-faint text-xs">Name</span>
              <Input
                value={p.name}
                onChange={(e) =>
                  edit(() => update(i, { name: e.target.value.trim() }))
                }
                className="w-32 font-mono"
                spellCheck={false}
              />
            </label>
            <label className="flex flex-col gap-0.5">
              <span className="text-faint text-xs">Container port</span>
              <Input
                value={String(p.container || "")}
                inputMode="numeric"
                onChange={(e) =>
                  edit(() =>
                    update(i, { container: Number(e.target.value) || 0 }),
                  )
                }
                className="w-28 font-mono"
              />
            </label>
            <label className="flex flex-col gap-0.5">
              <span className="text-faint text-xs">Protocol</span>
              <Select
                value={p.protocol}
                onChange={(v) =>
                  edit(() =>
                    update(i, {
                      protocol: v as Port["protocol"],
                    }),
                  )
                }
              >
                <option value="http">HTTP</option>
                <option value="tcp">TCP</option>
                <option value="udp">UDP</option>
              </Select>
            </label>
            <IconButton
              label="Remove port"
              onClick={() =>
                edit(() => setPorts(ports.filter((_, j) => j !== i)))
              }
            >
              <Trash2 className="size-3.5" />
            </IconButton>
          </div>
        ))}
        <div>
          <Button
            variant="ghost"
            onClick={() =>
              edit(() =>
                setPorts([
                  ...ports,
                  {
                    name: `port${ports.length + 1}`,
                    container: 0,
                    protocol: "http",
                  },
                ]),
              )
            }
          >
            <Plus className="size-3.5" /> Add port
          </Button>
        </div>
        <section className="flex flex-col gap-1.5">
          <h3 className="text-xs font-medium">Health check</h3>
          <p className="text-faint text-xs">
            Tasks receive traffic only once healthy, and an unhealthy task is
            replaced. Deployments wait for the new tasks to be healthy.
          </p>
          <div className="flex flex-wrap items-end gap-1.5">
            <label className="flex flex-col gap-0.5">
              <span className="text-faint text-xs">Type</span>
              <Select
                value={health.type}
                onChange={(v) =>
                  edit(() =>
                    setHealth({
                      ...health,
                      type: v as typeof health.type,
                    }),
                  )
                }
              >
                <option value="none">None</option>
                <option value="http">HTTP request</option>
                <option value="tcp">TCP connect</option>
                <option value="cmd">Command</option>
              </Select>
            </label>
            {health.type === "http" && (
              <label className="flex flex-col gap-0.5">
                <span className="text-faint text-xs">Path</span>
                <Input
                  value={health.path}
                  onChange={(e) =>
                    edit(() => setHealth({ ...health, path: e.target.value }))
                  }
                  className="w-48 font-mono"
                  spellCheck={false}
                />
              </label>
            )}
            {(health.type === "http" || health.type === "tcp") && (
              <label className="flex flex-col gap-0.5">
                <span className="text-faint text-xs">Port</span>
                <Select
                  value={health.port}
                  onChange={(v) =>
                    edit(() => setHealth({ ...health, port: v }))
                  }
                >
                  <option value="">first port</option>
                  {ports.map((p) => (
                    <option key={p.name} value={p.name}>
                      {p.name}
                    </option>
                  ))}
                </Select>
              </label>
            )}
            {health.type === "cmd" && (
              <label className="flex min-w-48 flex-1 flex-col gap-0.5">
                <span className="text-faint text-xs">Command (sh -c)</span>
                <Input
                  value={health.command}
                  onChange={(e) =>
                    edit(() =>
                      setHealth({ ...health, command: e.target.value }),
                    )
                  }
                  placeholder="pg_isready"
                  className="font-mono"
                  spellCheck={false}
                />
              </label>
            )}
            {health.type !== "none" && (
              <label className="flex flex-col gap-0.5">
                <span className="text-faint text-xs">Every (s)</span>
                <Input
                  value={health.interval}
                  inputMode="numeric"
                  onChange={(e) =>
                    edit(() =>
                      setHealth({ ...health, interval: e.target.value }),
                    )
                  }
                  className="w-20"
                />
              </label>
            )}
          </div>
        </section>
        {dirty && problem && <Alert tone="warn">{problem}</Alert>}
        {save.error && (
          <Alert>{errText(save.error, "Could not save the ports")}</Alert>
        )}
      </div>
    </Panel>
  );
}

/** Generated addresses and labels (http), public ports (tcp, udp). */
function AddressesPanel({ path }: { path: string }) {
  const qc = useQueryClient();
  const key = ["routing", path];
  const q = useQuery({
    queryKey: key,
    queryFn: async () =>
      (await api<{ items: PortRoute[] }>("GET", `${path}/routing`)).items,
  });
  const save = useMutation({
    mutationFn: (ports: Record<string, unknown>) =>
      api<{ items: PortRoute[] }>("PUT", `${path}/routing`, { ports }),
    onSuccess: (out) => {
      qc.setQueryData(key, out.items);
      void qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const routes = q.data ?? [];
  return (
    <Panel title="Addresses" flush>
      <div className="divide-line flex flex-col divide-y">
        {routes.length === 0 && !q.isLoading && (
          <p className="text-muted p-3 text-xs">
            The service has no ports to reach.
          </p>
        )}
        {routes.map((r) =>
          r.protocol === "http" ? (
            <HttpAddress
              key={r.port}
              r={r}
              pending={save.isPending}
              onSave={(x) => save.mutate({ [r.port]: x })}
            />
          ) : (
            <PublicPort
              key={r.port}
              r={r}
              pending={save.isPending}
              onSave={(x) => save.mutate({ [r.port]: x })}
            />
          ),
        )}
      </div>
      {save.error && (
        <div className="p-2">
          <Alert>{errText(save.error, "Could not change the address")}</Alert>
        </div>
      )}
    </Panel>
  );
}

function PortHeading({ r }: { r: PortRoute }) {
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-xs">
      <span className="font-mono font-medium">{r.port}</span>
      <StatusBadge tone="neutral">
        {r.protocol.toUpperCase()} {r.container}
      </StatusBadge>
    </div>
  );
}

function HttpAddress({
  r,
  pending,
  onSave,
}: {
  r: PortRoute;
  pending: boolean;
  onSave: (x: { generated?: boolean; label?: string }) => void;
}) {
  const [label, setLabel] = useState(r.label ?? "");
  const changed = label.trim() !== (r.label ?? "");
  return (
    <div className="flex flex-col gap-2 p-3">
      <PortHeading r={r} />
      <Toggle
        checked={r.generated}
        disabled={pending}
        onChange={(v) => onSave({ generated: v })}
        label="Generated address"
        hint={
          r.generated && r.host ? (
            <span className="font-mono break-all">{r.host}</span>
          ) : (
            "Off: the port is reached only through its custom domains."
          )
        }
      />
      {r.generated && (
        <div className="flex flex-wrap items-end gap-1.5">
          <Field
            label="Label"
            hint="A short name instead of the generated one: LABEL.<base domain>. Empty: the generated name."
          >
            <Input
              value={label}
              onChange={(e) => setLabel(e.target.value.toLowerCase())}
              placeholder="shop"
              className="w-56 font-mono"
              spellCheck={false}
            />
          </Field>
          <Button
            disabled={!changed || pending}
            onClick={() => onSave({ label: label.trim() })}
          >
            Save label
          </Button>
        </div>
      )}
    </div>
  );
}

function PublicPort({
  r,
  pending,
  onSave,
}: {
  r: PortRoute;
  pending: boolean;
  onSave: (x: { public?: boolean; allow?: string[] }) => void;
}) {
  const [allow, setAllow] = useState(r.allow.join(", "));
  const list = allow
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);
  const changed = list.join(",") !== r.allow.join(",");
  const toggle = async (on: boolean) => {
    const ok = await confirmDialog({
      title: on
        ? `Open a public ${r.protocol.toUpperCase()} port for ${r.port}?`
        : `Close the public port ${r.publicPort}?`,
      message: on
        ? "A port from the public range is assigned on the controller and edge nodes, and the host firewalls open it. The Traefik replicas restart to listen on it, so HTTP traffic pauses for a moment."
        : "Clients can no longer reach the port from outside. The Traefik replicas restart, so HTTP traffic pauses for a moment.",
      confirmLabel: on ? "Open port" : "Close port",
      tone: on ? "default" : "danger",
    });
    if (ok) onSave({ public: on });
  };
  return (
    <div className="flex flex-col gap-2 p-3">
      <PortHeading r={r} />
      <Toggle
        checked={r.public}
        disabled={pending}
        onChange={(v) => void toggle(v)}
        label="Public port"
        hint={
          r.public && r.address ? (
            <span>
              Clients connect to{" "}
              <span className="text-fg font-mono break-all">{r.address}</span>
              {r.protocol === "udp" && " (UDP)"}.
            </span>
          ) : (
            "Off: reached only by other services, at the service's private address."
          )
        }
      />
      {r.public && (
        <div className="flex flex-wrap items-end gap-1.5">
          <Field
            label="Allowed clients"
            hint="Addresses or CIDRs, comma separated. Empty: anyone. Enforced by the host firewall."
          >
            <Input
              value={allow}
              onChange={(e) => setAllow(e.target.value)}
              placeholder="203.0.113.0/24, 198.51.100.7"
              className="w-80 max-w-full font-mono"
              spellCheck={false}
            />
          </Field>
          <Button
            disabled={!changed || pending}
            onClick={() => onSave({ allow: list })}
          >
            Save
          </Button>
        </div>
      )}
    </div>
  );
}

export function NetworkingTab({ path, spec }: { path: string; spec: Spec }) {
  const httpPorts = (spec.ports ?? [])
    .filter((p) => (p.protocol ?? "http") === "http")
    .map((p, i) => p.name ?? (i === 0 ? "http" : String(p.container)));
  return (
    <div className={cn("flex flex-col", gap)}>
      <AddressesPanel path={path} />
      <DomainsPanel path={path} httpPorts={httpPorts} />
      <PortsPanel
        key={JSON.stringify([spec.ports, spec.health])}
        path={path}
        spec={spec}
      />
    </div>
  );
}
