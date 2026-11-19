import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  Landmark,
  Plus,
  Search,
  ShieldCheck,
  Sparkles,
  Upload,
} from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { StatTile } from "@/ui/StatTile";
import { Alert, Button, Input, StatusBadge } from "@/ui/controls";
import { Select } from "@/ui/select";
import { cn, gap } from "@/ui/cn";
import { CertLink, CertNames, CertSource, CertStatus } from "./CertBits";
import {
  dateOnly,
  errText,
  expiry,
  expiryTone,
  invalidateAll,
  statusLabel,
  useCertificates,
  useDomains,
  type Certificate,
} from "./lib";

interface DomainSettings {
  baseDomain: string;
}

/** A base domain that is a real domain (not sslip.io or nip.io) can keep one wildcard certificate for the generated addresses. */
function BaseWildcard({ certs }: { certs: Certificate[] }) {
  const qc = useQueryClient();
  const { data: s } = useQuery({
    queryKey: ["settings", "domain"],
    queryFn: () => api<DomainSettings>("GET", "/settings/domain"),
  });
  const base = s?.baseDomain ?? "";
  const real =
    base &&
    !/\.(sslip|nip)\.io$/.test(base) &&
    base !== "localhost" &&
    !base.endsWith(".localhost");
  const covered = certs.some(
    (c) => c.managed && c.status !== "revoked" && c.names.includes(`*.${base}`),
  );
  const ask = useMutation({
    mutationFn: () =>
      api("POST", "/certificates", {
        names: [base, `*.${base}`],
        method: "dns-01",
      }),
    onSuccess: () => invalidateAll(qc),
  });
  if (!real || covered) return null;
  return (
    <Panel title="Generated addresses">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-muted max-w-3xl text-xs">
          Every service address under{" "}
          <b className="text-fg font-mono">{base}</b> gets a certificate of its
          own. One wildcard certificate for{" "}
          <span className="font-mono">{base}</span> and{" "}
          <span className="font-mono">*.{base}</span> can replace them: fewer
          certificates, no per-address issuing, fewer Let's Encrypt limits. It
          needs DNS validation: automatic with a DNS provider on the domain,
          else a TXT record you add. Certificates that serve traffic stay until
          the new one is valid.
        </p>
        <Button disabled={ask.isPending} onClick={() => ask.mutate()}>
          <Sparkles className="size-3.5" /> Use one wildcard certificate
        </Button>
      </div>
      {ask.error && (
        <Alert>{errText(ask.error, "Could not request the certificate")}</Alert>
      )}
    </Panel>
  );
}

export function SslPage() {
  const { data = [], isLoading } = useCertificates();
  const { data: domains = [] } = useDomains();
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [zone, setZone] = useState("");
  const [source, setSource] = useState("");
  const rows = data.filter(
    (c) =>
      (!q ||
        c.names.some((n) => n.includes(q.toLowerCase())) ||
        c.authority.toLowerCase().includes(q.toLowerCase())) &&
      (!status || c.status === status) &&
      (!zone || c.zone === zone) &&
      (!source || c.source === source),
  );
  const live = data.filter((c) => c.status !== "revoked");
  const soon = live.filter(
    (c) => c.status === "valid" && c.daysLeft < 30,
  ).length;
  const attention = live.filter(
    (c) =>
      c.status === "failed" ||
      c.status === "rate_limited" ||
      c.status === "awaiting_dns",
  ).length;
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Domains"]}
        title="SSL certificates"
        actions={
          <>
            <Link to={"/domains/authorities" as string}>
              <Button>
                <Landmark className="size-3.5" /> Authorities
              </Button>
            </Link>
            <Link to={"/domains/certificates/import" as string}>
              <Button>
                <Upload className="size-3.5" /> Import
              </Button>
            </Link>
            <Link to={"/domains/certificates/new" as string}>
              <Button variant="primary">
                <Plus className="size-3.5" /> Request certificate
              </Button>
            </Link>
          </>
        }
      />
      <div className={cn("grid grid-cols-2 lg:grid-cols-4", gap)}>
        <StatTile
          label="Certificates"
          value={live.length}
          hint={`${live.filter((c) => c.managed).length} asked for, ${live.filter((c) => !c.managed).length} automatic`}
        />
        <StatTile
          label="Valid"
          value={live.filter((c) => c.status === "valid").length}
          tone="ok"
          hint={`${live.filter((c) => c.wildcard).length} wildcard`}
        />
        <StatTile
          label="Expiring in 30 days"
          value={soon}
          tone={soon ? "warn" : "ok"}
        />
        <StatTile
          label="Needs attention"
          value={attention}
          tone={attention ? "bad" : "ok"}
          hint="failing, rate limited or waiting for DNS"
        />
      </div>
      <BaseWildcard certs={data} />
      <Panel
        title="Every certificate"
        flush
        actions={
          <div className="flex flex-wrap items-center gap-1.5">
            <div className="relative">
              <Search className="text-faint pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2" />
              <Input
                value={q}
                onChange={(e) => setQ(e.target.value)}
                placeholder="Name or authority"
                className="h-7 w-44 pl-7"
              />
            </div>
            <Select
              value={status}
              onChange={setStatus}
              size="sm"
              aria-label="State"
            >
              <option value="">Any state</option>
              {Object.entries(statusLabel).map(([k, v]) => (
                <option key={k} value={k}>
                  {v}
                </option>
              ))}
            </Select>
            <Select
              value={zone}
              onChange={setZone}
              size="sm"
              aria-label="Domain"
            >
              <option value="">Any domain</option>
              {domains.map((d) => (
                <option key={d.name}>{d.name}</option>
              ))}
            </Select>
            <Select
              value={source}
              onChange={setSource}
              size="sm"
              aria-label="Source"
            >
              <option value="">Any source</option>
              <option value="acme">ACME</option>
              <option value="imported">Imported</option>
              <option value="origin-ca">Origin CA</option>
              <option value="self-signed">Self-signed</option>
            </Select>
          </div>
        }
      >
        <DataTable
          rows={rows}
          rowKey={(c) => c.id}
          empty={
            !isLoading && (
              <EmptyState
                icon={ShieldCheck}
                title={
                  data.length ? "No certificate matches" : "No certificates yet"
                }
              >
                {data.length
                  ? "Change the filters."
                  : "Certificates for routes appear here by themselves. Request a wildcard certificate, or import one from your provider."}
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Names",
              cell: (c) => (
                <span className="flex items-center gap-2">
                  <CertLink c={c} />
                  <CertNames names={c.names.slice(1)} max={2} />
                </span>
              ),
            },
            { header: "Status", cell: (c) => <CertStatus c={c} /> },
            { header: "Issued by", cell: (c) => <CertSource c={c} /> },
            {
              header: "Expires",
              cell: (c) => (
                <span
                  title={dateOnly(c.notAfter)}
                  className={cn(
                    expiryTone(c) === "bad" && "text-bad",
                    expiryTone(c) === "warn" && "text-warn",
                  )}
                >
                  {expiry(c)}
                </span>
              ),
            },
            {
              header: "Renews",
              cell: (c) =>
                c.autoRenew && c.source !== "imported" ? (
                  <StatusBadge tone="ok">automatic</StatusBadge>
                ) : (
                  <StatusBadge tone="neutral">by hand</StatusBadge>
                ),
            },
            {
              header: "Domain",
              cell: (c) =>
                c.zone ? (
                  <Link
                    to={`/domains/${c.zone}` as string}
                    className="hover:text-accent font-mono"
                  >
                    {c.zone}
                  </Link>
                ) : (
                  <span className="text-faint">-</span>
                ),
            },
            {
              header: "Serves",
              cell: (c) =>
                c.usedBy.length ? (
                  c.usedBy.length
                ) : (
                  <span className="text-faint">0</span>
                ),
            },
          ]}
        />
      </Panel>
    </div>
  );
}
