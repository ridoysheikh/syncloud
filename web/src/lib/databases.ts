import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";
import { subscribe } from "./stream";

export interface Range {
  min: number;
  max: number;
}

export interface DatabaseSpec {
  memory: Range;
  replicas: Range;
  cpu: number;
  persistence: "aof" | "rdb" | "none";
  evictionPolicy: string;
  nodes?: string[];
  autoscaling: { cpuTarget: number; memoryHigh: number };
  /** PostgreSQL only. */
  postgres?: { synchronous: boolean; maxConnections: number };
}

export interface DatabaseMember {
  id: string;
  name: string;
  kind: "data" | "sentinel";
  node: string;
  nodeId: string;
  state: string;
  ip: string;
  role: "primary" | "replica" | "sentinel" | "";
  linkUp: boolean;
  lagBytes: number;
  usedMemoryBytes: number;
  opsPerSec: number;
  clients: number;
  cpuPercent: number;
  error?: string;
  createdAt: string;
}

export interface DatabaseNetwork {
  /** Internal peers allowed in: project:P, environment:P/E, service:P/E/S, a CIDR or cluster. */
  access: string[];
  public: { enabled: boolean; allow: string[] };
}

export interface PublicEndpoint {
  enabled: boolean;
  /** False when it cannot work (no base domain); reason says why. */
  available: boolean;
  reason?: string;
  host?: string;
  readHost?: string;
  port: number;
  tls: boolean;
}

export interface DatabaseEngine {
  name: string;
  title: string;
  description: string;
  available: boolean;
  versions: string[];
  defaultVersion?: string;
  port: number;
  scheme: string;
  tlsScheme: string;
  features: string[];
}

export type DatabaseHealth =
  "healthy" | "degraded" | "starting" | "checking" | "down" | "deleting";

export interface Database {
  id: string;
  /** "" for a standalone database. */
  project: string;
  environment: string;
  standalone: boolean;
  network: DatabaseNetwork;
  public: PublicEndpoint;
  name: string;
  engine: string;
  version: string;
  spec: DatabaseSpec;
  state: {
    memoryMiB: number;
    replicas: number;
    primary: number;
    limitMiB: number;
  };
  status: string;
  health: DatabaseHealth;
  deleting: boolean;
  host: string;
  readHost: string;
  port: number;
  members: DatabaseMember[];
  usage: {
    usedMemoryBytes: number;
    maxMemoryBytes: number;
    keys: number;
    opsPerSec: number;
    clients: number;
    hitRate: number;
    uptimeSeconds: number;
  };
  autoscale: { memoryPercent: number; readCpu: number; blocked?: string };
  createdAt: string;
  updatedAt: string;
}

export const healthTone: Record<
  DatabaseHealth,
  "ok" | "warn" | "bad" | "info" | "neutral"
> = {
  healthy: "ok",
  degraded: "warn",
  starting: "info",
  checking: "neutral",
  down: "bad",
  deleting: "neutral",
};

export const EVICTION_POLICIES = [
  "noeviction",
  "allkeys-lru",
  "allkeys-lfu",
  "allkeys-random",
  "volatile-lru",
  "volatile-lfu",
  "volatile-random",
  "volatile-ttl",
];

/** API path of a database (names are unique in the cluster). */
export const dbPath = (name: string) =>
  `/databases/${encodeURIComponent(name)}`;

/** Dashboard path of a database's page. */
export const dbUrl = (d: { name: string }) =>
  `/databases/${encodeURIComponent(d.name)}`;

/** Where a database lives: "project / env", or "standalone". */
export const ownerOf = (d: { project: string; environment: string }) =>
  d.project ? `${d.project} / ${d.environment}` : "standalone";

/** Database engines (Valkey now; PostgreSQL planned). */
export function useDatabaseEngines() {
  return useQuery({
    queryKey: ["database-engines"],
    queryFn: async () =>
      (await api<{ items: DatabaseEngine[] }>("GET", "/databases/engines"))
        .items,
    staleTime: Infinity,
  });
}

const key = ["databases"];

/** Every database, kept live from the event stream. */
export function useDatabases() {
  const qc = useQueryClient();
  useEffect(
    () =>
      subscribe("database.updated", (e) => {
        const d = e.data as Database | { id: string; deleted: true };
        qc.setQueryData<Database[]>(key, (prev) => {
          if (!prev) return prev;
          if ("deleted" in d) return prev.filter((p) => p.id !== d.id);
          const i = prev.findIndex((p) => p.id === d.id);
          if (i === -1) return [...prev, d];
          const next = prev.slice();
          next[i] = d;
          return next;
        });
      }),
    [qc],
  );
  return useQuery({
    queryKey: key,
    queryFn: async () =>
      (await api<{ items: Database[] }>("GET", "/databases")).items,
    // Usage figures change without events; refresh them often.
    refetchInterval: 5_000,
    retry: false,
  });
}
