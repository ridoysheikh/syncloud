import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ArrowRight,
  Cloud,
  Hand,
  Play,
  Power,
  Server,
  ShieldCheck,
} from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { CopyBox } from "@/ui/CopyBox";
import { DataTable } from "@/ui/DataTable";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { confirmAction } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";
import {
  type ChallengeServer,
  errText,
  invalidateAll,
  useChallengeServer,
  useDomains,
} from "./lib";

interface CheckResult {
  ok: boolean;
  checks: { name: string; ok: boolean; detail: string }[];
}

/** How an authority reaches SynCloud: the CNAME of a domain, the zone's NS record, port 53. */
function Path({ zone }: { zone: string }) {
  const box = (label: string, value: string, accent?: boolean) => (
    <div
      className={cn(
        "bg-raised border-line min-w-0 flex-1 rounded-sm border px-2 py-1.5",
        accent && "border-line-accent",
      )}
    >
      <div className="text-faint text-[11px]">{label}</div>
      <div className="truncate font-mono text-xs" title={value}>
        {value}
      </div>
    </div>
  );
  const arrow = (
    <ArrowRight
      className="text-faint size-3.5 shrink-0 self-center"
      aria-hidden
    />
  );
  return (
    <div
      className="flex flex-wrap items-stretch gap-1.5 sm:flex-nowrap"
      aria-label="How the authority reaches SynCloud"
    >
      {box("The authority asks", "TXT _acme-challenge.example.org")}
      {arrow}
      {box("Your DNS: one CNAME, once", `→ <id>.${zone || "acme.example.com"}`)}
      {arrow}
      {box(
        "Your DNS: the zone's NS record",
        `${zone || "acme.example.com"} NS → this controller`,
      )}
      {arrow}
      {box("SynCloud answers", "TXT <value>", true)}
    </div>
  );
}

function Setup({ s }: { s: ChallengeServer }) {
  const qc = useQueryClient();
  const { data: domains = [] } = useDomains();
  const suggestion = domains[0]
    ? `acme.${domains[0].name}`
    : "acme.example.com";
  const [zone, setZone] = useState(s.zone);
  const [ns, setNs] = useState(s.ns);
  const [ip, setIp] = useState(s.ip);
  const [more, setMore] = useState(false);
  const save = useMutation({
    mutationFn: () =>
      api<ChallengeServer>("PUT", "/domains/challenge-server", {
        zone: zone.trim().toLowerCase(),
        ns: ns.trim().toLowerCase(),
        ip: ip.trim(),
      }),
    onSuccess: () => invalidateAll(qc),
  });
  const off = useMutation({
    mutationFn: () =>
      api<ChallengeServer>("PUT", "/domains/challenge-server", { zone: "" }),
    onSuccess: () => invalidateAll(qc),
  });
  const z = zone.trim().toLowerCase();
  const changed =
    z !== s.zone ||
    (ns.trim() && ns.trim() !== s.ns) ||
    (ip.trim() && ip.trim() !== s.ip);
  return (
    <Panel title={s.enabled ? "Settings" : "Turn it on"}>
      <div className="flex max-w-xl flex-col gap-3">
        <Field
          label="Challenge zone"
          hint={
            <>
              A name <b>below a domain you own</b> that SynCloud will answer
              for, like <span className="font-mono">{suggestion}</span>. It is
              only used for challenge records, so it can be any unused name.
            </>
          }
        >
          <Input
            value={zone}
            onChange={(e) => setZone(e.target.value)}
            placeholder={suggestion}
            className="font-mono"
            spellCheck={false}
            aria-label="Challenge zone"
          />
        </Field>
        <button
          type="button"
          className="text-muted hover:text-fg w-fit text-xs underline"
          onClick={() => setMore(!more)}
        >
          {more ? "Hide" : "Show"} name server and address
        </button>
        {more && (
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            <Field
              label="Name server host"
              hint="The A record that points at this controller. Default: derived from the zone."
            >
              <Input
                value={ns}
                onChange={(e) => setNs(e.target.value)}
                placeholder={
                  z
                    ? `${z.split(".")[0]}-ns.${z.split(".").slice(1).join(".")}`
                    : "acme-ns.example.com"
                }
                className="font-mono"
                spellCheck={false}
                aria-label="Name server host"
              />
            </Field>
            <Field
              label="Public address of this controller"
              hint="Default: detected."
            >
              <Input
                value={ip}
                onChange={(e) => setIp(e.target.value)}
                placeholder="203.0.113.9"
                className="font-mono"
                spellCheck={false}
                aria-label="Public address"
              />
            </Field>
          </div>
        )}
        {save.error && <Alert>{errText(save.error, "Could not save")}</Alert>}
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            disabled={save.isPending || !z || (s.enabled && !changed)}
            onClick={() => save.mutate()}
          >
            <Power className="size-3.5" /> {s.enabled ? "Save" : "Turn on"}
          </Button>
          {s.enabled && (
            <Button
              variant="danger"
              disabled={off.isPending}
              onClick={async () =>
                (await confirmAction(
                  `Turn the challenge server off? Domains that CNAME to it fall back to adding the TXT record by hand until you turn it on again. Certificates that already exist keep serving.`,
                )) && off.mutate()
              }
            >
              Turn off
            </Button>
          )}
        </div>
      </div>
    </Panel>
  );
}

function Records({ s }: { s: ChallengeServer }) {
  const qc = useQueryClient();
  const make = useMutation({
    mutationFn: (replace: boolean) =>
      api<{ created: number }>(
        "POST",
        `/domains/challenge-server/records${replace ? "?replace=conflicts" : ""}`,
      ),
    onSuccess: () => invalidateAll(qc),
  });
  const check = useMutation({
    mutationFn: () =>
      api<CheckResult>("POST", "/domains/challenge-server/check"),
  });
  const anyManaged = s.needed.some((n) => n.managed);
  const conflict =
    make.error && /confirmation|other records/i.test(errText(make.error, ""));
  return (
    <Panel
      title="Records to create"
      actions={
        anyManaged && (
          <Button
            variant="primary"
            disabled={make.isPending}
            onClick={() => make.mutate(false)}
          >
            <Cloud className="size-3.5" />{" "}
            {make.isPending ? "Creating…" : "Create them for me"}
          </Button>
        )
      }
    >
      <div className="flex flex-col gap-3">
        <p className="text-muted text-xs">
          Two records at the DNS of the domain the zone is under hand the zone
          to this controller. Domains at a provider can have them created here;
          for any other, create them at your DNS host.{" "}
          <b>Name server records cannot be proxied</b>: leave the address record
          on DNS only.
        </p>
        <DataTable
          rows={s.needed}
          rowKey={(n) => n.type + n.name}
          empty={null}
          columns={[
            {
              header: "Type",
              cell: (n) => <span className="font-mono">{n.type}</span>,
            },
            { header: "Name", cell: (n) => <CopyBox value={n.name} /> },
            { header: "Value", cell: (n) => <CopyBox value={n.content} /> },
            {
              header: "Where",
              cell: (n) =>
                n.managed ? (
                  <StatusBadge tone="info">
                    created by SynCloud in {n.zone}
                  </StatusBadge>
                ) : (
                  <StatusBadge tone="warn">
                    at your DNS host{n.zone ? ` (${n.zone})` : ""}
                  </StatusBadge>
                ),
            },
          ]}
        />
        {make.isSuccess && (
          <p className="text-ok text-xs">
            Created {make.data?.created ?? 0} record(s); the others were already
            there.
          </p>
        )}
        {conflict && (
          <Alert tone="warn">
            <div className="flex flex-col gap-1.5">
              <span>{errText(make.error, "")}</span>
              <div>
                <Button
                  onClick={async () =>
                    (await confirmAction(
                      "Replace the records that are in the way?",
                    )) && make.mutate(true)
                  }
                >
                  Replace them
                </Button>
              </div>
            </div>
          </Alert>
        )}
        {make.error && !conflict && (
          <Alert>{errText(make.error, "Could not create the records")}</Alert>
        )}
        <div className="border-line flex flex-col gap-2 border-t pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <Button disabled={check.isPending} onClick={() => check.mutate()}>
              <Play className="size-3.5" />{" "}
              {check.isPending
                ? "Checking from the internet…"
                : "Check it works"}
            </Button>
            <span className="text-faint text-xs">
              Asks public resolvers for the NS record, the address, and a probe
              value that only this server can answer. DNS can take a few minutes
              to spread.
            </span>
          </div>
          {check.error && (
            <Alert>{errText(check.error, "Could not check")}</Alert>
          )}
          {check.data && (
            <ul
              className="border-line divide-line divide-y rounded-sm border text-xs"
              aria-label="Check results"
            >
              {check.data.checks.map((c) => (
                <li
                  key={c.name}
                  className="flex flex-wrap items-start gap-2 px-2 py-1.5"
                >
                  <StatusBadge tone={c.ok ? "ok" : "bad"}>
                    {c.ok ? "ok" : "not yet"}
                  </StatusBadge>
                  <span className="w-44 shrink-0 font-medium">{c.name}</span>
                  <span className="text-muted min-w-0 flex-1">{c.detail}</span>
                </li>
              ))}
              {check.data.ok && (
                <li className="text-ok flex items-center gap-1.5 px-2 py-1.5">
                  <ShieldCheck className="size-3.5" /> Everything works. Domains
                  can now delegate with one CNAME.
                </li>
              )}
            </ul>
          )}
        </div>
      </div>
    </Panel>
  );
}

/** Domains › Challenge server: SynCloud answers _acme-challenge itself, so no DNS provider is needed. */
export function ChallengeServerPage() {
  const { data: s, error, isLoading } = useChallengeServer();
  if (isLoading) return <p className="text-muted text-xs">Loading…</p>;
  if (error || !s)
    return (
      <Alert>{errText(error, "Could not load the challenge server")}</Alert>
    );
  const state = !s.enabled
    ? "off"
    : s.listening
      ? "listening"
      : "not listening";
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Domains"]}
        title="Challenge server"
        status={
          <StatusBadge
            tone={!s.enabled ? "neutral" : s.listening ? "ok" : "bad"}
          >
            {state}
          </StatusBadge>
        }
      />
      <Alert tone="info">
        <div className="flex flex-col gap-0.5">
          <b>Wildcard certificates for any domain, with no DNS provider.</b>
          <span>
            Certificate authorities check wildcard names with a TXT record at{" "}
            <span className="font-mono">_acme-challenge</span>. SynCloud can
            answer that itself: you create one CNAME per domain, once, and every
            certificate and renewal after it is automatic. It answers challenge
            records for one zone only; it is not a name server for your domains.
            A DNS provider (Cloudflare) or another domain you own stays
            available as the other way.
          </span>
        </div>
      </Alert>
      <Panel title="How it works">
        <Path zone={s.zone} />
      </Panel>
      {s.enabled && (
        <div className={cn("grid grid-cols-2 lg:grid-cols-4", gap)}>
          <StatTile
            label="Server"
            value={state}
            tone={s.listening ? "ok" : "bad"}
            hint={s.listening ? `UDP and TCP on ${s.listen}` : (s.error ?? "")}
          />
          <StatTile
            label="Zone"
            value={<span className="font-mono text-sm">{s.zone}</span>}
            hint={`name server ${s.ns}`}
          />
          <StatTile
            label="Address"
            value={<span className="font-mono text-sm">{s.ip}</span>}
            hint="this controller"
          />
          <StatTile
            label="Domains"
            value={s.domains.length}
            hint={
              s.records > 0
                ? `${s.records} value(s) held right now`
                : "answered by it"
            }
          />
        </div>
      )}
      {s.enabled && !s.listening && (
        <Alert>
          <div className="flex flex-col gap-0.5">
            <b>The server could not start.</b>
            <span>
              {s.error} Port 53 is often taken by a local resolver
              (systemd-resolved, dnsmasq or bind). Free it, or start the
              controller with{" "}
              <span className="font-mono">
                --acme-dns-listen &lt;address:port&gt;
              </span>{" "}
              and forward port 53 to it. Saving again retries.
            </span>
          </div>
        </Alert>
      )}
      <div className={cn("grid grid-cols-1 xl:grid-cols-2", gap)}>
        <Setup key={`${s.zone}|${s.ns}|${s.ip}`} s={s} />
        {s.enabled ? (
          <Records s={s} />
        ) : (
          <Panel title="Before you start">
            <ul className="text-muted flex list-disc flex-col gap-1.5 pl-4 text-xs">
              <li>
                SynCloud's own host firewall already opens UDP <b>and</b> TCP
                port 53 on this controller (see{" "}
                <Link to={"/network/firewall" as string} className="underline">
                  Firewall
                </Link>
                ). Your cloud provider's firewall or security group is yours to
                open: allow UDP and TCP 53 from anywhere.
              </li>
              <li>Nothing else may listen on port 53 of the host.</li>
              <li>
                You need a domain whose DNS you can edit, to create two records
                under it.
              </li>
            </ul>
          </Panel>
        )}
      </div>
      {s.enabled && (
        <Panel title="Domains it answers" flush>
          <DataTable
            rows={s.domains}
            rowKey={(d) => d}
            empty={
              <p className="text-muted px-3 py-4 text-xs">
                No domain uses it yet. A domain without a DNS provider does
                automatically; a domain at a provider can choose it on its
                Challenge tab.
              </p>
            }
            columns={[
              {
                header: "Domain",
                cell: (d) => (
                  <Link
                    to={`/domains/${d}` as string}
                    className="hover:text-accent font-mono"
                  >
                    {d}
                  </Link>
                ),
              },
              {
                header: "Needs",
                cell: (d) => (
                  <span className="text-muted text-xs font-mono">
                    _acme-challenge.{d} CNAME …{s.zone}
                  </span>
                ),
              },
              {
                header: "",
                cell: () => (
                  <Hand className="text-faint size-3.5" aria-hidden />
                ),
              },
            ]}
          />
        </Panel>
      )}
      <p className="text-faint flex items-center gap-1.5 text-xs">
        <Server className="size-3" /> Records are kept while an order runs and
        removed after; unfinished ones are pruned after 8 days.
      </p>
    </div>
  );
}
