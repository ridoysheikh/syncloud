import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, Download, Play } from "lucide-react";
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

interface BackupObject {
  name: string;
  size: number;
  createdAt: string;
}

const empty: BackupConfig = { endpoint: "", region: "", bucket: "", prefix: "", accessKeyId: "", secretAccessKey: "", intervalMinutes: 60, retain: 48 };

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
  const [editing, setEditing] = useState(false);
  useEffect(() => {
    if (data?.config) setForm({ ...data.config, secretAccessKey: "" });
  }, [data?.config]);
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
    save.mutate(form);
  };
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
        Each backup holds a database snapshot, the internal CA and keys, encrypted with the master key. Restoring needs the recovery key shown at install:{" "}
        <code className="font-mono">syncloud-controller restore --s3-endpoint … --recovery-key …</code>
      </Alert>

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
                <Button variant="danger" onClick={() => confirm("Turn backups off? Stored backups are kept.") && disable.mutate()}>
                  Disable
                </Button>
              </>
            )
          }
        >
          {showForm ? (
            <form onSubmit={submit} className="grid grid-cols-1 gap-2 sm:grid-cols-2">
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
                <Field label="Secret access key" hint={data?.configured ? "Leave empty to keep the stored secret." : undefined}>
                  <Input type="password" value={form.secretAccessKey} onChange={set("secretAccessKey")} autoComplete="new-password" required={!data?.configured} />
                </Field>
              </div>
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
                <Button type="submit" variant="primary" disabled={save.isPending}>
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
              <dt className="text-muted">Endpoint</dt>
              <dd className="font-mono">{data?.config?.endpoint}</dd>
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
