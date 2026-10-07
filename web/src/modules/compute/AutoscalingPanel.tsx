import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type FormEvent,
} from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Gauge } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { since } from "@/lib/nodes";
import { useStreamTopic } from "@/lib/stream";
import { HistoryChart } from "@/charts/HistoryChart";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { confirmAction } from "@/ui/dialogs";

type Metric = "cpu" | "memory" | "rps" | "latency";

export interface ScalingPolicy {
  enabled: boolean;
  min: number;
  max: number;
  metric: Metric;
  target: number;
  scaleOutCooldown: number;
  scaleInCooldown: number;
  scaleInChecks: number;
  updatedAt?: string;
  updatedBy?: string;
}

interface Autoscaling {
  policy: ScalingPolicy | null;
  current: { value: number | null; evaluatedAt: string | null };
  units: Record<Metric, string>;
}

interface ScalingEvent {
  id: number;
  at: string;
  from: number;
  to: number;
  reason: string;
}

interface Charts {
  start: string;
  end: string;
  charts: Record<
    "tasks" | "metric",
    { key: string; points: [number, number][] }[]
  >;
}

const metricLabel: Record<Metric, string> = {
  cpu: "CPU",
  memory: "Memory",
  rps: "Requests per task",
  latency: "p95 latency",
};
const defaults: Record<Metric, number> = {
  cpu: 60,
  memory: 70,
  rps: 50,
  latency: 250,
};
const fmt = (m: Metric, v: number) =>
  m === "cpu" || m === "memory"
    ? `${v.toFixed(v < 10 ? 1 : 0)}%`
    : m === "rps"
      ? `${v.toFixed(v < 10 ? 2 : 0)}/s`
      : `${v.toFixed(0)}ms`;
const sel = "bg-bg border-line h-8 w-full rounded-sm border px-2 text-sm";

/** Target tracking autoscaling of one service (§5.5). */
export function AutoscalingPanel({
  path,
  desired,
  running,
}: {
  path: string;
  desired: number;
  running: number;
}) {
  const qc = useQueryClient();
  const key = ["autoscaling", path];
  const { data, error } = useQuery({
    queryKey: key,
    queryFn: () => api<Autoscaling>("GET", `${path}/autoscaling`),
    refetchInterval: 15_000,
  });
  const events = useQuery({
    queryKey: ["autoscaling", path, "events"],
    queryFn: async () =>
      (
        await api<{ items: ScalingEvent[] }>(
          "GET",
          `${path}/scaling-events?limit=50`,
        )
      ).items,
  });
  const onEvent = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["autoscaling", path] });
    void qc.invalidateQueries({ queryKey: ["services"] });
  }, [qc, path]);
  useStreamTopic("scaling.event", onEvent);

  const p = data?.policy;
  const [form, setForm] = useState<ScalingPolicy>({
    enabled: true,
    min: 1,
    max: 5,
    metric: "cpu",
    target: 60,
    scaleOutCooldown: 60,
    scaleInCooldown: 300,
    scaleInChecks: 4,
  });
  const [editing, setEditing] = useState(false);
  useEffect(() => {
    if (p) setForm(p);
  }, [p]);
  const save = useMutation({
    mutationFn: (body: ScalingPolicy) =>
      api<Autoscaling>("PUT", `${path}/autoscaling`, body),
    onSuccess: () => {
      setEditing(false);
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const off = useMutation({
    mutationFn: () => api("DELETE", `${path}/autoscaling`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(form);
  };
  const set = <K extends keyof ScalingPolicy>(k: K, v: ScalingPolicy[K]) =>
    setForm((f) => ({ ...f, [k]: v }));
  const err = save.error ?? off.error ?? error;

  return (
    <div className={cn("flex flex-col", gap)}>
      {err && (
        <Alert>
          {err instanceof ApiError ? err.message : "Request failed"}
        </Alert>
      )}
      {p && !editing ? (
        <>
          <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
            <StatTile label="Tasks" value={`${running} / ${desired}`} />
            <StatTile label="Range" value={`${p.min}–${p.max}`} />
            <StatTile
              label={`${metricLabel[p.metric]} target`}
              value={fmt(p.metric, p.target)}
            />
            <StatTile
              label="Now"
              value={
                data?.current.value != null
                  ? fmt(p.metric, data.current.value)
                  : "no data"
              }
              tone={
                data?.current.value == null
                  ? undefined
                  : data.current.value > p.target * 1.1
                    ? "warn"
                    : "ok"
              }
            />
          </div>
          <Panel
            title={
              <span className="flex items-center gap-2">
                Policy{" "}
                <StatusBadge tone={p.enabled ? "ok" : "neutral"}>
                  {p.enabled ? "active" : "paused"}
                </StatusBadge>
              </span>
            }
            actions={
              <>
                <Button
                  variant="ghost"
                  onClick={() => save.mutate({ ...p, enabled: !p.enabled })}
                  disabled={save.isPending}
                >
                  {p.enabled ? "Pause" : "Resume"}
                </Button>
                <Button variant="ghost" onClick={() => setEditing(true)}>
                  Edit
                </Button>
                <Button
                  variant="ghost"
                  onClick={async () =>
                    (await confirmAction(
                      "Stop autoscaling? The task count stays where it is.",
                    )) && off.mutate()
                  }
                >
                  Turn off
                </Button>
              </>
            }
          >
            <p className="text-xs">
              Keeps {metricLabel[p.metric].toLowerCase()} near{" "}
              <b>{fmt(p.metric, p.target)}</b>{" "}
              with {p.min} to {p.max} tasks, checked every 15 seconds. Scales
              out at most every {p.scaleOutCooldown}s; scales in after{" "}
              {p.scaleInChecks} checks below target and {p.scaleInCooldown}s
              since the last change.
              {data?.current.evaluatedAt && (
                <span className="text-faint">
                  {" "}
                  Last check {since(data.current.evaluatedAt)}.
                </span>
              )}
            </p>
            <p className="text-faint mt-1 text-xs">
              Changing the task count by hand works, but the autoscaler corrects
              it at its next check.
            </p>
          </Panel>
          <ScalingCharts path={path} metric={p.metric} />
        </>
      ) : (
        <Panel title={p ? "Edit autoscaling" : "Autoscaling"}>
          <form onSubmit={submit} className="flex max-w-2xl flex-col gap-2">
            {!p && (
              <p className="text-muted text-xs">
                Target tracking, like ECS: every 15 seconds the task count moves
                towards what brings the metric back to its target — quickly out,
                conservatively in.
              </p>
            )}
            <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
              <Field label="Metric">
                <select
                  value={form.metric}
                  onChange={(e) => {
                    const m = e.target.value as Metric;
                    setForm((f) => ({ ...f, metric: m, target: defaults[m] }));
                  }}
                  className={sel}
                >
                  {(Object.keys(metricLabel) as Metric[]).map((m) => (
                    <option key={m} value={m}>
                      {metricLabel[m]}
                    </option>
                  ))}
                </select>
              </Field>
              <Field
                label="Target"
                hint={
                  {
                    cpu: "% of reserved CPU",
                    memory: "% of reserved memory",
                    rps: "req/s per task",
                    latency: "ms",
                  }[form.metric]
                }
              >
                <Input
                  type="number"
                  min={0}
                  step="any"
                  value={form.target}
                  onChange={(e) => set("target", Number(e.target.value))}
                />
              </Field>
              <Field label="Min tasks">
                <Input
                  type="number"
                  min={0}
                  value={form.min}
                  onChange={(e) => set("min", Number(e.target.value))}
                />
              </Field>
              <Field label="Max tasks">
                <Input
                  type="number"
                  min={1}
                  value={form.max}
                  onChange={(e) => set("max", Number(e.target.value))}
                />
              </Field>
            </div>
            <div className="grid grid-cols-3 gap-2">
              <Field label="Scale-out cooldown" hint="seconds">
                <Input
                  type="number"
                  value={form.scaleOutCooldown}
                  onChange={(e) =>
                    set("scaleOutCooldown", Number(e.target.value))
                  }
                />
              </Field>
              <Field label="Scale-in cooldown" hint="seconds">
                <Input
                  type="number"
                  value={form.scaleInCooldown}
                  onChange={(e) =>
                    set("scaleInCooldown", Number(e.target.value))
                  }
                />
              </Field>
              <Field
                label="Scale in after"
                hint="checks below target (15s each)"
              >
                <Input
                  type="number"
                  value={form.scaleInChecks}
                  onChange={(e) => set("scaleInChecks", Number(e.target.value))}
                />
              </Field>
            </div>
            <div className="flex gap-2">
              <Button type="submit" variant="primary" disabled={save.isPending}>
                <Gauge className="size-3.5" />{" "}
                {p ? "Save" : "Turn on autoscaling"}
              </Button>
              {p && (
                <Button variant="ghost" onClick={() => setEditing(false)}>
                  Cancel
                </Button>
              )}
            </div>
          </form>
        </Panel>
      )}
      <Panel title="Scaling history" flush>
        <DataTable
          rows={events.data ?? []}
          rowKey={(e) => String(e.id)}
          empty={
            !events.isLoading && (
              <EmptyState icon={Gauge} title="No scaling yet">
                Every change the autoscaler makes is listed here with its
                reason.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "When",
              cell: (e) => (
                <span className="text-muted whitespace-nowrap">
                  {since(e.at)}
                </span>
              ),
            },
            {
              header: "Tasks",
              cell: (e) => (
                <span
                  className={cn(
                    "font-mono whitespace-nowrap",
                    e.to > e.from ? "text-ok" : "text-warn",
                  )}
                >
                  {e.from} → {e.to}
                </span>
              ),
            },
            {
              header: "Reason",
              className: "w-full",
              cell: (e) => <span className="text-muted">{e.reason}</span>,
            },
          ]}
        />
      </Panel>
    </div>
  );
}

function ScalingCharts({ path, metric }: { path: string; metric: Metric }) {
  const { data } = useQuery({
    queryKey: ["autoscaling", path, "charts"],
    queryFn: () => api<Charts>("GET", `${path}/autoscaling/charts?range=1h`),
    refetchInterval: 15_000,
    retry: false,
  });
  const lines = useMemo(
    () =>
      data
        ? {
            tasks: data.charts.tasks.map((s) => ({
              name: s.key,
              points: s.points,
            })),
            metric: data.charts.metric.map((s) => ({
              name: s.key,
              points: s.points,
            })),
          }
        : null,
    [data],
  );
  if (!data || !lines) return null;
  const start = Date.parse(data.start),
    end = Date.parse(data.end);
  return (
    <div className={cn("grid grid-cols-1 xl:grid-cols-2", gap)}>
      <Panel title="Tasks (last hour)">
        <HistoryChart
          series={lines.tasks}
          start={start}
          end={end}
          format={(v) => v.toFixed(0)}
        />
      </Panel>
      <Panel title={`${metricLabel[metric]} vs target`}>
        <HistoryChart
          series={lines.metric}
          start={start}
          end={end}
          format={(v) => fmt(metric, v)}
        />
      </Panel>
    </div>
  );
}
