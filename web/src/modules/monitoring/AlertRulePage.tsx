import {
  useEffect,
  useMemo,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { BellRing } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useProjects, useServices } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import {
  describeRule,
  useAlertChannels,
  useAlertRules,
  type AlertRule,
} from "./AlertsPage";

const sel =
  "bg-bg border-line-strong focus:border-line-accent h-8 w-full rounded-input border px-2 text-sm outline-none";

const types: { id: string; title: string; text: string }[] = [
  {
    id: "metric",
    title: "Service metric",
    text: "Error rate, latency, request rate, CPU or memory crosses a threshold",
  },
  {
    id: "log",
    title: "Log pattern",
    text: "Too many lines containing a text or with a level",
  },
  {
    id: "health",
    title: "Service health",
    text: "A service turns degraded or down",
  },
  {
    id: "deployment",
    title: "Failed deployment",
    text: "A rollout fails or is rolled back",
  },
  {
    id: "node",
    title: "Node down",
    text: "A node stops reporting for a minute",
  },
  { id: "build", title: "Failed build", text: "A Git build fails" },
  { id: "job", title: "Failed job", text: "A job run fails or times out" },
  {
    id: "promql",
    title: "PromQL",
    text: "Any VictoriaMetrics query crosses a threshold",
  },
];

const metricUnits: Record<string, string> = {
  error_rate: "% of requests answered 5xx",
  latency: "ms, p95",
  rps: "requests/s",
  cpu: "% of one core, average per task",
  memory: "% of the memory limit, busiest task",
};

const blank: AlertRule = {
  id: "",
  name: "",
  enabled: true,
  type: "metric",
  severity: "warning",
  metric: "error_rate",
  op: ">",
  threshold: 5,
  windowSeconds: 300,
  forSeconds: 60,
  channels: [],
};

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Panel title={title}>
      <div className="flex max-w-3xl flex-col gap-2">{children}</div>
    </Panel>
  );
}

/** Create or edit an alert rule (§9), as a full page. */
export function AlertRulePage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const isNew = !id || id === "new";
  const navigate = useNavigate();
  const qc = useQueryClient();
  const rules = useAlertRules();
  const { data: channels = [] } = useAlertChannels();
  const { data: projects = [] } = useProjects();
  const { data: services = [] } = useServices();
  const [r, setR] = useState<AlertRule>(blank);
  const existing = rules.data?.items.find((x) => x.id === id);
  useEffect(() => {
    if (existing) setR({ ...blank, ...existing });
  }, [existing]);
  const set = <K extends keyof AlertRule>(k: K, v: AlertRule[K]) =>
    setR((x) => ({ ...x, [k]: v }));
  const envs = useMemo(
    () => projects.find((p) => p.name === r.project)?.environments ?? [],
    [projects, r.project],
  );
  const svcNames = useMemo(
    () =>
      [
        ...new Set(
          services
            .filter(
              (s) =>
                (!r.project || s.project === r.project) &&
                (!r.environment || s.environment === r.environment),
            )
            .map((s) => s.name),
        ),
      ].sort(),
    [services, r.project, r.environment],
  );
  const save = useMutation({
    mutationFn: () =>
      api(
        isNew ? "POST" : "PUT",
        isNew ? "/alerts/rules" : `/alerts/rules/${id}`,
        r,
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["alerts"] });
      void navigate({
        to: "/monitoring/alerts" as string,
        search: { tab: "rules" } as never,
      });
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  const thresholded =
    r.type === "metric" || r.type === "log" || r.type === "promql";
  const scoped = r.type !== "node" && r.type !== "promql";
  const d = describeRule(r);

  if (!isNew && rules.isSuccess && !existing) {
    return (
      <div className={cn("flex flex-col", gap)}>
        <PageHeader crumbs={["Monitoring", "Alerts"]} title="Rule not found" />
      </div>
    );
  }
  return (
    <form onSubmit={submit} className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Monitoring", "Alerts"]}
        title={isNew ? "New alert rule" : r.name || "Alert rule"}
        actions={
          <>
            <Button
              variant="ghost"
              onClick={() =>
                navigate({
                  to: "/monitoring/alerts" as string,
                  search: { tab: "rules" } as never,
                })
              }
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!r.name.trim() || save.isPending}
            >
              <BellRing className="size-3.5" />{" "}
              {isNew ? "Create rule" : "Save rule"}
            </Button>
          </>
        }
      />
      <Section title="What to watch">
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-4">
          {types.map((t) => (
            <button
              type="button"
              key={t.id}
              onClick={() => set("type", t.id)}
              className={cn(
                "border-line hover:border-line-strong rounded-sm border p-2 text-left",
                r.type === t.id && "border-line-accent bg-hover",
              )}
            >
              <div className="text-sm">{t.title}</div>
              <div className="text-muted mt-0.5 text-xs">{t.text}</div>
            </button>
          ))}
        </div>
      </Section>

      {(scoped || r.type === "promql") && (
        <Section title="Condition">
          {scoped && (
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
              <Field label="Project" hint="Empty: every project">
                <select
                  value={r.project ?? ""}
                  onChange={(e) =>
                    setR((x) => ({
                      ...x,
                      project: e.target.value,
                      environment: "",
                      service: "",
                    }))
                  }
                  className={sel}
                >
                  <option value="">any project</option>
                  {projects.map((p) => (
                    <option key={p.name}>{p.name}</option>
                  ))}
                </select>
              </Field>
              <Field label="Environment">
                <select
                  value={r.environment ?? ""}
                  onChange={(e) => set("environment", e.target.value)}
                  className={sel}
                  disabled={!r.project}
                >
                  <option value="">any environment</option>
                  {envs.map((e) => (
                    <option key={e}>{e}</option>
                  ))}
                </select>
              </Field>
              <Field label="Service">
                <select
                  value={r.service ?? ""}
                  onChange={(e) => set("service", e.target.value)}
                  className={sel}
                >
                  <option value="">any service</option>
                  {svcNames.map((s) => (
                    <option key={s}>{s}</option>
                  ))}
                </select>
              </Field>
            </div>
          )}
          {r.type === "metric" && (
            <Field label="Metric" hint={metricUnits[r.metric ?? ""]}>
              <select
                value={r.metric}
                onChange={(e) => set("metric", e.target.value)}
                className={sel}
              >
                <option value="error_rate">5xx error rate</option>
                <option value="latency">p95 latency</option>
                <option value="rps">Request rate</option>
                <option value="cpu">CPU</option>
                <option value="memory">Memory</option>
              </select>
            </Field>
          )}
          {r.type === "promql" && (
            <Field
              label="Query"
              hint="Each series it returns is an alert instance."
            >
              <Input
                value={r.query ?? ""}
                onChange={(e) => set("query", e.target.value)}
                className="font-mono"
                placeholder='sum(rate(traefik_service_requests_total{code=~"5.."}[5m]))'
              />
            </Field>
          )}
          {r.type === "log" && (
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              <Field label="Containing" hint="Case-insensitive text">
                <Input
                  value={r.text ?? ""}
                  onChange={(e) => set("text", e.target.value)}
                  className="font-mono"
                  placeholder="timeout"
                />
              </Field>
              <Field label="Level">
                <select
                  value={r.level ?? ""}
                  onChange={(e) => set("level", e.target.value)}
                  className={sel}
                >
                  <option value="">any level</option>
                  <option value="error">error</option>
                  <option value="warn">warn</option>
                  <option value="fatal">fatal</option>
                </select>
              </Field>
            </div>
          )}
          {thresholded && (
            <div className="grid grid-cols-3 gap-2">
              <Field label={r.type === "log" ? "Lines" : "Is"}>
                <select
                  value={r.op}
                  onChange={(e) => set("op", e.target.value)}
                  className={sel}
                >
                  <option value=">">above</option>
                  <option value="<">below</option>
                </select>
              </Field>
              <Field label="Threshold">
                <Input
                  type="number"
                  step="any"
                  value={r.threshold}
                  onChange={(e) => set("threshold", Number(e.target.value))}
                />
              </Field>
              <Field label="Over" hint="minutes">
                <Input
                  type="number"
                  min={1}
                  value={(r.windowSeconds ?? 300) / 60}
                  onChange={(e) =>
                    set(
                      "windowSeconds",
                      Math.round(Number(e.target.value) * 60),
                    )
                  }
                />
              </Field>
            </div>
          )}
        </Section>
      )}

      <Section title="Notify">
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
          <Field label="Name">
            <Input
              value={r.name}
              onChange={(e) => set("name", e.target.value)}
              placeholder="web 5xx"
              autoFocus={isNew}
            />
          </Field>
          <Field label="Severity">
            <select
              value={r.severity}
              onChange={(e) =>
                set("severity", e.target.value as AlertRule["severity"])
              }
              className={sel}
            >
              <option value="info">info</option>
              <option value="warning">warning</option>
              <option value="critical">critical</option>
            </select>
          </Field>
          {!["deployment", "build", "job"].includes(r.type) && (
            <Field label="Fire after" hint="seconds the condition must hold">
              <Input
                type="number"
                min={0}
                value={r.forSeconds}
                onChange={(e) => set("forSeconds", Number(e.target.value))}
              />
            </Field>
          )}
        </div>
        <Field
          label="Channels"
          hint={
            channels.length === 0
              ? "No channels yet: alerts only appear in the history."
              : undefined
          }
        >
          <div className="flex flex-wrap gap-3 text-sm">
            {channels.map((c) => (
              <label key={c.id} className="flex items-center gap-1.5">
                <input
                  type="checkbox"
                  checked={r.channels.includes(c.id)}
                  onChange={(e) =>
                    set(
                      "channels",
                      e.target.checked
                        ? [...r.channels, c.id]
                        : r.channels.filter((x) => x !== c.id),
                    )
                  }
                />
                {c.name} <span className="text-faint text-xs">{c.type}</span>
              </label>
            ))}
          </div>
        </Field>
        <label className="flex items-center gap-2 text-xs">
          <input
            type="checkbox"
            checked={r.enabled}
            onChange={(e) => set("enabled", e.target.checked)}
          />{" "}
          Enabled
        </label>
        <p className="text-muted text-xs">
          Fires for <span className="font-mono">{d.scope}</span> when {d.cond};
          checked every 30 seconds, and notifies again when it resolves.
        </p>
        {save.error && (
          <Alert>
            {save.error instanceof ApiError
              ? save.error.message
              : "Could not save the rule"}
          </Alert>
        )}
      </Section>
    </form>
  );
}
