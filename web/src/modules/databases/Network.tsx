import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Plus, X } from "lucide-react";
import { statusQuery } from "@/lib/auth";
import { useProjects } from "@/lib/workloads";
import { Button, Field, Input, Toggle } from "@/ui/controls";
import { cn } from "@/ui/cn";

/** The platform's base domain ("" in development without one). */
export function useBaseDomain() {
  return useQuery(statusQuery).data?.baseDomain ?? "";
}

/** Peers an access list can name, for suggestions. */
function useAccessSuggestions() {
  const { data: projects = [] } = useProjects();
  const out: string[] = [];
  for (const p of projects) {
    out.push(`project:${p.name}`);
    for (const e of p.environments) out.push(`environment:${p.name}/${e}`);
  }
  return [...out, "cluster"];
}

const describe = (peer: string) => {
  if (peer.startsWith("project:"))
    return `every service of project ${peer.slice(8)}`;
  if (peer.startsWith("environment:"))
    return `services in ${peer.slice(12).replace("/", " / ")}`;
  if (peer.startsWith("service:"))
    return `service ${peer.slice(8).split("/").join(" / ")}`;
  if (peer.startsWith("group:"))
    return `security group ${peer.slice(6).replace("/", " / ")}`;
  if (peer === "cluster") return "every node of the cluster";
  return "addresses in this range";
};

/** Edits a database's internal access list (who may connect inside the cluster). */
export function AccessEditor({
  value,
  onChange,
}: {
  value: string[];
  onChange: (v: string[]) => void;
}) {
  const [draft, setDraft] = useState("");
  const suggestions = useAccessSuggestions().filter((s) => !value.includes(s));
  const add = () => {
    const v = draft.trim();
    if (v && !value.includes(v)) onChange([...value, v]);
    setDraft("");
  };
  return (
    <div className="flex flex-col gap-2 text-xs">
      {value.length === 0 ? (
        <p className="text-muted">
          No service in the cluster can connect. Add a project, an environment
          or a service.
        </p>
      ) : (
        <ul className="divide-line border-line divide-y rounded-sm border">
          {value.map((p) => (
            <li key={p} className="flex items-center gap-2 px-2 py-1">
              <code className="min-w-0 flex-1 truncate font-mono">{p}</code>
              <span className="text-faint hidden truncate sm:inline">
                {describe(p)}
              </span>
              <button
                type="button"
                aria-label={`Remove ${p}`}
                className="text-muted hover:text-bad shrink-0"
                onClick={() => onChange(value.filter((x) => x !== p))}
              >
                <X className="size-3.5" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex gap-2">
        <Input
          list="db-access-suggestions"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
          placeholder="project:shop or environment:shop/production"
          className="min-w-0 flex-1 font-mono"
        />
        <datalist id="db-access-suggestions">
          {suggestions.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
        <Button onClick={add} disabled={!draft.trim()}>
          <Plus className="size-3.5" /> Add
        </Button>
      </div>
    </div>
  );
}

/** The public TLS endpoint switch and its client allow-list. */
export function PublicFields({
  name,
  enabled,
  allow,
  port,
  engine,
  onEnabled,
  onAllow,
}: {
  name: string;
  enabled: boolean;
  /** One address or CIDR per line. */
  allow: string;
  port: number;
  engine: string;
  onEnabled: (v: boolean) => void;
  onAllow: (v: string) => void;
}) {
  const base = useBaseDomain();
  const host = `${name || "NAME"}.db.${base || "<base domain>"}`;
  const example =
    engine === "postgres"
      ? `postgresql://app:…@${host}:${port}/${(name || "NAME").replace(/-/g, "_")}?sslmode=require`
      : `rediss://default:…@${host}:${port}`;
  return (
    <div className="flex flex-col gap-3 text-xs">
      <Toggle
        checked={enabled}
        onChange={onEnabled}
        label="Public endpoint"
        hint={
          <>
            Reachable from outside the cluster at{" "}
            <code className="font-mono">
              {host}:{port}
            </code>{" "}
            over TLS (<code className="font-mono">{example}</code>
            ). Traefik terminates TLS with the platform's certificate and
            forwards to the current primary.
          </>
        }
      />
      {enabled && !base && (
        <p className="text-warn">
          The platform has no base domain yet, so the endpoint stays off until
          one is set under Settings › Domains.
        </p>
      )}
      {enabled && (
        <Field
          label="Allowed client addresses"
          hint="One IP address or CIDR per line. Empty: anywhere (the password still protects it)."
        >
          <textarea
            value={allow}
            onChange={(e) => onAllow(e.target.value)}
            rows={3}
            placeholder={"203.0.113.0/24\n198.51.100.7"}
            className={cn(
              "bg-bg border-line-strong focus:border-accent w-full rounded-sm border px-2 py-1 font-mono text-xs outline-none",
            )}
          />
        </Field>
      )}
    </div>
  );
}

/** Splits an allow-list textarea into entries. */
export const allowList = (s: string) =>
  s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);
