import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, LayoutGrid, Loader2, Plus, Rows3 } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { api, ApiError } from "@/lib/api";
import { useNodes, type Node } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { Dialog } from "@/ui/Dialog";
import { Alert, Button, Field, IconButton } from "@/ui/controls";
import { cn, gap, pad } from "@/ui/cn";
import { NodeGrid, NodesTable, nodeUrl } from "@/entities/nodes";
import { confirmAction } from "@/ui/dialogs";
import { CopyBox } from "@/ui/CopyBox";

type View = "table" | "cards";
const VIEW_KEY = "syncloud.nodes.view";

/** The remembered table/cards choice; storage can be unavailable. */
function useView() {
  const [view, set] = useState<View>(() => {
    try {
      return localStorage.getItem(VIEW_KEY) === "cards" ? "cards" : "table";
    } catch {
      return "table";
    }
  });
  const setView = (v: View) => {
    set(v);
    try {
      localStorage.setItem(VIEW_KEY, v);
    } catch {
      /* not remembered */
    }
  };
  return [view, setView] as const;
}

interface JoinToken {
  id: string;
  token?: string;
  command?: string;
  expiresAt: string;
  singleUse: boolean;
}

export function NodesPage() {
  const { data: nodes = [], isLoading } = useNodes();
  const qc = useQueryClient();
  const [adding, setAdding] = useState(false);
  const [view, setView] = useView();

  const remove = useMutation({
    mutationFn: (id: string) => api("DELETE", `/nodes/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["nodes"] }),
  });

  const schedule = useMutation({
    mutationFn: ({
      id,
      action,
    }: {
      id: string;
      action: "cordon" | "drain" | "uncordon";
    }) =>
      action === "drain"
        ? api("POST", `/nodes/${id}/drain`)
        : api("PUT", `/nodes/${id}/schedulable`, {
            schedulable: action === "uncordon",
          }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["nodes"] }),
  });

  const count = (s: Node["status"]) =>
    nodes.filter((n) => n.status === s).length;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Compute"]}
        title="Nodes"
        actions={
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Plus className="size-3.5" /> Add node
          </Button>
        }
      />
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile
          label="Ready"
          value={count("ready")}
          tone={count("ready") ? "ok" : undefined}
        />
        <StatTile
          label="Suspect"
          value={count("suspect")}
          tone={count("suspect") ? "warn" : undefined}
        />
        <StatTile
          label="Not ready"
          value={count("not_ready")}
          tone={count("not_ready") ? "bad" : undefined}
        />
        <StatTile
          label="Pending"
          value={count("pending")}
          hint="joined, never connected"
        />
      </div>
      <Panel
        title={`Nodes (${nodes.length})`}
        flush
        actions={
          <span className="flex items-center" role="group" aria-label="View">
            <IconButton label="Table" aria-pressed={view === "table"} onClick={() => setView("table")} className={cn(view === "table" && "text-fg bg-hover")}>
              <Rows3 className="size-3.5" />
            </IconButton>
            <IconButton label="Cards" aria-pressed={view === "cards"} onClick={() => setView("cards")} className={cn(view === "cards" && "text-fg bg-hover")}>
              <LayoutGrid className="size-3.5" />
            </IconButton>
          </span>
        }
      >
        {view === "cards" && nodes.length > 0 ? (
          <div className={pad}>
            <NodeGrid nodes={nodes} />
          </div>
        ) : (
          <NodesTable
            nodes={nodes}
            loading={isLoading}
            onSchedule={(n, action) => schedule.mutate({ id: n.id, action })}
            onDelete={async (n) =>
              (await confirmAction(
                `Remove node ${n.name}? Its certificate stops working immediately and it must join again.`,
              )) && remove.mutate(n.id)
            }
          />
        )}
      </Panel>
      <AddNodeDialog open={adding} onClose={() => setAdding(false)} />
    </div>
  );
}

function AddNodeDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const [token, setToken] = useState<JoinToken | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { data: nodes = [] } = useNodes();
  // Nodes that existed when the token was made; anything else joined with it.
  const [known, setKnown] = useState<Set<string> | null>(null);
  const [since, setSince] = useState(0);

  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      setKnown(new Set(nodes.map((n) => n.id)));
      setSince(Date.now());
      setToken(
        await api<JoinToken>("POST", "/nodes/join-tokens", {
          ttlMinutes: 60,
          singleUse: true,
          description: "dashboard",
        }),
      );
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Request failed.");
    } finally {
      setBusy(false);
    }
  };
  const close = () => {
    setToken(null);
    setKnown(null);
    setError(null);
    onClose();
  };
  const joined = known ? nodes.filter((n) => !known.has(n.id)) : [];
  const origin = window.location.origin;

  return (
    <Dialog
      open={open}
      onClose={close}
      title="Add a node"
      footer={
        token ? (
          <Button variant="primary" onClick={close}>
            Done
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button variant="primary" onClick={create} disabled={busy}>
              Create join token
            </Button>
          </>
        )
      }
    >
      {!token ? (
        <div className="flex flex-col gap-2 text-xs">
          <p className="text-muted">
            A single-use join token valid for 1 hour lets one server join. The
            server generates its own key; the controller only signs its
            certificate.
          </p>
          {error && <Alert>{error}</Alert>}
        </div>
      ) : (
        <div className="flex flex-col gap-2.5">
          <Field label="Run on the new server">
            <CopyBox wrap
              value={token.command ?? `curl -fsSL ${origin}/join.sh | sudo bash -s -- --token ${token.token}`}
            />
          </Field>
          <p className="text-faint text-xs">
            Checks the controller, installs Docker and nftables if missing
            (Docker takes 1–3 minutes), downloads the agent from this
            controller (checksum-verified), joins, and waits until it connects.
            It prints every step with its time. Add{" "}
            <code className="font-mono">--advertise-address IP</code> if other
            nodes must reach this one on a different address. Expires{" "}
            {new Date(token.expiresAt).toLocaleTimeString()}.
          </p>
          <JoinProgress nodes={joined} since={since} />
          <details className="text-xs">
            <summary className="text-muted cursor-pointer">
              Agent already installed?
            </summary>
            <div className="mt-1.5">
              <CopyBox wrap
                value={`syncloud-agent join --controller ${origin} --token ${token.token} && syncloud-agent run`}
              />
            </div>
          </details>
        </div>
      )}
    </Dialog>
  );
}

function fmtElapsed(ms: number) {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** Live progress of servers joining with the token just created. */
function JoinProgress({ nodes, since }: { nodes: Node[]; since: number }) {
  const [now, setNow] = useState(Date.now());
  const waiting = nodes.length === 0 || nodes.some((n) => n.status !== "ready");
  useEffect(() => {
    if (!waiting) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [waiting]);
  const elapsed = now - since;
  const n = nodes[0];
  const stages = [
    { label: "Join token created", done: true },
    {
      label: n ? `Joined as ${n.name}` : "Waiting for the server to run the command",
      done: !!n,
    },
    { label: "Agent connected", done: !!n && (n.connected || n.status === "ready") },
    { label: "Ready for tasks", done: n?.status === "ready" },
  ];
  const current = stages.findIndex((s) => !s.done);
  return (
    <div className="bg-raised border-line flex flex-col gap-1.5 rounded-sm border p-2.5 text-xs">
      <div className="flex items-center justify-between">
        <span className="text-faint text-[11px] font-medium tracking-wide uppercase">Progress</span>
        <span className="text-faint font-mono text-[11px] tabular-nums">{fmtElapsed(elapsed)}</span>
      </div>
      <ol className="flex flex-col gap-1">
        {stages.map((s, i) => (
          <li key={s.label} className={cn("flex items-center gap-2", !s.done && i !== current && "text-faint")}>
            {s.done ? (
              <Check className="text-ok size-3.5 shrink-0" />
            ) : i === current ? (
              <Loader2 className="text-accent size-3.5 shrink-0 animate-spin" />
            ) : (
              <span className="border-line-strong size-3.5 shrink-0 rounded-full border" />
            )}
            <span className="min-w-0 truncate">{s.label}</span>
          </li>
        ))}
      </ol>
      {n?.status === "ready" && (
        <Link to={nodeUrl(n.name) as string} className="text-accent mt-0.5 hover:underline">
          Open {n.name} →
        </Link>
      )}
      {nodes.length > 1 && <span className="text-muted">{nodes.length} servers joined.</span>}
      {!n && elapsed > 4 * 60_000 && (
        <p className="text-muted mt-0.5">
          Still waiting? The script shows each step on the server. If it stopped at a step, the message there says
          why. If it finished but nothing shows here, the token may have expired or been used.
        </p>
      )}
      {n && n.status !== "ready" && elapsed > 6 * 60_000 && (
        <p className="text-muted mt-0.5">
          {n.name} joined but isn&apos;t connected yet. On it, run{" "}
          <code className="font-mono">journalctl -u syncloud-agent -f</code>. Usually the controller&apos;s agent
          port (TCP 7443) or WireGuard (UDP 51820) is blocked.
        </p>
      )}
    </div>
  );
}
