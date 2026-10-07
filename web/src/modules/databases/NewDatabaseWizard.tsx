import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronLeft, ChevronRight } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useProjects } from "@/lib/workloads";
import { useNodes } from "@/lib/nodes";
import {
  dbPath,
  dbUrl,
  EVICTION_POLICIES,
  type Database,
  type DatabaseSpec,
} from "@/lib/databases";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

const nameRE = /^[a-z0-9]([a-z0-9-]{0,27}[a-z0-9])?$/;
const steps = ["Database", "Capacity", "Data", "Review"] as const;
type Step = (typeof steps)[number];

export const selectClass =
  "bg-bg border-line-strong focus:border-accent h-8 w-full rounded-sm border px-2 text-sm outline-none";

/** Sizes offered in the wizard (MiB). */
const SIZES = [64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384];

interface Form {
  project: string;
  env: string;
  name: string;
  version: string;
  memMin: number;
  memMax: number;
  repMin: number;
  repMax: number;
  cpu: string;
  cpuTarget: string;
  persistence: DatabaseSpec["persistence"];
  eviction: string;
  nodes: string[];
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
}): Partial<DatabaseSpec> {
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

/** Full-page wizard for a managed Valkey database (Phase 12). */
export function NewDatabaseWizard() {
  const params = useParams({ strict: false }) as {
    project?: string;
    env?: string;
  };
  const { data: projects = [] } = useProjects();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [step, setStep] = useState<Step>("Database");
  const [f, setF] = useState<Form>({
    project: params.project ?? "",
    env: params.env ?? "production",
    name: "",
    version: "8.1",
    memMin: 256,
    memMax: 1024,
    repMin: 1,
    repMax: 2,
    cpu: "0.1",
    cpuTarget: "60",
    persistence: "aof",
    eviction: "noeviction",
    nodes: [],
  });
  const set = <K extends keyof Form>(k: K, v: Form[K]) =>
    setF((x) => ({ ...x, [k]: v }));
  const project = f.project || projects[0]?.name || "";
  const proj = projects.find((p) => p.name === project);
  const envs = proj?.environments ?? [];
  const env = envs.includes(f.env) ? f.env : (envs[0] ?? "production");

  const problems: Record<Step, string> = {
    Database: !project
      ? "Create a project first."
      : !nameRE.test(f.name)
        ? "Name: 1–29 lowercase letters, digits or hyphens."
        : "",
    Capacity:
      f.memMin > f.memMax
        ? "The memory maximum is below the minimum."
        : f.repMin > f.repMax
          ? "The replica maximum is below the minimum."
          : "",
    Data: "",
    Review: "",
  };
  const idx = steps.indexOf(step);
  const blocked = steps.slice(0, idx + 1).find((s) => problems[s]);

  const create = useMutation({
    mutationFn: () =>
      api<Database>("POST", dbPath(project, env), {
        name: f.name,
        version: f.version,
        spec: specOf(f),
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
        <ol className="flex gap-1 md:flex-col">
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
                        ? "border-accent text-accent"
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
            {step === "Database" && (
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
                <Field
                  label="Name"
                  hint={
                    f.name
                      ? `${f.name}.${env}.${project}.syncloud.internal:6379`
                      : "Shares names with the environment's services"
                  }
                >
                  <Input
                    value={f.name}
                    onChange={(e) => set("name", e.target.value.toLowerCase())}
                    placeholder="cache"
                    className="font-mono"
                  />
                </Field>
                <Field
                  label="Engine"
                  hint="BSD-licensed; speaks the Redis protocol, so any Redis client works."
                >
                  <select
                    className={selectClass}
                    value={f.version}
                    onChange={(e) => set("version", e.target.value)}
                  >
                    <option value="8.1">Valkey 8.1 (recommended)</option>
                    <option value="8.0">Valkey 8.0</option>
                  </select>
                </Field>
              </div>
            )}
            {step === "Capacity" && <CapacityStep f={f} set={set} />}
            {step === "Data" && <DataStep f={f} set={set} />}
            {step === "Review" && <Review f={f} project={project} env={env} />}
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

type SetFn = <K extends keyof Form>(k: K, v: Form[K]) => void;

function SizeSelect({
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

function CountSelect({
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

function Section({ title, children }: { title: string; children: ReactNode }) {
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
          nodes). Members always go on different nodes.
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
  project,
  env,
}: {
  f: Form;
  project: string;
  env: string;
}) {
  const rows: [string, ReactNode][] = [
    [
      "Database",
      <span className="font-mono">
        {project}/{env}/{f.name}
      </span>,
    ],
    ["Engine", `Valkey ${f.version}`],
    [
      "Read-write",
      <span className="font-mono">
        {f.name}.{env}.{project}.syncloud.internal:6379
      </span>,
    ],
    [
      "Read-only",
      <span className="font-mono">
        {f.name}-ro.{env}.{project}.syncloud.internal:6379
      </span>,
    ],
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
