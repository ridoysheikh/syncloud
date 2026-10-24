import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Network, Server } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { CONTROLLER_NODE } from "@/lib/nodes";
import { NodePicker } from "@/entities/nodes";
import { ChoiceCards } from "@/ui/choice";
import { Panel } from "@/ui/Panel";
import { Alert, Button, StatusBadge } from "@/ui/controls";
import { confirmDialog } from "@/ui/dialogs";

/* Project › Load balancer › Nodes (D22): the controller, or nodes the
   project chose, serve its public traffic. */

interface IngressMove {
  from: string[];
  fromController?: boolean;
  moved: string[];
  startedAt: string;
}

interface ProjectIngress {
  project: string;
  nodes: { name: string; address: string; state: string }[];
  targets: string[];
  base: string;
  move?: IngressMove;
  pending: string[];
}

const stateTone: Record<string, "ok" | "bad" | "neutral"> = {
  healthy: "ok",
  unhealthy: "bad",
};

const errText = (e: unknown) =>
  e instanceof ApiError ? e.message : "Request failed";

function moveSource(m: IngressMove) {
  const parts = [...m.from];
  if (m.fromController || m.from.length === 0) parts.unshift("the controller");
  return parts.join(", ");
}

export function ProjectIngressPanel({ project }: { project: string }) {
  const qc = useQueryClient();
  const key = ["ingress", project];
  const q = useQuery({
    queryKey: key,
    queryFn: () => api<ProjectIngress>("GET", `/projects/${project}/ingress`),
    refetchInterval: (query) =>
      query.state.data?.move ||
      query.state.data?.nodes.some((n) => n.state !== "healthy")
        ? 15_000
        : 60_000,
  });
  const current = (q.data?.nodes ?? []).map((n) => n.name);
  const [value, setValue] = useState<string[] | null>(null);
  useEffect(
    () => setValue(current.length ? current : null),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [current.join(",")],
  );
  const done = (d: ProjectIngress) => {
    qc.setQueryData(key, d);
    void qc.invalidateQueries({ queryKey: ["projects"] });
    void qc.invalidateQueries({ queryKey: ["addresses", project] });
    void qc.invalidateQueries({ queryKey: ["edges"] });
  };
  const save = useMutation({
    mutationFn: () =>
      api<ProjectIngress>("PUT", `/projects/${project}/ingress`, {
        nodes: value ?? [],
      }),
    onSuccess: done,
  });
  const cut = useMutation({
    mutationFn: () =>
      api<ProjectIngress>("POST", `/projects/${project}/ingress/cut-over`),
    onSuccess: done,
  });
  const next = value ?? [];
  const dirty = next.join(",") !== current.join(",");
  const invalid = value !== null && value.length === 0;
  const submit = async () => {
    const ok = await confirmDialog({
      title: next.length
        ? `Serve ${project} from ${next.join(", ")}?`
        : `Serve ${project} from the controller again?`,
      message: next.length
        ? `Those nodes run Traefik for this project and serve all of its public traffic; the controller stops serving it. Generated addresses move to ${next[0]}'s IP at once. Custom domains keep being served where they are now until their DNS points at the new nodes.`
        : "The controller serves this project again. Custom domains keep being served by the current nodes until their DNS points at the controller.",
      confirmLabel: "Switch",
      tone: "warn",
    });
    if (ok) save.mutate();
  };
  const d = q.data;
  return (
    <Panel title="Nodes">
      <div className="flex flex-col gap-2 text-xs">
        <p className="text-muted">
          Which servers take this project's public traffic (HTTP, HTTPS and
          public TCP/UDP ports). By default it enters through the controller.
          With its own nodes, traffic goes straight to them and never passes
          through the controller, and it keeps flowing while the controller is
          down.
        </p>
        <ChoiceCards
          label="Load balancer"
          value={value === null ? "controller" : "nodes"}
          onChange={(v) => setValue(v === "controller" ? null : (value ?? []))}
          options={[
            {
              value: "controller",
              title: "The controller",
              description:
                "Shared with the dashboard and every other project on it (and any edge pool).",
              icon: Network,
            },
            {
              value: "nodes",
              title: "Its own nodes",
              description:
                "Pick nodes below. They can still run tasks. Several projects can share them.",
              icon: Server,
            },
          ]}
        />
        {value !== null && (
          <NodePicker
            label="Load-balancer nodes"
            value={value}
            onChange={setValue}
            exclude={[CONTROLLER_NODE]}
            ordered
            detail={(name) => {
              const n = d?.nodes.find((x) => x.name === name);
              return n?.address ? `public ${n.address}` : null;
            }}
          />
        )}
        {invalid && <Alert tone="warn">Pick at least one node.</Alert>}
        {save.error && <Alert>{errText(save.error)}</Alert>}
        <div className="flex items-center gap-2">
          <Button
            variant="primary"
            disabled={!dirty || invalid || save.isPending}
            onClick={() => void submit()}
          >
            Save
          </Button>
        </div>
        {d && d.nodes.length > 0 && (
          <div className="border-line flex flex-col gap-1 border-t pt-2">
            <span className="text-muted font-medium">Now serving</span>
            <div className="flex flex-wrap gap-1.5">
              {d.nodes.map((n) => (
                <StatusBadge key={n.name} tone={stateTone[n.state] ?? "neutral"}>
                  {n.name}
                  {n.address && (
                    <span className="text-faint font-mono">{n.address}</span>
                  )}
                  <span className="text-faint">{n.state}</span>
                </StatusBadge>
              ))}
            </div>
            {d.nodes.every((n) => n.state === "unhealthy") && (
              <Alert>
                No load balancer answers: the project is unreachable.
              </Alert>
            )}
            <p className="text-muted">
              Generated addresses end in{" "}
              <code className="text-fg font-mono">.{d.base}</code> and reach{" "}
              {d.nodes[0]?.name} only. For real load balancing, point custom
              domains at every node:{" "}
              <code className="text-fg font-mono">
                {d.targets.join(", ") || "addresses not known yet"}
              </code>
              .
            </p>
          </div>
        )}
        {d?.move && (
          <Alert tone="info">
            <div className="flex flex-col gap-1.5">
              <span>
                Moving: {moveSource(d.move)} still serves{" "}
                {d.pending.length
                  ? d.pending.join(", ")
                  : "this project's domains"}{" "}
                until their DNS points at{" "}
                {d.targets.join(", ") || "the new load balancer"} (checked
                every minute). {d.move.moved.length} of{" "}
                {d.move.moved.length + d.pending.length} domains moved.
              </span>
              {cut.error && <Alert>{errText(cut.error)}</Alert>}
              <span>
                <Button
                  disabled={cut.isPending}
                  onClick={async () => {
                    if (
                      await confirmDialog({
                        title: "Cut over now?",
                        message: `${moveSource(d.move!)} stops serving this project at once. Domains whose DNS still points there stop working until it is changed.`,
                        confirmLabel: "Cut over",
                        tone: "danger",
                      })
                    )
                      cut.mutate();
                  }}
                >
                  Cut over now
                </Button>
              </span>
            </div>
          </Alert>
        )}
      </div>
    </Panel>
  );
}
