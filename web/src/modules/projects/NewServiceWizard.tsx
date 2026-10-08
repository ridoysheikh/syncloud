import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Box, Check, ChevronLeft, ChevronRight, GitBranch } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import {
  AWAITING_BUILD,
  servicePath,
  useProjects,
  useServices,
  useSharedVars,
  type Spec,
  serviceUrl,
} from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { cn, gap, pad } from "@/ui/cn";
import { toVars, VarsEditor, varsError, type VarRow } from "./VarsEditor";
import {
  BranchInput,
  RepoPicker,
  useGitConnections,
  type GitConnection,
} from "@/modules/integrations/RepoPicker";

const nameRE = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/;
const steps = ["Source", "Service", "Variables", "Review"] as const;
type Step = (typeof steps)[number];

const select =
  "bg-bg border-line-strong focus:border-line-accent h-8 w-full rounded-input border px-2 text-sm outline-none";

interface Form {
  source: "image" | "git";
  image: string;
  gitUrl: string;
  /** A Git connection (null: the first one, if any; "": any Git URL). */
  connection: string | null;
  repo: string;
  branch: string;
  context: string;
  dockerfile: string;
  builder: "auto" | "dockerfile" | "nixpacks" | "static";
  paths: string;
  token: string;
  autoDeploy: boolean;
  project: string;
  env: string;
  name: string;
  port: string;
  exposure: "public" | "internal" | "none";
  tasks: string;
  cpu: string;
  memory: string;
  healthPath: string;
  /** A pre-deploy command such as database migrations (optional). */
  release: string;
}

/** Full-page wizard: source → service settings → variables → review (§4). */
export function NewServiceWizard() {
  const params = useParams({ strict: false }) as {
    project?: string;
    env?: string;
  };
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data: projects = [] } = useProjects();
  const { data: services = [] } = useServices();
  const [step, setStep] = useState<Step>("Source");
  const [vars, setVars] = useState<VarRow[]>([]);
  const [f, setF] = useState<Form>({
    source: "image",
    image: "",
    gitUrl: "",
    connection: null,
    repo: "",
    branch: "main",
    context: "",
    dockerfile: "Dockerfile",
    builder: "auto",
    paths: "",
    token: "",
    autoDeploy: true,
    project: params.project ?? "",
    env: params.env ?? "",
    name: "",
    port: "8080",
    exposure: "public",
    tasks: "1",
    cpu: "0.1",
    memory: "128",
    healthPath: "",
    release: "",
  });
  const set = <K extends keyof Form>(k: K, v: Form[K]) =>
    setF((x) => ({ ...x, [k]: v }));
  const { data: conns = [] } = useGitConnections();
  const connection = f.connection ?? conns[0]?.name ?? "";

  const project = f.project || projects[0]?.name || "";
  const envs = projects.find((p) => p.name === project)?.environments ?? [];
  const env = f.env && envs.includes(f.env) ? f.env : (envs[0] ?? "");
  const { data: shared } = useSharedVars(project, env);
  const taken = services.some(
    (s) => s.project === project && s.environment === env && s.name === f.name,
  );

  // Problems per step; Next is disabled while a step has one.
  const problems: Record<Step, string> = {
    Source:
      f.source === "image"
        ? !f.image.trim()
          ? "Enter an image"
          : /\s/.test(f.image.trim())
            ? "The image must not contain spaces"
            : ""
        : connection
          ? !f.repo
            ? "Pick a repository"
            : ""
          : !/^https?:\/\/[^/]+\/.+/.test(f.gitUrl.trim())
            ? "Enter the repository's https:// clone URL"
            : !f.branch.trim()
              ? "Enter a branch"
              : "",
    Service: !project
      ? "Create a project first"
      : !nameRE.test(f.name)
        ? "Name: lowercase letters, digits and dashes (max 32)"
        : taken
          ? `${f.name} already exists in ${project}/${env}`
          : f.exposure !== "none" &&
              !(Number(f.port) >= 1 && Number(f.port) <= 65535)
            ? "Port must be 1–65535"
            : !(Number(f.tasks) >= 0 && Number(f.tasks) <= 100)
              ? "Tasks must be 0–100"
              : !(Number(f.cpu) >= 0.01) || !(Number(f.memory) >= 4)
                ? "CPU must be at least 0.01 and memory at least 4 MiB"
                : f.healthPath && !f.healthPath.startsWith("/")
                  ? "The health check path must start with /"
                  : "",
    Variables: varsError(vars),
    Review: "",
  };
  const idx = steps.indexOf(step);
  const blocked = steps.slice(0, idx + 1).find((s) => problems[s]);

  const spec = (): Partial<Spec> & {
    desiredCount: number;
    releaseCommands?: { preDeploy: { command: string }[] };
  } => ({
    image: f.source === "image" ? f.image.trim() : AWAITING_BUILD,
    ports:
      f.exposure === "none"
        ? []
        : [
            {
              container: Number(f.port),
              protocol: f.exposure === "public" ? "http" : "tcp",
            },
          ],
    env: toVars(vars),
    resources: { cpu: Number(f.cpu), memory: Number(f.memory) },
    health:
      f.healthPath && f.exposure !== "none"
        ? { type: "http", path: f.healthPath }
        : undefined,
    desiredCount: Number(f.tasks),
    releaseCommands: f.release.trim()
      ? { preDeploy: [{ command: f.release.trim() }] }
      : undefined,
  });

  const create = useMutation({
    mutationFn: async () => {
      const path = servicePath({ project, environment: env, name: f.name });
      await api("PUT", path, spec());
      if (f.source === "git") {
        try {
          await api("PUT", `${path}/git`, {
            ...(connection
              ? { connection, repo: f.repo }
              : { url: f.gitUrl.trim(), token: f.token || undefined }),
            branch: f.branch.trim(),
            context: f.context.trim(),
            dockerfile: f.dockerfile.trim() || "Dockerfile",
            builder: f.builder,
            paths: f.paths.split(/\s+/).filter(Boolean),
            autoDeploy: f.autoDeploy,
          });
        } catch (e) {
          await api("DELETE", path).catch(() => {}); // do not leave a service that can never build
          throw e;
        }
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      const to: string = serviceUrl({
        project,
        environment: env,
        name: f.name,
      });
      void navigate({ to });
    },
  });

  const backTo: string = params.project
    ? `/projects/${params.project}/${params.env ?? ""}`
    : "/projects";
  return (
    <div className={cn("mx-auto flex w-full max-w-5xl flex-col", gap)}>
      <PageHeader
        crumbs={["Projects", ...(params.project ? [params.project] : [])]}
        title="New service"
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
            {step === "Source" && (
              <SourceStep
                f={f}
                set={set}
                conns={conns}
                connection={connection}
              />
            )}
            {step === "Service" && (
              <ServiceStep
                f={f}
                set={set}
                projects={projects.map((p) => p.name)}
                envs={envs}
                project={project}
                env={env}
              />
            )}
            {step === "Variables" && (
              <div className="flex flex-col gap-2">
                <p className="text-muted text-xs">
                  Variables for this service only. Shared variables of {project}
                  /{env} are inherited; a variable here with the same name
                  overrides them.
                </p>
                <VarsEditor rows={vars} onChange={setVars} inherited={shared} />
                <Field
                  label="Release command (optional)"
                  hint="Runs in the new image with these variables before every deployment takes traffic, for example database migrations. If it fails, the deployment stops and the running version stays. The first tasks start only after it passes. Change it later on the service's Deploy tab."
                >
                  <Input
                    value={f.release}
                    onChange={(e) => set("release", e.target.value)}
                    placeholder="npm run migrate"
                    className="font-mono"
                    spellCheck={false}
                  />
                </Field>
              </div>
            )}
            {step === "Review" && (
              <Review
                f={f}
                connection={connection}
                project={project}
                env={env}
                vars={toVars(vars)}
                shared={shared ?? {}}
              />
            )}
          </Panel>
          {blocked && blocked !== step && (
            <Alert tone="warn">
              Fix the {blocked} step: {problems[blocked]}
            </Alert>
          )}
          {problems[step] && step !== "Source" && (
            <Alert tone="warn">{problems[step]}</Alert>
          )}
          {create.error && (
            <Alert>
              {create.error instanceof ApiError
                ? create.error.message
                : "Could not create the service"}
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
                {create.isPending
                  ? "Creating…"
                  : f.source === "git"
                    ? "Create and build"
                    : "Create and deploy"}
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

function Choice({
  active,
  onClick,
  icon,
  title,
  children,
}: {
  active: boolean;
  onClick: () => void;
  icon: ReactNode;
  title: string;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex flex-col gap-1 rounded-md border text-left transition-colors",
        pad,
        active
          ? "border-line-accent bg-accent/10"
          : "border-line hover:border-line-strong",
      )}
    >
      <span className="flex items-center gap-2 text-sm font-medium">
        {icon}
        {title}
      </span>
      <span className="text-muted text-xs">{children}</span>
    </button>
  );
}

function SourceStep({
  f,
  set,
  conns,
  connection,
}: {
  f: Form;
  set: SetFn;
  conns: GitConnection[];
  connection: string;
}) {
  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <Choice
          active={f.source === "image"}
          onClick={() => set("source", "image")}
          icon={<Box className="size-4" />}
          title="Container image"
        >
          Deploy an existing image from Docker Hub, GHCR or the private
          registry.
        </Choice>
        <Choice
          active={f.source === "git"}
          onClick={() => set("source", "git")}
          icon={<GitBranch className="size-4" />}
          title="Git repository"
        >
          Build a branch with BuildKit (a Dockerfile, or Nixpacks without one);
          new commits are built and deployed.
        </Choice>
      </div>
      {f.source === "image" ? (
        <Field
          label="Image"
          hint="e.g. nginx:1.27, ghcr.io/acme/api:v2 or @registry/shop/api:v1 (the private registry)."
        >
          <Input
            value={f.image}
            onChange={(e) => set("image", e.target.value)}
            placeholder="nginx:1.27"
            className="font-mono"
            autoFocus
          />
        </Field>
      ) : (
        <div className="flex flex-col gap-2">
          <RepoPicker
            connections={conns}
            connection={connection}
            repo={f.repo}
            onConnection={(c) => set("connection", c)}
            onRepo={(r) => {
              set("repo", r.fullName);
              set("branch", r.defaultBranch);
            }}
          />
          {connection === "" && (
            <Field label="Repository URL">
              <Input
                value={f.gitUrl}
                onChange={(e) => set("gitUrl", e.target.value)}
                placeholder="https://github.com/acme/api.git"
                className="font-mono"
                autoFocus
              />
            </Field>
          )}
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
            <Field label="Branch">
              {connection ? (
                <BranchInput
                  connection={connection}
                  repo={f.repo}
                  value={f.branch}
                  onChange={(v) => set("branch", v)}
                />
              ) : (
                <Input
                  value={f.branch}
                  onChange={(e) => set("branch", e.target.value)}
                  className="font-mono"
                />
              )}
            </Field>
            <Field label="Context directory">
              <Input
                value={f.context}
                onChange={(e) => set("context", e.target.value)}
                placeholder="(repository root)"
                className="font-mono"
              />
            </Field>
            <Field label="Dockerfile">
              <Input
                value={f.dockerfile}
                onChange={(e) => set("dockerfile", e.target.value)}
                disabled={f.builder === "nixpacks" || f.builder === "static"}
                className="font-mono"
              />
            </Field>
          </div>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
            <Field label="Builder">
              <select
                value={f.builder}
                onChange={(e) =>
                  set("builder", e.target.value as Form["builder"])
                }
                className={select}
              >
                <option value="auto">Automatic</option>
                <option value="dockerfile">Dockerfile</option>
                <option value="nixpacks">Nixpacks</option>
                <option value="static">Static site (port 80)</option>
              </select>
            </Field>
            <div className="sm:col-span-2">
              <Field
                label="Watch paths"
                hint="Optional, for monorepos: only commits changing these build."
              >
                <Input
                  value={f.paths}
                  onChange={(e) => set("paths", e.target.value)}
                  placeholder="services/api/** !services/api/docs/**"
                  className="font-mono"
                />
              </Field>
            </div>
          </div>
          {f.builder === "auto" && (
            <p className="text-muted text-xs">
              Automatic uses the Dockerfile when the repository has one, else
              Nixpacks detects the language (Node, Python, Go, …), else a folder
              with an index.html is served as a static site on port 80.
            </p>
          )}
          {connection ? (
            <p className="text-muted text-xs">
              SynCloud creates the push webhook on the repository and shows each
              build's status on the commit.
            </p>
          ) : (
            <Field
              label="Access token"
              hint="Only for private repositories. Stored encrypted. Connect the Git host instead to pick repositories and get webhooks set up for you."
            >
              <Input
                type="password"
                value={f.token}
                onChange={(e) => set("token", e.target.value)}
                autoComplete="off"
              />
            </Field>
          )}
          <label className="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={f.autoDeploy}
              onChange={(e) => set("autoDeploy", e.target.checked)}
            />
            Deploy every successful build automatically
          </label>
        </div>
      )}
    </div>
  );
}

function ServiceStep({
  f,
  set,
  projects,
  envs,
  project,
  env,
}: {
  f: Form;
  set: SetFn;
  projects: string[];
  envs: string[];
  project: string;
  env: string;
}) {
  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
        <Field
          label="Project"
          hint={
            projects.length === 0 ? (
              <Link to={"/projects/new" as string} className="text-accent">
                Create a project
              </Link>
            ) : undefined
          }
        >
          <select
            value={project}
            onChange={(e) => (set("project", e.target.value), set("env", ""))}
            className={select}
          >
            {projects.map((p) => (
              <option key={p}>{p}</option>
            ))}
          </select>
        </Field>
        <Field label="Environment">
          <select
            value={env}
            onChange={(e) => set("env", e.target.value)}
            className={select}
          >
            {envs.map((e) => (
              <option key={e}>{e}</option>
            ))}
          </select>
        </Field>
        <Field
          label="Service name"
          hint={
            f.name
              ? `${f.name}.${env}.${project}.syncloud.internal`
              : "Also its internal DNS name."
          }
        >
          <Input
            value={f.name}
            onChange={(e) => set("name", e.target.value.toLowerCase())}
            placeholder="api"
            className="font-mono"
            autoFocus
          />
        </Field>
      </div>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
        <Field label="Network">
          <select
            value={f.exposure}
            onChange={(e) =>
              set("exposure", e.target.value as Form["exposure"])
            }
            className={select}
          >
            <option value="public">HTTP, public URL</option>
            <option value="internal">Internal only (TCP)</option>
            <option value="none">No port (worker)</option>
          </select>
        </Field>
        {f.exposure !== "none" && (
          <Field label="Container port">
            <Input
              value={f.port}
              onChange={(e) => set("port", e.target.value)}
              className="font-mono"
              inputMode="numeric"
            />
          </Field>
        )}
        {f.exposure !== "none" && (
          <Field
            label="Health check path"
            hint="Optional. Tasks that fail it are replaced."
          >
            <Input
              value={f.healthPath}
              onChange={(e) => set("healthPath", e.target.value)}
              placeholder="/healthz"
              className="font-mono"
            />
          </Field>
        )}
      </div>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
        <Field
          label="Tasks"
          hint="Containers to keep running, spread over nodes."
        >
          <Input
            type="number"
            min={0}
            max={100}
            value={f.tasks}
            onChange={(e) => set("tasks", e.target.value)}
          />
        </Field>
        <Field label="CPU per task (cores reserved)">
          <Input
            type="number"
            step={0.05}
            min={0.01}
            value={f.cpu}
            onChange={(e) => set("cpu", e.target.value)}
          />
        </Field>
        <Field
          label="Memory per task (MiB reserved)"
          hint="The hard limit is twice this."
        >
          <Input
            type="number"
            min={4}
            value={f.memory}
            onChange={(e) => set("memory", e.target.value)}
          />
        </Field>
      </div>
    </div>
  );
}

function Row({ k, children }: { k: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted">{k}</dt>
      <dd className="min-w-0 break-all">{children}</dd>
    </>
  );
}

function Review({
  f,
  project,
  env,
  vars,
  shared,
  connection,
}: {
  f: Form;
  connection: string;
  project: string;
  env: string;
  vars: Record<string, string>;
  shared: Record<string, string>;
}) {
  const names = [
    ...new Set([...Object.keys(shared), ...Object.keys(vars)]),
  ].sort();
  return (
    <dl className="grid grid-cols-[9rem_1fr] gap-x-2 gap-y-1.5 text-xs">
      <Row k="Service">
        <span className="font-mono">
          {project}/{env}/{f.name}
        </span>
      </Row>
      {f.source === "image" ? (
        <Row k="Image">
          <span className="font-mono">{f.image}</span>
        </Row>
      ) : (
        <>
          <Row k="Repository">
            <span className="font-mono">
              {connection ? `${f.repo} on ${connection}` : f.gitUrl} @{" "}
              {f.branch || "default branch"}
            </span>
          </Row>
          <Row k="Build">
            <span className="font-mono">
              {f.builder === "nixpacks" || f.builder === "static"
                ? `${f.builder}${f.context ? ` in ${f.context}` : ""}`
                : f.context
                  ? `${f.context}/${f.dockerfile}`
                  : f.dockerfile}
              {f.builder === "auto" && " (else Nixpacks or static)"}
            </span>
          </Row>
          {f.paths.trim() && (
            <Row k="Watch paths">
              <span className="font-mono">{f.paths}</span>
            </Row>
          )}
          <Row k="Deploys">
            {f.autoDeploy ? "every successful build" : "by hand"}; tasks start
            after the first build
          </Row>
        </>
      )}
      <Row k="Network">
        {f.exposure === "none"
          ? "no port"
          : `${f.exposure === "public" ? "HTTP with a public URL" : "internal TCP"} on port ${f.port}${f.healthPath ? `, health check ${f.healthPath}` : ""}`}
      </Row>
      <Row k="Tasks">
        {f.tasks} × {f.cpu} CPU, {f.memory} MiB
      </Row>
      <Row k="Release command">
        {f.release.trim() ? (
          <span className="font-mono break-all">{f.release.trim()}</span>
        ) : (
          <span className="text-faint">none</span>
        )}
      </Row>
      <Row k="Variables">
        {names.length === 0 ? (
          <span className="text-faint">none</span>
        ) : (
          <span className="flex flex-wrap gap-1">
            {names.map((n) => (
              <span
                key={n}
                className="border-line rounded-sm border px-1 font-mono"
                title={n in vars ? "service" : "shared"}
              >
                {n}
                {!(n in vars) && <span className="text-faint"> (shared)</span>}
              </span>
            ))}
          </span>
        )}
      </Row>
    </dl>
  );
}
