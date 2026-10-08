import { useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock, LockOpen, Plus, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { since } from "@/lib/nodes";
import type { Service } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import {
  Alert,
  Button,
  Field,
  IconButton,
  Input,
  StatusBadge,
  Toggle,
} from "@/ui/controls";
import { alertDialog, confirmChoice } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";

/* Project › Settings (Phase 15d): environments with their deploy policy,
   cloning, and deleting with everything in them. */

export interface EnvironmentInfo {
  id: string;
  name: string;
  createdAt: string;
  autoDeploy: boolean;
  lock: { reason: string; by: string; at: string } | null;
  deleting: boolean;
}

export function useEnvironments(project: string) {
  return useQuery({
    queryKey: ["environments", project],
    queryFn: async () =>
      (
        await api<{ items: EnvironmentInfo[] }>(
          "GET",
          `/projects/${project}/environments`,
        )
      ).items,
    enabled: !!project,
    refetchInterval: (q) =>
      q.state.data?.some((e) => e.deleting) ? 3000 : false,
  });
}

const errText = (e: unknown, fallback: string) =>
  e instanceof ApiError ? e.message : fallback;

/** The banner a locked environment shows on its pages. */
export function DeployLockBanner({
  project,
  env,
}: {
  project: string;
  env: string;
}) {
  const { data } = useEnvironments(project);
  const e = data?.find((x) => x.name === env);
  if (!e?.lock && !e?.deleting) return null;
  if (e.deleting)
    return (
      <Alert tone="warn">
        {env} is being deleted: its services stop, then it goes.
      </Alert>
    );
  return (
    <Alert tone="warn">
      <span className="inline-flex flex-wrap items-center gap-1">
        <Lock className="size-3.5" /> Deploys to {env} are locked:{" "}
        <span className="text-fg">{e.lock!.reason}</span>
        <span className="text-muted">
          ({e.lock!.by}, {since(e.lock!.at)}). Rollbacks and cancels still work;
          unlock it in the project's settings.
        </span>
      </span>
    </Alert>
  );
}

function EnvironmentRow({
  project,
  e,
  services,
  last,
}: {
  project: string;
  e: EnvironmentInfo;
  services: number;
  last: boolean;
}) {
  const qc = useQueryClient();
  const [reason, setReason] = useState("");
  const [locking, setLocking] = useState(false);
  const done = () => {
    void qc.invalidateQueries({ queryKey: ["environments", project] });
    void qc.invalidateQueries({ queryKey: ["projects"] });
    void qc.invalidateQueries({ queryKey: ["services"] });
  };
  const policy = useMutation({
    mutationFn: (body: object) =>
      api("PUT", `/projects/${project}/environments/${e.name}/policy`, body),
    onSuccess: () => {
      setLocking(false);
      setReason("");
      done();
    },
  });
  const del = useMutation({
    mutationFn: (force: boolean) =>
      api(
        "DELETE",
        `/projects/${project}/environments/${e.name}${force ? "?force=true" : ""}`,
      ),
    onSuccess: done,
    onError: (err) =>
      void alertDialog({
        title: `Could not delete ${e.name}`,
        message: errText(err, "The request failed."),
        tone: "danger",
      }),
  });
  const remove = async () => {
    const r = await confirmChoice({
      title: `Delete environment ${e.name}?`,
      message:
        services > 0
          ? `It has ${services} ${services === 1 ? "service" : "services"}. Databases must be deleted first; their data is never deleted along with an environment.`
          : "It has no services.",
      tone: "danger",
      typeToConfirm: e.name,
      option:
        services > 0
          ? {
              label: `Delete its ${services} ${services === 1 ? "service" : "services"} too`,
              hint: "Their tasks stop, then the environment goes. Without this, delete them first.",
            }
          : undefined,
    });
    if (r.ok) del.mutate(r.option);
  };
  return (
    <li className="flex flex-col gap-2 py-2.5 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="font-mono text-sm">{e.name}</span>
          <span className="text-faint text-xs">
            {services} {services === 1 ? "service" : "services"}
          </span>
          {e.lock && (
            <StatusBadge tone="warn">
              <Lock className="size-3" /> locked
            </StatusBadge>
          )}
          {e.deleting && <StatusBadge tone="warn">deleting</StatusBadge>}
        </span>
        <span className="flex items-center gap-1">
          {e.lock ? (
            <Button
              variant="ghost"
              disabled={policy.isPending || e.deleting}
              onClick={() => policy.mutate({ locked: false })}
            >
              <LockOpen className="size-3.5" /> Unlock
            </Button>
          ) : (
            <Button
              variant="ghost"
              disabled={e.deleting}
              onClick={() => setLocking(!locking)}
            >
              <Lock className="size-3.5" /> Lock deploys
            </Button>
          )}
          <IconButton
            label={`Delete environment ${e.name}`}
            disabled={last || e.deleting || del.isPending}
            title={
              last ? "A project keeps at least one environment" : undefined
            }
            onClick={() => void remove()}
          >
            <Trash2 className="size-3.5" />
          </IconButton>
        </span>
      </div>
      {e.lock && (
        <p className="text-muted text-xs">
          {e.lock.reason}{" "}
          <span className="text-faint">
            ({e.lock.by}, {since(e.lock.at)})
          </span>
        </p>
      )}
      {locking && !e.lock && (
        <form
          onSubmit={(ev) => {
            ev.preventDefault();
            if (reason.trim().length >= 3)
              policy.mutate({ locked: true, reason: reason.trim() });
          }}
          className="flex flex-wrap items-end gap-1.5"
        >
          <Field
            label="Why"
            hint="Shown to whoever tries to deploy. Rollbacks and cancels still work."
          >
            <Input
              value={reason}
              onChange={(ev) => setReason(ev.target.value)}
              placeholder="release freeze until Monday"
              className="w-72 max-w-full"
              autoFocus
            />
          </Field>
          <Button
            type="submit"
            variant="primary"
            disabled={reason.trim().length < 3 || policy.isPending}
          >
            Lock
          </Button>
          <Button variant="ghost" onClick={() => setLocking(false)}>
            Cancel
          </Button>
        </form>
      )}
      <Toggle
        checked={e.autoDeploy}
        disabled={policy.isPending || e.deleting}
        onChange={(v) => policy.mutate({ autoDeploy: v })}
        label="Builds deploy themselves"
        hint={
          e.autoDeploy
            ? "A successful build rolls out at once (when its Git source deploys automatically too)."
            : "Builds still run; deploy each one by hand from the Builds tab."
        }
      />
      {policy.error && (
        <Alert>{errText(policy.error, "Could not change the policy")}</Alert>
      )}
    </li>
  );
}

export function EnvironmentsPanel({
  project,
  services,
}: {
  project: string;
  services: Service[];
}) {
  const qc = useQueryClient();
  const { data: envs = [] } = useEnvironments(project);
  const [name, setName] = useState("");
  const [from, setFrom] = useState("");
  const [start, setStart] = useState(false);
  const add = useMutation({
    mutationFn: () =>
      api("POST", `/projects/${project}/environments`, {
        name,
        cloneFrom: from || undefined,
        startServices: from ? start : undefined,
      }),
    onSuccess: () => {
      setName("");
      void qc.invalidateQueries({ queryKey: ["environments", project] });
      void qc.invalidateQueries({ queryKey: ["projects"] });
      void qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (name) add.mutate();
  };
  const select =
    "bg-bg border-line-strong h-8 rounded-input border px-2 text-sm outline-none";
  return (
    <Panel title="Environments">
      <div className={cn("flex flex-col", gap)}>
        <ul className="divide-line flex flex-col divide-y">
          {envs.map((e) => (
            <EnvironmentRow
              key={e.id}
              project={project}
              e={e}
              services={services.filter((s) => s.environment === e.name).length}
              last={envs.length === 1}
            />
          ))}
        </ul>
        <form
          onSubmit={submit}
          className="border-line flex flex-wrap items-end gap-1.5 border-t pt-3"
        >
          <Field label="New environment">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value.toLowerCase())}
              placeholder="e.g. qa"
              className="w-40 font-mono"
            />
          </Field>
          <Field label="Copy from">
            <select
              value={from}
              onChange={(e) => setFrom(e.target.value)}
              className={select}
            >
              <option value="">nothing (empty)</option>
              {envs
                .filter((e) => !e.deleting)
                .map((e) => (
                  <option key={e.id} value={e.name}>
                    {e.name}
                  </option>
                ))}
            </select>
          </Field>
          {from && (
            <span className="flex h-8 items-center">
              <Toggle
                checked={start}
                onChange={setStart}
                label="Start the copied services"
              />
            </span>
          )}
          <Button type="submit" disabled={!name || add.isPending}>
            <Plus className="size-3.5" /> {from ? "Copy" : "Add"}
          </Button>
        </form>
        {from && (
          <p className="text-faint text-xs">
            Copies the shared variables, every service's current spec (at 0
            tasks unless started), jobs and security group memberships. Custom
            domains, public ports, Git sources, S3 bindings and databases are
            not copied.
          </p>
        )}
        {add.error && (
          <Alert>{errText(add.error, "Could not add the environment")}</Alert>
        )}
      </div>
    </Panel>
  );
}

export function DangerZone({
  project,
  services,
}: {
  project: string;
  services: Service[];
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const del = useMutation({
    mutationFn: (force: boolean) =>
      api("DELETE", `/projects/${project}${force ? "?force=true" : ""}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["projects"] });
      void qc.invalidateQueries({ queryKey: ["services"] });
      const to: string = "/projects";
      void navigate({ to });
    },
    onError: (err) =>
      void alertDialog({
        title: `Could not delete ${project}`,
        message: errText(err, "The request failed."),
        tone: "danger",
      }),
  });
  const n = services.length;
  const remove = async () => {
    const r = await confirmChoice({
      title: `Delete project ${project}?`,
      message:
        n > 0
          ? `It has ${n} ${n === 1 ? "service" : "services"}. Databases must be deleted first; their data is never deleted along with a project.`
          : "It has no services. Its environments, settings and access policies go with it.",
      tone: "danger",
      typeToConfirm: project,
      option:
        n > 0
          ? {
              label: `Delete its ${n} ${n === 1 ? "service" : "services"} too`,
              hint: "Their tasks stop, then the project goes.",
            }
          : undefined,
    });
    if (r.ok) del.mutate(r.option);
  };
  return (
    <Panel title="Danger zone">
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
        <span className="text-muted">
          Delete the project, its environments and, if you choose, its services.
        </span>
        <Button
          variant="danger"
          disabled={del.isPending}
          onClick={() => void remove()}
        >
          <Trash2 className="size-3.5" /> Delete project
        </Button>
      </div>
    </Panel>
  );
}
