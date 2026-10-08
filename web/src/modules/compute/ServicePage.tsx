import { useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, GitCompare, Minus, Plus, RefreshCw, RotateCcw, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import {
  deploymentActive,
  deploymentStatusLabel,
  AWAITING_BUILD,
  servicePath,
  useProjects,
  useServices,
  useSharedVars,
  type Spec,
  serviceState,
} from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { Alert, Button, IconButton, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { since } from "@/lib/nodes";
import { NetworkingTab } from "./NetworkingTab";
import { MetricsPanel } from "@/modules/projects/MetricsPanel";
import {
  toRows,
  toVars,
  VarsEditor,
  varsError,
  type VarRow,
} from "@/modules/projects/VarsEditor";
import { BuildsPanel } from "./BuildsPanel";
import { TasksTable } from "./TasksPage";
import { ServiceS3Panel } from "@/modules/storage/StoragePages";
import { useTasks } from "@/lib/workloads";
import { LogsView } from "@/modules/logs/LogsView";
import { RequestsTail, TrafficPanel } from "@/modules/traffic/Traffic";
import { ServiceSecurityPanel } from "@/modules/network/SecurityGroups";
import { AutoscalingPanel } from "./AutoscalingPanel";
import { Tabs } from "@/ui/Tabs";
import { ServicePlacementPanel } from "@/modules/projects/NodeLimits";
import { confirmAction } from "@/ui/dialogs";
import { lineDiff, withContext } from "@/lib/linediff";
import { DeploymentsTable, useDeploymentActions } from "./Deployments";
import { DeployTab } from "./DeployTab";

interface Revision {
  revision: number;
  current: boolean;
  spec: Spec;
  createdAt: string;
  createdBy: string;
  /** False once registry cleanup removed the image (no rollback). */
  imageAvailable: boolean;
}

type Tab =
  | "metrics"
  | "traffic"
  | "networking"
  | "autoscaling"
  | "security"
  | "tasks"
  | "logs"
  | "deployments"
  | "deploy"
  | "builds"
  | "variables"
  | "s3"
  | "revisions"
  | "placement"
  | "spec";

/** One service: scale, tasks, revisions and its spec (§4). */
export function ServicePage() {
  const { project, env, name } = useParams({ strict: false }) as {
    project: string;
    env: string;
    name: string;
  };
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data: services } = useServices();
  const { data: projects } = useProjects();
  const svc = services?.find(
    (s) => s.project === project && s.environment === env && s.name === name,
  );
  const path = servicePath({ project, environment: env, name });
  // ?tab=builds (links from the builds and sources lists) opens that tab.
  const [tab, setTab] = useState<Tab>(
    () =>
      (new URLSearchParams(window.location.search).get("tab") as Tab | null) ??
      "metrics",
  );

  const actions = useDeploymentActions();
  const scale = useMutation({
    mutationFn: (n: number) =>
      api("POST", `${path}/scale`, { desiredCount: n }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["services"] }),
  });
  const del = useMutation({
    mutationFn: () => api("DELETE", path),
    onSuccess: () => {
      const to: string = `/projects/${project}/${env}`;
      void navigate({ to });
    },
  });

  if (!svc) {
    return services ? (
      <Alert tone="warn">
        Service {`${project}/${env}/${name}`} not found.
      </Alert>
    ) : null;
  }
  const st = serviceState(svc);
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={[
          <Link key="ps" to={"/projects" as string} className="hover:text-fg">
            Projects
          </Link>,
          <Link
            key="p"
            to={`/projects/${project}/${env}` as string}
            className="hover:text-fg"
          >
            {project} / {env}
          </Link>,
        ]}
        title={name}
        status={<StatusBadge tone={st.tone}>{st.label}</StatusBadge>}
        actions={
          <>
            <div className="border-line flex items-center rounded-sm border">
              <IconButton
                label="Scale down"
                disabled={svc.desiredCount === 0 || scale.isPending}
                onClick={() => scale.mutate(svc.desiredCount - 1)}
              >
                <Minus className="size-3.5" />
              </IconButton>
              <span
                className="w-14 text-center font-mono text-xs"
                title="desired tasks"
              >
                {svc.desiredCount} {svc.desiredCount === 1 ? "task" : "tasks"}
              </span>
              <IconButton
                label="Scale up"
                disabled={scale.isPending}
                onClick={() => scale.mutate(svc.desiredCount + 1)}
              >
                <Plus className="size-3.5" />
              </IconButton>
            </div>
            <Button
              disabled={actions.pending || svc.spec.image === AWAITING_BUILD}
              onClick={() => void actions.redeploy(path)}
              title="Restart every task with a rolling deployment"
            >
              <RefreshCw className="size-3.5" /> Redeploy
            </Button>
            <Button
              variant="danger"
              onClick={async () =>
                (await confirmAction(`Delete ${name}? All its tasks stop.`)) && del.mutate()
              }
            >
              <Trash2 className="size-3.5" /> Delete
            </Button>
          </>
        }
      />
      {svc.status &&
        (svc.spec.image === AWAITING_BUILD ? (
          <Alert tone="info">
            Waiting for the first build. Tasks start when it is deployed; see
            the Builds tab.
          </Alert>
        ) : (
          <Alert>{svc.status}</Alert>
        ))}
      {svc.deployment &&
        svc.deployment.status !== "succeeded" &&
        svc.deployment.status !== "superseded" && (
          <Alert
            tone={deploymentActive(svc.deployment) ? "info" : "warn"}
          >
            <Link
              to={`/projects/${project}/${env}/services/${name}/deployments/${svc.deployment.id}` as string}
              className="underline-offset-2 hover:underline"
            >
              Deployment {svc.deployment.fromRevision} →{" "}
              {svc.deployment.toRevision}:{" "}
              {deploymentStatusLabel[svc.deployment.status]}
            </Link>
            {svc.deployment.message && ` — ${svc.deployment.message}`}
          </Alert>
        )}
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile
          label="Running"
          value={`${svc.running}/${svc.desiredCount}`}
          tone={svc.running < svc.desiredCount ? "warn" : "ok"}
        />
        <StatTile
          label="Revision"
          value={svc.revision}
          hint={
            svc.spec.image === AWAITING_BUILD
              ? "waiting for the first build"
              : svc.spec.image
          }
        />
        <StatTile
          label="Reserved per task"
          value={`${svc.spec.resources.cpu ?? 0.1} CPU`}
          hint={`${svc.spec.resources.memory ?? 128} MiB memory`}
        />
        <StatTile
          label="Placement"
          value={svc.spec.placement.strategy ?? "spread"}
        />
      </div>
      {(svc.endpoints.length > 0 || svc.vip) && (
        <Panel title="Endpoints">
          <dl className="grid grid-cols-[6rem_minmax(0,1fr)] gap-y-1 text-xs">
            {svc.endpoints.length > 0 && (
              <>
                <dt className="text-muted">Public</dt>
                <dd className="flex flex-col gap-0.5">
                  {svc.endpoints.map((e) =>
                    e.startsWith("http") ? (
                      <a
                        key={e}
                        href={e}
                        target="_blank"
                        rel="noreferrer"
                        className="hover:text-accent inline-flex min-w-0 items-center gap-1 font-mono"
                      >
                        <span className="break-all">{e}</span>{" "}
                        <ExternalLink className="size-3 shrink-0" />
                      </a>
                    ) : (
                      <span key={e} className="font-mono break-all">
                        {e}
                      </span>
                    ),
                  )}
                </dd>
              </>
            )}
            {svc.vip && (
              <>
                <dt className="text-muted">Internal</dt>
                <dd className="font-mono break-all">
                  {svc.dnsName} <span className="text-faint">→ {svc.vip}</span>
                  <div className="text-faint font-sans">
                    Other services in {svc.project}/{svc.environment} can use{" "}
                    <span className="font-mono">{svc.name}</span>; load-balanced
                    on every node.
                  </div>
                </dd>
              </>
            )}
          </dl>
        </Panel>
      )}
      <Tabs
        tabs={[
          "metrics",
          "traffic",
          "networking",
          "autoscaling",
          "security",
          "logs",
          "tasks",
          "deployments",
          "deploy",
          "builds",
          "variables",
          "s3",
          "revisions",
          "placement",
          "spec",
        ] as Tab[]}
        value={tab}
        onChange={setTab}
      />
      {tab === "metrics" && (
        <MetricsPanel
          path={path}
          by="task"
          shorten={shortTask}
          memoryLimit={(svc.spec.resources.memoryLimit ?? 0) * 1024 * 1024}
        />
      )}
      {tab === "traffic" && (
        <div className={cn("flex flex-col", gap)}>
          <TrafficPanel path={`${path}/traffic`} scope="service" />
          <RequestsTail filter={{ project, environment: env, service: name }} />
        </div>
      )}
      {tab === "networking" && <NetworkingTab path={path} spec={svc.spec} />}
      {tab === "autoscaling" && (
        <AutoscalingPanel
          path={path}
          desired={svc.desiredCount}
          running={svc.running}
        />
      )}
      {tab === "security" && (
        <ServiceSecurityPanel path={path} project={project} env={env} name={name} />
      )}
      {tab === "tasks" && <ServiceTasks id={svc.id} path={path} />}
      {tab === "logs" && (
        <LogsView
          filter={{ project, environment: env, service: name }}
          showSource={false}
        />
      )}
      {tab === "deployments" && (
        <DeploymentsTable
          path={`${path}/deployments`}
          project={project}
          env={env}
          service={name}
          currentRevision={svc.revision}
        />
      )}
      {tab === "deploy" && (
        <DeployTab project={project} env={env} name={name} path={path} spec={svc.spec} />
      )}
      {tab === "builds" && <BuildsPanel path={path} />}
      {tab === "variables" && (
        <ServiceVariables
          key={svc.revision}
          path={path}
          project={project}
          env={env}
          spec={svc.spec}
        />
      )}
      {tab === "s3" && <ServiceS3Panel path={path} />}
      {tab === "revisions" && (
        <Revisions path={path} project={project} window={projects?.find((x) => x.name === project)?.rollbackWindow} />
      )}
      {tab === "placement" && (
        <ServicePlacementPanel
          path={path}
          service={svc}
          projectNodes={projects?.find((x) => x.name === project)?.nodes ?? []}
          projectTo={`/projects/${project}/${env}`}
        />
      )}
      {tab === "spec" && <SpecEditor path={path} spec={svc.spec} />}
    </div>
  );
}

function ServiceTasks({ id, path }: { id: string; path: string }) {
  const { data = [], isLoading } = useTasks({ id, path });
  return (
    <Panel flush>
      <TasksTable tasks={data} loading={isLoading} showService={false} />
    </Panel>
  );
}

function Revisions({
  path,
  project,
  window,
}: {
  path: string;
  project: string;
  window?: number;
}) {
  const { data = [], isLoading } = useQuery({
    queryKey: ["revisions", path],
    queryFn: async () =>
      (await api<{ items: Revision[] }>("GET", `${path}/revisions`)).items,
  });
  const actions = useDeploymentActions();
  const [compare, setCompare] = useState<number | null>(null);
  const current = data.find((r) => r.current);
  const other = data.find((r) => r.revision === compare);
  return (
    <div className={cn("flex flex-col", gap)}>
      <Panel
        flush
        title="Revisions"
        actions={
          window !== undefined && (
            <Link
              to={`/projects/${project}?tab=settings` as string}
              className="text-muted hover:text-fg text-xs"
            >
              Images of the latest {window} revisions are kept for rollbacks
            </Link>
          )
        }
      >
        <DataTable
          rows={data}
          rowKey={(r) => String(r.revision)}
          empty={!isLoading && <p className="text-muted p-3 text-xs">No revisions.</p>}
          columns={[
            {
              header: "Revision",
              cell: (r) => (
                <span className="flex items-center gap-1.5 whitespace-nowrap">
                  <span className="font-mono">{r.revision}</span>
                  {r.current && <StatusBadge tone="ok">current</StatusBadge>}
                </span>
              ),
            },
            {
              header: "Image",
              className: "w-full",
              cell: (r) => (
                <span className="flex min-w-0 flex-col py-0.5">
                  <span className="truncate font-mono" title={r.spec.image}>
                    {r.spec.image}
                  </span>
                  {!r.imageAvailable && (
                    <span className="text-warn">
                      image removed by registry cleanup
                    </span>
                  )}
                </span>
              ),
            },
            {
              header: "Created",
              cell: (r) => (
                <span className="text-muted whitespace-nowrap" title={new Date(r.createdAt).toLocaleString()}>
                  {since(r.createdAt)}
                </span>
              ),
            },
            {
              header: "",
              cell: (r) =>
                !r.current && (
                  <span className="flex justify-end gap-1">
                    <Button
                      variant="ghost"
                      onClick={() => setCompare(compare === r.revision ? null : r.revision)}
                    >
                      <GitCompare className="size-3.5" />
                      {compare === r.revision ? "Hide" : "Compare"}
                    </Button>
                    <Button
                      variant="ghost"
                      disabled={!r.imageAvailable || actions.pending}
                      title={
                        r.imageAvailable
                          ? undefined
                          : "Its image is gone; raise the project's rollback window to keep more"
                      }
                      onClick={() => void actions.rollback(path, r.revision)}
                    >
                      <RotateCcw className="size-3.5" /> Roll back
                    </Button>
                  </span>
                ),
            },
          ]}
        />
      </Panel>
      {current && other && (
        <Panel title={`Revision ${other.revision} compared with the current revision ${current.revision}`}>
          <SpecDiff from={other.spec} to={current.spec} />
        </Panel>
      )}
    </div>
  );
}

/** A line diff of two specs, as the spec editor shows them. */
function SpecDiff({ from, to }: { from: Spec; to: Spec }) {
  const text = (s: Spec) => {
    const { sharedEnv: _, s3: _s3, redeployedAt: _r, ...own } = s as Spec & { s3?: unknown };
    return JSON.stringify(own, null, 2);
  };
  const lines = withContext(lineDiff(text(from), text(to)));
  if (!lines.length)
    return <p className="text-muted text-xs">The specs are the same apart from platform-set fields.</p>;
  return (
    <pre className="bg-bg border-line max-h-96 overflow-auto rounded-md border py-1 font-mono text-[11px] leading-relaxed">
      {lines.map((l, i) =>
        l === null ? (
          <div key={i} className="text-faint px-2">
            ⋯
          </div>
        ) : (
          <div
            key={i}
            className={cn(
              "px-2 whitespace-pre-wrap break-all",
              l.kind === "add" && "bg-ok/10 text-ok",
              l.kind === "del" && "bg-bad/10 text-bad",
            )}
          >
            {l.kind === "add" ? "+ " : l.kind === "del" ? "- " : "  "}
            {l.text}
          </div>
        ),
      )}
    </pre>
  );
}

function SpecEditor({ path, spec }: { path: string; spec: Spec }) {
  const qc = useQueryClient();
  const [text, setText] = useState(() => {
    const { sharedEnv: _, s3: _s3, redeployedAt: _r, ...own } = spec as Spec & { s3?: unknown }; // set by the platform, not editable
    return JSON.stringify(own, null, 2);
  });
  const [parseErr, setParseErr] = useState("");
  const apply = useMutation({
    mutationFn: (s: unknown) => api("PUT", path, s),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["revisions", path] });
    },
  });
  const submit = () => {
    try {
      const s = JSON.parse(text);
      setParseErr("");
      apply.mutate(s);
    } catch (e) {
      setParseErr(String(e));
    }
  };
  return (
    <Panel
      title="Task definition"
      actions={
        <Button variant="primary" onClick={submit} disabled={apply.isPending}>
          {apply.isPending ? "Deploying…" : "Deploy as new revision"}
        </Button>
      }
    >
      <div className="flex flex-col gap-2">
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          spellCheck={false}
          className="bg-bg border-line-strong h-96 w-full rounded-input border p-2 font-mono text-[11px] leading-relaxed"
        />
        {(parseErr || apply.error) && (
          <Alert>
            {parseErr ||
              (apply.error instanceof ApiError
                ? apply.error.message
                : "Deploy failed")}
          </Alert>
        )}
        {apply.isSuccess && (
          <Alert tone="info">
            Deployed. Tasks roll over to the new revision as the new ones start.
          </Alert>
        )}
        <p className="text-faint text-xs">
          Changing the definition creates a new revision and replaces tasks with
          a rolling update. Scaling does not.
        </p>
      </div>
    </Panel>
  );
}

function ServiceVariables({
  path,
  project,
  env,
  spec,
}: {
  path: string;
  project: string;
  env: string;
  spec: Spec;
}) {
  const qc = useQueryClient();
  const { data: shared } = useSharedVars(project, env);
  const [rows, setRows] = useState<VarRow[]>(() => toRows(spec.env));
  const save = useMutation({
    mutationFn: () => {
      const { sharedEnv: _, ...own } = spec;
      return api("PUT", path, { ...own, env: toVars(rows) });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["revisions", path] });
    },
  });
  const err = varsError(rows);
  const dirty =
    JSON.stringify(toVars(rows)) !== JSON.stringify(toVars(toRows(spec.env)));
  const projectTo: string = `/projects/${project}/${env}`;
  return (
    <Panel
      title="Variables"
      actions={
        <Button
          variant="primary"
          disabled={!dirty || !!err || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Deploying…" : "Save and deploy"}
        </Button>
      }
    >
      <div className="flex max-w-3xl flex-col gap-2">
        <p className="text-muted text-xs">
          This service's own variables. It also inherits the{" "}
          <Link to={projectTo} className="text-accent">
            shared variables of {project}/{env}
          </Link>
          ; a variable here with the same name wins. Saving deploys a new
          revision.
        </p>
        <VarsEditor rows={rows} onChange={setRows} inherited={shared} />
        {err && <Alert>{err}</Alert>}
        {save.error && (
          <Alert>
            {save.error instanceof ApiError
              ? save.error.message
              : "Could not save"}
          </Alert>
        )}
        {save.isSuccess && !dirty && (
          <Alert tone="info">Saved. A new revision is rolling out.</Alert>
        )}
      </div>
    </Panel>
  );
}

const shortTask = (id: string) => id.replace(/^(task|run)_/, "").slice(0, 8);
