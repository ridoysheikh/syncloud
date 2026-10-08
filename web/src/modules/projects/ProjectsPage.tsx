import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { FolderKanban, Plus } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import {
  useProjects,
  useServices,
  type Project,
  type Service,
  serviceState,
} from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap, pad } from "@/ui/cn";
import { TaskDots } from "@/entities/TaskDots";
import { useLazyList } from "@/ui/paging";

const nameRE = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/;

/** All projects as cards: environments, services and their health. */
export function ProjectsPage() {
  const { data: projects = [], isLoading } = useProjects();
  const { data: services = [] } = useServices();
  const newTo: string = "/projects/new";
  const lazy = useLazyList(projects);
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={[]}
        title="Projects"
        actions={
          <Link to={newTo}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New project
            </Button>
          </Link>
        }
      />
      {!isLoading && projects.length === 0 ? (
        <Panel>
          <EmptyState icon={FolderKanban} title="No projects yet">
            A project groups the services of one application. Each project has
            environments (production, staging, …) with their own services and
            shared variables.
          </EmptyState>
        </Panel>
      ) : (
        <>
          <div
            className={cn(
              "grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3",
              gap,
            )}
          >
            {lazy.shown.map((p) => (
              <ProjectCard
                key={p.id}
                project={p}
                services={services.filter((s) => s.project === p.name)}
              />
            ))}
          </div>
          {lazy.more}
        </>
      )}
    </div>
  );
}

function ProjectCard({
  project: p,
  services,
}: {
  project: Project;
  services: Service[];
}) {
  const to: string = `/projects/${p.name}`;
  const states = services.map(serviceState);
  const bad = states.filter((s) => s.tone === "bad").length;
  const running = services.reduce((n, s) => n + s.running, 0);
  const desired = services.reduce((n, s) => n + s.desiredCount, 0);
  return (
    <Link
      to={to}
      className={cn(
        "bg-surface border-line hover:border-line-strong flex flex-col gap-2 rounded-md border transition-colors",
        pad,
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold">{p.name}</div>
          {p.description && (
            <div className="text-muted truncate text-xs">{p.description}</div>
          )}
        </div>
        {services.length > 0 && (
          <StatusBadge tone={bad ? "bad" : running < desired ? "info" : "ok"}>
            {bad
              ? `${bad} degraded`
              : running < desired
                ? "deploying"
                : "healthy"}
          </StatusBadge>
        )}
      </div>
      <div className="flex flex-wrap gap-1">
        {p.environments.map((e) => (
          <span
            key={e}
            className="border-line text-muted rounded-sm border px-1.5 text-xs"
          >
            {e}
          </span>
        ))}
      </div>
      <div className="text-muted flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <span>
          <span className="text-fg font-mono">{services.length}</span> service
          {services.length === 1 ? "" : "s"}
        </span>
        <span>
          <span className="text-fg font-mono">
            {running}/{desired}
          </span>{" "}
          tasks
        </span>
        {desired > 0 && (
          <TaskDots
            running={running}
            starting={services.reduce((n, s) => n + s.pending, 0)}
            desired={desired}
            max={16}
          />
        )}
      </div>
    </Link>
  );
}

/** Full-page form for a new project and its first environment. */
export function NewProjectPage() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [env, setEnv] = useState("production");
  const create = useMutation({
    mutationFn: () =>
      api<Project>("POST", "/projects", {
        name,
        description,
        environment: env,
      }),
    onSuccess: (p) => {
      qc.invalidateQueries({ queryKey: ["projects"] });
      const to: string = `/projects/${p.name}`;
      void navigate({ to });
    },
  });
  const nameErr =
    name && !nameRE.test(name) ? "lowercase letters, digits and dashes" : "";
  const envErr =
    env && !nameRE.test(env) ? "lowercase letters, digits and dashes" : "";
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (name && !nameErr && !envErr) create.mutate();
  };
  return (
    <div className={cn("mx-auto flex w-full max-w-2xl flex-col", gap)}>
      <PageHeader crumbs={["Projects"]} title="New project" />
      <Panel>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <Field
            label="Name"
            hint={
              nameErr ||
              "Used in URLs and internal DNS names, e.g. web.production.shop."
            }
          >
            <Input
              value={name}
              onChange={(e) => setName(e.target.value.toLowerCase())}
              placeholder="shop"
              className="font-mono"
              autoFocus
            />
          </Field>
          <Field label="Description (optional)">
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Storefront and its workers"
            />
          </Field>
          <Field
            label="First environment"
            hint={
              envErr || "Add more (staging, preview, …) from the project page."
            }
          >
            <Input
              value={env}
              onChange={(e) => setEnv(e.target.value.toLowerCase())}
              className="font-mono"
            />
          </Field>
          {create.error && (
            <Alert>
              {create.error instanceof ApiError
                ? create.error.message
                : "Could not create the project"}
            </Alert>
          )}
          <div className="flex justify-end gap-1.5">
            <Button
              type="button"
              variant="ghost"
              onClick={() => history.back()}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={create.isPending || !name || !!nameErr || !!envErr}
            >
              {create.isPending ? "Creating…" : "Create project"}
            </Button>
          </div>
        </form>
      </Panel>
    </div>
  );
}
