import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import {
  AlertTriangle,
  Ban,
  Check,
  Clock,
  Download,
  KeyRound,
  RefreshCw,
  ShieldCheck,
  ShieldOff,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { Tabs } from "@/ui/Tabs";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { CopyBox } from "@/ui/CopyBox";
import {
  Alert,
  Button,
  Field,
  Input,
  StatusBadge,
  Toggle,
} from "@/ui/controls";
import { Segmented } from "@/ui/choice";
import { Select } from "@/ui/select";
import { confirmAction } from "@/ui/dialogs";
import { since } from "@/lib/nodes";
import { cn, gap } from "@/ui/cn";
import { CertNames, CertSource, CertStatus } from "./CertBits";
import { NamesEditor, nameProblem } from "./NamesEditor";
import { Propagation } from "./Propagation";
import { ReviewRow, TextArea } from "./Wizard";
import {
  type CertEvent,
  type Certificate,
  type Challenge,
  dateOnly,
  errText,
  expiry,
  expiryTone,
  invalidateAll,
  methodLabel,
  sourceLabel,
  useAuthorities,
  useCertificate,
  useDomains,
} from "./lib";

type Tab = "Overview" | "Challenge" | "Details" | "History" | "Settings";

/** The ACME challenge page: the TXT records an order needs, and whether they are there. */
function ChallengeTab({ c }: { c: Certificate }) {
  const qc = useQueryClient();
  const o = c.order;
  const verify = useMutation({
    mutationFn: () => api<Certificate>("POST", `/certificates/${c.id}/verify`),
    onSuccess: () => invalidateAll(qc),
  });
  const retry = useMutation({
    mutationFn: () => api("POST", `/certificates/${c.id}/renew`),
    onSuccess: () => invalidateAll(qc),
  });
  if (!o) {
    return (
      <Panel title="ACME challenge">
        <p className="text-muted text-xs">
          {c.method === "dns-01" || c.method === "dns-01-manual"
            ? "No order is waiting. When this certificate renews, the TXT records it needs are shown here."
            : "This certificate is validated over HTTP (HTTP-01): there is nothing to add to DNS. SynCloud answers the CA's request on port 80 by itself."}
        </p>
      </Panel>
    );
  }
  const groups = new Map<string, Challenge[]>();
  for (const ch of o.challenges)
    groups.set(ch.name, [...(groups.get(ch.name) ?? []), ch]);
  const missing = o.challenges.filter((x) => !x.present).length;
  const delegated = o.challenges.filter(
    (x) => x.mode === "delegated" || x.mode === "builtin",
  );
  // One CNAME per challenge name: the apex and its wildcard share it.
  const cnamesMissing = [
    ...new Set(
      delegated.filter((x) => !x.cnameOk && !x.cnameManaged).map((x) => x.name),
    ),
  ];
  return (
    <div className="flex flex-col gap-2">
      {cnamesMissing.length > 0 ? (
        <Alert tone="warn">
          <div className="flex flex-col gap-1">
            <b>
              Create{" "}
              {cnamesMissing.length === 1
                ? "this CNAME"
                : `these ${cnamesMissing.length} CNAMEs`}{" "}
              once at your DNS host.
            </b>
            <span>
              A CNAME at <span className="font-mono">_acme-challenge</span>{" "}
              sends the authority to a name SynCloud can write. Once it is
              there, SynCloud adds and removes the TXT record itself, now and at
              every renewal: nobody has to touch DNS again. The order finishes
              as soon as the CNAME is visible.
              {o.expiresAt && (
                <> The order is open until {dateOnly(o.expiresAt)}.</>
              )}
            </span>
          </div>
        </Alert>
      ) : o.manual ? (
        <Alert tone="warn">
          <div className="flex flex-col gap-1">
            <b>
              Add{" "}
              {groups.size === 1
                ? "this TXT record"
                : `these ${groups.size} TXT records`}{" "}
              at your DNS host.
            </b>
            <span>
              SynCloud checks them every minute and finishes the order as soon
              as every public resolver shows them. Several values at one name
              all need to exist. The name is the same for{" "}
              <span className="font-mono">*.example.com</span> and{" "}
              <span className="font-mono">example.com</span>; a nested wildcard
              like <span className="font-mono">*.sub.example.com</span> has its
              own name below.
              {o.expiresAt && (
                <> The order is open until {dateOnly(o.expiresAt)}.</>
              )}
            </span>
          </div>
        </Alert>
      ) : (
        <Alert tone="info">
          {delegated.length > 0
            ? "The CNAME delegates the challenge; SynCloud writes the TXT record where it points, waits for it, finishes the order and removes it again. Nothing to do."
            : "SynCloud writes these records through the DNS provider, waits for them to appear, finishes the order and removes them again. Nothing to do."}
        </Alert>
      )}
      {[...groups].map(([name, chs]) => {
        const first = chs[0];
        if (!first) return null;
        return (
          <Panel
            key={name}
            title={
              <span className="flex items-center gap-2">
                <span className="font-mono normal-case">{name}</span>
                <span className="text-faint normal-case">
                  proves {chs.flatMap((x) => x.names).join(", ")}
                </span>
              </span>
            }
          >
            <div className="flex flex-col gap-2">
              {(first.mode === "delegated" || first.mode === "builtin") &&
                first.target && (
                  <div className="border-line flex flex-col gap-2 rounded-sm border p-2">
                    <div className="flex flex-wrap items-center gap-2 text-xs">
                      <b>CNAME</b>
                      <StatusBadge
                        tone={
                          first.cnameOk
                            ? "ok"
                            : first.cnameManaged
                              ? "info"
                              : "warn"
                        }
                      >
                        {first.cnameOk
                          ? "in place"
                          : first.cnameManaged
                            ? "made by SynCloud, waiting to appear"
                            : "create it at your DNS host"}
                      </StatusBadge>
                      <span className="text-faint">
                        set once: it serves{" "}
                        {chs.flatMap((x) => x.names).join(", ")} for every
                        renewal
                      </span>
                    </div>
                    <div className="grid grid-cols-[4rem_1fr] items-center gap-2 text-xs">
                      <span className="text-muted">Name</span>
                      <CopyBox value={name} />
                      <span className="text-muted">Points to</span>
                      <CopyBox value={first.target} />
                    </div>
                    <Propagation
                      name={name}
                      values={[first.target]}
                      type="CNAME"
                      title="CNAME seen by resolvers"
                    />
                  </div>
                )}
              <div className="grid grid-cols-[4rem_1fr] items-center gap-2 text-xs">
                <span className="text-muted">Type</span>
                <span className="font-mono">TXT</span>
                <span className="text-muted">Name</span>
                <CopyBox
                  value={
                    (first.mode === "delegated" || first.mode === "builtin") &&
                    first.target
                      ? first.target
                      : name
                  }
                />
                {chs.map((ch, i) => (
                  <ValueRow
                    key={ch.value}
                    ch={ch}
                    i={i}
                    many={chs.length > 1}
                  />
                ))}
              </div>
              <Propagation name={name} values={chs.map((x) => x.value)} />
            </div>
          </Panel>
        );
      })}
      {o.lastError && <Alert tone="warn">{o.lastError}</Alert>}
      {verify.error && (
        <Alert>{errText(verify.error, "Could not check")}</Alert>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="primary"
          disabled={verify.isPending}
          onClick={() => verify.mutate()}
        >
          <Check className="size-3.5" />{" "}
          {verify.isPending ? "Checking…" : "Verify now"}
        </Button>
        <Button
          disabled={retry.isPending}
          onClick={async () =>
            (await confirmAction(
              "Start the order again? It gets new TXT values, so the records you added no longer match.",
            )) && retry.mutate()
          }
        >
          <RefreshCw className="size-3.5" /> Start over
        </Button>
        <span className="text-faint text-xs">
          {missing === 0
            ? "Every record is visible."
            : `${missing} of ${o.challenges.length} not seen yet.`}
          {o.lastCheckedAt && <> Last checked {since(o.lastCheckedAt)}.</>}
        </span>
      </div>
    </div>
  );
}

function ValueRow({
  ch,
  i,
  many,
}: {
  ch: Challenge;
  i: number;
  many: boolean;
}) {
  return (
    <>
      <span className="text-muted">{many ? `Value ${i + 1}` : "Value"}</span>
      <div className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <CopyBox value={ch.value} />
        </div>
        <StatusBadge tone={ch.present ? "ok" : ch.auto ? "info" : "warn"}>
          {ch.present
            ? "visible"
            : ch.auto
              ? "written by SynCloud"
              : "not seen yet"}
        </StatusBadge>
      </div>
    </>
  );
}

function Overview({ c, onTab }: { c: Certificate; onTab: (t: Tab) => void }) {
  const issued = c.notAfter && !c.notAfter.startsWith("0001");
  return (
    <div className="flex flex-col gap-2">
      {c.status === "awaiting_dns" && c.order && (
        <Alert tone="warn">
          This certificate waits for DNS records.{" "}
          <button className="underline" onClick={() => onTab("Challenge")}>
            See the records to add
          </button>
        </Alert>
      )}
      {(c.status === "failed" || c.status === "rate_limited") && (
        <Alert>
          <div className="flex flex-col gap-0.5">
            <b>
              {c.status === "rate_limited"
                ? "The authority rate limited the request."
                : "The last attempt failed."}
            </b>
            <span>{c.lastError}</span>
            {c.nextAttemptAt && (
              <span>
                Next attempt{" "}
                {since(c.nextAttemptAt) === "just now"
                  ? "now"
                  : `at ${new Date(c.nextAttemptAt).toLocaleString()}`}
                .
              </span>
            )}
          </div>
        </Alert>
      )}
      {c.source === "self-signed" && (
        <Alert tone="warn">
          A self-signed certificate: browsers do not trust it. It is for private
          or internal names.
        </Alert>
      )}
      {c.source === "origin-ca" && (
        <Alert tone="warn">
          An origin certificate: only trusted behind Cloudflare's proxy with SSL
          mode "Full (strict)", not by browsers directly.
        </Alert>
      )}
      <div className={cn("grid grid-cols-2 lg:grid-cols-4", gap)}>
        <StatTile
          label="Expires"
          value={issued ? expiry(c) : "not issued"}
          tone={expiryTone(c)}
          hint={issued ? dateOnly(c.notAfter) : undefined}
        />
        <StatTile
          label="Issued by"
          value={
            c.source === "imported"
              ? (c.issuerName ?? "provider")
              : c.authority || sourceLabel[c.source]
          }
          hint={methodLabel[c.method] ?? c.method}
        />
        <StatTile
          label="Renews"
          value={
            c.autoRenew && c.source !== "imported" ? "by itself" : "by hand"
          }
          hint={c.keyType}
        />
        <StatTile
          label="Serves"
          value={c.usedBy.length}
          hint={c.usedBy.length ? "hosts" : "no route uses it yet"}
        />
      </div>
      <div className={cn("grid grid-cols-1 lg:grid-cols-2", gap)}>
        <Panel title="Names">
          <CertNames names={c.names} max={50} />
        </Panel>
        <Panel title="Where it is used">
          {c.usedBy.length === 0 ? (
            <p className="text-muted text-xs">
              No host uses it yet. A certificate serves the hosts of routes it
              covers; pin it to a host under Settings.
            </p>
          ) : (
            <ul className="flex flex-col gap-0.5 font-mono text-xs">
              {c.usedBy.map((h) => (
                <li key={h}>{h}</li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </div>
  );
}

function Details({ c }: { c: Certificate }) {
  const [err, setErr] = useState("");
  const exportKey = async () => {
    if (
      !(await confirmAction(
        `Export the private key of ${c.names[0]}? Anyone with the file can impersonate this host. The download is audited.`,
        { confirmLabel: "Export" },
      ))
    )
      return;
    setErr("");
    try {
      const b = await api<{ certificate: string; privateKey: string }>(
        "GET",
        `/certificates/${c.id}/bundle`,
      );
      for (const [name, text] of [
        ["fullchain.pem", b.certificate],
        ["privkey.pem", b.privateKey],
      ] as const) {
        const a = document.createElement("a");
        a.href = URL.createObjectURL(
          new Blob([text], { type: "application/x-pem-file" }),
        );
        a.download = `${c.names[0]!.replace("*", "wildcard")}-${name}`;
        a.click();
        URL.revokeObjectURL(a.href);
      }
    } catch (e) {
      setErr(
        e instanceof ApiError && e.status === 403
          ? "Your account may not export private keys: it needs the certificate:ExportCertificate permission."
          : errText(e, "Could not export"),
      );
    }
  };
  return (
    <div className="flex flex-col gap-2">
      <Panel
        title="Certificate"
        actions={
          c.notAfter && !c.notAfter.startsWith("0001") ? (
            <a
              href={`/api/v1/certificates/${c.id}/chain`}
              download
              className="inline-flex"
            >
              <Button>
                <Download className="size-3.5" /> Chain (PEM)
              </Button>
            </a>
          ) : undefined
        }
      >
        <ReviewRow k="Names">{c.names.join(", ")}</ReviewRow>
        <ReviewRow k="Issuer">{c.issuerName || "-"}</ReviewRow>
        <ReviewRow k="Valid from">
          {c.notBefore ? new Date(c.notBefore).toLocaleString() : "-"}
        </ReviewRow>
        <ReviewRow k="Valid until">
          {c.notAfter && !c.notAfter.startsWith("0001")
            ? new Date(c.notAfter).toLocaleString()
            : "-"}
        </ReviewRow>
        <ReviewRow k="Serial">
          <span className="font-mono break-all">{c.serial || "-"}</span>
        </ReviewRow>
        <ReviewRow k="SHA-256">
          <span className="font-mono break-all">{c.fingerprint || "-"}</span>
        </ReviewRow>
        <ReviewRow k="Key">{c.keyType || "-"}</ReviewRow>
        <ReviewRow k="Source">
          {sourceLabel[c.source]} · {methodLabel[c.method] ?? c.method}
          {c.authority ? ` · ${c.authority}` : ""}
        </ReviewRow>
        <ReviewRow k="Domain">
          {c.zone ? (
            <Link
              to={`/domains/${c.zone}` as string}
              className="hover:text-accent font-mono"
            >
              {c.zone}
            </Link>
          ) : (
            "none registered"
          )}
        </ReviewRow>
        <ReviewRow k="Trusted by browsers">
          {c.trusted ? "yes" : "no"}
        </ReviewRow>
      </Panel>
      <Panel title="Private key">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-muted max-w-xl text-xs">
            The key stays sealed on the controller. Exporting it is for an app
            that terminates TLS itself (a mail server): it needs a permission
            nobody has until it is granted, is audited, and is limited to 10 a
            day per person.
          </p>
          <Button
            onClick={exportKey}
            disabled={!c.notAfter || c.notAfter.startsWith("0001")}
          >
            <KeyRound className="size-3.5" /> Export key and chain
          </Button>
        </div>
        {err && (
          <div className="pt-2">
            <Alert>{err}</Alert>
          </div>
        )}
      </Panel>
    </div>
  );
}

function History({ id }: { id: string }) {
  const { data = [], isLoading } = useQuery({
    queryKey: ["dm", "certificate-events", id],
    queryFn: async () =>
      (await api<{ items: CertEvent[] }>("GET", `/certificates/${id}/events`))
        .items,
    refetchInterval: 20_000,
  });
  const tone: Record<string, "ok" | "bad" | "warn" | "info" | "neutral"> = {
    issued: "ok",
    renewed: "ok",
    imported: "ok",
    replaced: "ok",
    failed: "bad",
    revoked: "bad",
    fallback: "warn",
    exported: "warn",
    challenge: "info",
    requested: "info",
  };
  return (
    <Panel title="History" flush>
      <DataTable
        rows={data}
        rowKey={(e) => String(e.id)}
        empty={
          !isLoading && (
            <EmptyState icon={Clock} title="No history yet">
              Orders, renewals, failures and what people did appear here for a
              year.
            </EmptyState>
          )
        }
        columns={[
          {
            header: "When",
            cell: (e) => (
              <span title={new Date(e.at).toLocaleString()}>{since(e.at)}</span>
            ),
          },
          {
            header: "Event",
            cell: (e) => (
              <StatusBadge tone={tone[e.kind] ?? "neutral"}>
                {e.kind}
              </StatusBadge>
            ),
          },
          {
            header: "Detail",
            className: "w-full whitespace-normal",
            cell: (e) => (
              <span className="break-words whitespace-normal">{e.detail}</span>
            ),
          },
          {
            header: "By",
            cell: (e) => e.actor || <span className="text-faint">system</span>,
          },
        ]}
      />
    </Panel>
  );
}

function SettingsTab({ c }: { c: Certificate }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data: authorities } = useAuthorities();
  const { data: domains = [] } = useDomains();
  const [days, setDays] = useState(c.alertDays.join(", "));
  const [pins, setPins] = useState(c.pinnedHosts.join(", "));
  const [names, setNames] = useState(c.names);
  const patch = useMutation({
    mutationFn: (b: Record<string, unknown>) =>
      api<Certificate>("PUT", `/certificates/${c.id}`, b),
    onSuccess: () => invalidateAll(qc),
  });
  const reissue = useMutation({
    mutationFn: () =>
      api<Certificate>("POST", `/certificates/${c.id}/reissue`, { names }),
    onSuccess: () => invalidateAll(qc),
  });
  const revoke = useMutation({
    mutationFn: () => api<Certificate>("POST", `/certificates/${c.id}/revoke`),
    onSuccess: () => invalidateAll(qc),
  });
  const del = useMutation({
    mutationFn: (force: boolean) =>
      api("DELETE", `/certificates/${c.id}${force ? "?force=true" : ""}`),
    onSuccess: () => {
      invalidateAll(qc);
      void navigate({ to: "/domains/certificates" as string });
    },
  });
  const acme = c.source === "acme" || c.source === "origin-ca";
  const parseDays = () =>
    days
      .split(/[\s,]+/)
      .filter(Boolean)
      .map(Number)
      .filter((n) => Number.isInteger(n) && n > 0);
  const namesChanged =
    names.length !== c.names.length || names.some((n) => !c.names.includes(n));
  return (
    <div className={cn("grid grid-cols-1 lg:grid-cols-2", gap)}>
      <div className="flex flex-col gap-2">
        <Panel title="Renewal">
          <div className="flex flex-col gap-3">
            <Toggle
              checked={c.autoRenew && c.source !== "imported"}
              disabled={c.source === "imported" || c.source === "self-signed"}
              onChange={(v) => patch.mutate({ autoRenew: v })}
              label="Renew by itself"
              hint={
                c.source === "imported"
                  ? "An imported certificate cannot renew: replace it with a new one from your provider; an alert warns before it expires."
                  : "30 days before it expires, or a third of its life for short certificates."
              }
            />
            {acme && (
              <>
                <Field
                  label="Authority"
                  hint="Used from the next issuance. The current certificate keeps serving until the new one is valid."
                >
                  <Select
                    value={c.authorityId}
                    onChange={(v) => patch.mutate({ authority: v })}
                  >
                    {(authorities?.items ?? []).map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field
                  label="Fallback authority"
                  hint="Tried when the authority above fails repeatedly or is rate limited."
                >
                  <Select
                    value={c.fallbackId ?? ""}
                    onChange={(v) => patch.mutate({ fallback: v })}
                  >
                    <option value="">The cluster's fallback (if any)</option>
                    {(authorities?.items ?? [])
                      .filter(
                        (a) => a.kind === "acme" && a.id !== c.authorityId,
                      )
                      .map((a) => (
                        <option key={a.id} value={a.id}>
                          {a.name}
                        </option>
                      ))}
                  </Select>
                </Field>
                <Field
                  label="Key type"
                  hint="Applied at the next issuance. RSA is for old clients."
                >
                  <Segmented
                    label="Key type"
                    value={
                      c.keyType && c.keyType !== "ecdsa-p256"
                        ? c.keyType
                        : "ecdsa-p256"
                    }
                    onChange={(v) => patch.mutate({ keyType: v })}
                    options={[
                      { value: "ecdsa-p256", label: "ECDSA P-256" },
                      { value: "rsa-2048", label: "RSA 2048" },
                      { value: "rsa-3072", label: "RSA 3072" },
                      { value: "rsa-4096", label: "RSA 4096" },
                    ]}
                  />
                </Field>
              </>
            )}
            {patch.error && (
              <Alert>{errText(patch.error, "Could not save")}</Alert>
            )}
          </div>
        </Panel>
        <Panel title="Alerts and pinning">
          <div className="flex flex-col gap-3">
            <Field
              label="Alert this many days before it expires"
              hint="Whole days, separated by commas. Imported and hand-renewed certificates need them most."
            >
              <div className="flex gap-1.5">
                <Input
                  value={days}
                  onChange={(e) => setDays(e.target.value)}
                  className="max-w-xs font-mono"
                />
                <Button
                  disabled={patch.isPending || parseDays().length === 0}
                  onClick={() => patch.mutate({ alertDays: parseDays() })}
                >
                  Save
                </Button>
              </div>
            </Field>
            <Field
              label="Pinned hosts"
              hint="Hosts this certificate must serve even when another covers them better (comma-separated)."
            >
              <div className="flex gap-1.5">
                <Input
                  value={pins}
                  onChange={(e) => setPins(e.target.value)}
                  className="max-w-md font-mono"
                  placeholder="www.example.com"
                  spellCheck={false}
                />
                <Button
                  disabled={patch.isPending}
                  onClick={() =>
                    patch.mutate({
                      pinnedHosts: pins.split(/[\s,]+/).filter(Boolean),
                    })
                  }
                >
                  Save
                </Button>
              </div>
            </Field>
          </div>
        </Panel>
      </div>
      <div className="flex flex-col gap-2">
        {c.managed && acme && (
          <Panel title="Names">
            <div className="flex flex-col gap-2">
              <NamesEditor
                value={names}
                onChange={setNames}
                domains={domains}
                preferDomain={c.zone}
              />
              {names.some((n) => nameProblem(n)) && (
                <Alert tone="warn">
                  {names.map(nameProblem).find(Boolean)}
                </Alert>
              )}
              <div>
                <Button
                  disabled={
                    !namesChanged ||
                    reissue.isPending ||
                    names.length === 0 ||
                    names.some((n) => nameProblem(n))
                  }
                  onClick={() => reissue.mutate()}
                >
                  Reissue with these names
                </Button>
              </div>
              <p className="text-faint text-xs">
                The old certificate serves until the new one is valid.
              </p>
              {reissue.error && (
                <Alert>{errText(reissue.error, "Could not reissue")}</Alert>
              )}
            </div>
          </Panel>
        )}
        {c.source === "imported" && (
          <Panel title="Replace">
            <div className="flex items-center justify-between gap-2">
              <p className="text-muted text-xs">
                Upload the renewed certificate from your provider: it takes this
                one's place and pinned hosts.
              </p>
              <Link
                to={`/domains/certificates/import?replace=${c.id}` as string}
              >
                <Button>Replace…</Button>
              </Link>
            </div>
          </Panel>
        )}
        <Panel title="Danger zone">
          <div className="flex flex-col gap-3">
            {c.status !== "revoked" && c.managed && (
              <div className="flex items-center justify-between gap-2">
                <p className="text-muted text-xs">
                  {c.source === "acme"
                    ? "Revoke at the authority: it is not trusted any more and stops being served."
                    : "Stop serving it (revoke it at your provider too)."}
                </p>
                <Button
                  variant="danger"
                  disabled={revoke.isPending}
                  onClick={async () =>
                    (await confirmAction(
                      `Revoke ${c.names[0]}? It stops being served and cannot be renewed.`,
                    )) && revoke.mutate()
                  }
                >
                  <Ban className="size-3.5" /> Revoke
                </Button>
              </div>
            )}
            {c.managed && (
              <div className="flex items-center justify-between gap-2">
                <p className="text-muted text-xs">
                  Delete the certificate and its key from SynCloud. Hosts it
                  serves get their own certificates again.
                </p>
                <Button
                  variant="danger"
                  disabled={del.isPending}
                  onClick={async () =>
                    (await confirmAction(
                      `Delete ${c.names[0]}? Its key is removed from SynCloud.`,
                    )) && del.mutate(false)
                  }
                >
                  <Trash2 className="size-3.5" /> Delete
                </Button>
              </div>
            )}
            {!c.managed && (
              <p className="text-muted text-xs">
                An automatic certificate follows its route: it is removed with
                the route's domain.
              </p>
            )}
            {(revoke.error || del.error) &&
              (del.error instanceof ApiError && del.error.status === 409 ? (
                <Alert tone="warn">
                  {del.error.message}{" "}
                  <button
                    className="underline"
                    onClick={() => del.mutate(true)}
                  >
                    Delete it anyway
                  </button>
                </Alert>
              ) : (
                <Alert>
                  {errText(revoke.error ?? del.error, "The change failed")}
                </Alert>
              ))}
          </div>
        </Panel>
      </div>
    </div>
  );
}

export function CertificatePage() {
  const { id } = useParams({ strict: false }) as { id: string };
  const qc = useQueryClient();
  const { data: c, error, isLoading } = useCertificate(id);
  const [tab, setTab] = useState<Tab>("Overview");
  const renew = useMutation({
    mutationFn: () => api("POST", `/certificates/${id}/renew`),
    onSuccess: () => invalidateAll(qc),
  });
  if (isLoading) return <p className="text-muted text-xs">Loading…</p>;
  if (error || !c) {
    return (
      <EmptyState icon={ShieldOff} title="No such certificate">
        {errText(error, "It may have been deleted.")}{" "}
        <Link to={"/domains/certificates" as string} className="underline">
          Back to certificates
        </Link>
      </EmptyState>
    );
  }
  const tabsList: Tab[] = c.order
    ? ["Overview", "Challenge", "Details", "History", "Settings"]
    : ["Overview", "Details", "History", "Settings"];
  const shown = tabsList.includes(tab) ? tab : "Overview";
  const canRenew = c.source !== "imported" && c.status !== "revoked";
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={[
          <Link key="d" to={"/domains" as string}>
            Domains
          </Link>,
          <Link key="s" to={"/domains/certificates" as string}>
            SSL certificates
          </Link>,
        ]}
        title={
          <span className="font-mono">
            {c.names[0] ?? c.host}
            {c.names.length > 1 && (
              <span className="text-muted font-sans text-xs">
                {" "}
                +{c.names.length - 1}
              </span>
            )}
          </span>
        }
        status={<CertStatus c={c} />}
        actions={
          <>
            <span className="text-muted hidden items-center gap-1.5 text-xs lg:flex">
              {c.trusted ? (
                <ShieldCheck className="text-ok size-3.5" />
              ) : (
                <AlertTriangle className="text-warn size-3.5" />
              )}
              <CertSource c={c} />
            </span>
            {canRenew && (
              <Button disabled={renew.isPending} onClick={() => renew.mutate()}>
                <RefreshCw
                  className={cn("size-3.5", renew.isPending && "animate-spin")}
                />{" "}
                {c.order
                  ? "Start over"
                  : c.source === "self-signed"
                    ? "Make again"
                    : "Renew now"}
              </Button>
            )}
          </>
        }
      />
      {renew.error && <Alert>{errText(renew.error, "Could not renew")}</Alert>}
      <Tabs tabs={tabsList} value={shown} onChange={setTab} />
      {shown === "Overview" && <Overview c={c} onTab={setTab} />}
      {shown === "Challenge" && <ChallengeTab c={c} />}
      {shown === "Details" && <Details c={c} />}
      {shown === "History" && <History id={c.id} />}
      {shown === "Settings" && <SettingsTab c={c} />}
    </div>
  );
}

// Re-exported for the import page: a pasted PEM box.
export { TextArea };
