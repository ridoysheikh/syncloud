import { useMemo, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Clock, Copy as CopyIcon, Search } from "lucide-react";
import { api } from "@/lib/api";
import { type Database, type DatabaseSpec } from "@/lib/databases";
import { useS3Buckets, useS3Endpoints, type PgBaseBackup } from "@/lib/pg";
import { useProjects } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { ChoiceCards, Segmented } from "@/ui/choice";
import { Combobox, Select } from "@/ui/select";
import { errText } from "./shared";

const fmt = (iso?: string) => (iso ? new Date(iso).toLocaleString() : "—");
const nameRE = "[a-z0-9]([a-z0-9-]*[a-z0-9])?";
/** Member sizes offered (MiB). */
const SIZES = [256, 512, 1024, 2048, 4096, 8192, 16384];

/** What POST /databases/pg-archive reports. */
interface ArchiveInfo {
  location: string;
  backups: (PgBaseBackup & { pgVersion?: number })[];
  window?: { from: string; to: string };
  version?: string;
  encrypted?: boolean;
}

type KeyTransform = "none" | "hex" | "base64";

/**
 * Restore a WAL-G archive another PostgreSQL server wrote into a new
 * managed database: a full page. The new database gets SynCloud's own
 * passwords; the source archive is only read.
 */
export function PgArchiveRestorePage() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const endpoints = useS3Endpoints();
  const { data: projects = [] } = useProjects();
  const [endpoint, setEndpoint] = useState("");
  const [bucket, setBucket] = useState("");
  const [prefix, setPrefix] = useState("");
  const buckets = useS3Buckets(endpoint);
  const [key, setKey] = useState("");
  const [transform, setTransform] = useState<KeyTransform>("none");
  const [mode, setMode] = useState<"latest" | "time">("latest");
  const [time, setTime] = useState("");
  const [backup, setBackup] = useState("");
  const [name, setName] = useState("");
  const [database, setDatabase] = useState("");
  const [placement, setPlacement] = useState<"project" | "standalone">(
    "project",
  );
  const [projectSel, setProject] = useState("");
  const [envSel, setEnv] = useState("");
  const [memory, setMemory] = useState(1024);
  const [replicas, setReplicas] = useState(0);

  const project = projectSel || projects[0]?.name || "";
  const envs = projects.find((p) => p.name === project)?.environments ?? [];
  const env = envs.includes(envSel) ? envSel : (envs[0] ?? "");
  const inProject = placement === "project" && project !== "" && env !== "";

  const archive = { endpoint, bucket, prefix: prefix.trim() };
  const inspect = useMutation({
    mutationFn: () =>
      api<ArchiveInfo>("POST", "/databases/pg-archive", archive),
  });
  const info = inspect.data;
  // Reading again is needed once the location changes.
  const [readAt, setReadAt] = useState("");
  const here = `${endpoint}|${bucket}|${archive.prefix}`;
  const current = info && readAt === here ? info : undefined;

  const target = mode === "time" && time ? new Date(time) : null;
  const w = current?.window;
  const outside =
    target && w && (target < new Date(w.from) || target > new Date(w.to));
  const startFrom = useMemo(() => {
    if (!current) return undefined;
    if (backup) return current.backups.find((b) => b.name === backup);
    return current.backups.find(
      (b) => !target || new Date(b.finishTime) <= target,
    );
  }, [current, backup, target]);
  const needsKey = current?.encrypted === true && key.trim() === "";

  const create = useMutation({
    mutationFn: () => {
      const spec: Partial<DatabaseSpec> = {
        memory: { min: memory, max: memory },
        replicas: { min: replicas, max: replicas },
        cpu: 0.5,
        autoscaling: { cpuTarget: 60, memoryHigh: 85 },
        postgres: {
          maxConnections: 0,
          synchronous: false,
        } as DatabaseSpec["postgres"],
      };
      const body: Record<string, unknown> = {
        name,
        engine: "postgres",
        spec,
        restore: {
          archive: {
            ...archive,
            key: key.trim() || undefined,
            keyTransform: transform,
            database: database.trim() || undefined,
          },
          backup: backup || undefined,
          targetTime: target ? target.toISOString() : undefined,
        },
      };
      if (inProject) {
        body.project = project;
        body.environment = env;
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

  const noEndpoints = endpoints.data && endpoints.data.length === 0;
  return (
    <div className={cn("mx-auto flex w-full max-w-4xl flex-col", gap)}>
      <PageHeader
        crumbs={[
          <Link to={"/databases" as string} className="hover:text-fg">
            Databases
          </Link>,
        ]}
        title="Restore from another server"
      />
      <form
        className={cn("flex flex-col", gap)}
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Panel title="Archive">
          <div className="flex flex-col gap-3 text-xs">
            <p className="text-muted">
              A WAL-G archive that a PostgreSQL server outside SynCloud wrote to
              S3: its base backups and archived WAL. SynCloud only reads it. The
              new database gets SynCloud's own superuser, replication and app
              passwords, and archives under its own prefix.
            </p>
            {noEndpoints && (
              <Alert tone="warn">
                No S3 endpoint is registered yet.{" "}
                <Link
                  to={"/storage/endpoints/new" as string}
                  className="underline"
                >
                  Add the one the archive is on
                </Link>{" "}
                under Storage.
              </Alert>
            )}
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <Field label="S3 endpoint">
                <Select
                  value={endpoint}
                  onChange={(v) => {
                    setEndpoint(v);
                    setBucket("");
                  }}
                  className="w-full"
                >
                  <option value="">choose…</option>
                  {(endpoints.data ?? []).map((e) => (
                    <option key={e.id} value={e.name}>
                      {e.name} — {e.url}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label="Bucket"
                hint={buckets.error ? errText(buckets.error) : undefined}
              >
                <Combobox
                  value={bucket}
                  onChange={setBucket}
                  suggestions={(buckets.data ?? []).map((b) => b.name)}
                  disabled={!endpoint}
                  placeholder="bucket"
                  className="w-full font-mono"
                />
              </Field>
              <Field
                label="Prefix"
                hint="The server's WALG_S3_PREFIX after s3://bucket/"
              >
                <Input
                  value={prefix}
                  onChange={(e) => setPrefix(e.target.value)}
                  placeholder="walg/main"
                  className="font-mono"
                />
              </Field>
            </div>
            <div>
              <Button
                type="button"
                disabled={!endpoint || !bucket || inspect.isPending}
                onClick={() => {
                  setReadAt(here);
                  setBackup("");
                  inspect.mutate();
                }}
              >
                <Search className="size-3.5" />
                {inspect.isPending ? "Reading…" : "Read archive"}
              </Button>
            </div>
            {inspect.error && readAt === here && (
              <Alert>{errText(inspect.error)}</Alert>
            )}
            {current && current.backups.length === 0 && (
              <Alert tone="warn">
                No WAL-G base backup in {current.location}: check the prefix (it
                holds basebackups_005/ and wal_005/).
              </Alert>
            )}
            {current && current.backups.length > 0 && (
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                <Fact label="PostgreSQL">{current.version ?? "unknown"}</Fact>
                <Fact label="Encrypted">
                  {current.encrypted === undefined
                    ? "can't tell"
                    : current.encrypted
                      ? "yes"
                      : "no"}
                </Fact>
                <Fact label="Base backups">{current.backups.length}</Fact>
                <Fact label="Restorable">
                  {w ? `${fmt(w.from)} → ${fmt(w.to)}` : "—"}
                </Fact>
              </div>
            )}
          </div>
        </Panel>

        {current && current.backups.length > 0 && (
          <>
            {current.encrypted !== false && (
              <Panel title="Decryption">
                <div className="flex flex-col gap-3 text-xs">
                  <p className="text-muted">
                    {current.encrypted
                      ? "The archive is encrypted with libsodium. Give the source server's key, as it is set there."
                      : "If the archive is encrypted, give the source server's key, as it is set there."}{" "}
                    It is stored sealed with the new database's secrets.
                  </p>
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-[1fr_auto]">
                    <Field label="WALG_LIBSODIUM_KEY">
                      <Input
                        type="password"
                        autoComplete="off"
                        value={key}
                        onChange={(e) => setKey(e.target.value)}
                        className="font-mono"
                      />
                    </Field>
                    <Field label="WALG_LIBSODIUM_KEY_TRANSFORM">
                      <Segmented
                        label="Key transform"
                        value={transform}
                        onChange={setTransform}
                        options={[
                          { value: "none", label: "none" },
                          { value: "hex", label: "hex" },
                          { value: "base64", label: "base64" },
                        ]}
                      />
                    </Field>
                  </div>
                </div>
              </Panel>
            )}

            <Panel title="Restore to">
              <div className="flex flex-col gap-3 text-xs">
                <ChoiceCards
                  label="Restore to"
                  value={mode}
                  onChange={setMode}
                  options={[
                    {
                      value: "latest",
                      title: "The latest state",
                      description:
                        "Replay the whole archive: the server as it was at its last archived WAL.",
                      icon: CopyIcon,
                    },
                    {
                      value: "time",
                      title: "A moment",
                      description:
                        "Replay the archive up to a point in time (your local time).",
                      icon: Clock,
                    },
                  ]}
                />
                {mode === "time" && (
                  <div className="flex flex-wrap items-center gap-2">
                    <Input
                      type="datetime-local"
                      step={1}
                      value={time}
                      onChange={(e) => setTime(e.target.value)}
                      className="w-60"
                      required
                    />
                    {target && (
                      <span className="text-faint">
                        = {target.toISOString()}
                      </span>
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
                    {current.backups.map((b) => (
                      <option key={b.name} value={b.name}>
                        {b.name} — finished {fmt(b.finishTime)}
                      </option>
                    ))}
                  </Select>
                </Field>
                {startFrom && (
                  <p className="text-faint">
                    Starts from {startFrom.name} (finished{" "}
                    {fmt(startFrom.finishTime)}) and replays the WAL archived
                    after it.
                  </p>
                )}
              </div>
            </Panel>

            <Panel title="New database">
              <div className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
                <Field label="Name">
                  <Input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    required
                    maxLength={29}
                    pattern={nameRE}
                  />
                </Field>
                <Field
                  label="App database"
                  hint="The database your app uses on the old server. The app user becomes its owner, and of everything in it."
                >
                  <Input
                    value={database}
                    onChange={(e) => setDatabase(e.target.value)}
                    placeholder={
                      name.replaceAll("-", "_") || "default: the name"
                    }
                    maxLength={63}
                    className="font-mono"
                  />
                </Field>
                <Field label="Where">
                  <Select
                    value={placement}
                    onChange={(v) => setPlacement(v as typeof placement)}
                    className="w-full"
                  >
                    {projects.length > 0 && (
                      <option value="project">In a project</option>
                    )}
                    <option value="standalone">Standalone</option>
                  </Select>
                </Field>
                {placement === "project" && projects.length > 0 && (
                  <div className="grid grid-cols-2 gap-3">
                    <Field label="Project">
                      <Select
                        value={project}
                        onChange={setProject}
                        className="w-full"
                      >
                        {projects.map((p) => (
                          <option key={p.name}>{p.name}</option>
                        ))}
                      </Select>
                    </Field>
                    <Field label="Environment">
                      <Select value={env} onChange={setEnv} className="w-full">
                        {envs.map((e) => (
                          <option key={e}>{e}</option>
                        ))}
                      </Select>
                    </Field>
                  </div>
                )}
                <Field label="Memory per member">
                  <Select
                    value={memory}
                    onChange={(v) => setMemory(Number(v))}
                    className="w-full"
                  >
                    {SIZES.map((n) => (
                      <option key={n} value={n}>
                        {n >= 1024 ? `${n / 1024} GiB` : `${n} MiB`}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field label="Read replicas">
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
                PostgreSQL {current.version ?? ""}, as in the archive. It
                archives to the same bucket under its own prefix. Add-ons the
                old server used (TimescaleDB, pg_cron, …) are enabled afterwards
                on its Extensions tab. Restoring takes as long as downloading
                the base backup and replaying the WAL.
              </p>
            </Panel>
          </>
        )}

        {create.error && <Alert>{errText(create.error)}</Alert>}
        <div className="flex items-center gap-2">
          <Button
            type="submit"
            variant="primary"
            disabled={
              create.isPending ||
              !current?.backups.length ||
              !!outside ||
              needsKey ||
              !name
            }
          >
            Restore
          </Button>
          <Link to={"/databases" as string}>
            <Button type="button" variant="ghost">
              Cancel
            </Button>
          </Link>
        </div>
      </form>
    </div>
  );
}

function Fact({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-faint">{label}</span>
      <span className="text-fg">{children}</span>
    </div>
  );
}
