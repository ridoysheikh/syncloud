import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import {
  Activity,
  Ban,
  Boxes,
  CircleCheck,
  CircleDot,
  CircleStop,
  CircleX,
  GitCommitHorizontal,
  History,
  Play,
  RefreshCw,
  RotateCcw,
  Settings2,
  ShieldAlert,
  SkipForward,
  SquareTerminal,
  TriangleAlert,
  User,
  Variable,
  type LucideIcon,
} from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { usePaged } from "@/lib/paged";
import { since } from "@/lib/nodes";
import { duration, runTone, type JobRun } from "@/lib/jobs";
import {
  deploymentActive,
  deploymentStatusLabel,
  deploymentTone,
  servicePath,
  useServices,
  useTasks,
  type Deployment,
  type DeploymentChange,
  type DeploymentEvent,
  type DeploymentStatus,
} from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { StatTile } from "@/ui/StatTile";
import { Alert, Button, IconButton, StatusBadge } from "@/ui/controls";
import { alertDialog, confirmAction, confirmChoice } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";
import { LogsView } from "@/modules/logs/LogsView";
import { TasksTable } from "@/entities/tasks";

/* Deployment history, the deployment page and their actions (Phase 15a). */

const triggers: Record<string, { label: string; icon: LucideIcon }> = {
  manual: { label: "Manual", icon: User },
  git: { label: "Git push", icon: GitCommitHorizontal },
  rollback: { label: "Rollback", icon: RotateCcw },
  "auto-rollback": { label: "Automatic rollback", icon: ShieldAlert },
  variables: { label: "Shared variables", icon: Variable },
  redeploy: { label: "Redeploy", icon: RefreshCw },
  config: { label: "Configuration", icon: Settings2 },
};

export function TriggerLabel({ d }: { d: Deployment }) {
  const t = triggers[d.trigger] ?? { label: "Deploy", icon: Play };
  const Icon = t.icon;
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5 whitespace-nowrap">
      <Icon className="text-muted size-3.5 shrink-0" />
      {t.label}
    </span>
  );
}

/** Who or what deployed: the commit for Git deployments, else the user. */
function DeployedBy({ d }: { d: Deployment }) {
  if (d.commit) {
    return (
      <span className="font-mono" title={`${d.commit.sha} (${d.commit.ref})`}>
        {d.commit.sha.slice(0, 7)}{" "}
        <span className="text-muted font-sans">{d.commit.ref}</span>
      </span>
    );
  }
  return <span>{d.actorName || d.actor || "—"}</span>;
}

/** "image, 2 variables" */
export function summarizeChanges(ch: DeploymentChange[]) {
  const count = (prefix: string) =>
    ch.filter((c) => c.field.startsWith(prefix)).length;
  const vars = count("variable ");
  const shared = count("shared variable ");
  const parts = ch
    .filter((c) => !c.field.includes("variable "))
    .map((c) => c.field);
  if (vars) parts.push(`${vars} ${vars === 1 ? "variable" : "variables"}`);
  if (shared)
    parts.push(`${shared} shared ${shared === 1 ? "variable" : "variables"}`);
  return parts.join(", ");
}

/** How long a deployment took, or has been running. */
export function deployDuration(d: Deployment) {
  const end = d.finishedAt ? Date.parse(d.finishedAt) : Date.now();
  const s = Math.max(0, Math.round((end - Date.parse(d.startedAt)) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

function HookBadges({ hooks }: { hooks: Deployment["hooks"] }) {
  if (!hooks.length) return <span className="text-faint">—</span>;
  // The latest attempt of each hook kind decides its badge.
  const byKind = new Map<string, string>();
  for (const h of hooks) byKind.set(h.trigger, h.status);
  return (
    <span className="flex flex-wrap gap-1">
      {[...byKind].map(([kind, status]) => (
        <StatusBadge
          key={kind}
          tone={runTone[status as keyof typeof runTone] ?? "neutral"}
        >
          {kind === "pre-deploy" ? "pre" : "post"} {status.replace("_", " ")}
        </StatusBadge>
      ))}
    </span>
  );
}

export function DeploymentStatusBadge({
  status,
}: {
  status: DeploymentStatus;
}) {
  return (
    <StatusBadge tone={deploymentTone[status] ?? "neutral"}>
      {deploymentStatusLabel[status] ?? status}
    </StatusBadge>
  );
}

const errMsg = (e: unknown) =>
  e instanceof ApiError ? e.message : "The request failed.";

/** Cancel, roll back and redeploy, each behind a confirmation. */
export function useDeploymentActions() {
  const qc = useQueryClient();
  const post = useMutation({
    mutationFn: ({ path, body }: { path: string; body?: unknown }) =>
      api("POST", path, body),
    onSuccess: () => {
      for (const key of ["deployments", "deployment", "services", "revisions"])
        void qc.invalidateQueries({ queryKey: [key] });
    },
    onError: (e) =>
      void alertDialog({
        title: "That didn't work",
        message: errMsg(e),
        tone: "danger",
      }),
  });
  const hooksHint =
    "Only matters when the service has pre-deploy jobs, such as database migrations.";
  return {
    pending: post.isPending,
    async cancel(svc: string, d: Deployment) {
      const ok = await confirmAction(
        `Cancel the deployment of revision ${d.toRevision}?`,
        {
          message:
            d.status === "waiting_hook"
              ? `Its pre-deploy jobs stop and revision ${d.fromRevision} keeps running.`
              : `Revision ${d.fromRevision} rolls out again and replaces the new tasks.`,
        },
      );
      if (ok) post.mutate({ path: `${svc}/deployments/${d.id}/cancel` });
    },
    async rollback(svc: string, revision: number) {
      const r = await confirmChoice({
        title: `Roll back to revision ${revision}?`,
        message:
          "Its spec rolls out as a new revision. The running tasks are replaced as the new ones become healthy.",
        confirmLabel: "Roll back",
        tone: "warn",
        option: {
          label: "Run pre-deploy jobs",
          hint: `Off by default: migrations usually only go forward. Turn it on when the jobs are safe to run with the old revision, such as down-migrations. ${hooksHint}`,
        },
      });
      if (r.ok)
        post.mutate({
          path: `${svc}/rollback`,
          body: { revision, runHooks: r.option },
        });
    },
    async redeploy(svc: string) {
      const r = await confirmChoice({
        title: "Redeploy the current revision?",
        message:
          "Every task restarts with a rolling deployment of the same spec, recorded as a new revision.",
        confirmLabel: "Redeploy",
        option: {
          label: "Run pre-deploy jobs",
          hint: hooksHint,
          checked: true,
        },
      });
      if (r.ok)
        post.mutate({ path: `${svc}/redeploy`, body: { runHooks: r.option } });
    },
  };
}

const deploymentLink = (
  project: string,
  env: string,
  service: string,
  id: string,
) => `/projects/${project}/${env}/services/${service}/deployments/${id}`;

/**
 * Deployments, newest first, loading older pages as the table scrolls. With
 * `project` the rows span services (the project's Deployments tab).
 */
export function DeploymentsTable({
  path,
  project,
  env,
  service,
  currentRevision,
}: {
  /** The API path to list (…/deployments). */
  path: string;
  project: string;
  env: string;
  /** Set for one service's history; unset for a project's. */
  service?: string;
  currentRevision?: number;
}) {
  const q = usePaged<Deployment>(["deployments", path], path, {
    refetchInterval: (items) => (items.some(deploymentActive) ? 3000 : 15000),
  });
  const actions = useDeploymentActions();
  const rows = q.items;
  const columns = [
    {
      header: "Deployment",
      cell: (d: Deployment) => {
        const svc = service ?? d.service ?? "";
        return (
          <Link
            to={
              deploymentLink(project, d.environment ?? env, svc, d.id) as string
            }
            className="hover:text-accent flex flex-col py-0.5"
          >
            <span className="font-mono whitespace-nowrap">
              {d.fromRevision ? `${d.fromRevision} → ` : "→ "}
              {d.toRevision}
            </span>
            <span className="text-faint">{since(d.startedAt)}</span>
          </Link>
        );
      },
    },
    ...(service
      ? []
      : [
          {
            header: "Service",
            cell: (d: Deployment) => (
              <Link
                to={
                  `/projects/${project}/${d.environment}/services/${d.service}` as string
                }
                className="hover:text-accent font-mono"
              >
                {d.service}
              </Link>
            ),
          },
        ]),
    {
      header: "Status",
      cell: (d: Deployment) => <DeploymentStatusBadge status={d.status} />,
    },
    { header: "Trigger", cell: (d: Deployment) => <TriggerLabel d={d} /> },
    {
      header: "By",
      cell: (d: Deployment) => <DeployedBy d={d} />,
    },
    {
      header: "Changes",
      className: "w-full",
      cell: (d: Deployment) => {
        const sum = summarizeChanges(d.changes ?? []);
        const problem =
          d.status === "failed" ||
          d.status === "rolled_back" ||
          d.status === "cancelled";
        return (
          <div className="flex max-w-xl min-w-40 flex-col py-0.5">
            <span className="truncate" title={sum}>
              {sum || (d.fromRevision ? "no spec changes" : "first deployment")}
            </span>
            {problem && d.message && (
              <span
                className={cn(
                  "truncate",
                  d.status === "cancelled" ? "text-muted" : "text-bad",
                )}
                title={d.message}
              >
                {d.message}
              </span>
            )}
          </div>
        );
      },
    },
    {
      header: "Hooks",
      cell: (d: Deployment) => <HookBadges hooks={d.hooks ?? []} />,
    },
    {
      header: "Took",
      cell: (d: Deployment) => (
        <span className="text-muted whitespace-nowrap">
          {deployDuration(d)}
        </span>
      ),
    },
    {
      header: "",
      cell: (d: Deployment) => {
        const svc = servicePath({
          project,
          environment: d.environment ?? env,
          name: service ?? d.service ?? "",
        });
        if (deploymentActive(d))
          return (
            <IconButton
              label={`Cancel the deployment of revision ${d.toRevision}`}
              disabled={actions.pending}
              onClick={() => void actions.cancel(svc, d)}
            >
              <Ban className="size-3.5" />
            </IconButton>
          );
        if (
          service &&
          d.status === "succeeded" &&
          currentRevision !== undefined &&
          d.toRevision !== currentRevision
        )
          return (
            <IconButton
              label={`Roll back to revision ${d.toRevision}`}
              disabled={actions.pending}
              onClick={() => void actions.rollback(svc, d.toRevision)}
            >
              <RotateCcw className="size-3.5" />
            </IconButton>
          );
        return null;
      },
    },
  ];
  return (
    <Panel flush>
      <DataTable
        {...q.table}
        rows={rows}
        rowKey={(d) => d.id}
        columns={columns}
        empty={
          !q.isLoading && (
            <EmptyState icon={History} title="No deployments yet">
              Every change to a service's spec, build, rollback or redeploy is
              recorded here with what changed.
            </EmptyState>
          )
        }
      />
    </Panel>
  );
}

const eventIcon: Record<string, { icon: LucideIcon; tone?: string }> = {
  started: { icon: Play },
  "hook-started": { icon: SquareTerminal },
  "hook-succeeded": { icon: CircleCheck, tone: "text-ok" },
  "hook-failed": { icon: CircleX, tone: "text-bad" },
  "hooks-done": { icon: CircleCheck, tone: "text-ok" },
  "tasks-started": { icon: Boxes },
  serving: { icon: Activity, tone: "text-ok" },
  drained: { icon: CircleStop },
  "task-failed": { icon: TriangleAlert, tone: "text-warn" },
  succeeded: { icon: CircleCheck, tone: "text-ok" },
  failed: { icon: CircleX, tone: "text-bad" },
  rolled_back: { icon: RotateCcw, tone: "text-warn" },
  cancelled: { icon: Ban },
  superseded: { icon: SkipForward },
};

function Timeline({ events }: { events: DeploymentEvent[] }) {
  if (!events.length)
    return (
      <p className="text-muted text-xs">
        No steps recorded (deployments before this version have none).
      </p>
    );
  return (
    <ol className="flex flex-col text-xs">
      {events.map((e, i) => {
        const m = eventIcon[e.kind] ?? { icon: CircleDot };
        const Icon = m.icon;
        return (
          <li key={e.id} className="flex gap-2">
            <div className="flex flex-col items-center">
              <Icon
                className={cn(
                  "mt-0.5 size-3.5 shrink-0",
                  m.tone ?? "text-muted",
                )}
              />
              {i < events.length - 1 && (
                <span className="bg-line my-0.5 w-px flex-1" />
              )}
            </div>
            <div className="flex min-w-0 flex-1 flex-wrap items-baseline justify-between gap-x-2 pb-2">
              <span className="min-w-0 break-words">{e.message}</span>
              <time
                className="text-faint shrink-0 font-mono"
                title={new Date(e.at).toLocaleString()}
              >
                {new Date(e.at).toLocaleTimeString()}
              </time>
            </div>
          </li>
        );
      })}
    </ol>
  );
}

function Changes({ d }: { d: Deployment }) {
  if (!d.fromRevision)
    return (
      <p className="text-muted text-xs">
        The service's first deployment: there is nothing to compare with.
      </p>
    );
  if (!d.changes.length)
    return (
      <p className="text-muted text-xs">
        The spec did not change; the tasks were replaced.
      </p>
    );
  return (
    <dl className="grid grid-cols-[minmax(6rem,auto)_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
      {d.changes.map((c) => (
        <div key={c.field} className="contents">
          <dt className="text-muted">{c.field}</dt>
          <dd className="font-mono break-all">
            {c.field === "redeploy" ? (
              <span className="font-sans">redeployed</span>
            ) : c.from === "changed" ? (
              <span className="font-sans">value changed</span>
            ) : (
              <>
                {c.from && (
                  <span className="text-bad line-through">{c.from}</span>
                )}
                {c.from && c.to && <span className="text-faint"> → </span>}
                {c.to && <span className="text-ok">{c.to}</span>}
              </>
            )}
          </dd>
        </div>
      ))}
    </dl>
  );
}

interface DeploymentDetail extends Deployment {
  events: DeploymentEvent[];
  runs: JobRun[];
}

function HookRuns({ runs }: { runs: JobRun[] }) {
  const [sel, setSel] = useState<string | null>(null);
  const shown = sel ?? runs.at(-1)?.id ?? null;
  return (
    <div className={cn("flex flex-col", gap)}>
      <Panel title="Pre- and post-deploy jobs" flush>
        <DataTable
          rows={runs}
          rowKey={(r) => r.id}
          columns={[
            {
              header: "Job",
              cell: (r) => (
                <button
                  type="button"
                  onClick={() => setSel(r.id)}
                  className={cn(
                    "hover:text-accent text-left font-mono",
                    r.id === shown && "text-accent",
                  )}
                >
                  {r.job || r.id}
                </button>
              ),
            },
            { header: "Kind", cell: (r) => r.trigger },
            {
              header: "Status",
              cell: (r) => (
                <StatusBadge tone={runTone[r.status]}>
                  {r.status.replace("_", " ")}
                </StatusBadge>
              ),
            },
            { header: "Attempt", cell: (r) => r.attempt },
            {
              header: "Took",
              cell: (r) => <span className="text-muted">{duration(r)}</span>,
            },
            {
              header: "Result",
              className: "w-full",
              cell: (r) => (
                <span className="text-muted break-words">
                  {r.exitCode !== null ? `exit ${r.exitCode}` : ""}
                  {r.message ? ` ${r.message}` : ""}
                </span>
              ),
            },
          ]}
        />
      </Panel>
      {shown && (
        <LogsView key={shown} filter={{ task: shown }} showSource={false} />
      )}
    </div>
  );
}

/** One deployment: its timeline, what changed, hook runs and tasks. */
export function DeploymentPage() {
  const { project, env, name, id } = useParams({ strict: false }) as {
    project: string;
    env: string;
    name: string;
    id: string;
  };
  const svcPath = servicePath({ project, environment: env, name });
  const q = useQuery({
    queryKey: ["deployment", svcPath, id],
    queryFn: () => api<DeploymentDetail>("GET", `${svcPath}/deployments/${id}`),
    refetchInterval: (query) =>
      query.state.data && deploymentActive(query.state.data) ? 2000 : false,
  });
  const { data: services } = useServices();
  const svc = services?.find(
    (s) => s.project === project && s.environment === env && s.name === name,
  );
  const tasks = useTasks(svc ? { id: svc.id, path: svcPath } : undefined);
  const actions = useDeploymentActions();
  const d = q.data;
  const svcLink = `/projects/${project}/${env}/services/${name}`;
  const crumbs: ReactNode[] = [
    <Link
      key="p"
      to={`/projects/${project}/${env}` as string}
      className="hover:text-fg"
    >
      {project} / {env}
    </Link>,
    <Link key="s" to={svcLink as string} className="hover:text-fg">
      {name}
    </Link>,
    <Link
      key="d"
      to={`${svcLink}?tab=deployments` as string}
      className="hover:text-fg"
    >
      Deployments
    </Link>,
  ];
  if (q.error)
    return (
      <div className={cn("flex flex-col", gap)}>
        <PageHeader crumbs={crumbs} title="Deployment" />
        <Alert>{errMsg(q.error)}</Alert>
      </div>
    );
  if (!d) return <PageHeader crumbs={crumbs} title="Deployment" />;
  const current = svc?.revision;
  const revTasks = (tasks.data ?? []).filter(
    (t) => t.revision === d.toRevision,
  );
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={crumbs}
        title={
          d.fromRevision
            ? `Revision ${d.fromRevision} → ${d.toRevision}`
            : `Revision ${d.toRevision}`
        }
        status={<DeploymentStatusBadge status={d.status} />}
        actions={
          <>
            {deploymentActive(d) && (
              <Button
                variant="danger"
                disabled={actions.pending}
                onClick={() => void actions.cancel(svcPath, d)}
              >
                <Ban className="size-3.5" /> Cancel deployment
              </Button>
            )}
            {!deploymentActive(d) &&
              d.fromRevision > 0 &&
              current !== d.fromRevision && (
                <Button
                  disabled={actions.pending}
                  onClick={() => void actions.rollback(svcPath, d.fromRevision)}
                >
                  <RotateCcw className="size-3.5" /> Roll back to revision{" "}
                  {d.fromRevision}
                </Button>
              )}
            {d.status === "succeeded" && current !== d.toRevision && (
              <Button
                disabled={actions.pending}
                onClick={() => void actions.rollback(svcPath, d.toRevision)}
              >
                <RotateCcw className="size-3.5" /> Roll back to this
              </Button>
            )}
          </>
        }
      />
      {d.message && (
        <Alert
          tone={
            d.status === "failed"
              ? "bad"
              : d.status === "rolled_back"
                ? "warn"
                : "info"
          }
        >
          {d.message}
        </Alert>
      )}
      <div className={cn("grid grid-cols-2 lg:grid-cols-4", gap)}>
        <StatTile
          label="Trigger"
          value={<TriggerLabel d={d} />}
          hint={<DeployedBy d={d} />}
        />
        <StatTile
          label="Started"
          value={since(d.startedAt)}
          hint={new Date(d.startedAt).toLocaleString()}
        />
        <StatTile
          label={d.finishedAt ? "Took" : "Running for"}
          value={deployDuration(d)}
        />
        <StatTile
          label="Failed tasks"
          value={d.failedTasks}
          tone={d.failedTasks ? "warn" : undefined}
        />
      </div>
      <div className={cn("grid lg:grid-cols-2", gap)}>
        <Panel title="What changed">
          <div className="flex flex-col gap-2">
            <div className="text-xs">
              <span className="text-muted">Image </span>
              <span className="font-mono break-all">{d.image}</span>
              {d.buildId && (
                <Link
                  to={`${svcLink}?tab=builds` as string}
                  className="text-accent ml-2 whitespace-nowrap"
                >
                  build
                </Link>
              )}
            </div>
            <Changes d={d} />
          </div>
        </Panel>
        <Panel title="Timeline">
          <Timeline events={d.events} />
        </Panel>
      </div>
      {d.runs.length > 0 && <HookRuns runs={d.runs} />}
      <Panel title={`Tasks of revision ${d.toRevision}`} flush>
        <TasksTable
          tasks={revTasks}
          loading={tasks.isLoading}
          showService={false}
          toolbar={false}
        />
      </Panel>
    </div>
  );
}

/** A project's deployments across services, filtered (Project › Deployments). */
export function ProjectDeployments({
  project,
  env,
  services,
}: {
  project: string;
  env: string;
  services: string[];
}) {
  const [service, setService] = useState("");
  const [status, setStatus] = useState("");
  const params = new URLSearchParams({ environment: env });
  if (service) params.set("service", service);
  if (status) params.set("status", status);
  const sel =
    "bg-bg border-line-strong h-7 rounded-input border px-1.5 text-xs";
  return (
    <div className={cn("flex flex-col", gap)}>
      <div className="flex flex-wrap items-center gap-2">
        <select
          value={service}
          onChange={(e) => setService(e.target.value)}
          className={cn(sel, "max-w-56")}
          aria-label="Service"
        >
          <option value="">All services</option>
          {services.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <select
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          className={sel}
          aria-label="Status"
        >
          <option value="">Any status</option>
          {Object.entries(deploymentStatusLabel).map(([k, label]) => (
            <option key={k} value={k}>
              {label}
            </option>
          ))}
        </select>
      </div>
      <DeploymentsTable
        path={`/projects/${project}/deployments?${params}`}
        project={project}
        env={env}
      />
    </div>
  );
}
