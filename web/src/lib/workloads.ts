import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";
import { subscribe } from "./stream";

export interface Port {
  name?: string;
  container: number;
  protocol?: "http" | "tcp" | "udp";
}

export interface Spec {
  image: string;
  entrypoint?: string[];
  command?: string[];
  env?: Record<string, string>;
  /** Snapshot of the environment's shared variables (set by the platform). */
  sharedEnv?: Record<string, string>;
  ports?: Port[];
  resources: {
    cpu?: number;
    memory?: number;
    cpuLimit?: number;
    memoryLimit?: number;
  };
  placement: {
    strategy?: "spread" | "binpack";
    /** Pinned to one node by name. */
    node?: string;
    pools?: string[];
    /** Runs only on these nodes (within the project's allowed nodes). */
    nodes?: string[];
  };
  health?: {
    type: "http" | "tcp" | "cmd";
    path?: string;
    port?: string;
    command?: string[];
    interval?: number;
    timeout?: number;
    retries?: number;
    startPeriod?: number;
  };
  deployment?: { circuitBreaker?: boolean; rollback?: boolean };
}

export interface Deployment {
  id: string;
  fromRevision: number;
  toRevision: number;
  status: "in_progress" | "succeeded" | "failed" | "rolled_back" | "superseded";
  failedTasks: number;
  message: string;
  startedAt: string;
  finishedAt: string | null;
}

export const deploymentTone = {
  in_progress: "info",
  succeeded: "ok",
  failed: "bad",
  rolled_back: "warn",
  superseded: "neutral",
} as const;

export interface Service {
  id: string;
  project: string;
  environment: string;
  name: string;
  revision: number;
  desiredCount: number;
  running: number;
  pending: number;
  status: string;
  deleting: boolean;
  spec: Spec;
  endpoints: string[];
  vip: string;
  dnsName: string;
  deployment: Deployment | null;
  createdAt: string;
  updatedAt: string;
}

export interface Task {
  id: string;
  serviceId: string;
  project: string;
  environment: string;
  service: string;
  revision: number;
  nodeId: string;
  node: string;
  desired: "running" | "stopped";
  state:
    | "pending"
    | "pulling"
    | "starting"
    | "running"
    | "exited"
    | "failed"
    | "stopped"
    | "lost";
  ip: string;
  containerId: string;
  health: string;
  /** The controller's probe over the private network (§5.6). */
  central?: "ok" | "failing" | "unreachable";
  centralError?: string;
  exitCode: number;
  error: string;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
}

export interface Project {
  id: string;
  name: string;
  description: string;
  /** Nodes the project's services and jobs may run on; empty = any. */
  nodes: string[];
  environments: string[];
}

export const taskTone = {
  pending: "neutral",
  pulling: "info",
  starting: "info",
  running: "ok",
  exited: "bad",
  failed: "bad",
  stopped: "neutral",
  lost: "warn",
} as const;

export const servicePath = (s: {
  project: string;
  environment: string;
  name: string;
}) => `/projects/${s.project}/environments/${s.environment}/services/${s.name}`;

/** Every service, kept live from the event stream. */
export function useServices() {
  const qc = useQueryClient();
  useEffect(
    () =>
      subscribe("service.updated", (e) => {
        const s = e.data as Service | { id: string; deleted: true };
        qc.setQueryData<Service[]>(["services"], (prev) => {
          if (!prev) return prev;
          if ("deleted" in s) return prev.filter((p) => p.id !== s.id);
          const i = prev.findIndex((p) => p.id === s.id);
          if (i === -1) return [...prev, s];
          const next = prev.slice();
          next[i] = s;
          return next;
        });
      }),
    [qc],
  );
  return useQuery({
    queryKey: ["services"],
    queryFn: async () =>
      (await api<{ items: Service[] }>("GET", "/services")).items,
  });
}

export function useProjects() {
  return useQuery({
    queryKey: ["projects"],
    queryFn: async () =>
      (await api<{ items: Project[] }>("GET", "/projects")).items,
  });
}

/** Tasks of one service (or all active tasks), kept live. */
export function useTasks(service?: { id: string; path: string }) {
  const qc = useQueryClient();
  const key = service ? ["tasks", service.id] : ["tasks"];
  useEffect(
    () =>
      subscribe("task.updated", (e) => {
        const t = e.data as Task;
        if (service && t.serviceId !== service.id) return;
        qc.setQueryData<Task[]>(key, (prev) => {
          if (!prev) return prev;
          const i = prev.findIndex((p) => p.id === t.id);
          if (i === -1) return [t, ...prev];
          const next = prev.slice();
          next[i] = t;
          return service
            ? next
            : next.filter(
                (x) =>
                  x.desired === "running" ||
                  !["stopped", "lost", "exited", "failed"].includes(x.state),
              );
        });
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [qc, service?.id],
  );
  return useQuery({
    queryKey: key,
    queryFn: async () =>
      (
        await api<{ items: Task[] }>(
          "GET",
          service ? `${service.path}/tasks` : "/tasks",
        )
      ).items,
  });
}

/** Image of a Git-built service before its first build is deployed. */
export const AWAITING_BUILD = "@build";

export const envPath = (project: string, env: string) =>
  `/projects/${project}/environments/${env}`;

/** Shared variables of one environment of a project. */
export function useSharedVars(project: string, env: string) {
  return useQuery({
    queryKey: ["shared-vars", project, env],
    enabled: !!project && !!env,
    queryFn: async () =>
      (
        await api<{ variables: Record<string, string> }>(
          "GET",
          `${envPath(project, env)}/variables`,
        )
      ).variables,
  });
}

/** Dashboard URL of a service: services always live inside a project. */
export const serviceUrl = (s: {
  project: string;
  environment: string;
  name: string;
}) => `/projects/${s.project}/${s.environment}/services/${s.name}`;

export function serviceState(s: Service): {
  tone: "ok" | "warn" | "bad" | "info" | "neutral";
  label: string;
} {
  if (s.deleting) return { tone: "neutral", label: "deleting" };
  if (s.spec.image === AWAITING_BUILD)
    return { tone: "info", label: "awaiting build" };
  if (s.status) return { tone: "bad", label: "degraded" };
  if (s.desiredCount === 0) return { tone: "neutral", label: "stopped" };
  if (s.running >= s.desiredCount && s.pending === 0)
    return { tone: "ok", label: "healthy" };
  if (s.running === 0) return { tone: "warn", label: "starting" };
  return { tone: "info", label: "deploying" };
}
