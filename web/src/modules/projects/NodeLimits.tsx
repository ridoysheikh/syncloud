import { useEffect, useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, ApiError } from "@/lib/api";
import { bytes, CONTROLLER_NODE, pct, useNodes } from "@/lib/nodes";
import { ControllerTag, NodeStatus } from "@/entities/nodes";
import { useTasks, type Service } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import { Alert, Button, StatusBadge } from "@/ui/controls";
import { cn } from "@/ui/cn";

/**
 * Checkboxes over the cluster's nodes. `limit` restricts the choice to a
 * project's allowed nodes; names in `value` that are not (or no longer)
 * nodes stay listed so they can be removed.
 */
function NodeChecklist({
  value,
  onChange,
  limit,
  counts,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  limit?: string[];
  /** Running tasks per node name, shown beside each node. */
  counts?: Record<string, number>;
}) {
  const { data: nodes = [] } = useNodes();
  const names = useMemo(() => {
    const all = new Set(nodes.map((n) => n.name));
    for (const v of value) all.add(v);
    return [...all]
      .filter((n) => !limit?.length || limit.includes(n))
      .sort((a, b) =>
        a === CONTROLLER_NODE
          ? -1
          : b === CONTROLLER_NODE
            ? 1
            : a.localeCompare(b),
      );
  }, [nodes, value, limit]);
  const toggle = (n: string) =>
    onChange(
      value.includes(n) ? value.filter((x) => x !== n) : [...value, n].sort(),
    );
  return (
    <ul className="border-line divide-line divide-y rounded-sm border">
      {names.map((name) => {
        const n = nodes.find((x) => x.name === name);
        const m = n?.metrics;
        return (
          <li key={name}>
            <label className="hover:bg-hover/50 flex min-h-8 cursor-pointer flex-wrap items-center gap-x-2 gap-y-0.5 px-2 py-1 text-xs">
              <input
                type="checkbox"
                checked={value.includes(name)}
                onChange={() => toggle(name)}
              />
              <span className="font-medium">{name}</span>
              <ControllerTag name={name} />
              {n ? (
                <NodeStatus node={n} />
              ) : (
                <StatusBadge tone="neutral">not joined</StatusBadge>
              )}
              <span className="text-muted ml-auto flex items-center gap-2">
                {counts && (
                  <span>
                    {counts[name] ?? 0} task{counts[name] === 1 ? "" : "s"}
                  </span>
                )}
                {n && m && (
                  <span className="hidden sm:inline">
                    {n.info.cpuCores} cores · CPU {m.cpuPercent.toFixed(0)}% ·
                    mem {pct(m.memoryUsedBytes, m.memoryTotalBytes).toFixed(0)}%
                    of {bytes(m.memoryTotalBytes)}
                  </span>
                )}
              </span>
            </label>
          </li>
        );
      })}
    </ul>
  );
}

/** "Any node" or "only these nodes", with the checklist for the latter. */
function NodeLimitEditor({
  value,
  onChange,
  limit,
  counts,
  anyLabel,
}: {
  value: string[] | null;
  onChange: (v: string[] | null) => void;
  limit?: string[];
  counts?: Record<string, number>;
  anyLabel: string;
}) {
  return (
    <div className="flex flex-col gap-2 text-xs">
      <div className="flex flex-wrap gap-x-4 gap-y-1">
        <label className="flex items-center gap-1.5">
          <input
            type="radio"
            checked={value === null}
            onChange={() => onChange(null)}
          />
          {anyLabel}
        </label>
        <label className="flex items-center gap-1.5">
          <input
            type="radio"
            checked={value !== null}
            onChange={() => onChange(value ?? [])}
          />
          Only these nodes
        </label>
      </div>
      {value !== null && (
        <NodeChecklist
          value={value}
          onChange={onChange}
          limit={limit}
          counts={counts}
        />
      )}
    </div>
  );
}

const errText = (e: unknown) =>
  e instanceof ApiError ? e.message : "Request failed";

/** Task counts per node name for a set of services. */
function useTaskCounts(serviceIds: string[]) {
  const { data: tasks = [] } = useTasks();
  return useMemo(() => {
    const out: Record<string, number> = {};
    for (const t of tasks) {
      if (t.desired === "running" && serviceIds.includes(t.serviceId)) {
        out[t.node] = (out[t.node] ?? 0) + 1;
      }
    }
    return out;
  }, [tasks, serviceIds]);
}

/** Project settings: the nodes its services and jobs may run on (§6.3). */
export function ProjectNodesPanel({
  project,
  nodes,
  services,
}: {
  project: string;
  nodes: string[];
  services: Service[];
}) {
  const qc = useQueryClient();
  const [value, setValue] = useState<string[] | null>(
    nodes.length ? nodes : null,
  );
  useEffect(() => setValue(nodes.length ? nodes : null), [nodes]);
  const counts = useTaskCounts(
    useMemo(() => services.map((s) => s.id), [services]),
  );
  const save = useMutation({
    mutationFn: () =>
      api("PUT", `/projects/${project}/nodes`, { nodes: value ?? [] }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["projects"] }),
  });
  const next = value ?? [];
  const dirty = next.join(",") !== nodes.join(",");
  const invalid = value !== null && value.length === 0;
  return (
    <Panel title="Allowed nodes">
      <div className="flex flex-col gap-2 text-xs">
        <p className="text-muted">
          Where this project's services and jobs may run. Each service can
          narrow it further under its Placement tab. Tasks on a node you remove
          move to an allowed node, the new task starting first. Naming the
          controller lets the project run there even when it takes no general
          workloads.
        </p>
        <NodeLimitEditor
          value={value}
          onChange={setValue}
          counts={counts}
          anyLabel="Any schedulable node"
        />
        {invalid && <Alert tone="warn">Pick at least one node.</Alert>}
        {save.error && <Alert>{errText(save.error)}</Alert>}
        <div className="flex items-center gap-2">
          <Button
            variant="primary"
            disabled={!dirty || invalid || save.isPending}
            onClick={() => save.mutate()}
          >
            Save
          </Button>
          {save.isSuccess && !dirty && (
            <span className="text-ok">Saved. Tasks are moving if needed.</span>
          )}
        </div>
      </div>
    </Panel>
  );
}

/** Service placement: strategy and the nodes it may run on. */
export function ServicePlacementPanel({
  path,
  service,
  projectNodes,
  projectTo,
}: {
  path: string;
  service: Service;
  projectNodes: string[];
  projectTo: string;
}) {
  const qc = useQueryClient();
  const spec = service.spec;
  const current = spec.placement.nodes ?? [];
  const [value, setValue] = useState<string[] | null>(
    current.length ? current : null,
  );
  const [strategy, setStrategy] = useState(spec.placement.strategy ?? "spread");
  useEffect(() => {
    setValue(current.length ? current : null);
    setStrategy(spec.placement.strategy ?? "spread");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [service.revision]);
  const counts = useTaskCounts(useMemo(() => [service.id], [service.id]));
  const save = useMutation({
    mutationFn: () => {
      const { sharedEnv: _, ...own } = spec;
      return api("PUT", path, {
        ...own,
        placement: { ...spec.placement, strategy, nodes: value ?? [] },
      });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["revisions", path] });
    },
  });
  const dirty =
    (value ?? []).join(",") !== current.join(",") ||
    strategy !== (spec.placement.strategy ?? "spread");
  const invalid = value !== null && value.length === 0;
  return (
    <Panel title="Placement">
      <div className="flex flex-col gap-3 text-xs">
        <div className="flex flex-col gap-1">
          <span className="text-muted font-medium">Nodes</span>
          <p className="text-muted">
            {projectNodes.length ? (
              <>
                The project allows{" "}
                <span className="text-fg">{projectNodes.join(", ")}</span> (
                <Link to={projectTo} className="text-accent hover:underline">
                  change
                </Link>
                ). This service can run on fewer of them.
              </>
            ) : (
              "The project allows every node. Limit this service to some of them, for example to keep it on one worker or on the controller."
            )}
          </p>
          <NodeLimitEditor
            value={value}
            onChange={setValue}
            limit={projectNodes}
            counts={counts}
            anyLabel={
              projectNodes.length
                ? "Any node the project allows"
                : "Any schedulable node"
            }
          />
        </div>
        <div className="flex flex-col gap-1">
          <span className="text-muted font-medium">Spreading</span>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {(
              [
                ["spread", "Spread tasks over the nodes (default)"],
                ["binpack", "Pack tasks onto as few nodes as possible"],
              ] as const
            ).map(([v, label]) => (
              <label key={v} className="flex items-center gap-1.5">
                <input
                  type="radio"
                  checked={strategy === v}
                  onChange={() => setStrategy(v)}
                />
                {label}
              </label>
            ))}
          </div>
        </div>
        {spec.placement.node && (
          <Alert tone="info">
            Also pinned to node {spec.placement.node} in its spec.
          </Alert>
        )}
        {invalid && <Alert tone="warn">Pick at least one node.</Alert>}
        {save.error && <Alert>{errText(save.error)}</Alert>}
        <div className="flex items-center gap-2">
          <Button
            variant="primary"
            disabled={!dirty || invalid || save.isPending}
            onClick={() => save.mutate()}
          >
            Save and roll out
          </Button>
          <span
            className={cn("text-faint", save.isSuccess && !dirty && "text-ok")}
          >
            {save.isSuccess && !dirty
              ? "Saved: a new revision rolls out."
              : "Saving creates a revision; tasks move with a rolling update."}
          </span>
        </div>
      </div>
    </Panel>
  );
}
