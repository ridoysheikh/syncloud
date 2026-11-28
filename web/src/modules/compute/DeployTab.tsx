import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import type { JobRun } from "@/lib/jobs";
import type { Spec } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import {
  Alert,
  Button,
  Field,
  IconButton,
  Input,
  Toggle,
} from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { CommandPanel } from "./CommandOverride";
import { ImageUpdatesPanel } from "./ImageUpdates";
import {
  ReleaseCommandLists,
  releaseCommandsError,
  type CommandRow,
} from "./ReleaseCommandList";

/* Service › Deploy (Phase 15b): image updates, command, release commands, build settings, rollout. */

interface HookJob {
  name: string;
  project: string;
  environment: string;
  spec: {
    kind: string;
    service?: string;
    entrypoint?: string[];
    command?: string[];
    timeout?: number;
    retries?: number;
    order?: number;
  };
  lastRun: JobRun | null;
  createdAt: string;
}

type Kind = "pre-deploy" | "post-deploy";

interface Row extends CommandRow {
  /** The job's command is not a shell line; kept unless edited. */
  raw?: HookJob["spec"];
}

const errText = (e: unknown, fallback: string) =>
  e instanceof ApiError ? e.message : fallback;

/** The shell line a hook job runs. */
function shellLine(s: HookJob["spec"]): { line: string; shell: boolean } {
  const cmd = s.command ?? [];
  if (s.entrypoint?.join(" ") === "sh -c" && cmd.length === 1)
    return { line: cmd[0] ?? "", shell: true };
  if (cmd[0] === "sh" && cmd[1] === "-c" && cmd.length === 3)
    return { line: cmd[2] ?? "", shell: true };
  return { line: cmd.join(" "), shell: false };
}

function toRows(jobs: HookJob[], kind: Kind): Row[] {
  return jobs
    .filter((j) => j.spec.kind === kind)
    .sort(
      (a, b) =>
        (a.spec.order ?? 0) - (b.spec.order ?? 0) ||
        a.createdAt.localeCompare(b.createdAt) ||
        a.name.localeCompare(b.name),
    )
    .map((j) => {
      const { line, shell } = shellLine(j.spec);
      return {
        name: j.name,
        command: line,
        timeout: j.spec.timeout ? String(j.spec.timeout) : "",
        retries: j.spec.retries ? String(j.spec.retries) : "",
        raw: shell ? undefined : j.spec,
        lastRun: j.lastRun,
      };
    });
}

/** Ordered pre- and post-deploy commands, stored as the service's hook jobs. */
function ReleaseCommands({
  project,
  env,
  service,
}: {
  project: string;
  env: string;
  service: string;
}) {
  const qc = useQueryClient();
  const jobsPath = `/projects/${project}/environments/${env}/jobs`;
  const q = useQuery({
    queryKey: ["jobs", jobsPath],
    queryFn: async () =>
      (await api<{ items: HookJob[] }>("GET", jobsPath)).items.filter(
        (j) => j.spec.service === service,
      ),
  });
  const jobs = q.data;
  const [edit, setEdit] = useState<Record<Kind, Row[]> | null>(null);
  const rows: Record<Kind, Row[]> = edit ?? {
    "pre-deploy": toRows(jobs ?? [], "pre-deploy"),
    "post-deploy": toRows(jobs ?? [], "post-deploy"),
  };
  const change = (kind: Kind, next: Row[]) =>
    setEdit({ ...rows, [kind]: next });

  const save = useMutation({
    mutationFn: async () => {
      const keep = new Set<string>();
      const taken = new Set((jobs ?? []).map((j) => j.name));
      for (const kind of ["pre-deploy", "post-deploy"] as Kind[]) {
        let n = 1;
        for (const [i, r] of rows[kind].entries()) {
          let name = r.name;
          if (!name) {
            const short = kind === "pre-deploy" ? "pre" : "post";
            const base = service.slice(0, 32 - short.length - 4);
            while (taken.has(`${base}-${short}-${n}`)) n++;
            name = `${base}-${short}-${n}`;
            taken.add(name);
          }
          keep.add(name);
          const body = r.raw
            ? { ...r.raw, order: i + 1 }
            : {
                kind,
                service,
                entrypoint: ["sh", "-c"],
                command: [r.command.trim()],
                timeout: Number(r.timeout) || undefined,
                retries: Number(r.retries) || undefined,
                order: i + 1,
              };
          await api("PUT", `${jobsPath}/${name}`, body);
        }
      }
      for (const j of jobs ?? [])
        if (
          (j.spec.kind === "pre-deploy" || j.spec.kind === "post-deploy") &&
          !keep.has(j.name)
        )
          await api("DELETE", `${jobsPath}/${j.name}`);
    },
    onSuccess: () => {
      setEdit(null);
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
  const problem = releaseCommandsError(rows["pre-deploy"], rows["post-deploy"]);
  return (
    <Panel
      title="Release commands"
      actions={
        <>
          {edit && (
            <Button variant="ghost" onClick={() => setEdit(null)}>
              Discard
            </Button>
          )}
          <Button
            variant="primary"
            disabled={!edit || !!problem || save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </>
      }
    >
      <div className={cn("flex flex-col", gap)}>
        <ReleaseCommandLists
          pre={rows["pre-deploy"]}
          post={rows["post-deploy"]}
          onPre={(r) => change("pre-deploy", r as Row[])}
          onPost={(r) => change("post-deploy", r as Row[])}
        />
        {problem && <Alert tone="warn">{problem}</Alert>}
        {save.error && (
          <Alert>{errText(save.error, "Could not save the commands")}</Alert>
        )}
      </div>
    </Panel>
  );
}

interface BuildSettings {
  installCommand: string;
  buildCommand: string;
  startCommand: string;
  postBuild: string[];
  variables: string[];
}

interface VarRow {
  key: string;
  /** null: keep the stored value. */
  value: string | null;
}

/** Nixpacks command overrides, build variables and after-build checks. */
function BuildSettingsPanel({ path }: { path: string }) {
  const qc = useQueryClient();
  const key = ["build-settings", path];
  const q = useQuery({
    queryKey: key,
    queryFn: () => api<BuildSettings>("GET", `${path}/git/build-settings`),
    retry: false,
  });
  if (q.error instanceof ApiError && q.error.status === 404)
    return (
      <Panel title="Build">
        <p className="text-muted text-xs">
          This service runs an image. Connect a Git repository on the Builds tab
          to build it here, with build commands, build variables and checks that
          run before each build is deployed.
        </p>
      </Panel>
    );
  if (!q.data) return null;
  return (
    <BuildSettingsForm
      key={JSON.stringify(q.data)}
      path={path}
      settings={q.data}
      onSaved={() => qc.invalidateQueries({ queryKey: key })}
    />
  );
}

function BuildSettingsForm({
  path,
  settings,
  onSaved,
}: {
  path: string;
  settings: BuildSettings;
  onSaved: () => void;
}) {
  const [cmds, setCmds] = useState({
    installCommand: settings.installCommand,
    buildCommand: settings.buildCommand,
    startCommand: settings.startCommand,
  });
  const [checks, setChecks] = useState<string[]>(settings.postBuild);
  const [vars, setVars] = useState<VarRow[]>(
    settings.variables.map((k) => ({ key: k, value: null })),
  );
  const [dirty, setDirty] = useState(false);
  const touch = () => setDirty(true);
  const keys = vars.map((v) => v.key.trim()).filter(Boolean);
  const varProblem = vars.some(
    (v) => v.key.trim() && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(v.key.trim()),
  )
    ? "Variable names are letters, digits and _ (not starting with a digit)."
    : new Set(keys).size !== keys.length
      ? "Each variable name may appear once."
      : "";
  const save = useMutation({
    mutationFn: () =>
      api("PUT", `${path}/git/build-settings`, {
        ...cmds,
        postBuild: checks.map((c) => c.trim()).filter(Boolean),
        variables: Object.fromEntries(
          vars.filter((v) => v.key.trim()).map((v) => [v.key.trim(), v.value]),
        ),
      }),
    onSuccess: () => {
      setDirty(false);
      onSaved();
    },
  });
  return (
    <Panel
      title="Build"
      actions={
        <Button
          variant="primary"
          disabled={!dirty || !!varProblem || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      }
    >
      <div className={cn("flex flex-col", gap)}>
        <section className="flex flex-col gap-1.5">
          <h3 className="text-xs font-medium">After-build checks</h3>
          <p className="text-faint text-xs">
            Shell commands run in order in the freshly built image, with the
            service's variables, before it is deployed: tests, linters, a smoke
            start. If one fails, the build is marked failed and nothing is
            deployed. Database migrations belong in the release commands above.
          </p>
          {checks.map((c, i) => (
            <div key={i} className="flex items-center gap-1">
              <Input
                value={c}
                onChange={(e) => {
                  touch();
                  setChecks(
                    checks.map((x, j) => (j === i ? e.target.value : x)),
                  );
                }}
                placeholder="npm test"
                aria-label={`Check ${i + 1}`}
                className="font-mono"
                spellCheck={false}
              />
              <IconButton
                label="Remove"
                onClick={() => {
                  touch();
                  setChecks(checks.filter((_, j) => j !== i));
                }}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            </div>
          ))}
          <div>
            <Button
              variant="ghost"
              disabled={checks.length >= 10}
              onClick={() => {
                touch();
                setChecks([...checks, ""]);
              }}
            >
              <Plus className="size-3.5" /> Add check
            </Button>
          </div>
        </section>
        <section className="flex flex-col gap-1.5">
          <h3 className="text-xs font-medium">Commands</h3>
          <p className="text-faint text-xs">
            Override what Nixpacks detects. Leave empty to keep the detected
            command. Builds from a Dockerfile ignore these.
          </p>
          <div className={cn("grid grid-cols-1 md:grid-cols-3", gap)}>
            {(
              [
                ["installCommand", "Install", "npm ci"],
                ["buildCommand", "Build", "npm run build"],
                ["startCommand", "Start", "npm start"],
              ] as const
            ).map(([k, label, ph]) => (
              <Field key={k} label={label}>
                <Input
                  value={cmds[k]}
                  onChange={(e) => {
                    touch();
                    setCmds({ ...cmds, [k]: e.target.value });
                  }}
                  placeholder={ph}
                  className="font-mono"
                  spellCheck={false}
                />
              </Field>
            ))}
          </div>
        </section>
        <section className="flex flex-col gap-1.5">
          <h3 className="text-xs font-medium">Build variables</h3>
          <p className="text-faint text-xs">
            Available while building only: Dockerfile build arguments (declare
            them with ARG) and Nixpacks environment. Values are stored encrypted
            and never shown again. The service's runtime variables are on the
            Variables tab.
          </p>
          {vars.map((v, i) => (
            <div key={i} className="flex items-center gap-1">
              <Input
                value={v.key}
                onChange={(e) => {
                  touch();
                  setVars(
                    vars.map((x, j) =>
                      j === i ? { ...x, key: e.target.value } : x,
                    ),
                  );
                }}
                placeholder="NPM_TOKEN"
                aria-label={`Variable ${i + 1} name`}
                className="w-48 font-mono"
                spellCheck={false}
              />
              <Input
                value={v.value ?? ""}
                onChange={(e) => {
                  touch();
                  setVars(
                    vars.map((x, j) =>
                      j === i ? { ...x, value: e.target.value } : x,
                    ),
                  );
                }}
                placeholder={v.value === null ? "unchanged" : "value"}
                aria-label={`Variable ${i + 1} value`}
                className="min-w-0 flex-1 font-mono"
                spellCheck={false}
                autoComplete="off"
              />
              <IconButton
                label="Remove"
                onClick={() => {
                  touch();
                  setVars(vars.filter((_, j) => j !== i));
                }}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            </div>
          ))}
          <div>
            <Button
              variant="ghost"
              onClick={() => {
                touch();
                setVars([...vars, { key: "", value: "" }]);
              }}
            >
              <Plus className="size-3.5" /> Add variable
            </Button>
          </div>
          {varProblem && <Alert tone="warn">{varProblem}</Alert>}
        </section>
        {save.error && (
          <Alert>
            {errText(save.error, "Could not save the build settings")}
          </Alert>
        )}
      </div>
    </Panel>
  );
}

/** Circuit breaker, automatic rollback and drain time (part of the spec). */
function RolloutPanel({ path, spec }: { path: string; spec: Spec }) {
  const qc = useQueryClient();
  const d = spec.deployment ?? {};
  const [breaker, setBreaker] = useState(d.circuitBreaker ?? true);
  const [rollback, setRollback] = useState(d.rollback ?? true);
  const [drain, setDrain] = useState(String(d.drainSeconds ?? 5));
  const dirty =
    breaker !== (d.circuitBreaker ?? true) ||
    rollback !== (d.rollback ?? true) ||
    Number(drain) !== (d.drainSeconds ?? 5);
  const badDrain = !(Number(drain) >= 0 && Number(drain) <= 600);
  const save = useMutation({
    mutationFn: () => {
      const {
        sharedEnv: _,
        s3: _s3,
        redeployedAt: _r,
        ...own
      } = spec as Spec & { s3?: unknown };
      return api("PUT", path, {
        ...own,
        deployment: {
          circuitBreaker: breaker,
          rollback,
          drainSeconds: Number(drain),
        },
      });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["services"] });
      void qc.invalidateQueries({ queryKey: ["deployments"] });
    },
  });
  return (
    <Panel
      title="Rollout"
      actions={
        <Button
          variant="primary"
          disabled={!dirty || badDrain || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      }
    >
      <div className="flex flex-col gap-2">
        <Toggle
          checked={breaker}
          onChange={setBreaker}
          label="Circuit breaker"
          hint="Stop a deployment whose new tasks keep failing or turn unhealthy (half the tasks, at least 3)."
        />
        <Toggle
          checked={rollback}
          disabled={!breaker}
          onChange={setRollback}
          label="Roll back automatically"
          hint="When the breaker trips, return to the previous revision."
        />
        <Field
          label="Drain time (seconds)"
          hint="How long a replaced task keeps running after it leaves the load balancer, so in-flight requests finish (0–600)."
        >
          <Input
            value={drain}
            onChange={(e) => setDrain(e.target.value)}
            inputMode="numeric"
            className="w-28"
            aria-invalid={badDrain}
          />
        </Field>
        <p className="text-faint text-xs">
          These are part of the service's spec: saving rolls out a new revision.
        </p>
        {save.error && (
          <Alert>
            {errText(save.error, "Could not save the rollout settings")}
          </Alert>
        )}
      </div>
    </Panel>
  );
}

export function DeployTab({
  project,
  env,
  name,
  path,
  spec,
}: {
  project: string;
  env: string;
  name: string;
  path: string;
  spec: Spec;
}) {
  return (
    <div className={cn("flex flex-col", gap)}>
      <ImageUpdatesPanel path={path} image={spec.image} />
      <CommandPanel
        key={JSON.stringify([spec.entrypoint, spec.command])}
        path={path}
        spec={spec}
      />
      <ReleaseCommands project={project} env={env} service={name} />
      <BuildSettingsPanel path={path} />
      <RolloutPanel
        key={JSON.stringify(spec.deployment)}
        path={path}
        spec={spec}
      />
    </div>
  );
}
