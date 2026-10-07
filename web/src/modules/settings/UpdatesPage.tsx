import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpCircle, RefreshCw } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { confirmAction } from "@/ui/dialogs";

interface UpgradeState {
  from: string;
  to: string;
  phase: string;
  message?: string;
  steps: { at: string; text: string }[];
  startedAt: string;
  finishedAt?: string;
  settleSeconds: number;
}
interface Upgrade {
  current: string;
  channel: string;
  latest?: string;
  latestError?: string;
  available: boolean;
  state?: UpgradeState | null;
}
interface AgentUpgrade {
  target: string;
  nodes: { id: string; name: string; version: string; arch: string; connected: boolean; outdated: boolean }[];
  rollout?: {
    state: string;
    message?: string;
    startedAt: string;
    nodes: { nodeId: string; node: string; from: string; state: string; error?: string }[];
  } | null;
}

const terminal = (p?: string) => !p || p === "done" || p === "rolled-back" || p === "failed";
const phaseTone = (p: string) => (p === "done" ? "ok" : p === "rolled-back" ? "warn" : p === "failed" ? "bad" : "info");
const errText = (e: unknown, f: string) => (e instanceof ApiError ? e.message : f);

/** Settings › Updates (§5.0.1): controller upgrades with automatic rollback, then agents node by node. */
export function UpdatesPage() {
  const qc = useQueryClient();
  const up = useQuery({
    queryKey: ["upgrade"],
    queryFn: () => api<Upgrade>("GET", "/system/upgrade"),
    // The controller restarts during an upgrade: keep polling through it.
    refetchInterval: (q) => (terminal(q.state.data?.state?.phase) ? 30_000 : 2000),
    retry: true,
  });
  const agents = useQuery({
    queryKey: ["agent-upgrade"],
    queryFn: () => api<AgentUpgrade>("GET", "/nodes/agent-upgrade"),
    refetchInterval: (q) => (q.state.data?.rollout?.state === "running" ? 2000 : 15_000),
  });
  const [version, setVersion] = useState("");
  const check = useMutation({
    mutationFn: () => api<Upgrade>("GET", "/system/upgrade?refresh=1"),
    onSuccess: (d) => qc.setQueryData(["upgrade"], d),
  });
  const start = useMutation({
    mutationFn: () => api<Upgrade>("POST", "/system/upgrade", { version }),
    onSuccess: (d) => qc.setQueryData(["upgrade"], d),
  });
  const roll = useMutation({
    mutationFn: (nodes: string[]) => api<AgentUpgrade>("POST", "/nodes/agent-upgrade", { nodes }),
    onSuccess: (d) => qc.setQueryData(["agent-upgrade"], d),
  });
  const u = up.data;
  const st = u?.state;
  const running = st && !terminal(st.phase);
  const outdated = agents.data?.nodes.filter((n) => n.outdated && n.connected) ?? [];
  const rollStatus = new Map(agents.data?.rollout?.nodes.map((n) => [n.nodeId, n]) ?? []);

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Settings"]} title="Updates" />
      <Panel
        title="Controller"
        actions={
          <Button variant="ghost" onClick={() => check.mutate()} disabled={check.isPending}>
            <RefreshCw className="size-3.5" /> Check now
          </Button>
        }
      >
        <div className="flex flex-col gap-2 text-xs">
          <div className="flex flex-wrap items-center gap-x-6 gap-y-1">
            <span>
              Running <span className="font-mono font-medium">{u?.current ?? "…"}</span>
            </span>
            <span className="text-muted">
              Channel <span className="font-mono">{u?.channel}</span>:{" "}
              {u?.latest ? <span className="text-fg font-mono">{u.latest}</span> : <span className="text-bad">{u?.latestError ?? "…"}</span>}
            </span>
            {u?.available && <StatusBadge tone="info">update available</StatusBadge>}
          </div>
          <div className="flex flex-wrap items-end gap-1.5">
            <Field label="Version (empty: the newest on the channel)">
              <Input value={version} onChange={(e) => setVersion(e.target.value)} placeholder={u?.latest} className="h-7 w-44 font-mono" />
            </Field>
            <Button
              variant="primary"
              disabled={!!running || start.isPending || (!version && !u?.available)}
              onClick={async () => (await confirmAction(`Upgrade the controller to ${version || u?.latest}? It restarts; tasks keep running.`)) && start.mutate()}
            >
              <ArrowUpCircle className="size-3.5" /> Upgrade
            </Button>
          </div>
          <p className="text-muted">
            The release is downloaded and verified, the database is snapshotted, and a guard restarts the controller with the new binary. If it is not healthy
            (database and system tasks) within 5 minutes, or stops being healthy in its first {st?.settleSeconds ?? 120} seconds, the previous binary and
            database are restored. Tasks keep running throughout; the dashboard is briefly unavailable.
          </p>
          {(start.error || check.error) && <Alert>{errText(start.error ?? check.error, "Failed")}</Alert>}
        </div>
      </Panel>

      {st && (
        <Panel title={running ? "Upgrade in progress" : "Last upgrade"}>
          <div className="flex flex-col gap-1.5 text-xs">
            <div className="flex items-center gap-2">
              <StatusBadge tone={phaseTone(st.phase)}>{st.phase}</StatusBadge>
              <span className="font-mono">
                {st.from} → {st.to}
              </span>
              <span className="text-muted">
                {new Date(st.startedAt).toLocaleString()}
                {st.finishedAt && ` – ${new Date(st.finishedAt).toLocaleTimeString()}`}
              </span>
            </div>
            {st.message && st.phase !== "done" && <Alert tone={st.phase === "failed" ? "bad" : "warn"}>{st.message}</Alert>}
            <div className="bg-bg border-line flex flex-col gap-0.5 rounded-sm border p-2 font-mono">
              {st.steps.map((s, i) => (
                <span key={i}>
                  <span className="text-faint">{new Date(s.at).toLocaleTimeString()}</span> {s.text}
                </span>
              ))}
            </div>
          </div>
        </Panel>
      )}

      <Panel
        title="Agents"
        actions={
          <Button disabled={outdated.length === 0 || agents.data?.rollout?.state === "running" || roll.isPending} onClick={() => roll.mutate([])}>
            <ArrowUpCircle className="size-3.5" /> Upgrade {outdated.length || ""} agent{outdated.length === 1 ? "" : "s"}
          </Button>
        }
        flush
      >
        <DataTable
          rows={agents.data?.nodes ?? []}
          rowKey={(n) => n.id}
          columns={[
            { header: "Node", cell: (n) => <span className="font-medium">{n.name}</span> },
            {
              header: "Agent",
              cell: (n) => (
                <span className={cn("font-mono", n.outdated ? "text-warn" : "text-muted")}>
                  {n.version || "—"}
                  {n.outdated && ` → ${agents.data?.target}`}
                </span>
              ),
            },
            { header: "Arch", cell: (n) => <span className="text-muted">{n.arch}</span> },
            { header: "Connected", cell: (n) => <StatusBadge tone={n.connected ? "ok" : "bad"}>{n.connected ? "yes" : "no"}</StatusBadge> },
            {
              header: "Last rollout",
              className: "w-full",
              cell: (n) => {
                const r = rollStatus.get(n.id);
                if (!r) return <span className="text-faint">—</span>;
                return (
                  <span className="flex items-center gap-1.5">
                    <StatusBadge tone={r.state === "done" ? "ok" : r.state === "failed" ? "bad" : r.state === "skipped" ? "neutral" : "info"}>{r.state}</StatusBadge>
                    <span className={r.state === "failed" ? "text-bad" : "text-muted"}>{r.error}</span>
                  </span>
                );
              },
            },
            {
              header: "",
              cell: (n) =>
                n.outdated && n.connected ? (
                  <Button variant="ghost" disabled={agents.data?.rollout?.state === "running"} onClick={() => roll.mutate([n.id])}>
                    Upgrade
                  </Button>
                ) : null,
            },
          ]}
        />
        <div className="text-muted flex flex-col gap-1 p-2 text-xs">
          <span>
            Agents are upgraded one node at a time to the controller's version: each receives the binary over its connection, restarts into it with its
            containers left running, and must reconnect before the next node starts. An agent that does not come back restores its previous binary.
          </span>
          {agents.data?.rollout?.message && <Alert tone="bad">{agents.data.rollout.message}</Alert>}
          {roll.error && <Alert>{errText(roll.error, "Failed")}</Alert>}
        </div>
      </Panel>
    </div>
  );
}
