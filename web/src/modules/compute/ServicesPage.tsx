import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Boxes, ExternalLink, Plus } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useProjects, useServices, type Service } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { Dialog } from "@/ui/Dialog";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

export function serviceState(s: Service): {
  tone: "ok" | "warn" | "bad" | "info" | "neutral";
  label: string;
} {
  if (s.deleting) return { tone: "neutral", label: "deleting" };
  if (s.status) return { tone: "bad", label: "degraded" };
  if (s.desiredCount === 0) return { tone: "neutral", label: "stopped" };
  if (s.running >= s.desiredCount && s.pending === 0)
    return { tone: "ok", label: "healthy" };
  if (s.running === 0) return { tone: "warn", label: "starting" };
  return { tone: "info", label: "deploying" };
}

/** Every service in the cluster (§4). */
export function ServicesPage() {
  const { data: services = [], isLoading } = useServices();
  const [creating, setCreating] = useState(false);
  const running = services.reduce((n, s) => n + s.running, 0);
  const desired = services.reduce((n, s) => n + s.desiredCount, 0);
  const degraded = services.filter(
    (s) => serviceState(s).tone === "bad",
  ).length;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Compute"]}
        title="Services"
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Plus className="size-3.5" /> New service
          </Button>
        }
      />
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile label="Services" value={services.length} />
        <StatTile
          label="Tasks running"
          value={`${running}/${desired}`}
          tone={running < desired ? "warn" : running ? "ok" : undefined}
        />
        <StatTile
          label="Degraded"
          value={degraded}
          tone={degraded ? "bad" : undefined}
        />
        <StatTile
          label="Projects"
          value={new Set(services.map((s) => s.project)).size}
        />
      </div>
      <Panel title="Services" flush>
        <DataTable
          rows={services}
          rowKey={(s) => s.id}
          empty={
            !isLoading && (
              <EmptyState icon={Boxes} title="No services yet">
                A service keeps a number of containers running from one image,
                places them across nodes, and replaces them when they fail.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Service",
              cell: (s) => {
                const to: string = `/compute/services/${s.project}/${s.environment}/${s.name}`;
                return (
                  <Link
                    to={to}
                    className="hover:text-accent flex flex-col py-1"
                  >
                    <span className="font-medium">{s.name}</span>
                    <span className="text-faint">
                      {s.project} / {s.environment}
                    </span>
                  </Link>
                );
              },
            },
            {
              header: "State",
              cell: (s) => (
                <div className="flex flex-col items-start gap-0.5">
                  <StatusBadge tone={serviceState(s).tone}>
                    {serviceState(s).label}
                  </StatusBadge>
                  {s.status && (
                    <span
                      className="text-bad max-w-xs truncate"
                      title={s.status}
                    >
                      {s.status}
                    </span>
                  )}
                </div>
              ),
            },
            {
              header: "Tasks",
              cell: (s) => (
                <span className="font-mono">{`${s.running}/${s.desiredCount}`}</span>
              ),
            },
            {
              header: "Rev",
              cell: (s) => <span className="text-muted">{s.revision}</span>,
            },
            {
              header: "Image",
              cell: (s) => <span className="font-mono">{s.spec.image}</span>,
            },
            {
              header: "Endpoints",
              className: "w-full",
              cell: (s) =>
                s.endpoints.length ? (
                  <div className="flex flex-col">
                    {s.endpoints.map((e) => (
                      <a
                        key={e}
                        href={e}
                        target="_blank"
                        rel="noreferrer"
                        className="text-muted hover:text-accent inline-flex items-center gap-1 font-mono"
                      >
                        {e.replace(/^https?:\/\//, "")}{" "}
                        <ExternalLink className="size-3" />
                      </a>
                    ))}
                  </div>
                ) : (
                  <span className="text-faint">—</span>
                ),
            },
          ]}
        />
      </Panel>
      {creating && <NewServiceDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

function NewServiceDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const { data: projects = [] } = useProjects();
  const [f, setF] = useState({
    project: "",
    newProject: "",
    env: "production",
    name: "",
    image: "",
    port: "80",
    replicas: "1",
    cpu: "0.1",
    memory: "128",
  });
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) =>
    setF({ ...f, [k]: e.target.value });
  const project = f.project || projects[0]?.name || "";
  const isNew = project === "" || project === "__new";
  const envs = projects.find((p) => p.name === project)?.environments ?? [
    "production",
  ];

  const create = useMutation({
    mutationFn: async () => {
      let proj = project;
      if (isNew) {
        proj = f.newProject;
        await api("POST", "/projects", { name: proj, environment: f.env });
      }
      const spec = {
        image: f.image,
        ports: f.port ? [{ container: Number(f.port) }] : [],
        resources: { cpu: Number(f.cpu), memory: Number(f.memory) },
        desiredCount: Number(f.replicas),
      };
      return api(
        "PUT",
        `/projects/${proj}/environments/${isNew ? f.env : f.env || envs[0]}/services/${f.name}`,
        spec,
      );
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["projects"] });
      onClose();
    },
  });

  return (
    <Dialog
      open
      onClose={onClose}
      title="New service"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={create.isPending || !f.name || !f.image}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create and deploy"}
          </Button>
        </>
      }
    >
      <div className="grid grid-cols-2 gap-2">
        <Field label="Project">
          <select
            value={isNew ? "__new" : project}
            onChange={set("project")}
            className="bg-bg border-line h-7 rounded-sm border px-1.5 text-xs"
          >
            {projects.map((p) => (
              <option key={p.id} value={p.name}>
                {p.name}
              </option>
            ))}
            <option value="__new">New project…</option>
          </select>
        </Field>
        {isNew ? (
          <Field label="Project name">
            <Input
              value={f.newProject}
              onChange={set("newProject")}
              placeholder="shop"
              className="font-mono"
            />
          </Field>
        ) : (
          <Field label="Environment">
            <select
              value={f.env}
              onChange={set("env")}
              className="bg-bg border-line h-7 rounded-sm border px-1.5 text-xs"
            >
              {envs.map((e) => (
                <option key={e}>{e}</option>
              ))}
            </select>
          </Field>
        )}
        <Field label="Service name">
          <Input
            value={f.name}
            onChange={set("name")}
            placeholder="api"
            className="font-mono"
          />
        </Field>
        <Field label="Image">
          <Input
            value={f.image}
            onChange={set("image")}
            placeholder="nginx:1.27"
            className="font-mono"
          />
        </Field>
        <Field
          label="HTTP port"
          hint="Gets a public URL. Leave empty for workers."
        >
          <Input value={f.port} onChange={set("port")} className="font-mono" />
        </Field>
        <Field label="Tasks">
          <Input
            type="number"
            min={0}
            max={100}
            value={f.replicas}
            onChange={set("replicas")}
          />
        </Field>
        <Field label="CPU (cores reserved)">
          <Input
            type="number"
            step={0.05}
            min={0.01}
            value={f.cpu}
            onChange={set("cpu")}
          />
        </Field>
        <Field label="Memory (MiB reserved)">
          <Input
            type="number"
            min={4}
            value={f.memory}
            onChange={set("memory")}
          />
        </Field>
      </div>
      {create.error && (
        <div className="mt-2">
          <Alert>
            {create.error instanceof ApiError
              ? create.error.message
              : "Could not create the service"}
          </Alert>
        </div>
      )}
    </Dialog>
  );
}
