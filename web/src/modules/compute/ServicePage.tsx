import { useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Minus, Plus, RotateCcw, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import {
  deploymentTone,
  servicePath,
  useServices,
  type Deployment,
  type Spec,
} from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { Alert, Button, IconButton, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { since } from "@/lib/nodes";
import { serviceState } from "./ServicesPage";
import { DomainsPanel } from "./DomainsPanel";
import { BuildsPanel } from "./BuildsPanel";
import { TasksTable } from "./TasksPage";
import { useTasks } from "@/lib/workloads";
import { LogsView } from "@/modules/logs/LogsView";

interface Revision {
  revision: number;
  current: boolean;
  spec: Spec;
  createdAt: string;
  createdBy: string;
}

type Tab = "tasks" | "logs" | "deployments" | "builds" | "revisions" | "spec";

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
  const svc = services?.find(
    (s) => s.project === project && s.environment === env && s.name === name,
  );
  const path = servicePath({ project, environment: env, name });
  const [tab, setTab] = useState<Tab>("tasks");

  const scale = useMutation({
    mutationFn: (n: number) =>
      api("POST", `${path}/scale`, { desiredCount: n }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["services"] }),
  });
  const del = useMutation({
    mutationFn: () => api("DELETE", path),
    onSuccess: () => {
      const to: string = "/compute/services";
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
        crumbs={["Compute", "Services", `${project} / ${env}`]}
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
                {svc.desiredCount} tasks
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
              variant="danger"
              onClick={() =>
                confirm(`Delete ${name}? All its tasks stop.`) && del.mutate()
              }
            >
              <Trash2 className="size-3.5" /> Delete
            </Button>
          </>
        }
      />
      {svc.status && <Alert>{svc.status}</Alert>}
      {svc.deployment &&
        svc.deployment.status !== "succeeded" &&
        svc.deployment.status !== "superseded" && (
          <Alert
            tone={svc.deployment.status === "in_progress" ? "info" : "warn"}
          >
            Deployment {svc.deployment.fromRevision} →{" "}
            {svc.deployment.toRevision}:{" "}
            {svc.deployment.status.replace("_", " ")}
            {svc.deployment.message && ` — ${svc.deployment.message}`}
          </Alert>
        )}
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile
          label="Running"
          value={`${svc.running}/${svc.desiredCount}`}
          tone={svc.running < svc.desiredCount ? "warn" : "ok"}
        />
        <StatTile label="Revision" value={svc.revision} hint={svc.spec.image} />
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
          <dl className="grid grid-cols-[6rem_1fr] gap-y-1 text-xs">
            {svc.endpoints.length > 0 && (
              <>
                <dt className="text-muted">Public</dt>
                <dd className="flex flex-col gap-0.5">
                  {svc.endpoints.map((e) => (
                    <a
                      key={e}
                      href={e}
                      target="_blank"
                      rel="noreferrer"
                      className="hover:text-accent inline-flex items-center gap-1 font-mono"
                    >
                      {e} <ExternalLink className="size-3" />
                    </a>
                  ))}
                </dd>
              </>
            )}
            {svc.vip && (
              <>
                <dt className="text-muted">Internal</dt>
                <dd className="font-mono">
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
      <DomainsPanel
        path={path}
        httpPorts={(svc.spec.ports ?? [])
          .filter((p) => (p.protocol ?? "http") === "http")
          .map((p) => p.name ?? String(p.container))}
      />
      <div className="border-line flex gap-3 border-b text-xs">
        {(
          [
            "tasks",
            "logs",
            "deployments",
            "builds",
            "revisions",
            "spec",
          ] as Tab[]
        ).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={cn(
              "-mb-px border-b-2 px-1 pb-1.5 capitalize",
              tab === t
                ? "border-accent text-fg"
                : "text-muted hover:text-fg border-transparent",
            )}
          >
            {t}
          </button>
        ))}
      </div>
      {tab === "tasks" && <ServiceTasks id={svc.id} path={path} />}
      {tab === "logs" && (
        <LogsView
          filter={{ project, environment: env, service: name }}
          showSource={false}
        />
      )}
      {tab === "deployments" && <Deployments path={path} />}
      {tab === "builds" && <BuildsPanel path={path} />}
      {tab === "revisions" && <Revisions path={path} />}
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

function Deployments({ path }: { path: string }) {
  const { data = [] } = useQuery({
    queryKey: ["deployments", path],
    queryFn: async () =>
      (await api<{ items: Deployment[] }>("GET", `${path}/deployments`)).items,
    refetchInterval: 5000,
  });
  return (
    <Panel flush>
      <DataTable
        rows={data}
        rowKey={(d) => d.id}
        columns={[
          {
            header: "Revisions",
            cell: (d) => (
              <span className="font-mono">
                {d.fromRevision
                  ? `${d.fromRevision} → ${d.toRevision}`
                  : `→ ${d.toRevision}`}
              </span>
            ),
          },
          {
            header: "Status",
            cell: (d) => (
              <StatusBadge tone={deploymentTone[d.status]}>
                {d.status.replace("_", " ")}
              </StatusBadge>
            ),
          },
          {
            header: "Failed tasks",
            cell: (d) => (
              <span className={d.failedTasks ? "text-bad" : "text-muted"}>
                {d.failedTasks}
              </span>
            ),
          },
          {
            header: "Started",
            cell: (d) => (
              <span className="text-muted">{since(d.startedAt)}</span>
            ),
          },
          {
            header: "Message",
            className: "w-full",
            cell: (d) => <span className="text-muted">{d.message || "—"}</span>,
          },
        ]}
      />
    </Panel>
  );
}

function Revisions({ path }: { path: string }) {
  const qc = useQueryClient();
  const { data = [] } = useQuery({
    queryKey: ["revisions", path],
    queryFn: async () =>
      (await api<{ items: Revision[] }>("GET", `${path}/revisions`)).items,
  });
  const rollback = useMutation({
    mutationFn: (rev: number) =>
      api("POST", `${path}/rollback`, { revision: rev }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["revisions", path] });
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  return (
    <Panel flush>
      <DataTable
        rows={data}
        rowKey={(r) => String(r.revision)}
        columns={[
          {
            header: "Revision",
            cell: (r) => <span className="font-mono">{r.revision}</span>,
          },
          {
            header: "",
            cell: (r) =>
              r.current && <StatusBadge tone="ok">current</StatusBadge>,
          },
          {
            header: "Image",
            cell: (r) => <span className="font-mono">{r.spec.image}</span>,
            className: "w-full",
          },
          {
            header: "Created",
            cell: (r) => (
              <span className="text-muted">{since(r.createdAt)}</span>
            ),
          },
          {
            header: "",
            cell: (r) =>
              !r.current && (
                <Button
                  variant="ghost"
                  onClick={() =>
                    confirm(`Roll out revision ${r.revision} again?`) &&
                    rollback.mutate(r.revision)
                  }
                >
                  <RotateCcw className="size-3.5" /> Roll back
                </Button>
              ),
          },
        ]}
      />
    </Panel>
  );
}

function SpecEditor({ path, spec }: { path: string; spec: Spec }) {
  const qc = useQueryClient();
  const [text, setText] = useState(JSON.stringify(spec, null, 2));
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
          className="bg-bg border-line h-96 w-full rounded-sm border p-2 font-mono text-[11px] leading-relaxed"
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
