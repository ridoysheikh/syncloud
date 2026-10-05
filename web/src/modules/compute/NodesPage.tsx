import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Plus } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useNodes, type Node } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { Dialog } from "@/ui/Dialog";
import { Alert, Button, Field, IconButton } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { NodesTable } from "./NodesTable";

interface JoinToken {
  id: string;
  token?: string;
  expiresAt: string;
  singleUse: boolean;
}

export function NodesPage() {
  const { data: nodes = [], isLoading } = useNodes();
  const qc = useQueryClient();
  const [adding, setAdding] = useState(false);

  const remove = useMutation({
    mutationFn: (id: string) => api("DELETE", `/nodes/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["nodes"] }),
  });

  const count = (s: Node["status"]) => nodes.filter((n) => n.status === s).length;

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
        <StatTile label="Ready" value={count("ready")} tone={count("ready") ? "ok" : undefined} />
        <StatTile label="Suspect" value={count("suspect")} tone={count("suspect") ? "warn" : undefined} />
        <StatTile label="Not ready" value={count("not_ready")} tone={count("not_ready") ? "bad" : undefined} />
        <StatTile label="Pending" value={count("pending")} hint="joined, never connected" />
      </div>
      <Panel title={`Nodes (${nodes.length})`} flush>
        <NodesTable
          nodes={nodes}
          loading={isLoading}
          onDelete={(n) =>
            confirm(`Remove node ${n.name}? Its certificate stops working immediately and it must join again.`) && remove.mutate(n.id)
          }
        />
      </Panel>
      <AddNodeDialog open={adding} onClose={() => setAdding(false)} />
    </div>
  );
}

function AddNodeDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [token, setToken] = useState<JoinToken | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      setToken(await api<JoinToken>("POST", "/nodes/join-tokens", { ttlMinutes: 60, singleUse: true, description: "dashboard" }));
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Request failed.");
    } finally {
      setBusy(false);
    }
  };
  const close = () => {
    setToken(null);
    setError(null);
    onClose();
  };
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
            A single-use join token valid for 1 hour lets one server join. The server generates its own key; the controller only signs its
            certificate.
          </p>
          {error && <Alert>{error}</Alert>}
        </div>
      ) : (
        <div className="flex flex-col gap-2.5">
          <Field label="Run on the new server (as root)">
            <CommandBox value={`syncloud-agent join --controller ${origin} --token ${token.token} && syncloud-agent run`} />
          </Field>
          <p className="text-faint text-xs">
            The one-line <code className="font-mono">curl … | sudo bash</code> installer that also installs Docker and WireGuard arrives with
            the installer (plan §6.1). Expires {new Date(token.expiresAt).toLocaleTimeString()}.
          </p>
        </div>
      )}
    </Dialog>
  );
}

function CommandBox({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="bg-bg border-line-strong flex items-start gap-1 rounded-sm border p-1.5 pl-2">
      <code className="flex-1 font-mono text-xs break-all select-all">{value}</code>
      <IconButton
        label="Copy"
        onClick={() =>
          navigator.clipboard?.writeText(value).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          })
        }
      >
        {copied ? <Check className="text-ok size-3.5" /> : <Copy className="size-3.5" />}
      </IconButton>
    </div>
  );
}
