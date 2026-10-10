import {
  useEffect,
  useMemo,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import {
  Activity,
  ArrowDown,
  ArrowUp,
  BellRing,
  Code,
  Hammer,
  HeartPulse,
  Rocket,
  ScrollText,
  Server,
  Timer,
  Info,
  Siren,
  TriangleAlert,
} from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useProjects, useServices } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input, Toggle } from "@/ui/controls";
import { ChipSelect, ChoiceCards, ChoiceField, Segmented } from "@/ui/choice";
import { cn, gap } from "@/ui/cn";
import {
  describeRule,
  useAlertChannels,
  useAlertRules,
  type AlertRule,
} from "./AlertsPage";
import { Select } from "@/ui/select";


const typeIcons: Record<string, typeof Activity> = {
  metric: Activity,
  log: ScrollText,
  health: HeartPulse,
  deployment: Rocket,
  node: Server,
  build: Hammer,
  job: Timer,
  promql: Code,
};

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
        <ChoiceCards
          label="What to watch"
          value={r.type}
          onChange={(v) => set("type", v)}
          columns={4}
          options={types.map((t) => ({
            value: t.id,
            title: t.title,
            description: t.text,
            icon: typeIcons[t.id],
          }))}
        />
      </Section>

      {(scoped || r.type === "promql") && (
        <Section title="Condition">
          {scoped && (
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
              <Field label="Project" hint="Empty: every project">
                <Select
                  value={r.project ?? ""}
                  onChange={(v) =>
                    setR((x) => ({
                      ...x,
                      project: v,
                      environment: "",
                      service: "",
                    }))
                  }
                  className="w-full"
                >
                  <option value="">any project</option>
                  {projects.map((p) => (
                    <option key={p.name}>{p.name}</option>
                  ))}
                </Select>
              </Field>
              <Field label="Environment">
                <Select
                  value={r.environment ?? ""}
                  onChange={(v) => set("environment", v)}
                  disabled={!r.project}
                  className="w-full"
                >
                  <option value="">any environment</option>
                  {envs.map((e) => (
                    <option key={e}>{e}</option>
                  ))}
                </Select>
              </Field>
              <Field label="Service">
                <Select
                  value={r.service ?? ""}
                  onChange={(v) => set("service", v)}
                  className="w-full"
                >
                  <option value="">any service</option>
                  {svcNames.map((s) => (
                    <option key={s}>{s}</option>
                  ))}
                </Select>
              </Field>
            </div>
          )}
          {r.type === "metric" && (
            <Field label="Metric" hint={metricUnits[r.metric ?? ""]}>
              <Select
                value={r.metric}
                onChange={(v) => set("metric", v)}
                className="w-full"
              >
                <option value="error_rate">5xx error rate</option>
                <option value="latency">p95 latency</option>
                <option value="rps">Request rate</option>
                <option value="cpu">CPU</option>
                <option value="memory">Memory</option>
              </Select>
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
                <Select
                  value={r.level ?? ""}
                  onChange={(v) => set("level", v)}
                  className="w-full"
                >
                  <option value="">any level</option>
                  <option value="error">error</option>
                  <option value="warn">warn</option>
                  <option value="fatal">fatal</option>
                </Select>
              </Field>
            </div>
          )}
          {thresholded && (
            <div className="grid grid-cols-3 gap-2">
              <ChoiceField label={r.type === "log" ? "Lines" : "Is"}>
                <Segmented
                  label={r.type === "log" ? "Lines" : "Is"}
                  value={r.op === "<" ? "<" : ">"}
                  onChange={(v) => set("op", v)}
                  options={[
                    { value: ">", label: "above", icon: ArrowUp },
                    { value: "<", label: "below", icon: ArrowDown },
                  ]}
                />
              </ChoiceField>
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
          <ChoiceField label="Severity">
            <Segmented
              label="Severity"
              value={r.severity}
              onChange={(v) => set("severity", v)}
              options={[
                { value: "info", label: "info", icon: Info },
                { value: "warning", label: "warning", icon: TriangleAlert },
                { value: "critical", label: "critical", icon: Siren },
              ]}
            />
          </ChoiceField>
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
        <ChoiceField label="Channels">
          <ChipSelect
            label="Channels"
            empty="No channels yet: alerts only appear in the history."
            value={r.channels}
            onChange={(v) => set("channels", v)}
            options={channels.map((c) => ({
              value: c.id,
              label: (
                <>
                  {c.name} <span className="text-faint">{c.type}</span>
                </>
              ),
            }))}
          />
        </ChoiceField>
        <Toggle
          checked={r.enabled}
          onChange={(v) => set("enabled", v)}
          label="Enabled"
        />
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
