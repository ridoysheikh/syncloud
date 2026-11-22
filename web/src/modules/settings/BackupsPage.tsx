import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, Cloud, Download, KeyRound, Play } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { subscribe } from "@/lib/stream";
import { backupQuery, type BackupConfig, type BackupStatus } from "@/lib/backups";
import { since } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { confirmAction } from "@/ui/dialogs";
import { ChoiceCards } from "@/ui/choice";
import { Select } from "@/ui/select";
import { useS3Buckets, useS3Endpoints } from "@/lib/pg";
import { RecoveryKeyPanel } from "./RecoveryKeyPanel";

interface BackupObject {
  name: string;
  size: number;
  createdAt: string;
}

const empty: BackupConfig = { endpointId: "", endpoint: "", region: "", bucket: "", prefix: "", accessKeyId: "", secretAccessKey: "", intervalMinutes: 60, retain: 48 };

type Source = "connection" | "keys";

function size(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / (1 << 20)).toFixed(1)} MiB`;
}

/** Encrypted controller backups to S3 (§13). */
export function BackupsPage() {
  const qc = useQueryClient();
  const { data } = useQuery(backupQuery);
  const objects = useQuery({
    queryKey: ["backups", "list"],
    queryFn: async () => (await api<{ items: BackupObject[] }>("GET", "/backups")).items,
    enabled: !!data?.configured,
  });
  useEffect(
    () =>
      subscribe("backup.updated", (e) => {
        qc.setQueryData(backupQuery.queryKey, (prev) => prev && { ...prev, status: e.data as BackupStatus });
        qc.invalidateQueries({ queryKey: ["backups", "list"] });
      }),
    [qc],
  );

  const [form, setForm] = useState<BackupConfig>(empty);
  const [source, setSource] = useState<Source>("connection");
  const [editing, setEditing] = useState(false);
  const endpoints = useS3Endpoints();
  const buckets = useS3Buckets(source === "connection" ? (form.endpointId ?? "") : "");
  useEffect(() => {
    if (data?.config) {
      setForm({ ...data.config, endpointId: data.config.endpointId ?? "", secretAccessKey: "" });
      setSource(data.config.endpointId ? "connection" : "keys");
    }
  }, [data?.config]);
  useEffect(() => {
    if (!data?.config && endpoints.data?.length === 0) setSource("keys");
  }, [data?.config, endpoints.data]);
  const save = useMutation({
    mutationFn: (c: BackupConfig) => api("PUT", "/backups/config", c),
    onSuccess: () => {
      setEditing(false);
      qc.invalidateQueries({ queryKey: ["backups"] });
    },
  });
  const disable = useMutation({
    mutationFn: () => api("DELETE", "/backups/config"),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["backups"] }),
  });
  const run = useMutation({
    mutationFn: () => api<BackupObject>("POST", "/backups"),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["backups"] }),
  });
  const set = (k: keyof BackupConfig) => (e: { target: { value: string } }) =>
    setForm({ ...form, [k]: k === "intervalMinutes" || k === "retain" ? Number(e.target.value) : e.target.value });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (source === "connection") {
      const { endpointId, bucket, prefix, intervalMinutes, retain } = form;
      save.mutate({ endpointId, bucket, prefix, intervalMinutes, retain, endpoint: "", region: "", accessKeyId: "" });
    } else save.mutate({ ...form, endpointId: "" });
  };
  const inlineSaved = !!data?.configured && !data.config?.endpointId;
  const showForm = !data?.configured || editing;
  const st = data?.status;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Settings"]}
        title="Backups"
        status={
          data &&
          (data.configured ? (
            <StatusBadge tone={st?.lastError ? "bad" : "ok"}>{st?.lastError ? "failing" : "on"}</StatusBadge>
          ) : (
            <StatusBadge tone="warn">off</StatusBadge>
          ))
        }
        actions={
          <Button onClick={() => (window.location.href = "/api/v1/backups/download")} title="A fresh encrypted bundle; works without S3">
            <Download className="size-3.5" /> Download now
          </Button>
        }
      />
      <Alert tone="info">
        Each backup holds a database snapshot, the internal CA and keys, encrypted with the master key. Restoring needs the recovery key below:{" "}
        <code className="font-mono">syncloud-controller restore --s3-endpoint … --recovery-key …</code>
      </Alert>

      <RecoveryKeyPanel />

      <div className={cn("grid lg:grid-cols-2", gap)}>
        <Panel
          title="S3 destination"
          actions={
            data?.configured &&
            !editing && (
              <>
                <Button variant="ghost" onClick={() => setEditing(true)}>
                  Edit
                </Button>
                <Button variant="danger" onClick={async () => (await confirmAction("Turn backups off? Stored backups are kept.")) && disable.mutate()}>
                  Disable
                </Button>
              </>
            )
          }
        >
          {showForm ? (
            <form onSubmit={submit} className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              <div className="sm:col-span-2">
                <ChoiceCards
                  label="Credentials"
                  value={source}
                  onChange={(v) => setSource(v)}
                  options={[
                    {
                      value: "connection",
                      title: "S3 connection",
                      icon: Cloud,
                      description: "A connection registered under Storage. Its keys are read at every backup, so a key rotated there applies here at once.",
                    },
                    {
                      value: "keys",
                      title: "Keys entered here",
                      icon: KeyRound,
                      description: "An endpoint and keys only for backups. A rotated key must be entered here again.",
                    },
                  ]}
                />
              </div>
              {source === "connection" ? (
                <>
                  <Field label="S3 connection">
                    <Select value={form.endpointId ?? ""} onChange={(v) => setForm({ ...form, endpointId: v, bucket: "" })} className="w-full">
                      <option value="">Choose…</option>
                      {(endpoints.data ?? []).map((e) => (
                        <option key={e.id} value={e.id}>
                          {e.name} — {e.url}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <Field label="Bucket">
                    <Select value={form.bucket} onChange={(v) => setForm({ ...form, bucket: v })} className="w-full">
                      <option value="">Choose…</option>
                      {form.bucket && !(buckets.data ?? []).some((b) => b.name === form.bucket) && <option value={form.bucket}>{form.bucket}</option>}
                      {(buckets.data ?? []).map((b) => (
                        <option key={b.name} value={b.name}>
                          {b.name}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <Field label="Prefix">
                    <Input value={form.prefix} onChange={set("prefix")} placeholder="syncloud/prod" className="font-mono" />
                  </Field>
                  {endpoints.data && endpoints.data.length === 0 && (
                    <div className="sm:col-span-2">
                      <Alert tone="warn">No S3 connection is registered yet. Add one under Storage, or enter keys here.</Alert>
                    </div>
                  )}
                </>
              ) : (
                <>
                  <div className="sm:col-span-2">
                    <Field label="Endpoint" hint="AWS, Cloudflare R2, Backblaze B2, MinIO, SeaweedFS… (http only on a private network)">
                      <Input value={form.endpoint} onChange={set("endpoint")} placeholder="https://s3.eu-central-1.amazonaws.com" className="font-mono" required />
                    </Field>
                  </div>
                  <Field label="Bucket">
                    <Input value={form.bucket} onChange={set("bucket")} className="font-mono" required />
                  </Field>
                  <Field label="Region">
                    <Input value={form.region} onChange={set("region")} placeholder="us-east-1" className="font-mono" />
                  </Field>
                  <Field label="Prefix">
                    <Input value={form.prefix} onChange={set("prefix")} placeholder="syncloud/prod" className="font-mono" />
                  </Field>
                  <Field label="Access key ID">
                    <Input value={form.accessKeyId} onChange={set("accessKeyId")} className="font-mono" autoComplete="off" required />
                  </Field>
                  <div className="sm:col-span-2">
                    <Field label="Secret access key" hint={inlineSaved ? "Leave empty to keep the stored secret." : undefined}>
                      <Input type="password" value={form.secretAccessKey} onChange={set("secretAccessKey")} autoComplete="new-password" required={!inlineSaved} />
                    </Field>
                  </div>
                </>
              )}
              <Field label="Every (minutes)">
                <Input type="number" min={5} value={form.intervalMinutes} onChange={set("intervalMinutes")} />
              </Field>
              <Field label="Keep (backups)">
                <Input type="number" min={1} max={1000} value={form.retain} onChange={set("retain")} />
              </Field>
              {save.error && (
                <div className="sm:col-span-2">
                  <Alert>{save.error instanceof ApiError ? save.error.message : "Could not save"}</Alert>
                </div>
              )}
              <div className="flex gap-1.5 sm:col-span-2">
                <Button type="submit" variant="primary" disabled={save.isPending || (source === "connection" && (!form.endpointId || !form.bucket))}>
                  {save.isPending ? "Checking bucket…" : "Save and back up"}
                </Button>
                {editing && (
                  <Button type="button" variant="ghost" onClick={() => setEditing(false)}>
                    Cancel
                  </Button>
                )}
              </div>
            </form>
          ) : (
            <dl className="grid grid-cols-[8rem_1fr] gap-y-1.5 text-xs">
              <dt className="text-muted">{data?.config?.endpointId ? "S3 connection" : "Endpoint"}</dt>
              <dd className="font-mono">
                {data?.config?.endpointId ? `${data.config.endpointName || data.config.endpointId} — ${data.config.endpoint}` : data?.config?.endpoint}
              </dd>
              <dt className="text-muted">Keys</dt>
              <dd>
                {data?.config?.endpointId ? (
                  "the connection's, read at every backup"
                ) : (
                  <span className="text-muted">entered here (a rotated key must be entered again)</span>
                )}
              </dd>
              <dt className="text-muted">Bucket</dt>
              <dd className="font-mono">
                {data?.config?.bucket}
                {data?.config?.prefix && `/${data.config.prefix}`}
              </dd>
              <dt className="text-muted">Schedule</dt>
              <dd>
                every {data?.config?.intervalMinutes} min, keep {data?.config?.retain}
              </dd>
            </dl>
          )}
        </Panel>

        <Panel
          title="Last run"
          actions={
            data?.configured && (
              <Button onClick={() => run.mutate()} disabled={run.isPending}>
                <Play className="size-3.5" /> {run.isPending ? "Backing up…" : "Back up now"}
              </Button>
            )
          }
        >
          <dl className="grid grid-cols-[8rem_1fr] gap-y-1.5 text-xs">
            <dt className="text-muted">Last success</dt>
            <dd>{st?.lastSuccessAt ? since(st.lastSuccessAt) : <span className="text-faint">never</span>}</dd>
            <dt className="text-muted">Object</dt>
            <dd className="font-mono">{st?.lastObject || "—"}</dd>
            <dt className="text-muted">Size</dt>
            <dd>{st?.lastSize ? size(st.lastSize) : "—"}</dd>
            <dt className="text-muted">Last error</dt>
            <dd className={st?.lastError ? "text-bad" : "text-faint"}>{st?.lastError || "none"}</dd>
          </dl>
          {run.error && <Alert>{run.error instanceof ApiError ? run.error.message : "Backup failed"}</Alert>}
        </Panel>
      </div>

      {data?.configured && (
        <Panel title="Stored backups" flush>
          <DataTable
            rows={objects.data ?? []}
            rowKey={(o) => o.name}
            empty={!objects.isLoading && <EmptyState icon={Archive} title="No backups yet" />}
            columns={[
              { header: "Name", cell: (o) => <span className="font-mono">{o.name}</span>, className: "w-full" },
              { header: "Size", cell: (o) => size(o.size) },
              { header: "Created", cell: (o) => <span title={new Date(o.createdAt).toLocaleString()}>{since(o.createdAt)}</span> },
            ]}
          />
        </Panel>
      )}
    </div>
  );
}
