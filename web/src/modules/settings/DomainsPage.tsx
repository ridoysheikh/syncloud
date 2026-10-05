import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, RefreshCw, ShieldCheck } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { subscribe } from "@/lib/stream";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface DomainSettings {
  baseDomain: string;
  dashboardUrl: string;
  registryHost: string;
  publicIp: string;
  publicIpError?: string;
  suggestions: string[];
  acme: { enabled: boolean; directoryUrl: string; email: string };
  warnings: string[];
}

interface Certificate {
  host: string;
  issuer: "acme" | "self-signed";
  status: "valid" | "pending" | "failed" | "rate_limited" | "self_signed";
  notAfter: string;
  lastError: string;
  failures: number;
  nextAttemptAt: string | null;
  updatedAt: string;
}

const certTone = { valid: "ok", pending: "info", failed: "bad", rate_limited: "warn", self_signed: "neutral" } as const;
const certLabel = { valid: "valid", pending: "requesting", failed: "failed", rate_limited: "rate limited", self_signed: "self-signed" } as const;
const domainKey = ["settings", "domain"];
const certKey = ["certificates"];

/** Base domain and TLS certificates (§5.0.2). */
export function DomainsPage() {
  const qc = useQueryClient();
  const { data: settings } = useQuery({ queryKey: domainKey, queryFn: () => api<DomainSettings>("GET", "/settings/domain") });
  const { data: certs = [], isLoading } = useQuery({
    queryKey: certKey,
    queryFn: async () => (await api<{ items: Certificate[] }>("GET", "/certificates")).items,
  });
  useEffect(
    () =>
      subscribe("certificate.updated", (e) => {
        const c = e.data as Certificate;
        qc.setQueryData<Certificate[]>(certKey, (prev) => prev?.map((p) => (p.host === c.host ? c : p)));
      }),
    [qc],
  );

  const [value, setValue] = useState("");
  const [result, setResult] = useState<DomainSettings | null>(null);
  const save = useMutation({
    mutationFn: (baseDomain: string) => api<DomainSettings>("PUT", "/settings/domain", { baseDomain }),
    onSuccess: (s) => {
      setResult(s);
      setValue("");
      qc.setQueryData(domainKey, s);
      qc.invalidateQueries({ queryKey: certKey });
      qc.invalidateQueries({ queryKey: ["system", "status"] });
    },
  });
  const renew = useMutation({
    mutationFn: (host: string) => api("POST", `/certificates/${encodeURIComponent(host)}/renew`),
    onSuccess: () => setTimeout(() => qc.invalidateQueries({ queryKey: certKey }), 1500),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (value.trim()) save.mutate(value.trim());
  };
  const rateLimited = certs.some((c) => c.status === "rate_limited");
  const nipSuggestion = settings?.suggestions.find((s) => s.endsWith(".nip.io"));
  const onNewHost = result && typeof window !== "undefined" && window.location.hostname !== result.baseDomain;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Settings"]} title="Domains & certificates" />

      {result && onNewHost && (
        <Alert tone="info">
          The dashboard now lives at{" "}
          <a className="underline" href={result.dashboardUrl}>
            {result.dashboardUrl}
          </a>
          . Sign in again there once its certificate is ready.
        </Alert>
      )}
      {result?.warnings.map((w) => (
        <Alert key={w} tone="warn">
          {w}
        </Alert>
      ))}
      {rateLimited && (
        <Alert tone="warn">
          The certificate authority rate-limited this domain. HTTPS uses a self-signed certificate until the retry succeeds.
          {nipSuggestion && settings?.baseDomain.endsWith(".sslip.io") && (
            <>
              {" "}
              You can switch to{" "}
              <button className="underline" onClick={() => save.mutate(nipSuggestion)}>
                {nipSuggestion}
              </button>
              , which has a separate limit.
            </>
          )}
        </Alert>
      )}

      <div className={cn("grid lg:grid-cols-2", gap)}>
        <Panel title="Base domain">
          <dl className="grid grid-cols-[8rem_1fr] gap-y-1.5 text-xs">
            <dt className="text-muted">Base domain</dt>
            <dd className="font-mono">{settings?.baseDomain || <span className="text-faint">not set (development)</span>}</dd>
            <dt className="text-muted">Dashboard</dt>
            <dd className="font-mono">
              {settings && (
                <a className="hover:text-accent inline-flex items-center gap-1" href={settings.dashboardUrl}>
                  {settings.dashboardUrl} <ExternalLink className="size-3" />
                </a>
              )}
            </dd>
            <dt className="text-muted">Registry</dt>
            <dd className="font-mono">{settings?.registryHost}</dd>
            <dt className="text-muted">Services</dt>
            <dd className="font-mono">{settings?.baseDomain ? `<service>.<project>.${settings.baseDomain}` : "—"}</dd>
            <dt className="text-muted">Public IP</dt>
            <dd className="font-mono">{settings?.publicIp || <span className="text-warn">{settings?.publicIpError ? "not detected" : "…"}</span>}</dd>
          </dl>
        </Panel>

        <Panel title="Change base domain">
          <form onSubmit={submit} className="flex flex-col gap-2">
            <Field label="Domain" hint="Point an A record for the domain and its registry. subdomain (or a wildcard) at the public IP first.">
              <div className="flex gap-1.5">
                <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder="cloud.example.com" className="flex-1 font-mono" />
                <Button variant="primary" type="submit" disabled={save.isPending || !value.trim()}>
                  Save
                </Button>
              </div>
            </Field>
            {settings && settings.suggestions.length > 0 && (
              <div className="flex flex-wrap items-center gap-1.5 text-xs">
                <span className="text-muted">No DNS? Use</span>
                {settings.suggestions.map((s) => (
                  <Button key={s} type="button" variant="ghost" className="font-mono" disabled={s === settings.baseDomain || save.isPending} onClick={() => save.mutate(s)}>
                    {s}
                  </Button>
                ))}
              </div>
            )}
            {save.error && <Alert>{save.error instanceof ApiError ? save.error.message : "Could not save the domain"}</Alert>}
          </form>
        </Panel>
      </div>

      <Panel
        title="Certificates"
        flush
        actions={
          settings && (
            <span className="text-faint text-xs">
              {settings.acme.enabled ? `ACME: ${new URL(settings.acme.directoryUrl).host}` : "ACME disabled: self-signed only"}
            </span>
          )
        }
      >
        <DataTable
          rows={certs}
          rowKey={(c) => c.host}
          empty={
            !isLoading && (
              <EmptyState icon={ShieldCheck} title="No certificates">
                Certificates are requested automatically once a base domain is set.
              </EmptyState>
            )
          }
          columns={[
            { header: "Host", cell: (c) => <span className="font-mono">{c.host}</span>, className: "w-full" },
            {
              header: "Status",
              cell: (c) => (
                <div className="flex flex-col items-start gap-0.5">
                  <StatusBadge tone={certTone[c.status]}>{certLabel[c.status]}</StatusBadge>
                  {c.lastError && c.status !== "valid" && (
                    <span className="text-bad max-w-md truncate" title={c.lastError}>
                      {c.lastError}
                    </span>
                  )}
                </div>
              ),
            },
            { header: "Issuer", cell: (c) => <span className="text-muted">{c.issuer || "—"}</span> },
            { header: "Expires", cell: (c) => (c.notAfter ? new Date(c.notAfter).toLocaleDateString() : "—") },
            {
              header: "Next retry",
              cell: (c) => <span className="text-muted">{c.nextAttemptAt && new Date(c.nextAttemptAt) > new Date() ? new Date(c.nextAttemptAt).toLocaleTimeString() : "—"}</span>,
            },
            {
              header: "",
              cell: (c) =>
                settings?.acme.enabled && (
                  <IconButton label="Request now" onClick={() => renew.mutate(c.host)} disabled={renew.isPending}>
                    <RefreshCw className="size-3.5" />
                  </IconButton>
                ),
            },
          ]}
        />
      </Panel>
    </div>
  );
}
