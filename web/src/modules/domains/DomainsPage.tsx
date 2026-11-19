import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import {
  Cloud,
  Globe,
  Plus,
  Search,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { StatTile } from "@/ui/StatTile";
import { Button, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import {
  type Certificate,
  type Domain,
  useCertificates,
  useDomains,
  useProviders,
} from "./lib";

/** Who may use a domain, in a few words. */
export function accessText(d: Pick<Domain, "grants" | "name">): string {
  if (d.grants.some((g) => g.project === "*")) return "all projects";
  const ps = [...new Set(d.grants.map((g) => g.project))];
  return ps.length === 0
    ? "nobody"
    : ps.length <= 2
      ? ps.join(", ")
      : `${ps.length} projects`;
}

/** The certificate state of a domain, from its certificates. */
function certSummary(certs: Certificate[]) {
  const live = certs.filter((c) => c.status !== "revoked");
  const soon = live.filter(
    (c) => c.daysLeft < 30 && c.status === "valid",
  ).length;
  const waiting = live.filter(
    (c) => c.status === "awaiting_dns" || c.status === "verifying",
  ).length;
  const bad = live.filter(
    (c) => c.status === "failed" || c.status === "rate_limited",
  ).length;
  return { total: live.length, soon, waiting, bad };
}

export function DomainsPage() {
  const { data: domains = [], isLoading } = useDomains();
  const { data: certs = [] } = useCertificates();
  const { data: providers = [] } = useProviders();
  const [q, setQ] = useState("");
  const byZone = useMemo(() => {
    const m = new Map<string, Certificate[]>();
    for (const c of certs)
      if (c.zone) m.set(c.zone, [...(m.get(c.zone) ?? []), c]);
    return m;
  }, [certs]);
  const rows = domains.filter((d) => !q || d.name.includes(q.toLowerCase()));
  const managed = domains.filter((d) => d.mode === "managed").length;
  const attention = certs.filter(
    (c) =>
      c.managed &&
      (c.status === "failed" ||
        c.status === "rate_limited" ||
        c.status === "awaiting_dns" ||
        (c.status === "valid" && c.daysLeft < 14)),
  ).length;
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Domains"]}
        title="Domains"
        actions={
          <>
            <Link to={"/domains/providers" as string}>
              <Button>
                <Cloud className="size-3.5" /> DNS providers
              </Button>
            </Link>
            <Link to={"/domains/new" as string}>
              <Button variant="primary">
                <Plus className="size-3.5" /> Add domain
              </Button>
            </Link>
          </>
        }
      />
      <div className={cn("grid grid-cols-2 lg:grid-cols-4", gap)}>
        <StatTile
          label="Domains"
          value={domains.length}
          hint={`${managed} managed by a provider`}
        />
        <StatTile
          label="DNS providers"
          value={providers.length}
          hint={
            providers.length
              ? providers.map((p) => p.name).join(", ")
              : "none: records are shown, not written"
          }
        />
        <StatTile
          label="Certificates"
          value={certs.filter((c) => c.managed).length}
          hint={`${certs.filter((c) => c.wildcard).length} wildcard`}
        />
        <StatTile
          label="Needs attention"
          value={attention}
          tone={attention ? "warn" : "ok"}
          hint={
            attention
              ? "expiring, waiting for DNS or failing"
              : "all certificates are in order"
          }
        />
      </div>
      <Panel
        title="Registered domains"
        flush
        actions={
          <div className="relative">
            <Search className="text-faint pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2" />
            <Input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Find a domain"
              className="h-7 w-44 pl-7"
            />
          </div>
        }
      >
        <DataTable
          rows={rows}
          rowKey={(d) => d.name}
          empty={
            !isLoading && (
              <EmptyState
                icon={Globe}
                title={q ? "No such domain" : "No domains yet"}
              >
                Register a domain to choose it and its subdomains for services,
                keep its DNS records here and issue wildcard certificates. A
                domain works with or without a DNS provider.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Domain",
              cell: (d) => (
                <Link
                  to={`/domains/${d.name}` as string}
                  className="hover:text-accent font-mono"
                >
                  {d.name}
                </Link>
              ),
            },
            {
              header: "DNS",
              cell: (d) =>
                d.mode === "managed" ? (
                  <StatusBadge tone="ok">
                    {d.provider ?? "provider"}
                  </StatusBadge>
                ) : (
                  <StatusBadge tone="neutral">manual</StatusBadge>
                ),
            },
            {
              header: "Certificates",
              cell: (d) => {
                const s = certSummary(byZone.get(d.name) ?? []);
                if (s.total === 0)
                  return <span className="text-faint">none</span>;
                return (
                  <span className="flex items-center gap-1.5">
                    <ShieldCheck className="text-ok size-3.5" />
                    {s.total}
                    {s.bad > 0 && (
                      <StatusBadge tone="bad">{s.bad} failing</StatusBadge>
                    )}
                    {s.waiting > 0 && (
                      <StatusBadge tone="warn">
                        {s.waiting} waiting for DNS
                      </StatusBadge>
                    )}
                    {s.soon > 0 && (
                      <StatusBadge tone="warn">{s.soon} expiring</StatusBadge>
                    )}
                  </span>
                );
              },
            },
            {
              header: "Wildcard certificate",
              cell: (d) =>
                d.wildcardPolicy === "on" ? (
                  <span className="text-accent inline-flex items-center gap-1">
                    <Sparkles className="size-3" /> kept
                  </span>
                ) : (
                  <span className="text-faint">off</span>
                ),
            },
            { header: "Service domains", cell: (d) => d.use.services },
            { header: "Usable by", cell: (d) => accessText(d) },
          ]}
        />
      </Panel>
    </div>
  );
}
