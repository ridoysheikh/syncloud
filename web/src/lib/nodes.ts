import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";
import { subscribe } from "./stream";

export type NodeStatus = "pending" | "ready" | "suspect" | "not_ready";

export interface NodeInfo {
  hostname: string;
  os: string;
  kernel: string;
  arch: string;
  cpuCores: number;
  memoryBytes: number;
  diskBytes: number;
  dockerVersion: string;
  agentVersion: string;
}

export interface NodeMetrics {
  cpuPercent: number;
  memoryUsedBytes: number;
  memoryTotalBytes: number;
  diskUsedBytes: number;
  diskTotalBytes: number;
  load1: number;
  load5: number;
  load15: number;
  netRxBytes: number;
  netTxBytes: number;
  uptimeSeconds: number;
}

export interface Node {
  id: string;
  name: string;
  status: NodeStatus;
  statusAt: string;
  connected: boolean;
  lastSeenAt: string | null;
  createdAt: string;
  info: NodeInfo;
  metrics: NodeMetrics | null;
  schedulable: boolean;
  draining: boolean;
}

const key = ["nodes"];

/** All nodes, kept live from the event stream (node.updated / node.removed). */
export function useNodes() {
  const qc = useQueryClient();
  useEffect(() => {
    const upsert = subscribe("node.updated", (e) => {
      const n = e.data as Node;
      qc.setQueryData<Node[]>(key, (prev) => {
        if (!prev) return prev;
        const i = prev.findIndex((p) => p.id === n.id);
        if (i === -1) return [...prev, n].sort((a, b) => a.name.localeCompare(b.name));
        const next = prev.slice();
        next[i] = n;
        return next;
      });
    });
    const remove = subscribe("node.removed", (e) => {
      const { id } = e.data as { id: string };
      qc.setQueryData<Node[]>(key, (prev) => prev?.filter((p) => p.id !== id));
    });
    return () => {
      upsert();
      remove();
    };
  }, [qc]);

  return useQuery({
    queryKey: key,
    queryFn: async () => (await api<{ items: Node[] }>("GET", "/nodes")).items,
    // Live updates arrive over the stream; refetch occasionally to heal missed events.
    refetchInterval: 60_000,
  });
}

export const statusTone: Record<NodeStatus, "ok" | "warn" | "bad" | "neutral"> = {
  ready: "ok",
  suspect: "warn",
  not_ready: "bad",
  pending: "neutral",
};

export const statusLabel: Record<NodeStatus, string> = {
  ready: "Ready",
  suspect: "Suspect",
  not_ready: "Not ready",
  pending: "Pending",
};

export function bytes(n: number) {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

export function pct(used: number, total: number) {
  return total > 0 ? (used / total) * 100 : 0;
}

export function since(iso: string | null) {
  if (!iso) return "never";
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000);
  if (s < 10) return "just now";
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
