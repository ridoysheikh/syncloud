import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Play, Server, ShieldCheck } from "lucide-react";
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
  useChallengeServer,
  useSetChallengeServer,
} from "./lib";
import { TurnOnChallengeServer } from "./ChallengeServerButton";

interface CheckResult {
  ok: boolean;
  checks: { name: string; ok: boolean; detail: string }[];
}

/** How an authority reaches SynCloud: the domain's CNAME, sslip.io's delegation, port 53. */
function Path({ s }: { s: ChallengeServer }) {
  const zone = s.zone || "_acme-challenge.203-0-113-9.sslip.io";
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
      {box("The authority asks", "TXT _acme-challenge.example.com")}
      {arrow}
      {box("Your DNS: one CNAME, once", `→ <id>.${zone}`)}
      {arrow}
      {box(
        "sslip.io: already delegated",
        `${zone} → ${s.ip || "this controller"}`,
      )}
      {arrow}
      {box("SynCloud answers", "TXT <value>", true)}
    </div>
  );
}

function TurnOn() {
  const [ip, setIp] = useState("");
  const [more, setMore] = useState(false);
  return (
    <Panel title="Turn it on">
      <div className="flex max-w-xl flex-col gap-3">
        <p className="text-muted text-xs">
          One click. SynCloud answers{" "}
          <span className="font-mono">
            _acme-challenge.&lt;its IP&gt;.sslip.io
          </span>
          , which sslip.io already hands to this controller: there is no name
          server or address record to create anywhere.
        </p>
        <TurnOnChallengeServer ip={ip} label="Turn on" />
        <button
          type="button"
          className="text-muted hover:text-fg w-fit text-xs underline"
          onClick={() => setMore(!more)}
        >
          {more ? "Hide" : "Behind NAT? Give the public address"}
        </button>
        {more && (
          <Field
            label="Public address of this controller"
            hint="Default: detected, and followed when it changes. An address given here is kept."
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
        )}
      </div>
    </Panel>
  );
}

function Check() {
  const check = useMutation({
    mutationFn: () =>
      api<CheckResult>("POST", "/domains/challenge-server/check"),
  });
  return (
    <Panel title="Check from the internet">
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <Button disabled={check.isPending} onClick={() => check.mutate()}>
            <Play className="size-3.5" />{" "}
            {check.isPending ? "Checking from the internet…" : "Check it works"}
          </Button>
          <span className="text-faint text-xs">
            Sends a probe value that only this server can answer through public
            resolvers.
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
                <ShieldCheck className="size-3.5" /> Everything works. Each
                domain needs one CNAME, once.
              </li>
            )}
          </ul>
        )}
      </div>
    </Panel>
  );
}

/** Domains › Challenge server: SynCloud answers _acme-challenge itself, so no DNS provider is needed. */
export function ChallengeServerPage() {
  const { data: s, error, isLoading } = useChallengeServer();
  const set = useSetChallengeServer();
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
  const turnOff = async () =>
    (await confirmAction(
      "Turn the challenge server off? Domains that CNAME to it fall back to adding the TXT record by hand until you turn it on again. Certificates that already exist keep serving.",
    )) && set.mutate({ enabled: false });
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
        actions={
          s.enabled && (
            <Button variant="danger" disabled={set.isPending} onClick={turnOff}>
              Turn off
            </Button>
          )
        }
      />
      <Alert tone="info">
        <div className="flex flex-col gap-0.5">
          <b>Wildcard certificates for any domain, with no DNS provider.</b>
          <span>
            Certificate authorities check wildcard names with a TXT record at{" "}
            <span className="font-mono">_acme-challenge</span>. SynCloud answers
            it itself: you create one CNAME per domain, once, and every
            certificate and renewal after it is automatic. It answers challenge
            records only; it is not a name server for your domains.
          </span>
        </div>
      </Alert>
      <Panel title="How it works">
        <Path s={s} />
      </Panel>
      {set.error && <Alert>{errText(set.error, "Could not save")}</Alert>}
      {!s.enabled && (
        <div className={cn("grid grid-cols-1 xl:grid-cols-2", gap)}>
          <TurnOn />
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
                The zone holds the controller's IP. If it changes, SynCloud
                moves the zone and each domain's Challenge tab shows the new
                CNAME.
              </li>
            </ul>
          </Panel>
        </div>
      )}
      {s.enabled && (
        <>
          <div className={cn("grid grid-cols-2 lg:grid-cols-4", gap)}>
            <StatTile
              label="Server"
              value={state}
              tone={s.listening ? "ok" : "bad"}
              hint={
                s.listening
                  ? `UDP and TCP on ${(s.addresses?.length ? s.addresses : [s.listen]).join(", ")}`
                  : (s.error ?? "")
              }
            />
            <StatTile
              label="Zone"
              value={<CopyBox value={s.zone} />}
              hint={s.sslip ? "delegated by sslip.io" : `name server ${s.ns}`}
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
          {!s.listening && (
            <Alert>
              <div className="flex flex-col gap-0.5">
                <b>The server could not start.</b>
                <span>
                  {s.error} Another DNS server holds port 53 on this host's
                  public addresses (systemd-resolved alone is fine: it only
                  holds 127.0.0.53). Free it, or start the controller with{" "}
                  <span className="font-mono">
                    --acme-dns-listen &lt;address:port&gt;
                  </span>{" "}
                  and forward port 53 to it. Turning it on again retries.
                </span>
                <div className="pt-1">
                  <TurnOnChallengeServer label="Retry" />
                </div>
              </div>
            </Alert>
          )}
          {!s.sslip && (
            <Alert tone="warn">
              <div className="flex flex-col gap-1">
                <span>
                  This server uses an older zone under one of your domains (
                  <span className="font-mono">{s.zone}</span>). Switch to the
                  sslip.io zone to drop its NS and address records; each
                  domain's CNAME then changes once.
                </span>
                <TurnOnChallengeServer label="Switch to sslip.io" />
              </div>
            </Alert>
          )}
          <Check />
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
                  header: "Needs, once",
                  cell: (d) => (
                    <span className="text-muted font-mono text-xs">
                      _acme-challenge.{d} CNAME …{s.zone}
                    </span>
                  ),
                },
              ]}
            />
          </Panel>
        </>
      )}
      <p className="text-faint flex items-center gap-1.5 text-xs">
        <Server className="size-3" /> Records are kept while an order runs and
        removed after; unfinished ones are pruned after 8 days.
      </p>
    </div>
  );
}
