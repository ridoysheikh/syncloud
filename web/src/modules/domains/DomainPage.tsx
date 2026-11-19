import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Cloud, Globe, Plus, ShieldCheck, Trash2, Upload } from "lucide-react";
import { api } from "@/lib/api";
import { useProjects } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { Tabs } from "@/ui/Tabs";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { CopyBox } from "@/ui/CopyBox";
import { Alert, Button, Field, StatusBadge, Toggle } from "@/ui/controls";
import { ChipSelect, ChoiceCards } from "@/ui/choice";
import { Select } from "@/ui/select";
import { confirmAction } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";
import { CertLink, CertNames, CertSource, CertStatus } from "./CertBits";
import { RecordsTab } from "./RecordsTab";
import { ChallengeTab } from "./ChallengeTab";
import { accessText } from "./DomainsPage";
import {
  challengeSummary,
  errText,
  expiry,
  expiryTone,
  invalidateAll,
  useAuthorities,
  useCertificates,
  useChallengeServer,
  useDomain,
  useProviders,
  type Domain,
} from "./lib";

const tabs = [
  "Overview",
  "Records",
  "Certificates",
  "Challenge",
  "Access",
] as const;
type Tab = (typeof tabs)[number];

interface DomainCheck {
  host: string;
  targets: string[];
  missing: string[];
  addresses: string[];
  ready: boolean;
  partial: boolean;
  record: string;
}

/** Does a name point at the cluster? Uses the same check as a service's domain. */
function Reach({ host, label }: { host: string; label: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ["dm", "reach", host],
    queryFn: () =>
      api<DomainCheck>(
        "GET",
        `/domains/check?host=${encodeURIComponent(host)}`,
      ),
    refetchInterval: 30_000,
  });
  return (
    <div className="border-line flex flex-wrap items-center gap-x-3 gap-y-1 border-b py-2 text-xs last:border-b-0">
      <span className="w-40 shrink-0 font-mono">{label}</span>
      {isLoading || !data ? (
        <span className="text-faint">checking…</span>
      ) : data.ready ? (
        <StatusBadge tone="ok">points at the cluster</StatusBadge>
      ) : data.addresses.length === 0 ? (
        <StatusBadge tone="neutral">no record</StatusBadge>
      ) : (
        <StatusBadge tone="warn">
          {data.partial
            ? `missing ${data.missing.join(", ")}`
            : `points at ${data.addresses.join(", ")}`}
        </StatusBadge>
      )}
      {data && data.targets.length > 0 && (
        <span className="text-faint">needs {data.targets.join(", ")}</span>
      )}
    </div>
  );
}

function Overview({ d, onTab }: { d: Domain; onTab: (t: Tab) => void }) {
  const { data: server } = useChallengeServer();
  const { data: certs = [] } = useCertificates({ zone: d.name });
  const live = certs.filter((c) => c.status !== "revoked");
  return (
    <div className="flex flex-col gap-2">
      <div className={cn("grid grid-cols-2 lg:grid-cols-5", gap)}>
        <StatTile
          label="DNS"
          value={d.mode === "managed" ? (d.provider ?? "provider") : "manual"}
          hint={
            d.mode === "managed"
              ? "records are written by SynCloud"
              : "records are shown, you create them"
          }
        />
        <StatTile
          label="_acme-challenge"
          value={challengeSummary(d, server).label}
          hint={challengeSummary(d, server).hint}
        />
        <StatTile
          label="Certificates"
          value={live.length}
          hint={`${live.filter((c) => c.wildcard).length} wildcard`}
        />
        <StatTile
          label="Service domains"
          value={d.use.services}
          hint="hosts of services under it"
        />
        <StatTile
          label="Usable by"
          value={accessText(d)}
          hint={
            d.grants.some((g) => g.wildcards)
              ? "wildcards allowed"
              : "no wildcards"
          }
        />
      </div>
      <div className={cn("grid grid-cols-1 lg:grid-cols-2", gap)}>
        <Panel
          title="Does it point here?"
          actions={
            d.mode === "managed" && (
              <Button onClick={() => onTab("Records")}>
                <Cloud className="size-3.5" /> Point at the cluster
              </Button>
            )
          }
        >
          <Reach host={d.name} label={d.name} />
          <Reach host={`*.${d.name}`} label={`*.${d.name}`} />
          <p className="text-faint pt-1.5 text-xs">
            A wildcard DNS record answers every name below it, also a.b.{d.name}
            ; a wildcard certificate covers one label only.
          </p>
        </Panel>
        <Panel title="Name servers">
          {d.nameServers && d.nameServers.length > 0 ? (
            <div className="flex flex-col gap-1.5">
              <p className="text-muted text-xs">
                Set these at your registrar so the provider answers for {d.name}
                :
              </p>
              {d.nameServers.map((ns) => (
                <CopyBox key={ns} value={ns} />
              ))}
            </div>
          ) : (
            <p className="text-muted text-xs">
              {d.mode === "managed"
                ? "The provider reports none."
                : "Manual domain: its name servers are wherever you keep the DNS. SynCloud is not a name server."}
            </p>
          )}
        </Panel>
      </div>
      <Panel
        title="Certificates"
        flush
        actions={
          <div className="flex gap-1.5">
            <Link
              to={`/domains/certificates/import?domain=${d.name}` as string}
            >
              <Button>
                <Upload className="size-3.5" /> Import
              </Button>
            </Link>
            <Link to={`/domains/certificates/new?domain=${d.name}` as string}>
              <Button variant="primary">
                <Plus className="size-3.5" /> Request certificate
              </Button>
            </Link>
          </div>
        }
      >
        <CertTable certs={live} />
      </Panel>
    </div>
  );
}

export function CertTable({
  certs,
  empty,
}: {
  certs: ReturnType<typeof useCertificates>["data"];
  empty?: string;
}) {
  return (
    <DataTable
      rows={certs ?? []}
      rowKey={(c) => c.id}
      empty={
        <EmptyState icon={ShieldCheck} title="No certificates">
          {empty ??
            "Request a wildcard certificate (*.example.com) or import one from your provider."}
        </EmptyState>
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
          header: "Serves",
          cell: (c) =>
            c.usedBy.length ? (
              `${c.usedBy.length} host${c.usedBy.length === 1 ? "" : "s"}`
            ) : (
              <span className="text-faint">none yet</span>
            ),
        },
      ]}
    />
  );
}

function CertificatesTab({ d }: { d: Domain }) {
  const { data: certs = [] } = useCertificates({ zone: d.name });
  return (
    <Panel
      title="Certificates of this domain"
      flush
      actions={
        <div className="flex gap-1.5">
          <Link to={`/domains/certificates/import?domain=${d.name}` as string}>
            <Button>
              <Upload className="size-3.5" /> Import
            </Button>
          </Link>
          <Link to={`/domains/certificates/new?domain=${d.name}` as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> Request certificate
            </Button>
          </Link>
        </div>
      }
    >
      <CertTable certs={certs} />
    </Panel>
  );
}

function AccessTab({ d }: { d: Domain }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data: projects = [] } = useProjects();
  const { data: providers = [] } = useProviders();
  const { data: authorities } = useAuthorities();
  const all = d.grants.some((g) => g.project === "*");
  const [access, setAccess] = useState<"all" | "chosen">(
    all ? "all" : "chosen",
  );
  const [chosen, setChosen] = useState<string[]>(
    d.grants.filter((g) => g.project !== "*").map((g) => g.project),
  );
  const [wildcards, setWildcards] = useState(
    d.grants.every((g) => g.wildcards),
  );
  const patch = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      api<Domain>("PUT", `/domains/${d.name}`, body),
    onSuccess: () => invalidateAll(qc),
  });
  const remove = useMutation({
    mutationFn: (force: boolean) =>
      api("DELETE", `/domains/${d.name}${force ? "?force=true" : ""}`),
    onSuccess: () => {
      invalidateAll(qc);
      void navigate({ to: "/domains" as string });
    },
  });
  const current = d.grants
    .filter((g) => g.project !== "*")
    .map((g) => g.project);
  const dirty =
    access === "all"
      ? !all
      : all ||
        chosen.length !== current.length ||
        !chosen.every((p) => current.includes(p)) ||
        wildcards !== d.grants.every((g) => g.wildcards);
  return (
    <div className={cn("grid grid-cols-1 lg:grid-cols-2", gap)}>
      <Panel title="Who may use it">
        <div className="flex flex-col gap-3">
          <ChoiceCards
            label="Who may use it"
            value={access}
            onChange={setAccess}
            options={[
              {
                value: "all",
                title: "All projects",
                description:
                  "Any service may take this domain, a subdomain or a wildcard under it.",
              },
              {
                value: "chosen",
                title: "Chosen projects",
                description: "Only the projects you pick.",
              },
            ]}
          />
          {access === "chosen" && (
            <>
              <ChipSelect
                label="Projects"
                value={chosen}
                onChange={setChosen}
                options={projects.map((p) => ({
                  value: p.name,
                  label: p.name,
                }))}
                mono
                empty="No projects yet."
              />
              <Toggle
                checked={wildcards}
                onChange={setWildcards}
                label="They may use wildcard hosts"
              />
            </>
          )}
          {patch.error && (
            <Alert>{errText(patch.error, "Could not save")}</Alert>
          )}
          <div>
            <Button
              variant="primary"
              disabled={
                patch.isPending ||
                (access === "chosen" && chosen.length === 0) ||
                !dirty
              }
              onClick={() =>
                patch.mutate({
                  grants:
                    access === "all"
                      ? [{ project: "*", wildcards: true }]
                      : chosen.map((p) => ({ project: p, wildcards })),
                })
              }
            >
              Save access
            </Button>
          </div>
          <p className="text-faint text-xs">
            Services that already use a name keep working; this decides who may
            add names. The platform's own names (the base domain, registry.,
            git., agent., db.) are never available.
          </p>
        </div>
      </Panel>
      <div className="flex flex-col gap-2">
        <Panel title="DNS provider">
          <div className="flex flex-col gap-2">
            <Field
              label="Provider"
              hint={
                d.mode === "managed"
                  ? "SynCloud writes this domain's records through it."
                  : "No provider: records are shown for you to create."
              }
            >
              <Select
                value={d.providerId ?? ""}
                onChange={(v) => patch.mutate({ provider: v })}
              >
                <option value="">None (manual)</option>
                {providers.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
        </Panel>
        <Panel title="Certificates">
          <div className="flex flex-col gap-2">
            <Toggle
              checked={d.wildcardPolicy === "on"}
              onChange={(v) =>
                patch.mutate({ wildcardPolicy: v ? "on" : "off" })
              }
              label={`Keep one wildcard certificate for ${d.name} and *.${d.name}`}
              hint={
                d.mode === "managed"
                  ? "Issued and renewed by itself with DNS-01."
                  : "Manual domain: its TXT record must be added at each renewal."
              }
            />
            <Field label="Certificate authority for this domain">
              <Select
                value={d.authorityId ?? ""}
                onChange={(v) => patch.mutate({ authority: v })}
              >
                <option value="">The cluster's default</option>
                {(authorities?.items ?? [])
                  .filter((a) => a.kind === "acme")
                  .map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}
                    </option>
                  ))}
              </Select>
            </Field>
          </div>
        </Panel>
        <Panel title="Remove">
          <div className="flex items-center justify-between gap-2">
            <p className="text-muted text-xs">
              Stops managing {d.name}. Nothing is deleted at the provider, and
              services and certificates keep working.
            </p>
            <Button
              variant="danger"
              onClick={async () => {
                const use = d.use.services + d.use.certificates;
                if (
                  !(await confirmAction(
                    `Remove ${d.name}? ${use > 0 ? `${d.use.services} service domain(s) and ${d.use.certificates} certificate(s) use it; they keep working.` : "Nothing uses it."}`,
                  ))
                )
                  return;
                remove.mutate(use > 0);
              }}
            >
              <Trash2 className="size-3.5" /> Remove domain
            </Button>
          </div>
          {remove.error && (
            <Alert>
              {errText(remove.error, "Could not remove the domain")}
            </Alert>
          )}
        </Panel>
      </div>
    </div>
  );
}

export function DomainPage() {
  const { zone } = useParams({ strict: false }) as { zone: string };
  const { data: d, error, isLoading } = useDomain(zone);
  const [tab, setTab] = useState<Tab>("Overview");
  if (isLoading) return <p className="text-muted text-xs">Loading…</p>;
  if (error || !d) {
    return (
      <EmptyState icon={Globe} title="No such domain">
        {errText(error, `${zone} is not registered.`)}{" "}
        <Link to={"/domains" as string} className="underline">
          Back to domains
        </Link>
      </EmptyState>
    );
  }
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={[
          <Link key="d" to={"/domains" as string}>
            Domains
          </Link>,
        ]}
        title={<span className="font-mono">{d.name}</span>}
        status={
          d.mode === "managed" ? (
            <StatusBadge tone="ok">{d.provider ?? "provider"}</StatusBadge>
          ) : (
            <StatusBadge tone="neutral">manual DNS</StatusBadge>
          )
        }
      />
      <Tabs tabs={tabs} value={tab} onChange={setTab} />
      {tab === "Overview" && <Overview d={d} onTab={setTab} />}
      {tab === "Records" && <RecordsTab domain={d} />}
      {tab === "Certificates" && <CertificatesTab d={d} />}
      {tab === "Challenge" && <ChallengeTab d={d} />}
      {tab === "Access" && <AccessTab d={d} />}
    </div>
  );
}
