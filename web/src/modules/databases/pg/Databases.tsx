import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Ban,
  Pencil,
  Plus,
  Power,
  RefreshCw,
  Search,
  Trash2,
} from "lucide-react";
import { api } from "@/lib/api";
import { bytes } from "@/lib/nodes";
import {
  usePgDatabases,
  usePgRoles,
  usePgSessions,
  type PgDatabase,
  type PgSession,
} from "@/lib/pg";
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
import { cn, gap } from "@/ui/cn";
import { selectClass } from "../NewDatabaseWizard";
import { duration, errText } from "./shared";
import { confirmAction } from "@/ui/dialogs";

/** The cluster's databases: list, create, rename, change owner, drop. */
export function PgDatabases({
  path,
  onExplore,
}: {
  path: string;
  onExplore: (db: string) => void;
}) {
  const qc = useQueryClient();
  const dbs = usePgDatabases(path);
  const roles = usePgRoles(path);
  const [form, setForm] = useState<{
    mode: "new" | "edit";
    of?: PgDatabase;
    name: string;
    owner: string;
    connLimit: string;
    comment: string;
  } | null>(null);
  const [dropping, setDropping] = useState<{
    name: string;
    typed: string;
  } | null>(null);
  const done = () => {
    setForm(null);
    setDropping(null);
    void qc.invalidateQueries({ queryKey: ["pg", path] });
  };
  const save = useMutation({
    mutationFn: () => {
      if (!form) return Promise.resolve();
      const body: Record<string, unknown> = {
        owner: form.owner,
        comment: form.comment,
      };
      if (form.connLimit.trim() !== "") body.connLimit = Number(form.connLimit);
      if (form.mode === "new")
        return api("POST", `${path}/pg/databases`, {
          ...body,
          name: form.name,
        });
      if (form.name !== form.of?.name) body.name = form.name;
      return api(
        "PUT",
        `${path}/pg/databases/${encodeURIComponent(form.of?.name ?? "")}`,
        body,
      );
    },
    onSuccess: done,
  });
  const drop = useMutation({
    mutationFn: (name: string) =>
      api("DELETE", `${path}/pg/databases/${encodeURIComponent(name)}`),
    onSuccess: done,
  });
  const owners = (roles.data ?? [])
    .filter((r) => !r.protected)
    .map((r) => r.name);

  return (
    <div className={cn("flex flex-col", gap)}>
      {form && (
        <Panel
          title={
            form.mode === "new" ? "New database" : `Change ${form.of?.name}`
          }
        >
          <form
            className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2 lg:grid-cols-4"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate();
            }}
          >
            <Field
              label="Name"
              hint={
                form.of?.primary
                  ? "The cluster's own database keeps its name: the credentials use it."
                  : undefined
              }
            >
              <Input
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                disabled={form.of?.primary}
                autoFocus
                required
                pattern="[A-Za-z_][A-Za-z0-9_.$\-]{0,62}"
              />
            </Field>
            <Field label="Owner">
              <select
                className={selectClass}
                value={form.owner}
                onChange={(e) => setForm({ ...form, owner: e.target.value })}
              >
                {owners.map((o) => (
                  <option key={o}>{o}</option>
                ))}
              </select>
            </Field>
            <Field label="Connection limit" hint="Empty or -1: no limit">
              <Input
                type="number"
                min={-1}
                value={form.connLimit}
                onChange={(e) =>
                  setForm({ ...form, connLimit: e.target.value })
                }
              />
            </Field>
            <Field label="Comment">
              <Input
                value={form.comment}
                onChange={(e) => setForm({ ...form, comment: e.target.value })}
              />
            </Field>
            <div className="flex items-center gap-2 sm:col-span-2 lg:col-span-4">
              <Button type="submit" variant="primary" disabled={save.isPending}>
                {form.mode === "new" ? "Create database" : "Save"}
              </Button>
              <Button
                type="button"
                variant="ghost"
                onClick={() => setForm(null)}
              >
                Cancel
              </Button>
              {form.mode === "edit" && form.name !== form.of?.name && (
                <span className="text-warn">
                  Renaming disconnects the database's sessions.
                </span>
              )}
            </div>
            {save.error && (
              <div className="sm:col-span-2 lg:col-span-4">
                <Alert>{errText(save.error)}</Alert>
              </div>
            )}
          </form>
        </Panel>
      )}
      {dropping && (
        <Panel title={`Drop ${dropping.name}`}>
          <div className="flex flex-col gap-2 text-xs">
            <Alert tone="warn">
              This deletes the database and everything in it, and disconnects
              its sessions. Backups (when configured) are the only way back.
            </Alert>
            <Field label={`Type ${dropping.name} to confirm`}>
              <Input
                value={dropping.typed}
                onChange={(e) =>
                  setDropping({ ...dropping, typed: e.target.value })
                }
                className="max-w-60 font-mono"
                autoFocus
              />
            </Field>
            <div className="flex gap-2">
              <Button
                variant="danger"
                disabled={dropping.typed !== dropping.name || drop.isPending}
                onClick={() => drop.mutate(dropping.name)}
              >
                Drop database
              </Button>
              <Button variant="ghost" onClick={() => setDropping(null)}>
                Cancel
              </Button>
            </div>
            {drop.error && <Alert>{errText(drop.error)}</Alert>}
          </div>
        </Panel>
      )}
      <Panel
        title="Databases"
        flush
        actions={
          <>
            <IconButton label="Reload" onClick={() => void dbs.refetch()}>
              <RefreshCw
                className={cn("size-3.5", dbs.isFetching && "animate-spin")}
              />
            </IconButton>
            <Button
              variant="primary"
              onClick={() =>
                setForm({
                  mode: "new",
                  name: "",
                  owner: "app",
                  connLimit: "",
                  comment: "",
                })
              }
            >
              <Plus className="size-3.5" /> New database
            </Button>
          </>
        }
      >
        {dbs.error && (
          <div className="p-2">
            <Alert>{errText(dbs.error)}</Alert>
          </div>
        )}
        <DataTable
          rows={dbs.data ?? []}
          rowKey={(d) => d.name}
          empty={<EmptyState icon={Search} title="No databases" />}
          columns={[
            {
              header: "Database",
              cell: (d) => (
                <button
                  type="button"
                  className="hover:text-accent font-mono"
                  onClick={() => onExplore(d.name)}
                >
                  {d.name}
                  {d.primary && (
                    <span className="text-faint ml-1 font-sans">
                      cluster database
                    </span>
                  )}
                  {d.protected && (
                    <span className="text-faint ml-1 font-sans">platform</span>
                  )}
                </button>
              ),
            },
            {
              header: "Owner",
              cell: (d) => <span className="font-mono">{d.owner}</span>,
            },
            { header: "Size", cell: (d) => bytes(d.sizeBytes) },
            {
              header: "Connections",
              cell: (d) =>
                `${d.connections}${d.connLimit >= 0 ? ` / ${d.connLimit}` : ""}`,
            },
            {
              header: "Encoding",
              cell: (d) => `${d.encoding} · ${d.collation}`,
            },
            {
              header: "Comment",
              className: "whitespace-normal",
              cell: (d) => <span className="text-muted">{d.comment}</span>,
            },
            {
              header: "",
              cell: (d) =>
                d.protected ? null : (
                  <span className="flex justify-end gap-0.5">
                    <Button variant="ghost" onClick={() => onExplore(d.name)}>
                      Explore
                    </Button>
                    <IconButton
                      label={`Change ${d.name}`}
                      onClick={() =>
                        setForm({
                          mode: "edit",
                          of: d,
                          name: d.name,
                          owner: d.owner,
                          connLimit:
                            d.connLimit >= 0 ? String(d.connLimit) : "",
                          comment: d.comment,
                        })
                      }
                    >
                      <Pencil className="size-3.5" />
                    </IconButton>
                    {!d.primary && (
                      <IconButton
                        label={`Drop ${d.name}`}
                        onClick={() => setDropping({ name: d.name, typed: "" })}
                      >
                        <Trash2 className="size-3.5" />
                      </IconButton>
                    )}
                  </span>
                ),
            },
          ]}
        />
      </Panel>
    </div>
  );
}

/** Client sessions, with cancel and terminate. */
export function PgSessions({ path }: { path: string }) {
  const [all, setAll] = useState(false);
  const sessions = usePgSessions(path, all);
  const [busy, setBusy] = useState<number | null>(null);
  const signal = useMutation({
    mutationFn: ({
      pid,
      verb,
    }: {
      pid: number;
      verb: "cancel" | "terminate";
    }) => api("POST", `${path}/pg/sessions/${pid}/${verb}`),
    onSettled: () => {
      setBusy(null);
      void sessions.refetch();
    },
  });
  const tone = (s: PgSession) =>
    s.state === "active"
      ? "ok"
      : s.state.startsWith("idle in transaction")
        ? "warn"
        : "neutral";
  return (
    <Panel
      title="Sessions"
      flush
      actions={
        <>
          <Toggle checked={all} onChange={setAll} label="Platform sessions" />
          <IconButton label="Reload" onClick={() => void sessions.refetch()}>
            <RefreshCw
              className={cn("size-3.5", sessions.isFetching && "animate-spin")}
            />
          </IconButton>
        </>
      }
    >
      {(sessions.error || signal.error) && (
        <div className="p-2">
          <Alert>{errText(sessions.error ?? signal.error)}</Alert>
        </div>
      )}
      <DataTable
        rows={sessions.data ?? []}
        rowKey={(s) => String(s.pid)}
        empty={
          <EmptyState icon={Search} title="No client sessions">
            Connections from apps and tools show up here, refreshed every 5
            seconds.
          </EmptyState>
        }
        columns={[
          {
            header: "PID",
            cell: (s) => <span className="font-mono">{s.pid}</span>,
          },
          {
            header: "User",
            cell: (s) => <span className="font-mono">{s.user}</span>,
          },
          { header: "Database", cell: (s) => s.database },
          {
            header: "Client",
            cell: (s) => <span title={s.application}>{s.client}</span>,
          },
          {
            header: "State",
            cell: (s) => (
              <StatusBadge tone={tone(s)}>{s.state || "—"}</StatusBadge>
            ),
          },
          {
            header: "For",
            cell: (s) =>
              duration(
                s.state === "active"
                  ? s.queryStart
                  : (s.xactStart ?? s.queryStart),
              ),
          },
          {
            header: "Waiting",
            cell: (s) => (
              <span className={cn(s.blockedBy?.length && "text-warn")}>
                {s.blockedBy?.length
                  ? `blocked by ${s.blockedBy.join(", ")}`
                  : s.waitEvent}
              </span>
            ),
          },
          {
            header: "Query",
            className: "max-w-[28rem] truncate",
            cell: (s) => (
              <span title={s.query} className="font-mono">
                {s.query}
              </span>
            ),
          },
          {
            header: "",
            cell: (s) =>
              s.platform ? null : (
                <span className="flex justify-end gap-0.5">
                  <IconButton
                    label={`Cancel the query of ${s.pid}`}
                    disabled={busy === s.pid || s.state !== "active"}
                    onClick={() => (
                      setBusy(s.pid),
                      signal.mutate({ pid: s.pid, verb: "cancel" })
                    )}
                  >
                    <Ban className="size-3.5" />
                  </IconButton>
                  <IconButton
                    label={`End session ${s.pid}`}
                    disabled={busy === s.pid}
                    onClick={async () => {
                      if (
                        await confirmAction(
                          `End session ${s.pid} (${s.user})? Its open transaction is rolled back.`,
                        )
                      ) {
                        setBusy(s.pid);
                        signal.mutate({ pid: s.pid, verb: "terminate" });
                      }
                    }}
                  >
                    <Power className="size-3.5" />
                  </IconButton>
                </span>
              ),
          },
        ]}
      />
    </Panel>
  );
}
