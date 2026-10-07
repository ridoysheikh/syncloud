import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Boxes, ExternalLink, GitBranch, Plus, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import {
  AWAITING_BUILD,
  envPath,
  useProjects,
  useServices,
  useSharedVars,
  type Service,
  serviceState,
  serviceUrl,
} from "@/lib/workloads";
import { since } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap, pad } from "@/ui/cn";
import { MetricsPanel } from "./MetricsPanel";
import { LogsView } from "@/modules/logs/LogsView";
import { RequestsTail, TrafficPanel } from "@/modules/traffic/Traffic";
import {
  toRows,
  toVars,
  VarsEditor,
  varsError,
  type VarRow,
} from "./VarsEditor";
import { Tabs } from "@/ui/Tabs";
import { ProjectNodesPanel } from "./NodeLimits";
import { DatabasesTable } from "@/modules/databases/DatabasesPage";
import { useDatabases } from "@/lib/databases";
import { confirmAction } from "@/ui/dialogs";

type Tab =
  | "services"
  | "databases"
  | "metrics"
  | "traffic"
  | "logs"
  | "variables"
  | "settings";

/** One project: its environments, their services and shared variables. */
export function ProjectPage() {
  const params = useParams({ strict: false }) as {
    project: string;
    env?: string;
  };
  const { data: projects, isLoading } = useProjects();
  const { data: services = [] } = useServices();
  const [tab, setTab] = useState<Tab>("services");
  const p = projects?.find((x) => x.name === params.project);
  if (!p) {
    return isLoading ? null : (
      <Alert tone="warn">Project {params.project} not found.</Alert>
    );
  }
  const env =
    params.env && p.environments.includes(params.env)
      ? params.env
      : (p.environments[0] ?? "production");
  const inEnv = services.filter(
    (s) => s.project === p.name && s.environment === env,
  );
  const newTo: string = `/projects/${p.name}/${env}/new-service`;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={[
          <Link key="p" to={"/projects" as string} className="hover:text-fg">
            Projects
          </Link>,
        ]}
        title={p.name}
        actions={
          <Link to={newTo}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New service
            </Button>
          </Link>
        }
      />
      {p.description && (
        <p className="text-muted -mt-1 text-xs">{p.description}</p>
      )}
      <div className="flex flex-wrap-reverse items-end justify-between gap-x-2 gap-y-1">
        <Tabs
          className="flex-1"
          tabs={
            [
              "services",
              "databases",
              "metrics",
              "traffic",
              "logs",
              "variables",
              "settings",
            ] as Tab[]
          }
          value={tab}
          onChange={setTab}
          label={(t) => (t === "variables" ? "Shared variables" : t)}
        />
        <div className="flex items-center gap-1 pb-1 sm:shadow-[inset_0_-1px_0_var(--color-line)]">
          <span className="text-faint text-xs">Environment</span>
          {p.environments.map((e) => {
            const to: string = `/projects/${p.name}/${e}`;
            return (
              <Link
                key={e}
                to={to}
                className={cn(
                  "rounded-sm border px-1.5 py-0.5 text-xs",
                  e === env
                    ? "border-accent text-fg bg-accent/10"
                    : "border-line text-muted hover:text-fg",
                )}
              >
                {e}
              </Link>
            );
          })}
        </div>
      </div>
      {tab === "services" && <ServiceGrid services={inEnv} newTo={newTo} />}
      {tab === "databases" && <ProjectDatabases project={p.name} env={env} />}
      {tab === "metrics" && (
        <MetricsPanel key={env} path={envPath(p.name, env)} by="service" />
      )}
      {tab === "traffic" && (
        <div className={cn("flex flex-col", gap)}>
          <TrafficPanel
            key={env}
            path={`${envPath(p.name, env)}/traffic`}
            scope="environment"
          />
          <RequestsTail filter={{ project: p.name, environment: env }} />
        </div>
      )}
      {tab === "logs" && (
        <LogsView key={env} filter={{ project: p.name, environment: env }} />
      )}
      {tab === "variables" && (
        <SharedVariables
          key={env}
          project={p.name}
          env={env}
          services={inEnv}
        />
      )}
      {tab === "settings" && (
        <ProjectSettings
          project={p.name}
          nodes={p.nodes ?? []}
          environments={p.environments}
          services={services.filter((s) => s.project === p.name)}
        />
      )}
    </div>
  );
}

function ServiceGrid({
  services,
  newTo,
}: {
  services: Service[];
  newTo: string;
}) {
  if (services.length === 0) {
    return (
      <Panel>
        <EmptyState icon={Boxes} title="No services in this environment">
          <Link to={newTo} className="text-accent">
            Create a service
          </Link>{" "}
          from a container image or a Git repository.
        </EmptyState>
      </Panel>
    );
  }
  return (
    <div
      className={cn(
        "grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4",
        gap,
      )}
    >
      {services.map((s) => (
        <ServiceCard key={s.id} s={s} />
      ))}
    </div>
  );
}

function ServiceCard({ s }: { s: Service }) {
  const st = serviceState(s);
  const to: string = serviceUrl(s);
  const built =
    s.spec.image === AWAITING_BUILD ||
    s.spec.image.startsWith(`@registry/${s.project}/${s.name}:`);
  return (
    <Link
      to={to}
      className={cn(
        "bg-surface border-line hover:border-line-strong flex min-w-0 flex-col gap-1.5 rounded-md border transition-colors",
        pad,
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-sm font-semibold">{s.name}</span>
        <StatusBadge tone={st.tone}>{st.label}</StatusBadge>
      </div>
      <div
        className="text-muted flex items-center gap-1 truncate font-mono text-xs"
        title={s.spec.image}
      >
        {built && <GitBranch className="size-3 shrink-0" />}
        {s.spec.image === AWAITING_BUILD
          ? "waiting for the first build"
          : s.spec.image}
      </div>
      <div className="text-muted flex items-center justify-between gap-2 text-xs">
        <span>
          <span className="text-fg font-mono">
            {s.running}/{s.desiredCount}
          </span>{" "}
          tasks · rev {s.revision}
        </span>
        <span className="text-faint">{since(s.updatedAt)}</span>
      </div>
      {s.endpoints.length > 0 ? (
        <span className="text-faint inline-flex items-center gap-1 truncate font-mono text-xs">
          <ExternalLink className="size-3 shrink-0" />
          {s.endpoints[0]!.replace(/^https?:\/\//, "")}
        </span>
      ) : (
        <span className="text-faint truncate font-mono text-xs">
          {s.dnsName || "internal"}
        </span>
      )}
      {s.status && s.spec.image !== AWAITING_BUILD && (
        <span className="text-bad truncate text-xs" title={s.status}>
          {s.status}
        </span>
      )}
    </Link>
  );
}

function SharedVariables({
  project,
  env,
  services,
}: {
  project: string;
  env: string;
  services: Service[];
}) {
  const qc = useQueryClient();
  const { data, isLoading } = useSharedVars(project, env);
  const [rows, setRows] = useState<VarRow[]>([]);
  const [result, setResult] = useState<string[] | null>(null);
  useEffect(() => {
    if (data) setRows(toRows(data));
  }, [data]);
  const save = useMutation({
    mutationFn: () =>
      api<{ variables: Record<string, string>; redeployed: string[] }>(
        "PUT",
        `${envPath(project, env)}/variables`,
        {
          variables: toVars(rows),
        },
      ),
    onSuccess: (r) => {
      setResult(r.redeployed);
      qc.setQueryData(["shared-vars", project, env], r.variables);
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const err = varsError(rows);
  const dirty =
    JSON.stringify(toVars(rows)) !== JSON.stringify(toVars(toRows(data)));
  if (isLoading) return null;
  return (
    <Panel
      title={`Shared variables · ${env}`}
      actions={
        <Button
          variant="primary"
          disabled={!dirty || !!err || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending
            ? "Saving…"
            : services.length
              ? "Save and redeploy"
              : "Save"}
        </Button>
      }
    >
      <div className="flex max-w-3xl flex-col gap-2">
        <p className="text-muted text-xs">
          Every service in <span className="text-fg">{env}</span> gets these
          variables. A service's own variable with the same name wins. Saving
          rolls the affected services out as a new revision, one task at a time.
        </p>
        <VarsEditor
          rows={rows}
          onChange={(r) => (setRows(r), setResult(null))}
        />
        {err && <Alert>{err}</Alert>}
        {save.error && (
          <Alert>
            {save.error instanceof ApiError
              ? save.error.message
              : "Could not save"}
          </Alert>
        )}
        {result && (
          <Alert tone="info">
            Saved.{" "}
            {result.length
              ? `Redeploying ${result.join(", ")}.`
              : "No service needed a redeploy."}
          </Alert>
        )}
      </div>
    </Panel>
  );
}

function ProjectSettings({
  project,
  nodes,
  environments,
  services,
}: {
  project: string;
  nodes: string[];
  environments: string[];
  services: Service[];
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const done = () => qc.invalidateQueries({ queryKey: ["projects"] });
  const addEnv = useMutation({
    mutationFn: () =>
      api("POST", `/projects/${project}/environments`, { name }),
    onSuccess: () => {
      setName("");
      done();
    },
  });
  const delEnv = useMutation({
    mutationFn: (e: string) => api("DELETE", envPath(project, e)),
    onSuccess: done,
  });
  const delProject = useMutation({
    mutationFn: () => api("DELETE", `/projects/${project}`),
    onSuccess: () => {
      done();
      const to: string = "/projects";
      void navigate({ to });
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (name) addEnv.mutate();
  };
  const err = addEnv.error ?? delEnv.error ?? delProject.error;
  return (
    <div className={cn("grid grid-cols-1 items-start lg:grid-cols-2", gap)}>
      <div className="lg:col-span-2">
        <ProjectNodesPanel
          project={project}
          nodes={nodes}
          services={services}
        />
      </div>
      <Panel title="Environments">
        <div className="flex flex-col gap-2">
          {environments.map((e) => {
            const n = services.filter((s) => s.environment === e).length;
            return (
              <div
                key={e}
                className="flex items-center justify-between text-xs"
              >
                <span className="font-mono">{e}</span>
                <span className="flex items-center gap-2">
                  <span className="text-faint">
                    {n} service{n === 1 ? "" : "s"}
                  </span>
                  <Button
                    variant="ghost"
                    disabled={n > 0 || environments.length === 1}
                    title={n > 0 ? "Delete its services first" : undefined}
                    onClick={async () =>
                      (await confirmAction(`Delete environment ${e}?`)) &&
                      delEnv.mutate(e)
                    }
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </span>
              </div>
            );
          })}
          <form onSubmit={submit} className="flex items-end gap-1.5">
            <Field label="New environment">
              <Input
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase())}
                placeholder="staging"
                className="h-7 font-mono"
              />
            </Field>
            <Button type="submit" disabled={!name || addEnv.isPending}>
              Add
            </Button>
          </form>
        </div>
      </Panel>
      <Panel title="Danger zone">
        <div className="flex items-center justify-between gap-2 text-xs">
          <span className="text-muted">
            {services.length
              ? `Delete the project's ${services.length} service${services.length === 1 ? "" : "s"} first.`
              : "Delete the project and its environments."}
          </span>
          <Button
            variant="danger"
            disabled={services.length > 0 || delProject.isPending}
            onClick={async () =>
              (await confirmAction(`Delete project ${project}?`)) &&
              delProject.mutate()
            }
          >
            <Trash2 className="size-3.5" /> Delete project
          </Button>
        </div>
      </Panel>
      {err && (
        <div className="lg:col-span-2">
          <Alert>
            {err instanceof ApiError ? err.message : "Request failed"}
          </Alert>
        </div>
      )}
    </div>
  );
}

function ProjectDatabases({ project, env }: { project: string; env: string }) {
  const { data = [], isLoading } = useDatabases();
  const items = data.filter(
    (d) => d.project === project && d.environment === env,
  );
  // Databases elsewhere (standalone or other projects) this environment may
  // connect to through their access lists.
  const reachable = data.filter(
    (d) =>
      !(d.project === project && d.environment === env) &&
      d.network.access.some(
        (a) =>
          a === `project:${project}` ||
          a === `environment:${project}/${env}` ||
          a.startsWith(`service:${project}/${env}/`),
      ),
  );
  const newTo: string = `/projects/${project}/${env}/new-database`;
  return (
    <>
      <Panel
        title={`Databases in ${env} (${items.length})`}
        flush
        actions={
          <Link to={newTo}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New database
            </Button>
          </Link>
        }
      >
        <DatabasesTable items={items} loading={isLoading} showProject={false} />
      </Panel>
      {reachable.length > 0 && (
        <Panel
          title={`Other databases ${env} can reach (${reachable.length})`}
          flush
        >
          <DatabasesTable items={reachable} />
        </Panel>
      )}
    </>
  );
}
