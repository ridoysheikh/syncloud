import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Clock, Copy as CopyIcon } from "lucide-react";
import { api } from "@/lib/api";
import { dbPath, useDatabases, type Database } from "@/lib/databases";
import { usePgBackups } from "@/lib/pg";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { ChoiceCards } from "@/ui/choice";
import { errText } from "./shared";
import { Select } from "@/ui/select";

const fmt = (iso?: string) => (iso ? new Date(iso).toLocaleString() : "—");

/** datetime-local value (local time) for an ISO instant, to the second. */
function toLocalInput(iso: string) {
  const d = new Date(iso);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/** Point-in-time restore into a new database (§13c): a full page. */
export function PgRestorePage() {
  const { name } = useParams({ strict: false }) as { name: string };
  const backupParam =
    new URLSearchParams(window.location.search).get("backup") ?? "";
  const path = dbPath(name);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data: all } = useDatabases();
  const src = all?.find((x) => x.name === name);
  const status = usePgBackups(path);
  const s = status.data;
  const [mode, setMode] = useState<"latest" | "time">("time");
  const [time, setTime] = useState("");
  const [backup, setBackup] = useState(backupParam);
  const [newName, setNewName] = useState(`${name}-restore`.slice(0, 29));
  const [placement, setPlacement] = useState<"same" | "standalone">("same");
  const [replicas, setReplicas] = useState(0);

  const window_ = s?.window;
  const target = mode === "time" && time ? new Date(time) : null;
  const outside =
    target &&
    window_ &&
    (target < new Date(window_.from) || target > new Date(window_.to));
  // The backup a restore would start from, as the server picks it.
  const startFrom = useMemo(() => {
    if (!s) return undefined;
    if (backup) return s.backups.find((b) => b.name === backup);
    return s.backups.find((b) => !target || new Date(b.finishTime) <= target);
  }, [s, backup, target]);

  const create = useMutation({
    mutationFn: () => {
      const d = src as Database;
      const spec = structuredClone(d.spec);
      spec.replicas = {
        min: replicas,
        max: Math.max(replicas, d.spec.replicas.max),
      };
      if (spec.postgres)
        spec.postgres.synchronous = spec.postgres.synchronous && replicas > 0;
      const body: Record<string, unknown> = {
        name: newName,
        engine: "postgres",
        spec,
        restore: {
          from: name,
          backup: backup || undefined,
          targetTime: target ? target.toISOString() : undefined,
        },
      };
      if (placement === "same" && !d.standalone) {
        body.project = d.project;
        body.environment = d.environment;
      }
      return api<Database>("POST", "/databases", body);
    },
    onSuccess: (d) => {
      void qc.invalidateQueries({ queryKey: ["databases"] });
      void navigate({
        to: `/databases/${encodeURIComponent(d.name)}` as string,
      });
    },
  });

  return (
    <div className={cn("mx-auto flex w-full max-w-4xl flex-col", gap)}>
      <PageHeader
        crumbs={[
          <Link to={"/databases" as string} className="hover:text-fg">
            Databases
          </Link>,
          <Link
            to={`/databases/${encodeURIComponent(name)}?tab=backups` as string}
            className="hover:text-fg"
          >
            {name}
          </Link>,
        ]}
        title="Restore to a new database"
      />
      {status.error && <Alert>{errText(status.error)}</Alert>}
      {s && !s.configured && (
        <Alert tone="warn">{name} has no backups configured.</Alert>
      )}
      {s && s.configured && s.backups.length === 0 && (
        <Alert tone="warn">
          {name} has no base backup yet; take one on its Backups tab first.
        </Alert>
      )}
      <form
        className={cn("flex flex-col", gap)}
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Panel title="Restore to">
          <div className="flex flex-col gap-3 text-xs">
            <p className="text-muted">
              {name} is not changed: the restore builds a new database from its
              base backup and replays the archived WAL up to the moment you
              choose.
              {window_ && (
                <>
                  {" "}
                  Any moment from <b>{fmt(window_.from)}</b> to{" "}
                  <b>{fmt(window_.to)}</b> can be restored.
                </>
              )}
            </p>
            <ChoiceCards
              label="Restore to"
              value={mode}
              onChange={setMode}
              options={[
                {
                  value: "time",
                  title: "A moment",
                  description:
                    "Replay the archive up to a point in time (your local time).",
                  icon: Clock,
                },
                {
                  value: "latest",
                  title: "The latest state",
                  description:
                    "Replay the whole archive: a clone of the database as it is now.",
                  icon: CopyIcon,
                },
              ]}
            />
            {mode === "time" && (
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  type="datetime-local"
                  step={1}
                  value={time}
                  min={window_ ? toLocalInput(window_.from) : undefined}
                  max={window_ ? toLocalInput(window_.to) : undefined}
                  onChange={(e) => setTime(e.target.value)}
                  className="w-60"
                  required
                />
                {window_ && (
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() =>
                      setTime(
                        toLocalInput(
                          new Date(
                            Date.parse(window_.to) - 60000,
                          ).toISOString(),
                        ),
                      )
                    }
                  >
                    A minute before the latest
                  </Button>
                )}
                {target && (
                  <span className="text-faint">= {target.toISOString()}</span>
                )}
              </div>
            )}
            {outside && (
              <Alert tone="warn">
                That moment is outside the restore window.
              </Alert>
            )}
            <Field label="Start from base backup">
              <Select
                value={backup}
                onChange={setBackup}
                className="w-full max-w-md"
              >
                <option value="">The newest one before the moment</option>
                {(s?.backups ?? []).map((b) => (
                  <option key={b.name} value={b.name}>
                    {b.name} — finished {fmt(b.finishTime)}
                  </option>
                ))}
              </Select>
            </Field>
            {startFrom && (
              <p className="text-faint">
                Starts from {startFrom.name} (finished{" "}
                {fmt(startFrom.finishTime)}) and replays the WAL archived after
                it.
              </p>
            )}
          </div>
        </Panel>
        <Panel title="New database">
          <div className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-3">
            <Field
              label="Name"
              hint="Its database inside keeps the source's name, and the source's users and passwords come along."
            >
              <Input
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                required
                maxLength={29}
                pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?"
              />
            </Field>
            <Field label="Where">
              <Select
                value={placement}
                onChange={(v) =>
                  setPlacement(v as typeof placement)
                }
                className="w-full"
              >
                {src && !src.standalone && (
                  <option value="same">
                    {src.project} / {src.environment}
                  </option>
                )}
                <option value="standalone">Standalone</option>
              </Select>
            </Field>
            <Field
              label="Read replicas"
              hint="Size and settings are copied from the source."
            >
              <Select
                value={replicas}
                onChange={(v) => setReplicas(Number(v))}
                className="w-full"
              >
                {[0, 1, 2, 3].map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <p className="text-faint mt-2 text-xs">
            It archives to the same bucket under its own prefix. Restoring takes
            as long as downloading the base backup and replaying the WAL.
          </p>
        </Panel>
        {create.error && <Alert>{errText(create.error)}</Alert>}
        <div className="flex items-center gap-2">
          <Button
            type="submit"
            variant="primary"
            disabled={
              create.isPending || !src || !s?.backups.length || !!outside
            }
          >
            Restore
          </Button>
          <Link
            to={`/databases/${encodeURIComponent(name)}?tab=backups` as string}
          >
            <Button type="button" variant="ghost">
              Cancel
            </Button>
          </Link>
        </div>
      </form>
    </div>
  );
}
