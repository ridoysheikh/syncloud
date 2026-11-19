import { useEffect, useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, Check, Globe, Sparkles } from "lucide-react";
import { Field, Input, StatusBadge, Toggle } from "@/ui/controls";
import { Segmented } from "@/ui/choice";
import { Select } from "@/ui/select";
import { cn } from "@/ui/cn";
import {
  type Certificate,
  type Domain,
  dateOnly,
  useCertificates,
  useDomains,
} from "./lib";

/* The domain and subdomain a service takes (Phase 28): chosen from the
   registered domains, with a wildcard switch, or typed as a custom hostname
   (the old way, still there). It shows the resulting host, whether the
   project may use it and how its certificate will be had. */

const labelRE =
  /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/;

/** Whether a project may use a host under a domain, as the controller decides. */
export function allowed(d: Domain, project: string, host: string): string {
  const wild = host.startsWith("*.");
  const base = host.replace(/^\*\./, "");
  for (const g of d.grants) {
    if (g.project !== "*" && g.project !== project) continue;
    const suffix = g.suffix || d.name;
    if (!(base === suffix || base.endsWith("." + suffix))) continue;
    if (wild && !g.wildcards) continue;
    return "";
  }
  return wild
    ? `${project || "This project"} may not use wildcards under ${d.name}`
    : `${d.name} is not granted to ${project || "this project"}`;
}

/** Which certificate covers a host: exact, or one label of a wildcard. */
export function coverage(
  host: string,
  certs: Certificate[],
): Certificate | undefined {
  const covers = (n: string) =>
    n === host ||
    (n.startsWith("*.") &&
      !host.startsWith("*.") &&
      host.endsWith(n.slice(1)) &&
      !host.slice(0, -(n.length - 1)).includes("."));
  return certs.find(
    (c) =>
      c.status !== "revoked" &&
      c.daysLeft >= 0 &&
      c.source !== "self-signed" &&
      c.names.some(covers) &&
      (c.status === "valid" || c.status === "awaiting_dns"),
  );
}

function CertLine({ host, certs }: { host: string; certs: Certificate[] }) {
  const c = coverage(host, certs);
  if (c) {
    return (
      <span className="text-ok inline-flex items-center gap-1">
        <Check className="size-3" /> covered by{" "}
        <span className="font-mono">
          {c.names.find((n) => n === host || n.startsWith("*.")) ?? c.names[0]}
        </span>
        , valid until {dateOnly(c.notAfter)}
      </span>
    );
  }
  if (host.startsWith("*.")) {
    return (
      <span className="text-warn inline-flex flex-wrap items-center gap-1">
        <AlertTriangle className="size-3" /> a wildcard needs a wildcard
        certificate (DNS validation).
        <Link
          to={
            `/domains/certificates/new?domain=${host.replace(/^\*\./, "")}` as string
          }
          className="underline"
        >
          Request one
        </Link>
      </span>
    );
  }
  return (
    <span className="text-muted">
      a certificate is issued automatically once DNS points here
    </span>
  );
}

export function DomainPicker({
  project,
  value,
  onChange,
  disabled,
}: {
  project: string;
  value: string;
  onChange: (host: string) => void;
  disabled?: boolean;
}) {
  const { data: domains = [], isLoading } = useDomains();
  const { data: certs = [] } = useCertificates();
  const [mode, setMode] = useState<"domain" | "custom">("domain");
  const [zone, setZone] = useState("");
  const [sub, setSub] = useState("");
  const [wild, setWild] = useState(false);
  const usable = useMemo(
    () =>
      domains.filter(
        (d) =>
          !allowed(d, project, d.name) || !allowed(d, project, `x.${d.name}`),
      ),
    [domains, project],
  );
  const z = domains.find((d) => d.name === (zone || usable[0]?.name));
  const subOK = sub === "" || labelRE.test(sub);
  const host = z ? `${wild ? "*." : ""}${sub ? sub + "." : ""}${z.name}` : "";
  useEffect(() => {
    if (mode === "domain") onChange(z && subOK ? host : "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, host, subOK, z?.name]);
  if (isLoading || domains.length === 0) {
    return (
      <Field
        label="Domain"
        hint={
          domains.length === 0 && !isLoading ? (
            <>
              Register domains under{" "}
              <Link to={"/domains" as string} className="underline">
                Domains
              </Link>{" "}
              to choose them from a list.
            </>
          ) : undefined
        }
      >
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="shop.example.com"
          className="w-56 font-mono"
          spellCheck={false}
          disabled={disabled}
        />
      </Field>
    );
  }
  const problem = z && host ? allowed(z, project, host) : "";
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-end gap-1.5">
        <Segmented
          label="Where the name comes from"
          size="md"
          value={mode}
          onChange={(m) => {
            setMode(m);
            if (m === "custom") onChange("");
          }}
          options={[
            { value: "domain", label: "My domains", icon: Globe },
            { value: "custom", label: "Custom hostname" },
          ]}
        />
        {mode === "domain" ? (
          <>
            <Field
              label={
                wild
                  ? "Subdomain (the wildcard sits below it)"
                  : "Subdomain (empty = the domain itself)"
              }
            >
              <div className="flex items-center gap-1">
                {wild && (
                  <span className="text-accent font-mono text-sm">*.</span>
                )}
                <Input
                  value={sub}
                  onChange={(e) => setSub(e.target.value.toLowerCase())}
                  placeholder={wild ? "optional" : "shop"}
                  className="w-36 font-mono"
                  spellCheck={false}
                  disabled={disabled}
                  aria-invalid={!subOK}
                />
                <span className="text-faint font-mono text-sm">.</span>
              </div>
            </Field>
            <Field label="Domain">
              <Select
                value={z?.name ?? ""}
                onChange={setZone}
                disabled={disabled}
              >
                {domains.map((d) => (
                  <option key={d.name} value={d.name}>
                    {d.name}
                  </option>
                ))}
              </Select>
            </Field>
            <span className="flex h-8 items-center">
              <Toggle
                checked={wild}
                onChange={setWild}
                label={
                  <span className="inline-flex items-center gap-1">
                    <Sparkles className="text-accent size-3" /> Wildcard
                  </span>
                }
                disabled={disabled}
              />
            </span>
          </>
        ) : (
          <Field
            label="Hostname"
            hint="Any name you control. A wildcard is *.example.com."
          >
            <Input
              value={value}
              onChange={(e) => onChange(e.target.value)}
              placeholder="shop.example.com"
              className="w-64 font-mono"
              spellCheck={false}
              disabled={disabled}
            />
          </Field>
        )}
      </div>
      {mode === "domain" && host && (
        <div className="flex flex-col gap-0.5 text-xs">
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-muted">Host</span>
            <code className={cn("font-mono", wild && "text-accent")}>
              {host}
            </code>
            {wild && (
              <span className="text-faint">
                matches one label: a.{sub ? sub + "." : ""}
                {z?.name}, not {sub ? "" : `${z?.name} itself or `}a.b.
                {sub ? sub + "." : ""}
                {z?.name}
              </span>
            )}
            {z && (
              <StatusBadge tone={z.mode === "managed" ? "ok" : "neutral"}>
                {z.mode === "managed"
                  ? (z.provider ?? "provider")
                  : "manual DNS"}
              </StatusBadge>
            )}
          </span>
          {problem ? (
            <span className="text-bad">{problem}</span>
          ) : (
            <CertLine host={host} certs={certs} />
          )}
          {!subOK && (
            <span className="text-bad">
              A subdomain uses lowercase letters, digits and hyphens, with dots
              between labels.
            </span>
          )}
        </div>
      )}
    </div>
  );
}
