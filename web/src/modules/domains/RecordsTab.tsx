import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  Check,
  Eraser,
  Minus,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  Wand2,
} from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import {
  Alert,
  Button,
  Field,
  IconButton,
  Input,
  StatusBadge,
  Toggle,
} from "@/ui/controls";
import { Dialog } from "@/ui/Dialog";
import { Select } from "@/ui/select";
import { CopyBox } from "@/ui/CopyBox";
import { confirmAction } from "@/ui/dialogs";
import { cn } from "@/ui/cn";
import {
  errText,
  type Domain,
  type DnsRecord,
  type PlanStep,
  type PointResult,
} from "./lib";

const types = ["A", "AAAA", "CNAME", "TXT", "MX", "SRV", "CAA", "NS"] as const;
const proxiable = (t: string) => t === "A" || t === "AAAA" || t === "CNAME";

const shortName = (name: string, zone: string) =>
  name === zone
    ? "@"
    : name.endsWith("." + zone)
      ? name.slice(0, -zone.length - 1)
      : name;

function useDebounce<T>(v: T, ms = 350): T {
  const [x, setX] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setX(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return x;
}

// ── one record ──────────────────────────────────────────────────────────────

function RecordDialog({
  zone,
  record,
  onClose,
}: {
  zone: string;
  record: DnsRecord | null;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [type, setType] = useState(record?.type ?? "A");
  const [name, setName] = useState(record ? shortName(record.name, zone) : "");
  const [content, setContent] = useState(record?.content ?? "");
  const [ttl, setTtl] = useState(
    record && record.ttl > 1 ? String(record.ttl) : "",
  );
  const [proxied, setProxied] = useState(record?.proxied ?? false);
  const [priority, setPriority] = useState(
    record?.priority != null ? String(record.priority) : "",
  );
  const save = useMutation({
    mutationFn: () => {
      const body = {
        type,
        name: name.trim() || "@",
        content: content.trim(),
        ttl: ttl ? Number(ttl) : 0,
        proxied: proxiable(type) && proxied,
        ...(priority ? { priority: Number(priority) } : {}),
      };
      return record
        ? api("PUT", `/domains/${zone}/records/${record.id}`, body)
        : api("POST", `/domains/${zone}/records`, body);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["dm", "records", zone] });
      onClose();
    },
  });
  const hint: Record<string, string> = {
    A: "An IPv4 address, like 203.0.113.10",
    AAAA: "An IPv6 address",
    CNAME: "The name it is an alias of, like lb.example.net",
    TXT: "The text (quotes are added for you)",
    MX: "The mail server's name",
    SRV: "priority weight port target",
    CAA: "flags tag value, like 0 issue letsencrypt.org",
    NS: "A name server",
  };
  return (
    <Dialog
      open
      onClose={onClose}
      title={record ? "Edit record" : "Add record"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={!content.trim() || save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-2">
        <div className="grid grid-cols-[6rem_1fr] gap-2">
          <Field label="Type">
            <Select value={type} onChange={setType}>
              {types.map((t) => (
                <option key={t}>{t}</option>
              ))}
            </Select>
          </Field>
          <Field
            label="Name"
            hint={`Under ${zone}: a label, a name like apps.shop, or @ for the domain itself`}
          >
            <div className="flex items-center gap-1">
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="@"
                className="font-mono"
                spellCheck={false}
              />
              <span className="text-faint font-mono text-xs whitespace-nowrap">
                .{zone}
              </span>
            </div>
          </Field>
        </div>
        <Field label="Content" hint={hint[type]}>
          <Input
            value={content}
            onChange={(e) => setContent(e.target.value)}
            className="font-mono"
            spellCheck={false}
          />
        </Field>
        <div className="grid grid-cols-2 gap-2">
          <Field label="TTL (seconds, empty = automatic)">
            <Input
              value={ttl}
              onChange={(e) => setTtl(e.target.value.replace(/\D/g, ""))}
              inputMode="numeric"
              placeholder="automatic"
            />
          </Field>
          {(type === "MX" || type === "SRV") && (
            <Field label="Priority">
              <Input
                value={priority}
                onChange={(e) => setPriority(e.target.value.replace(/\D/g, ""))}
                inputMode="numeric"
                placeholder="10"
              />
            </Field>
          )}
        </div>
        {proxiable(type) && (
          <Toggle
            checked={proxied}
            onChange={setProxied}
            label="Proxied through Cloudflare"
            hint="Hides the server's address. HTTP-01 certificates may then fail (DNS-01 works), and the real client IP needs Network → Traefik → Trusted proxies → Cloudflare ranges."
          />
        )}
        {save.error && (
          <Alert>
            {errText(save.error, "Could not save the record")}
            {save.error instanceof ApiError &&
              save.error.status === 409 &&
              " Edit or remove the one that is there instead."}
          </Alert>
        )}
      </div>
    </Dialog>
  );
}

// ── pointing a name at the cluster ──────────────────────────────────────────

const stepIcon = {
  create: Plus,
  keep: Check,
  update: Pencil,
  delete: Minus,
} as const;
const reasonText: Record<PlanStep["reason"], string> = {
  missing: "needed",
  ok: "already right",
  duplicate: "the same record twice",
  stale: "SynCloud made it, no longer needed",
  conflict: "in the way",
  settings: "other settings",
};

export function PlanList({ steps }: { steps: PlanStep[] }) {
  if (steps.length === 0)
    return <p className="text-muted text-xs">Nothing to do.</p>;
  return (
    <ul className="border-line divide-line divide-y rounded-sm border text-xs">
      {steps.map((s, i) => {
        const Icon = stepIcon[s.action];
        return (
          <li
            key={i}
            className="flex flex-wrap items-center gap-x-2 gap-y-0.5 px-2 py-1.5"
          >
            <span
              className={cn(
                "flex w-16 items-center gap-1 font-medium",
                s.action === "delete"
                  ? "text-bad"
                  : s.action === "create"
                    ? "text-ok"
                    : s.action === "update"
                      ? "text-warn"
                      : "text-muted",
              )}
            >
              <Icon className="size-3.5" /> {s.action}
            </span>
            <span className="text-muted w-10 font-mono">{s.record.type}</span>
            <span className="font-mono break-all">{s.record.name}</span>
            <ArrowRight className="text-faint size-3" />
            <span className="font-mono break-all">{s.record.content}</span>
            <span className="text-faint ml-auto">
              {reasonText[s.reason]}
              {s.needsConfirm && " · a record you made"}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

function PointDialog({
  domain,
  onClose,
}: {
  domain: Domain;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [replace, setReplace] = useState(false);
  const [proxied, setProxied] = useState(false);
  const debounced = useDebounce(name);
  // The preview is for the name typed a moment ago: what is applied is
  // what was previewed, so Apply waits until they are the same.
  const stale = name !== debounced;
  const body = {
    name: debounced.trim() || "@",
    proxied: proxied ? true : undefined,
  };
  const preview = useQuery({
    queryKey: ["dm", "point", domain.name, debounced, proxied],
    queryFn: () =>
      api<PointResult>(
        "POST",
        `/domains/${domain.name}/records/point?plan=true`,
        body,
      ),
    retry: false,
  });
  const apply = useMutation({
    mutationFn: () =>
      api<PointResult>(
        "POST",
        `/domains/${domain.name}/records/point${replace ? "?replace=conflicts" : ""}`,
        body,
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["dm", "records", domain.name] });
    },
  });
  const steps = preview.data?.plan.steps ?? [];
  const conflicts = steps.filter((s) => s.needsConfirm).length;
  const changes = steps.filter((s) => s.action !== "keep").length;
  const done = apply.data?.applied;
  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title="Point a name at the cluster"
      description="SynCloud works out the A and AAAA records the name needs, keeps the ones that are right, collapses duplicates and removes what it made earlier and no longer needs."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {done ? "Close" : "Cancel"}
          </Button>
          {!done && (
            <Button
              variant="primary"
              disabled={
                stale ||
                preview.isPending ||
                preview.isFetching ||
                changes === 0 ||
                apply.isPending ||
                (conflicts > 0 && !replace && changes === conflicts)
              }
              onClick={() => apply.mutate()}
            >
              {apply.isPending
                ? "Applying…"
                : `Apply ${changes} change${changes === 1 ? "" : "s"}`}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-2">
        <Field
          label="Name"
          hint="Where services will be reached: @ for the domain itself, a label like shop or a name like apps.shop. A wildcard record is *."
        >
          <div className="flex items-center gap-1">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="@"
              className="font-mono"
              spellCheck={false}
              autoFocus
            />
            <span className="text-faint font-mono text-xs whitespace-nowrap">
              .{domain.name}
            </span>
          </div>
        </Field>
        {domain.mode === "managed" && (
          <Toggle
            checked={proxied}
            onChange={setProxied}
            label="Proxy through Cloudflare"
            hint="Hides the server's address; HTTP-01 certificates may then need DNS-01."
          />
        )}
        <div className="flex items-center gap-2">
          <span className="text-muted text-xs">What would change</span>
          {(stale || preview.isFetching) && (
            <RefreshCw className="text-faint size-3 animate-spin" />
          )}
        </div>
        {preview.error ? (
          <Alert>
            {errText(preview.error, "Could not work out the records")}
          </Alert>
        ) : (
          <PlanList steps={steps} />
        )}
        {conflicts > 0 && !done && (
          <Alert tone="warn">
            <div className="flex flex-col gap-1">
              <span>
                {conflicts} record{conflicts === 1 ? " is" : "s are"} in the way
                and {conflicts === 1 ? "was" : "were"} not made by SynCloud.
                They stay unless you confirm.
              </span>
              <Toggle
                checked={replace}
                onChange={setReplace}
                label="Also remove the records that are in the way"
              />
            </div>
          </Alert>
        )}
        {apply.error && (
          <Alert>{errText(apply.error, "Could not apply")}</Alert>
        )}
        {done && (
          <Alert tone="info">
            Done: {done.Created} created, {done.Updated} updated, {done.Deleted}{" "}
            removed
            {done.Skipped && done.Skipped.length > 0
              ? `, ${done.Skipped.length} left as they were (not confirmed)`
              : ""}
            .
          </Alert>
        )}
      </div>
    </Dialog>
  );
}

function CleanupDialog({
  zone,
  onClose,
}: {
  zone: string;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const found = useQuery({
    queryKey: ["dm", "cleanup", zone],
    queryFn: () =>
      api<{
        duplicates: DnsRecord[];
        challenges: DnsRecord[];
        removed: number;
      }>("POST", `/domains/${zone}/records/cleanup`),
  });
  const apply = useMutation({
    mutationFn: () =>
      api<{ removed: number }>(
        "POST",
        `/domains/${zone}/records/cleanup?apply=true`,
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["dm", "records", zone] });
      void found.refetch();
    },
  });
  const d = found.data;
  const n =
    (d?.duplicates.length ?? 0) +
    (d?.challenges.filter((c) => c.managed).length ?? 0);
  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title="Clean up records"
      description="Exact duplicates and leftover _acme-challenge records. Only what SynCloud created is removed from the challenges; other tools' records are listed, never touched."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button
            variant="primary"
            disabled={n === 0 || apply.isPending}
            onClick={() => apply.mutate()}
          >
            {apply.isPending ? "Removing…" : `Remove ${n}`}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-2">
        {found.isPending && <p className="text-muted text-xs">Looking…</p>}
        {d && (
          <>
            <Panel title={`Duplicates (${d.duplicates.length})`}>
              {d.duplicates.length === 0 ? (
                <p className="text-muted text-xs">None.</p>
              ) : (
                <ul className="text-xs">
                  {d.duplicates.map((r) => (
                    <li key={r.id} className="font-mono break-all">
                      {r.type} {r.name} → {r.content}
                    </li>
                  ))}
                </ul>
              )}
            </Panel>
            <Panel title={`Challenge records (${d.challenges.length})`}>
              {d.challenges.length === 0 ? (
                <p className="text-muted text-xs">None.</p>
              ) : (
                <ul className="text-xs">
                  {d.challenges.map((r) => (
                    <li
                      key={r.id}
                      className="flex flex-wrap items-center gap-2"
                    >
                      <span className="font-mono break-all">{r.name}</span>
                      {r.managed ? (
                        <StatusBadge tone="info">
                          made by SynCloud: removed
                        </StatusBadge>
                      ) : (
                        <StatusBadge tone="neutral">
                          another tool's: kept
                        </StatusBadge>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </Panel>
          </>
        )}
        {(found.error || apply.error) && (
          <Alert>
            {errText(found.error ?? apply.error, "Could not clean up")}
          </Alert>
        )}
        {apply.data && <Alert tone="info">Removed {apply.data.removed}.</Alert>}
      </div>
    </Dialog>
  );
}

// ── the tab ─────────────────────────────────────────────────────────────────

export function RecordsTab({ domain }: { domain: Domain }) {
  const qc = useQueryClient();
  const [type, setType] = useState("");
  const [q, setQ] = useState("");
  const [edit, setEdit] = useState<DnsRecord | "new" | null>(null);
  const [point, setPoint] = useState(false);
  const [clean, setClean] = useState(false);
  const managed = domain.mode === "managed";
  const dq = useDebounce(q);
  const {
    data = [],
    isLoading,
    error,
    isFetching,
  } = useQuery({
    queryKey: ["dm", "records", domain.name, type, dq],
    enabled: managed,
    queryFn: async () => {
      const p = new URLSearchParams();
      if (type) p.set("type", type);
      if (dq) p.set("q", dq);
      return (
        await api<{ items: DnsRecord[] }>(
          "GET",
          `/domains/${domain.name}/records?${p}`,
        )
      ).items;
    },
  });
  const remove = useMutation({
    mutationFn: ({ id, force }: { id: string; force?: boolean }) =>
      api(
        "DELETE",
        `/domains/${domain.name}/records/${id}${force ? "?force=true" : ""}`,
      ),
    onSuccess: () =>
      void qc.invalidateQueries({ queryKey: ["dm", "records", domain.name] }),
  });
  if (!managed) return <ManualRecords domain={domain} />;
  return (
    <>
      <Panel
        title="DNS records"
        flush
        actions={
          <div className="flex flex-wrap items-center gap-1.5">
            <Input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Name or content"
              className="h-7 w-40"
            />
            <Select value={type} onChange={setType} size="sm">
              <option value="">All types</option>
              {types.map((t) => (
                <option key={t}>{t}</option>
              ))}
            </Select>
            <IconButton
              label="Refresh"
              onClick={() =>
                void qc.invalidateQueries({
                  queryKey: ["dm", "records", domain.name],
                })
              }
            >
              <RefreshCw
                className={cn("size-3.5", isFetching && "animate-spin")}
              />
            </IconButton>
            <Button onClick={() => setClean(true)}>
              <Eraser className="size-3.5" /> Clean up
            </Button>
            <Button onClick={() => setPoint(true)}>
              <Wand2 className="size-3.5" /> Point at the cluster
            </Button>
            <Button variant="primary" onClick={() => setEdit("new")}>
              <Plus className="size-3.5" /> Add record
            </Button>
          </div>
        }
      >
        {error ? (
          <div className="p-2">
            <Alert>{errText(error, "Could not read the records")}</Alert>
          </div>
        ) : (
          <DataTable
            rows={data}
            rowKey={(r) => r.id}
            empty={
              !isLoading && (
                <EmptyState icon={Wand2} title="No records">
                  {q || type
                    ? "No record matches."
                    : "This zone has no records yet. Point a name at the cluster to create the ones a service needs."}
                </EmptyState>
              )
            }
            columns={[
              {
                header: "Type",
                cell: (r) => (
                  <span className="bg-raised border-line rounded-sm border px-1.5 font-mono">
                    {r.type}
                  </span>
                ),
              },
              {
                header: "Name",
                cell: (r) => (
                  <span className="font-mono">
                    {shortName(r.name, domain.name)}
                  </span>
                ),
              },
              {
                header: "Content",
                className: "max-w-[28rem]",
                cell: (r) => (
                  <span className="block truncate font-mono" title={r.content}>
                    {r.priority != null ? `${r.priority} ` : ""}
                    {r.content}
                  </span>
                ),
              },
              {
                header: "TTL",
                cell: (r) => (r.ttl > 1 ? `${r.ttl}s` : "auto"),
              },
              {
                header: "",
                cell: (r) =>
                  r.proxied ? (
                    <StatusBadge tone="warn">proxied</StatusBadge>
                  ) : null,
              },
              {
                header: "",
                cell: (r) =>
                  r.managed ? (
                    <StatusBadge tone="info">SynCloud</StatusBadge>
                  ) : null,
              },
              {
                header: "",
                cell: (r) => (
                  <span className="flex justify-end gap-0.5">
                    <IconButton label="Edit" onClick={() => setEdit(r)}>
                      <Pencil className="size-3.5" />
                    </IconButton>
                    <IconButton
                      label="Delete"
                      onClick={async () => {
                        if (
                          !(await confirmAction(
                            `Delete ${r.type} ${r.name}? ${r.name} stops answering with ${r.content}.`,
                          ))
                        )
                          return;
                        remove.mutate({ id: r.id });
                      }}
                    >
                      <Trash2 className="size-3.5" />
                    </IconButton>
                  </span>
                ),
              },
            ]}
          />
        )}
      </Panel>
      {remove.error &&
        (remove.error instanceof ApiError && remove.error.status === 409 ? (
          <Alert tone="warn">
            {remove.error.message}{" "}
            <button
              className="underline"
              onClick={() => {
                const v = remove.variables;
                if (v) remove.mutate({ id: v.id, force: true });
              }}
            >
              Remove it anyway
            </button>
          </Alert>
        ) : (
          <Alert>{errText(remove.error, "Could not delete the record")}</Alert>
        ))}
      {edit && (
        <RecordDialog
          zone={domain.name}
          record={edit === "new" ? null : edit}
          onClose={() => setEdit(null)}
        />
      )}
      {point && <PointDialog domain={domain} onClose={() => setPoint(false)} />}
      {clean && (
        <CleanupDialog zone={domain.name} onClose={() => setClean(false)} />
      )}
    </>
  );
}

// ── a manual domain: records to create by hand ──────────────────────────────

function ManualRecords({ domain }: { domain: Domain }) {
  const [name, setName] = useState("");
  const [asked, setAsked] = useState<string | null>(null);
  const shown = useQuery({
    queryKey: ["dm", "manual", domain.name, asked],
    enabled: asked !== null,
    queryFn: () =>
      api<PointResult>("POST", `/domains/${domain.name}/records/point`, {
        name: asked || "@",
      }),
  });
  const records = shown.data?.manual ?? [];
  return (
    <div className="flex flex-col gap-2">
      <Alert tone="info">
        {domain.name} has no DNS provider, so SynCloud cannot write its records:
        it shows what to create at your DNS host and checks it live. Connect
        Cloudflare under Access to manage the records here.
      </Alert>
      <Panel title="Records to create at your DNS host">
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-end gap-1.5">
            <Field
              label="Name"
              hint="@ for the domain itself, a label like shop, or * for a wildcard record"
            >
              <div className="flex items-center gap-1">
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && setAsked(name)}
                  placeholder="@"
                  className="w-48 font-mono"
                  spellCheck={false}
                />
                <span className="text-faint font-mono text-xs whitespace-nowrap">
                  .{domain.name}
                </span>
              </div>
            </Field>
            <Button variant="primary" onClick={() => setAsked(name)}>
              Show the records
            </Button>
          </div>
          {shown.error && (
            <Alert>
              {errText(shown.error, "Could not work out the records")}
            </Alert>
          )}
          {records.length > 0 && (
            <div className="flex flex-col gap-1.5">
              {records.map((r) => (
                <div
                  key={r.type + r.content}
                  className="grid grid-cols-[3rem_1fr_1fr] items-center gap-2"
                >
                  <span className="bg-raised border-line rounded-sm border px-1.5 text-center font-mono text-xs">
                    {r.type}
                  </span>
                  <CopyBox value={r.name} />
                  <CopyBox value={r.content} />
                </div>
              ))}
              <p className="text-faint text-xs">
                A wildcard DNS record also answers a.b.{domain.name}, which a
                wildcard certificate does not cover (one label only).
              </p>
            </div>
          )}
        </div>
      </Panel>
    </div>
  );
}
