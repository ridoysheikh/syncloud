import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Download, Gauge, Pencil } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useProjects } from "@/lib/workloads";
import { HistoryChart, type HistorySeries } from "@/charts/HistoryChart";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Meter } from "@/ui/Meter";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface Limits {
  cpu?: number;
  memoryMiB?: number;
  tasks?: number;
  services?: number;
  jobs?: number;
  domains?: number;
  concurrentBuilds?: number;
  logsMiBPerDay?: number;
}
interface QuotaView {
  project: string;
  environment?: string;
  limits: Limits;
  usage: { cpu: number; memoryMiB: number; tasks: number; services: number; jobs: number; domains: number; builds: number; logsMiBToday: number };
  warnings: string[];
}
interface UsageRow {
  project: string;
  environment: string;
  day: string;
  cpuReservedHours: number;
  memReservedGiBHours: number;
  cpuUsedHours: number;
  memUsedGiBHours: number;
  netOutBytes: number;
  logBytes: number;
  buildSeconds: number;
}

const rows: { key: keyof Limits; usage: keyof QuotaView["usage"]; label: string; unit: string }[] = [
  { key: "cpu", usage: "cpu", label: "CPU", unit: "cores" },
  { key: "memoryMiB", usage: "memoryMiB", label: "Memory", unit: "MiB" },
  { key: "tasks", usage: "tasks", label: "Tasks", unit: "" },
  { key: "services", usage: "services", label: "Services", unit: "" },
  { key: "jobs", usage: "jobs", label: "Jobs", unit: "" },
  { key: "domains", usage: "domains", label: "Custom domains", unit: "" },
  { key: "concurrentBuilds", usage: "builds", label: "Builds at once", unit: "" },
  { key: "logsMiBPerDay", usage: "logsMiBToday", label: "Logs per day", unit: "MiB" },
];

function UsageCell({ used, limit, unit }: { used: number; limit?: number; unit: string }) {
  const u = Math.round(used * 100) / 100;
  if (!limit) return <span className="text-muted">{`${u}${unit ? " " + unit : ""}`}</span>;
  return <Meter value={(used / limit) * 100} label={`${u}/${limit}`} className="min-w-36" />;
}

/** Quotas with usage (§7.2), and metered usage per day. */
export function QuotasPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ["quotas"],
    queryFn: async () => (await api<{ items: QuotaView[] }>("GET", "/quotas")).items,
    refetchInterval: 15_000,
  });
  const quotas = data ?? [];
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Projects"]} title="Quotas & usage" />
      {error && <Alert tone="warn">{error instanceof ApiError ? error.message : "Unavailable"}</Alert>}
      {quotas.flatMap((q) => q.warnings.map((w) => ({ q, w }))).map(({ q, w }) => (
        <Alert key={q.project + q.environment + w} tone={w.includes("100%") || /\d{3,}%/.test(w) ? "bad" : "warn"}>
          {q.project}
          {q.environment ? `/${q.environment}` : ""}: {w}
        </Alert>
      ))}
      <Panel title="Quotas" flush>
        <DataTable
          rows={quotas}
          rowKey={(q) => q.project + "/" + (q.environment ?? "")}
          empty={!isLoading && <EmptyState icon={Gauge} title="No projects" />}
          columns={[
            {
              header: "Scope",
              cell: (q) => (
                <span className={cn("whitespace-nowrap", q.environment ? "text-muted pl-3" : "font-medium")}>{q.environment ? q.environment : q.project}</span>
              ),
            },
            ...rows.map((r) => ({ header: r.label, cell: (q: QuotaView) => <UsageCell used={q.usage[r.usage]} limit={q.limits[r.key]} unit={r.unit} /> })),
            {
              header: "",
              cell: (q: QuotaView) => (
                <Link to={`/projects/quotas/${q.project}${q.environment ? "/" + q.environment : ""}` as string}>
                  <Button variant="ghost">
                    <Pencil className="size-3.5" /> Limits
                  </Button>
                </Link>
              ),
            },
          ]}
        />
      </Panel>
      <UsagePanel />
    </div>
  );
}

function UsagePanel() {
  const { data: projects = [] } = useProjects();
  const [project, setProject] = useState("");
  const now = new Date();
  const [month, setMonth] = useState(`${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, "0")}`);
  const q = new URLSearchParams({ month, ...(project && { project }) });
  const { data } = useQuery({ queryKey: ["usage", q.toString()], queryFn: async () => (await api<{ items: UsageRow[] }>("GET", `/usage?${q}`)).items });
  const usage = data ?? [];
  const { series, start, end } = useMemo(() => {
    const by: Record<string, Record<string, number>> = {};
    for (const r of usage) {
      const k = r.project;
      by[k] ??= {};
      by[k][r.day] = (by[k][r.day] ?? 0) + r.cpuReservedHours;
    }
    const days = [...new Set(usage.map((r) => r.day))].sort();
    const s: HistorySeries[] = Object.entries(by).map(([name, d]) => ({ name, points: days.map((day) => [Date.parse(day), d[day] ?? 0] as [number, number]) }));
    return { series: s, start: days.length ? Date.parse(days[0]!) : 0, end: days.length ? Date.parse(days[days.length - 1]!) : 0 };
  }, [usage]);
  const totals = useMemo(() => {
    const t: Record<string, UsageRow> = {};
    for (const r of usage) {
      const k = `${r.project}/${r.environment}`;
      t[k] ??= { ...r, day: "", cpuReservedHours: 0, memReservedGiBHours: 0, cpuUsedHours: 0, memUsedGiBHours: 0, netOutBytes: 0, logBytes: 0, buildSeconds: 0 };
      const x = t[k];
      x.cpuReservedHours += r.cpuReservedHours;
      x.memReservedGiBHours += r.memReservedGiBHours;
      x.cpuUsedHours += r.cpuUsedHours;
      x.memUsedGiBHours += r.memUsedGiBHours;
      x.netOutBytes += r.netOutBytes;
      x.logBytes += r.logBytes;
      x.buildSeconds += r.buildSeconds;
    }
    return Object.values(t);
  }, [usage]);
  const f = (v: number) => (Math.round(v * 100) / 100).toString();
  return (
    <>
      <Panel
        title="Usage"
        actions={
          <>
            <select value={project} onChange={(e) => setProject(e.target.value)} className="bg-bg border-line h-7 rounded-sm border px-1.5 text-xs">
              <option value="">every project</option>
              {projects.map((p) => (
                <option key={p.name}>{p.name}</option>
              ))}
            </select>
            <Input type="month" value={month} onChange={(e) => setMonth(e.target.value)} className="h-7 w-36" />
            <a href={`/api/v1/usage/export?${q}`} download>
              <Button variant="ghost">
                <Download className="size-3.5" /> CSV
              </Button>
            </a>
          </>
        }
      >
        {series.length ? (
          <>
            <span className="text-muted text-xs">Reserved CPU per day (core-hours)</span>
            <HistoryChart series={series} start={start} end={end} format={(v) => `${f(v)} h`} stack area />
          </>
        ) : (
          <EmptyState icon={Gauge} title="No usage recorded this month">
            Usage is metered every minute from running tasks, the agents' samples, logs and builds.
          </EmptyState>
        )}
      </Panel>
      <Panel title="Month totals" flush>
        <DataTable
          rows={totals}
          rowKey={(r) => r.project + r.environment}
          columns={[
            { header: "Scope", cell: (r) => <span className="font-medium">{`${r.project}/${r.environment}`}</span> },
            { header: "CPU reserved", cell: (r) => `${f(r.cpuReservedHours)} core-h` },
            { header: "CPU used", cell: (r) => `${f(r.cpuUsedHours)} core-h` },
            { header: "Memory reserved", cell: (r) => `${f(r.memReservedGiBHours)} GiB-h` },
            { header: "Memory used", cell: (r) => `${f(r.memUsedGiBHours)} GiB-h` },
            { header: "Network out", cell: (r) => `${f(r.netOutBytes / 2 ** 20)} MiB` },
            { header: "Logs", cell: (r) => `${f(r.logBytes / 2 ** 20)} MiB` },
            { header: "Builds", className: "w-full", cell: (r) => `${f(r.buildSeconds / 60)} min` },
          ]}
        />
      </Panel>
    </>
  );
}

/** Set a project's (or environment's) limits, as a full page. */
export function QuotaPage() {
  const { project, env } = useParams({ strict: false }) as { project: string; env?: string };
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ["quotas", project],
    queryFn: async () => (await api<{ items: QuotaView[] }>("GET", `/projects/${project}/quota`)).items,
  });
  const view = data?.find((q) => (q.environment ?? "") === (env ?? ""));
  const [limits, setLimits] = useState<Limits>({});
  useEffect(() => {
    if (view) setLimits(view.limits);
  }, [view]);
  const path = `/projects/${project}${env ? `/environments/${env}` : ""}/quota`;
  const back = () => navigate({ to: "/projects/quotas" as string });
  const save = useMutation({
    mutationFn: () => api("PUT", path, limits),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["quotas"] });
      void back();
    },
  });
  const remove = useMutation({
    mutationFn: () => api("DELETE", path),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["quotas"] });
      void back();
    },
  });
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      className={cn("flex flex-col", gap)}
    >
      <PageHeader
        crumbs={["Projects", "Quotas & usage"]}
        title={`Limits of ${project}${env ? "/" + env : ""}`}
        actions={
          <>
            <Button variant="ghost" onClick={() => remove.mutate()}>
              Remove limits
            </Button>
            <Button variant="ghost" onClick={back}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={save.isPending}>
              Save
            </Button>
          </>
        }
      />
      <Panel title="Limits (empty or 0 = unlimited)">
        <div className="grid max-w-3xl grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-4">
          {rows.map((r) => (
            <Field key={r.key} label={r.label} hint={view ? `now ${Math.round((view.usage[r.usage] ?? 0) * 100) / 100}${r.unit ? " " + r.unit : ""}` : undefined}>
              <Input
                type="number"
                min={0}
                step={r.key === "cpu" ? "0.1" : "1"}
                value={limits[r.key] ?? ""}
                onChange={(e) => setLimits({ ...limits, [r.key]: e.target.value === "" ? undefined : Number(e.target.value) })}
              />
            </Field>
          ))}
        </div>
        <p className="text-muted mt-2 text-xs">
          CPU, memory and tasks count what services ask for (desired tasks × reservation), so deploying or scaling past a limit, by hand or by the autoscaler,
          is refused with a clear error. Builds over the limit wait in the queue. Log volume is metered and warned about, not enforced.
          {env ? " Environment limits apply on top of the project's." : ""}
        </p>
      </Panel>
      {(save.error || remove.error) && <Alert>{(save.error ?? remove.error) instanceof ApiError ? (save.error ?? remove.error)!.message : "Could not save"}</Alert>}
    </form>
  );
}
