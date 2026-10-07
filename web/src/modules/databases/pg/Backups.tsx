import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Archive, History, Play, RotateCcw, Settings2 } from "lucide-react";
import { api } from "@/lib/api";
import { bytes } from "@/lib/nodes";
import type { Database, PgBackupSpec } from "@/lib/databases";
import {
  useS3Buckets,
  useS3Endpoints,
  usePgBackups,
  type PgBackupRun,
} from "@/lib/pg";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { selectClass } from "../NewDatabaseWizard";
import { ago, errText } from "./shared";
import { confirmAction } from "@/ui/dialogs";

const restoreTo = (name: string, backup?: string) =>
  `/databases/${encodeURIComponent(name)}/restore${backup ? `?backup=${encodeURIComponent(backup)}` : ""}`;

const fmt = (iso?: string | null) =>
  iso ? new Date(iso).toLocaleString() : "—";

/** WAL archiving, base backups and point-in-time restore (Phase 13c). */
/** A duration as minutes, hours or days. */
function span(ms: number) {
  const min = Math.max(0, Math.round(ms / 60000));
  if (min < 60) return `${min}m`;
  if (min < 48 * 60) return `${Math.round(min / 60)}h`;
  return `${Math.round(min / 1440)}d`;
}

export function PgBackupsTab({ d, path }: { d: Database; path: string }) {
  const qc = useQueryClient();
  const status = usePgBackups(path);
  const [editing, setEditing] = useState(false);
  const s = status.data;
  const now = useMutation({
    mutationFn: () => api("POST", `${path}/backups`),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ["pg", path, "backups"] }),
  });
  const running = s?.runs.find((r) => r.state === "running");
  const lastOk = s?.runs.find((r) => r.state === "ok");
  const failing =
    s?.archiver &&
    s.archiver.failedCount > 0 &&
    s.archiver.lastFailedAt &&
    (!s.archiver.lastArchivedAt ||
      s.archiver.lastFailedAt > s.archiver.lastArchivedAt);

  if (status.error) return <Alert>{errText(status.error)}</Alert>;
  if (!s) return <p className="text-faint text-xs">Loading…</p>;
  if (!s.configured || editing) {
    return (
      <div className={cn("flex flex-col", gap)}>
        {s.restoredFrom && <RestoredFrom s={s.restoredFrom} />}
        <BackupSettings
          d={d}
          path={path}
          current={s.config}
          onDone={() => setEditing(false)}
        />
      </div>
    );
  }
  return (
    <div className={cn("flex flex-col", gap)}>
      {s.restoredFrom && <RestoredFrom s={s.restoredFrom} />}
      {s.error && <Alert>The archive cannot be read: {s.error}</Alert>}
      {failing && (
        <Alert>
          Archiving WAL is failing ({s.archiver?.failedCount} failures, last{" "}
          {ago(s.archiver?.lastFailedAt)}). Until it works, WAL piles up on the
          members and a restore cannot reach past{" "}
          {fmt(s.archiver?.lastArchivedAt)}. Check the endpoint's credentials
          and the database's logs.
        </Alert>
      )}
      <div className={cn("grid grid-cols-2 lg:grid-cols-4", gap)}>
        <StatTile
          label="Last base backup"
          value={s.backups[0] ? ago(s.backups[0].finishTime) : "none yet"}
          hint={
            running
              ? `running on ${running.member}…`
              : s.nextRun
                ? `next ${fmt(s.nextRun)}`
                : `every ${s.config?.everyHours}h`
          }
        />
        <StatTile
          label="WAL archived"
          value={
            s.archiver?.lastArchivedAt ? ago(s.archiver.lastArchivedAt) : "—"
          }
          hint={
            s.archiver
              ? `${s.archiver.archivedCount} segments since the primary started`
              : "primary not reachable"
          }
          tone={failing ? "bad" : undefined}
        />
        <StatTile
          label="Restore window"
          value={
            s.window
              ? span(Date.parse(s.window.to) - Date.parse(s.window.from))
              : "—"
          }
          hint={
            s.window
              ? `${fmt(s.window.from)} → ${fmt(s.window.to)}`
              : "after the first base backup"
          }
        />
        <StatTile
          label="Stored"
          value={bytes(s.backups.reduce((n, b) => n + b.compressedSize, 0))}
          hint={`${s.backups.length} base backups, keep ${s.config?.retainFull} and ${s.config?.retainDays} days`}
        />
      </div>
      <Panel
        title="Base backups"
        flush
        actions={
          <>
            <Button variant="ghost" onClick={() => setEditing(true)}>
              <Settings2 className="size-3.5" /> Settings
            </Button>
            <Button
              disabled={!!running || now.isPending}
              onClick={() => now.mutate()}
            >
              <Play className="size-3.5" /> Back up now
            </Button>
            <Link to={restoreTo(d.name)}>
              <Button variant="primary" disabled={!s.backups.length}>
                <RotateCcw className="size-3.5" /> Restore…
              </Button>
            </Link>
          </>
        }
      >
        {now.error && (
          <div className="p-2">
            <Alert>{errText(now.error)}</Alert>
          </div>
        )}
        <div className="text-faint border-line border-b px-3 py-1.5 font-mono text-xs">
          {s.location}
        </div>
        <DataTable
          rows={s.backups}
          rowKey={(b) => b.name}
          empty={
            <EmptyState icon={Archive} title="No base backup yet">
              {running
                ? `The first one is running on ${running.member}.`
                : "The first one starts within a minute; or take one now."}
            </EmptyState>
          }
          columns={[
            {
              header: "Backup",
              cell: (b) => <span className="font-mono">{b.name}</span>,
            },
            {
              header: "Finished",
              cell: (b) => (
                <span title={b.finishTime}>{fmt(b.finishTime)}</span>
              ),
            },
            {
              header: "Took",
              cell: (b) =>
                `${Math.max(1, Math.round((Date.parse(b.finishTime) - Date.parse(b.startTime)) / 1000))}s`,
            },
            {
              header: "Size",
              cell: (b) =>
                `${bytes(b.compressedSize)} (${bytes(b.uncompressedSize)} raw)`,
            },
            {
              header: "WAL",
              cell: (b) => (
                <span className="font-mono">
                  {b.startLsn} → {b.finishLsn}
                </span>
              ),
            },
            {
              header: "",
              cell: (b) => (
                <Link to={restoreTo(d.name, b.name)}>
                  <Button variant="ghost">Restore</Button>
                </Link>
              ),
            },
          ]}
        />
      </Panel>
      <Runs runs={s.runs} lastOk={lastOk} />
    </div>
  );
}

function RestoredFrom({
  s,
}: {
  s: NonNullable<ReturnType<typeof usePgBackups>["data"]>["restoredFrom"];
}) {
  if (!s) return null;
  return (
    <Alert tone="info">
      Restored from <span className="font-mono">{s.sourceName}</span>, base
      backup <span className="font-mono">{s.backup}</span>, to{" "}
      {s.targetTime ? fmt(s.targetTime) : "the end of its archive"}.
    </Alert>
  );
}

function Runs({ runs, lastOk }: { runs: PgBackupRun[]; lastOk?: PgBackupRun }) {
  const tone = (r: PgBackupRun) =>
    r.state === "ok" ? "ok" : r.state === "running" ? "info" : "bad";
  return (
    <Panel title="Runs" flush>
      <DataTable
        rows={runs}
        rowKey={(r) => r.id}
        empty={<EmptyState icon={History} title="No runs yet" />}
        columns={[
          { header: "Started", cell: (r) => fmt(r.startedAt) },
          { header: "Trigger", cell: (r) => r.trigger },
          {
            header: "From",
            cell: (r) => <span className="font-mono">{r.member}</span>,
          },
          {
            header: "State",
            cell: (r) => <StatusBadge tone={tone(r)}>{r.state}</StatusBadge>,
          },
          {
            header: "Took",
            cell: (r) =>
              r.finishedAt
                ? `${Math.max(1, Math.round((Date.parse(r.finishedAt) - Date.parse(r.startedAt)) / 1000))}s`
                : "",
          },
          {
            header: "Error",
            className: "whitespace-normal",
            cell: (r) => <span className="text-bad">{r.error}</span>,
          },
        ]}
      />
      {lastOk === undefined && runs.length > 0 && (
        <p className="text-faint px-3 py-2 text-xs">
          No run has succeeded yet; the database's logs show WAL-G's output.
        </p>
      )}
    </Panel>
  );
}

/** Where and how often: the backup settings (saving restarts members one at a time). */
function BackupSettings({
  d,
  path,
  current,
  onDone,
}: {
  d: Database;
  path: string;
  current?: PgBackupSpec;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const endpoints = useS3Endpoints();
  const [f, setF] = useState<PgBackupSpec>(
    current ?? {
      endpoint: "",
      bucket: "",
      prefix: "",
      everyHours: 24,
      retainFull: 7,
      retainDays: 7,
    },
  );
  const buckets = useS3Buckets(f.endpoint);
  const set = <K extends keyof PgBackupSpec>(k: K, v: PgBackupSpec[K]) =>
    setF((x) => ({ ...x, [k]: v }));
  const save = useMutation({
    mutationFn: (backup: PgBackupSpec | null) => {
      const spec = structuredClone(d.spec);
      spec.postgres = { ...spec.postgres!, backup: backup ?? undefined };
      return api("PUT", path, { spec });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["databases"] });
      void qc.invalidateQueries({ queryKey: ["pg", path, "backups"] });
      onDone();
    },
  });
  const noEndpoints = endpoints.data && endpoints.data.length === 0;
  return (
    <Panel title={current ? "Backup settings" : "Backups are off"}>
      <form
        className="flex flex-col gap-3 text-xs"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate({ ...f, prefix: f.prefix?.trim() || undefined });
        }}
      >
        <p className="text-muted">
          WAL-G archives every change (WAL) to S3 as it happens, at least once a
          minute, and takes a full base backup on a schedule. Together they let
          you restore to any moment in the window into a new database. Backups
          are encrypted with a key only this cluster's members hold.
        </p>
        {noEndpoints && (
          <Alert tone="warn">
            No S3 endpoint is registered yet.{" "}
            <Link to={"/storage/endpoints/new" as string} className="underline">
              Add one under Storage
            </Link>{" "}
            (AWS S3, R2, B2, MinIO, …).
          </Alert>
        )}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <Field label="S3 endpoint">
            <select
              className={selectClass}
              value={f.endpoint}
              onChange={(e) =>
                setF({ ...f, endpoint: e.target.value, bucket: "" })
              }
              required
            >
              <option value="">choose…</option>
              {(endpoints.data ?? []).map((e) => (
                <option key={e.id} value={e.name}>
                  {e.name} — {e.url}
                </option>
              ))}
            </select>
          </Field>
          <Field
            label="Bucket"
            hint={
              buckets.error
                ? errText(buckets.error)
                : "Create buckets under Storage."
            }
          >
            <select
              className={selectClass}
              value={f.bucket}
              onChange={(e) => set("bucket", e.target.value)}
              required
              disabled={!f.endpoint}
            >
              <option value="">choose…</option>
              {(buckets.data ?? []).map((b) => (
                <option key={b.name}>{b.name}</option>
              ))}
            </select>
          </Field>
          <Field label="Prefix" hint={`Default syncloud-pg/${d.id}`}>
            <Input
              value={f.prefix ?? ""}
              onChange={(e) => set("prefix", e.target.value)}
              placeholder="optional"
              className="font-mono"
            />
          </Field>
          <Field label="Base backup every (hours)">
            <Input
              type="number"
              min={1}
              max={168}
              value={f.everyHours}
              onChange={(e) => set("everyHours", Number(e.target.value))}
            />
          </Field>
          <Field label="Keep base backups">
            <Input
              type="number"
              min={1}
              max={100}
              value={f.retainFull}
              onChange={(e) => set("retainFull", Number(e.target.value))}
            />
          </Field>
          <Field
            label="And everything from the last (days)"
            hint="How far back a restore can go."
          >
            <Input
              type="number"
              min={0}
              max={365}
              value={f.retainDays}
              onChange={(e) => set("retainDays", Number(e.target.value))}
            />
          </Field>
        </div>
        {save.error && <Alert>{errText(save.error)}</Alert>}
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="submit"
            variant="primary"
            disabled={save.isPending || !f.endpoint || !f.bucket}
          >
            {current ? "Save" : "Turn on backups"}
          </Button>
          {current && (
            <>
              <Button type="button" variant="ghost" onClick={onDone}>
                Cancel
              </Button>
              <span className="flex-1" />
              <Button
                type="button"
                variant="danger"
                disabled={save.isPending}
                onClick={async () => {
                  if (
                    await confirmAction(
                      "Turn backups off? Archiving stops and no restore past this moment will be possible. What is already in S3 stays.",
                    )
                  )
                    save.mutate(null);
                }}
              >
                Turn off
              </Button>
            </>
          )}
          <span className="text-faint">
            Members restart one at a time to apply this (a switchover of a few
            seconds).
          </span>
        </div>
      </form>
    </Panel>
  );
}
