import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronLeft, ChevronRight } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useProjects } from "@/lib/workloads";
import { useNodes } from "@/lib/nodes";
import {
  dbUrl,
  defaultPgReplication,
  EVICTION_POLICIES,
  useDatabaseEngines,
  type Database,
  type DatabaseSpec,
  type PgReplicationSpec,
} from "@/lib/databases";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import {
  AccessEditor,
  allowList,
  PublicFields,
  useBaseDomain,
} from "./Network";
import { PgCapacityStep, PgDataStep } from "./PostgresSteps";

const nameRE = /^[a-z0-9]([a-z0-9-]{0,27}[a-z0-9])?$/;
const steps = [
  "Engine",
  "Database",
  "Capacity",
  "Data",
  "Network",
  "Review",
] as const;
type Step = (typeof steps)[number];

export const selectClass =
  "bg-bg border-line-strong focus:border-line-accent h-8 w-full rounded-input border px-2 text-sm outline-none";

/** Sizes offered in the wizard (MiB). */
const SIZES = [64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384];

export interface Form {
  engine: string;
  version: string;
  standalone: boolean;
  project: string;
  env: string;
  name: string;
  memMin: number;
  memMax: number;
  repMin: number;
  repMax: number;
  cpu: string;
  cpuTarget: string;
  persistence: DatabaseSpec["persistence"];
  eviction: string;
  nodes: string[];
  /** null: the default for the owner (its own environment, or nobody). */
  access: string[] | null;
  public: boolean;
  allow: string;
  /** PostgreSQL: add-ons, parameters and replication (§13c2). */
  pg: PgConfigForm;
  /** PostgreSQL: WAL-G backups to this S3 endpoint and bucket ("" = off). */
  backupEndpoint: string;
  backupBucket: string;
}

/** PostgreSQL settings chosen in the wizard. */
export interface PgConfigForm {
  extensions: string[];
  parameters: Record<string, string>;
  replication: PgReplicationSpec;
}

export function specOf(f: {
  memMin: number;
  memMax: number;
  repMin: number;
  repMax: number;
  cpu: string;
  cpuTarget: string;
  persistence: DatabaseSpec["persistence"];
  eviction: string;
  nodes: string[];
  engine?: string;
  pg?: PgConfigForm;
  /** PostgreSQL settings kept as they are (backups, connection limit). */
  postgres?: DatabaseSpec["postgres"];
  backupEndpoint?: string;
  backupBucket?: string;
}): Partial<DatabaseSpec> {
  if (f.engine === "postgres") {
    // Fixed size and replica count for now (autoscaling comes with 13d).
    return {
      memory: { min: f.memMin, max: f.memMin },
      replicas: { min: f.repMin, max: f.repMin },
      cpu: Number(f.cpu) || 0.5,
      nodes: f.nodes,
      autoscaling: { cpuTarget: Number(f.cpuTarget) || 60, memoryHigh: 85 },
      postgres: {
        maxConnections: 0,
        ...f.postgres,
        ...f.pg,
        synchronous:
          (f.pg?.replication ?? f.postgres?.replication)?.mode !== undefined &&
          (f.pg?.replication ?? f.postgres?.replication)?.mode !== "async",
        ...(f.backupEndpoint && f.backupBucket
          ? {
              backup: {
                endpoint: f.backupEndpoint,
                bucket: f.backupBucket,
                everyHours: 24,
                retainFull: 7,
                retainDays: 7,
              },
            }
          : {}),
      },
    };
  }
  return {
    memory: { min: f.memMin, max: f.memMax },
    replicas: { min: f.repMin, max: f.repMax },
    cpu: Number(f.cpu) || 0.1,
    persistence: f.persistence,
    evictionPolicy: f.eviction,
    nodes: f.nodes,
    autoscaling: { cpuTarget: Number(f.cpuTarget) || 60, memoryHigh: 85 },
  };
}

const mib = (v: number) => (v >= 1024 ? `${v / 1024} GiB` : `${v} MiB`);

/** Full-page wizard for a managed database (Phase 12): standalone, or in a project environment. */
export function NewDatabaseWizard() {
  const params = useParams({ strict: false }) as {
    project?: string;
    env?: string;
  };
  const { data: projects = [] } = useProjects();
  const { data: engines = [] } = useDatabaseEngines();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [step, setStep] = useState<Step>("Engine");
  const [f, setF] = useState<Form>({
    engine: "valkey",
    version: "8.1",
    standalone: !params.project,
    project: params.project ?? "",
    env: params.env ?? "production",
    name: "",
    memMin: 256,
    memMax: 1024,
    repMin: 1,
    repMax: 2,
    cpu: "0.1",
    cpuTarget: "60",
    persistence: "aof",
    eviction: "noeviction",
    nodes: [],
    access: null,
    public: false,
    allow: "",
    pg: {
      extensions: [],
      parameters: {},
      replication: { ...defaultPgReplication },
    },
    backupEndpoint: "",
    backupBucket: "",
  });
  const set = <K extends keyof Form>(k: K, v: Form[K]) =>
    setF((x) => ({ ...x, [k]: v }));
  const project = f.project || projects[0]?.name || "";
  const proj = projects.find((p) => p.name === project);
  const envs = proj?.environments ?? [];
  const env = envs.includes(f.env) ? f.env : (envs[0] ?? "production");
  const engine = engines.find((e) => e.name === f.engine);
  const port = engine?.port ?? 6379;
  const owner = f.standalone ? null : { project, env };
  const access =
    f.access ?? (owner ? [`environment:${owner.project}/${owner.env}`] : []);
  const host = owner
    ? `${f.name || "NAME"}.${env}.${project}.syncloud.internal`
    : `${f.name || "NAME"}.db.syncloud.internal`;

  const problems: Record<Step, string> = {
    Engine: engine && !engine.available ? `${engine.title} is planned.` : "",
    Database: !nameRE.test(f.name)
      ? "Name: 1–29 lowercase letters, digits or hyphens."
      : f.name === "engines" || f.name === "new" || f.name.endsWith("-ro")
        ? 'The names "engines" and "new", and names ending in "-ro", are reserved.'
        : !f.standalone && !project
          ? "Create a project first, or make the database standalone."
          : "",
    Capacity:
      f.memMin > f.memMax
        ? "The memory maximum is below the minimum."
        : f.repMin > f.repMax
          ? "The replica maximum is below the minimum."
          : "",
    Data: "",
    Network: "",
    Review: "",
  };
  const idx = steps.indexOf(step);
  const blocked = steps.slice(0, idx + 1).find((s) => problems[s]);

  const create = useMutation({
    mutationFn: () =>
      api<Database>("POST", "/databases", {
        name: f.name,
        engine: f.engine,
        version: f.version,
        ...(owner ? { project: owner.project, environment: owner.env } : {}),
        spec: specOf(f),
        network: {
          access,
          public: { enabled: f.public, allow: allowList(f.allow) },
        },
      }),
    onSuccess: (d) => {
      void qc.invalidateQueries({ queryKey: ["databases"] });
      void navigate({ to: dbUrl(d) as string });
    },
  });

  const backTo: string = params.project
    ? `/projects/${params.project}/${params.env ?? ""}`
    : "/databases";
  return (
    <div className={cn("mx-auto flex w-full max-w-5xl flex-col", gap)}>
      <PageHeader
        crumbs={["Databases"]}
        title="New database"
        actions={
          <Link to={backTo}>
            <Button variant="ghost">Cancel</Button>
          </Link>
        }
      />
      <div className={cn("grid grid-cols-1 md:grid-cols-[12rem_1fr]", gap)}>
        <ol className="flex gap-1 overflow-x-auto md:flex-col">
          {steps.map((s, i) => (
            <li key={s}>
              <button
                onClick={() =>
                  i <= idx || !steps.slice(0, i).find((x) => problems[x])
                    ? setStep(s)
                    : undefined
                }
                className={cn(
                  "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-xs",
                  s === step ? "bg-raised text-fg" : "text-muted hover:text-fg",
                )}
              >
                <span
                  className={cn(
                    "flex size-5 shrink-0 items-center justify-center rounded-full border text-[10px]",
                    i < idx
                      ? "border-accent bg-accent text-accent-fg"
                      : s === step
                        ? "border-line-accent text-accent"
                        : "border-line",
                  )}
                >
                  {i < idx ? <Check className="size-3" /> : i + 1}
                </span>
                <span className="hidden sm:inline">{s}</span>
              </button>
            </li>
          ))}
        </ol>
        <div className="flex min-w-0 flex-col gap-2">
          <Panel title={step}>
            {step === "Engine" && (
              <div className="flex flex-col gap-3 text-xs">
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  {engines.map((e) => (
                    <button
                      key={e.name}
                      type="button"
                      disabled={!e.available}
                      onClick={() => {
                        set("engine", e.name);
                        set("version", e.defaultVersion ?? "");
                        // Engine-sized defaults.
                        set("memMin", e.name === "postgres" ? 1024 : 256);
                        set("memMax", 1024);
                        set("cpu", e.name === "postgres" ? "0.5" : "0.1");
                      }}
                      className={cn(
                        "flex flex-col gap-1 rounded-sm border p-3 text-left",
                        f.engine === e.name
                          ? "border-line-accent bg-raised"
                          : "border-line hover:border-line-strong",
                        !e.available && "cursor-not-allowed opacity-60",
                      )}
                    >
                      <span className="flex items-center gap-2">
                        <span className="text-sm font-medium">{e.title}</span>
                        {!e.available && (
                          <span className="border-line text-muted rounded-sm border px-1 text-[10px]">
                            planned
                          </span>
                        )}
                        <span className="text-faint ml-auto font-mono">
                          :{e.port}
                        </span>
                      </span>
                      <span className="text-muted">{e.description}</span>
                    </button>
                  ))}
                </div>
                {engine?.available && (
                  <Field
                    label="Version"
                    hint={
                      engine.name === "valkey"
                        ? "BSD-licensed; speaks the Redis protocol, so any Redis client works."
                        : undefined
                    }
                  >
                    <select
                      className={cn(selectClass, "max-w-60")}
                      value={f.version}
                      onChange={(e) => set("version", e.target.value)}
                    >
                      {engine.versions.map((v) => (
                        <option key={v} value={v}>
                          {engine.title} {v}
                          {v === engine.defaultVersion ? " (recommended)" : ""}
                        </option>
                      ))}
                    </select>
                  </Field>
                )}
              </div>
            )}
            {step === "Database" && (
              <div className="flex flex-col gap-3 text-xs">
                <Field
                  label="Name"
                  hint="Unique in the cluster, like an AWS database identifier."
                >
                  <Input
                    value={f.name}
                    onChange={(e) => set("name", e.target.value.toLowerCase())}
                    placeholder="sessions"
                    className="max-w-80 font-mono"
                  />
                </Field>
                <div className="flex flex-col gap-2">
                  {(
                    [
                      [
                        true,
                        "Standalone",
                        "Not tied to a project. You choose which projects, environments or services may connect.",
                      ],
                      [
                        false,
                        "In a project environment",
                        "Follows the project's allowed nodes and is reachable from its environment by default.",
                      ],
                    ] as const
                  ).map(([v, label, help]) => (
                    <label key={label} className="flex items-start gap-2">
                      <input
                        type="radio"
                        className="mt-0.5"
                        checked={f.standalone === v}
                        onChange={() => set("standalone", v)}
                      />
                      <span>
                        <span className="font-medium">{label}</span>
                        <span className="text-muted block">{help}</span>
                      </span>
                    </label>
                  ))}
                </div>
                {!f.standalone && (
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <Field label="Project">
                      <select
                        className={selectClass}
                        value={project}
                        onChange={(e) => set("project", e.target.value)}
                      >
                        {projects.map((p) => (
                          <option key={p.name}>{p.name}</option>
                        ))}
                      </select>
                    </Field>
                    <Field label="Environment">
                      <select
                        className={selectClass}
                        value={env}
                        onChange={(e) => set("env", e.target.value)}
                      >
                        {envs.map((e) => (
                          <option key={e}>{e}</option>
                        ))}
                      </select>
                    </Field>
                  </div>
                )}
                <p className="text-faint">
                  Inside the cluster:{" "}
                  <code className="font-mono">
                    {host}:{port}
                  </code>
                </p>
              </div>
            )}
            {step === "Capacity" &&
              (f.engine === "postgres" ? (
                <PgCapacityStep f={f} set={set} />
              ) : (
                <CapacityStep f={f} set={set} />
              ))}
            {step === "Data" &&
              (f.engine === "postgres" ? (
                <PgDataStep f={f} set={set} backups config />
              ) : (
                <DataStep f={f} set={set} />
              ))}
            {step === "Network" && (
              <div className="flex flex-col gap-4 text-xs">
                <Section title="Inside the cluster: who may connect">
                  <AccessEditor
                    value={access}
                    onChange={(v) => set("access", v)}
                  />
                  {f.access === null && owner && (
                    <p className="text-faint">
                      Default: the services of {owner.project} / {owner.env}.
                    </p>
                  )}
                </Section>
                <Section title="From outside the cluster">
                  <PublicFields
                    name={f.name}
                    enabled={f.public}
                    allow={f.allow}
                    port={port}
                    engine={f.engine}
                    onEnabled={(v) => set("public", v)}
                    onAllow={(v) => set("allow", v)}
                  />
                </Section>
              </div>
            )}
            {step === "Review" && (
              <Review
                f={f}
                engineTitle={engine?.title ?? f.engine}
                owner={owner}
                host={host}
                port={port}
                access={access}
              />
            )}
          </Panel>
          {blocked && blocked !== step && (
            <Alert tone="warn">
              Fix the {blocked} step: {problems[blocked]}
            </Alert>
          )}
          {problems[step] && <Alert tone="warn">{problems[step]}</Alert>}
          {create.error && (
            <Alert>
              {create.error instanceof ApiError
                ? create.error.message
                : "Could not create the database"}
            </Alert>
          )}
          <div className="flex justify-between">
            <Button
              variant="ghost"
              disabled={idx === 0}
              onClick={() => setStep(steps[idx - 1] ?? step)}
            >
              <ChevronLeft className="size-3.5" /> Back
            </Button>
            {step === "Review" ? (
              <Button
                variant="primary"
                disabled={!!blocked || create.isPending}
                onClick={() => create.mutate()}
              >
                {create.isPending ? "Creating…" : "Create database"}
              </Button>
            ) : (
              <Button
                variant="primary"
                disabled={!!problems[step]}
                onClick={() => setStep(steps[idx + 1] ?? step)}
              >
                Next <ChevronRight className="size-3.5" />
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

export type SetFn = <K extends keyof Form>(k: K, v: Form[K]) => void;

export function SizeSelect({
  value,
  onChange,
}: {
  value: number;
  onChange: (v: number) => void;
}) {
  return (
    <select
      className={selectClass}
      value={value}
      onChange={(e) => onChange(Number(e.target.value))}
    >
      {SIZES.map((s) => (
        <option key={s} value={s}>
          {mib(s)}
        </option>
      ))}
    </select>
  );
}

export function CountSelect({
  value,
  onChange,
}: {
  value: number;
  onChange: (v: number) => void;
}) {
  return (
    <select
      className={selectClass}
      value={value}
      onChange={(e) => onChange(Number(e.target.value))}
    >
      {[0, 1, 2, 3, 4, 5].map((n) => (
        <option key={n} value={n}>
          {n}
        </option>
      ))}
    </select>
  );
}

export function Section({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-muted text-xs font-medium">{title}</h3>
      {children}
    </div>
  );
}

export function CapacityStep({
  f,
  set,
}: {
  f: Pick<
    Form,
    "memMin" | "memMax" | "repMin" | "repMax" | "cpu" | "cpuTarget"
  >;
  set: SetFn;
}) {
  return (
    <div className="flex flex-col gap-4 text-xs">
      <Section title="Memory (maxmemory)">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Field label="Start at / never below">
            <SizeSelect value={f.memMin} onChange={(v) => set("memMin", v)} />
          </Field>
          <Field label="Grow up to">
            <SizeSelect value={f.memMax} onChange={(v) => set("memMax", v)} />
          </Field>
        </div>
        <p className="text-faint">
          {f.memMin < f.memMax
            ? `Autoscaled online, without restarts: +50% when more than 85% is used, −25% after 30 minutes under 40%.`
            : "Fixed size. Set a higher maximum to let it grow."}
        </p>
      </Section>
      <Section title="Read replicas and failover">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <Field label="At least">
            <CountSelect value={f.repMin} onChange={(v) => set("repMin", v)} />
          </Field>
          <Field label="At most">
            <CountSelect value={f.repMax} onChange={(v) => set("repMax", v)} />
          </Field>
          <Field label="Scale out above read CPU (%)">
            <Input
              type="number"
              min={5}
              max={95}
              value={f.cpuTarget}
              onChange={(e) => set("cpuTarget", e.target.value)}
            />
          </Field>
        </div>
        <p className="text-faint">
          {f.repMax === 0
            ? "One server, no failover: if its node goes down the database is down until it returns."
            : `Replicas run on other nodes and serve the read-only endpoint. Three Sentinels promote a replica within seconds if the primary's node fails${f.repMin === 0 ? " (once a replica exists)" : ""}.`}
        </p>
      </Section>
      <Section title="CPU">
        <Field label="Reserved per member (cores)">
          <Input
            type="number"
            step="0.05"
            min={0.01}
            value={f.cpu}
            onChange={(e) => set("cpu", e.target.value)}
            className="max-w-40"
          />
        </Field>
      </Section>
    </div>
  );
}

export function DataStep({
  f,
  set,
}: {
  f: Pick<Form, "persistence" | "eviction" | "nodes">;
  set: SetFn;
}) {
  const { data: nodes = [] } = useNodes();
  return (
    <div className="flex flex-col gap-4 text-xs">
      <Section title="Persistence">
        {(
          [
            [
              "aof",
              "Append-only file + snapshots",
              "Every write is logged (fsync every second); loses at most a second of writes on a crash. Recommended.",
            ],
            [
              "rdb",
              "Snapshots only",
              "Saved every 1–60 minutes depending on activity; a crash loses writes since the last snapshot.",
            ],
            [
              "none",
              "In memory only",
              "Nothing is written to disk: a pure cache. A restarted member starts empty (replicas resync).",
            ],
          ] as const
        ).map(([v, label, help]) => (
          <label key={v} className="flex items-start gap-2">
            <input
              type="radio"
              className="mt-0.5"
              checked={f.persistence === v}
              onChange={() => set("persistence", v)}
            />
            <span>
              <span className="font-medium">{label}</span>
              <span className="text-muted block">{help}</span>
            </span>
          </label>
        ))}
      </Section>
      <Section title="When memory is full">
        <select
          className={cn(selectClass, "max-w-80")}
          value={f.eviction}
          onChange={(e) => set("eviction", e.target.value)}
        >
          {EVICTION_POLICIES.map((p) => (
            <option key={p} value={p}>
              {p === "noeviction"
                ? "noeviction: refuse writes (for data)"
                : p === "allkeys-lru"
                  ? "allkeys-lru: drop least recently used (for caches)"
                  : p}
            </option>
          ))}
        </select>
      </Section>
      <Section title="Nodes">
        <p className="text-muted">
          Optional: keep members on some nodes (within the project's allowed
          nodes, for a project database). Members always go on different nodes.
        </p>
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {nodes.map((n) => (
            <label key={n.id} className="flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={f.nodes.includes(n.name)}
                onChange={() =>
                  set(
                    "nodes",
                    f.nodes.includes(n.name)
                      ? f.nodes.filter((x) => x !== n.name)
                      : [...f.nodes, n.name],
                  )
                }
              />
              {n.name}
            </label>
          ))}
        </div>
      </Section>
    </div>
  );
}

function Review({
  f,
  engineTitle,
  owner,
  host,
  port,
  access,
}: {
  f: Form;
  engineTitle: string;
  owner: { project: string; env: string } | null;
  host: string;
  port: number;
  access: string[];
}) {
  const base = useBaseDomain();
  const ro = host.replace(f.name + ".", f.name + "-ro.");
  const rows: [string, ReactNode][] = [
    ["Name", <span className="font-mono">{f.name}</span>],
    ["Engine", `${engineTitle} ${f.version}`],
    ["Owner", owner ? `${owner.project} / ${owner.env}` : "standalone"],
    [
      "Read-write",
      <span className="font-mono">
        {host}:{port}
      </span>,
    ],
    [
      "Read-only",
      <span className="font-mono">
        {ro}:{port}
      </span>,
    ],
    ["Access", access.length ? access.join(", ") : "nobody inside the cluster"],
    [
      "Public",
      f.public ? (
        <span className="font-mono">
          {f.name}.db.{base || "<base domain>"}:{port} (TLS)
          {allowList(f.allow).length
            ? ` from ${allowList(f.allow).join(", ")}`
            : " from anywhere"}
        </span>
      ) : (
        "off"
      ),
    ],
    ...(f.engine === "postgres"
      ? ([
          ["Memory", `${mib(f.memMin)} per member, ${f.cpu} CPU`],
          ["Replicas", `${f.repMin}`],
          [
            "Failover",
            f.repMin > 0
              ? `Patroni, ${f.pg.replication.mode === "async" ? "asynchronous" : `${f.pg.replication.mode === "strict" ? "strict " : ""}synchronous (${f.pg.replication.syncReplicas} replica${f.pg.replication.syncReplicas > 1 ? "s" : ""} confirm each commit)`}, leader lease ${f.pg.replication.failoverTtl}s`
              : "none (single server)",
          ],
          [
            "Add-ons",
            f.pg.extensions.length
              ? f.pg.extensions.join(", ")
              : "none (plain PostgreSQL)",
          ],
          [
            "Parameters",
            Object.keys(f.pg.parameters).length ? (
              <span className="font-mono">
                {Object.entries(f.pg.parameters)
                  .map(([k, v]) => `${k}=${v}`)
                  .join(", ")}
              </span>
            ) : (
              "the platform's (tuned from memory)"
            ),
          ],
          [
            "Database",
            <span className="font-mono">{f.name.replace(/-/g, "_")}</span>,
          ],
          ["User", <span className="font-mono">app</span>],
          [
            "Backups",
            f.backupEndpoint && f.backupBucket
              ? `WAL-G to ${f.backupEndpoint}/${f.backupBucket}: WAL continuously, a base backup daily, 7 days of point-in-time recovery`
              : "off (turn on later under Backups)",
          ],
        ] as [string, ReactNode][])
      : ([
          [
            "Memory",
            f.memMin === f.memMax
              ? mib(f.memMin)
              : `${mib(f.memMin)} → up to ${mib(f.memMax)} (autoscaled)`,
          ],
          [
            "Replicas",
            f.repMin === f.repMax
              ? `${f.repMin}`
              : `${f.repMin} → up to ${f.repMax} (autoscaled above ${f.cpuTarget}% read CPU)`,
          ],
          [
            "Failover",
            f.repMax > 0 ? "Sentinel (3 instances)" : "none (single server)",
          ],
          ["Persistence", f.persistence],
          ["Eviction", f.eviction],
        ] as [string, ReactNode][])),
    ["Nodes", f.nodes.length ? f.nodes.join(", ") : "any allowed node"],
  ];
  return (
    <dl className="divide-line divide-y text-xs">
      {rows.map(([k, v]) => (
        <div key={k} className="flex gap-2 py-1.5">
          <dt className="text-muted w-28 shrink-0">{k}</dt>
          <dd className="min-w-0 break-words">{v}</dd>
        </div>
      ))}
    </dl>
  );
}
