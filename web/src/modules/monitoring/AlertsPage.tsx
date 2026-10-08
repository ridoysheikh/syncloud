import { useCallback, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { BellRing, Plus, Send, Trash2 } from "lucide-react";
import { usePaged } from "@/lib/paged";
import { api, ApiError } from "@/lib/api";
import { since } from "@/lib/nodes";
import { useStreamTopic } from "@/lib/stream";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import {
  Alert,
  Button,
  Field,
  IconButton,
  Input,
  StatusBadge,
} from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { Tabs } from "@/ui/Tabs";
import { confirmAction } from "@/ui/dialogs";

export type Severity = "info" | "warning" | "critical";

export interface AlertRule {
  id: string;
  name: string;
  enabled: boolean;
  type: string;
  severity: Severity;
  project?: string;
  environment?: string;
  service?: string;
  metric?: string;
  query?: string;
  text?: string;
  level?: string;
  op?: string;
  threshold: number;
  windowSeconds?: number;
  forSeconds: number;
  channels: string[];
}

export interface AlertChannel {
  id: string;
  name: string;
  type: "webhook" | "slack" | "discord" | "telegram" | "email";
  summary: string;
}

interface ActiveAlert {
  ruleId: string;
  rule: string;
  severity: Severity;
  key: string;
  label: string;
  state: "pending" | "firing";
  since: string;
  message: string;
}

interface AlertEvent {
  id: number;
  rule: string;
  severity: Severity;
  kind: "firing" | "resolved" | "event";
  label: string;
  message: string;
  delivery: string;
  at: string;
}

export const sevTone = {
  info: "info",
  warning: "warn",
  critical: "bad",
} as const;
const errText = (e: unknown) =>
  e instanceof ApiError ? e.message : "Request failed";

/** Firing alerts, for the header bell and the page (§9). */
export function useActiveAlerts() {
  const qc = useQueryClient();
  const onEvent = useCallback(
    () => void qc.invalidateQueries({ queryKey: ["alerts"] }),
    [qc],
  );
  useStreamTopic("alert.event", onEvent);
  return useQuery({
    queryKey: ["alerts", "active"],
    queryFn: async () =>
      (await api<{ items: ActiveAlert[] }>("GET", "/alerts/active")).items,
    refetchInterval: 30_000,
    retry: false,
  });
}

export function useAlertChannels() {
  return useQuery({
    queryKey: ["alerts", "channels"],
    queryFn: async () =>
      (await api<{ items: AlertChannel[] }>("GET", "/alerts/channels")).items,
  });
}

export function useAlertRules() {
  return useQuery({
    queryKey: ["alerts", "rules"],
    queryFn: () =>
      api<{
        items: AlertRule[];
        types: Record<string, string>;
        metrics: Record<string, string>;
      }>("GET", "/alerts/rules"),
  });
}

type Tab = "active" | "rules" | "channels" | "history";

/** Monitoring › Alerts: what is firing, the rules, the channels and the history. */
export function AlertsPage() {
  const [tab, setTab] = useState<Tab>(
    () =>
      (new URLSearchParams(window.location.search).get("tab") as Tab | null) ??
      "active",
  );
  const active = useActiveAlerts();
  const rules = useAlertRules();
  const channels = useAlertChannels();
  const firing = (active.data ?? []).filter((a) => a.state === "firing");
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Monitoring"]}
        title="Alerts"
        actions={
          <Link to={"/monitoring/alerts/rules/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New rule
            </Button>
          </Link>
        }
      />
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile
          label="Firing"
          value={firing.length}
          tone={firing.length ? "bad" : "ok"}
        />
        <StatTile
          label="Pending"
          value={(active.data ?? []).length - firing.length}
        />
        <StatTile label="Rules" value={rules.data?.items.length ?? 0} />
        <StatTile label="Channels" value={channels.data?.length ?? 0} />
      </div>
      <Tabs
        tabs={["active", "rules", "channels", "history"] as Tab[]}
        value={tab}
        onChange={setTab}
      />
      {tab === "active" && (
        <ActiveTab rows={active.data ?? []} loading={active.isLoading} />
      )}
      {tab === "rules" && <RulesTab />}
      {tab === "channels" && <ChannelsTab />}
      {tab === "history" && <HistoryTab />}
    </div>
  );
}

function ActiveTab({
  rows,
  loading,
}: {
  rows: ActiveAlert[];
  loading: boolean;
}) {
  return (
    <Panel title="Active alerts" flush>
      <DataTable
        rows={rows}
        rowKey={(a) => a.ruleId + a.key}
        empty={
          !loading && (
            <EmptyState icon={BellRing} title="All clear">
              Nothing is firing. Rules are checked every 30 seconds.
            </EmptyState>
          )
        }
        columns={[
          {
            header: "State",
            cell: (a) => (
              <StatusBadge
                tone={a.state === "firing" ? sevTone[a.severity] : "neutral"}
              >
                {a.state}
              </StatusBadge>
            ),
          },
          {
            header: "Rule",
            cell: (a) => <span className="whitespace-nowrap">{a.rule}</span>,
          },
          {
            header: "Instance",
            cell: (a) => (
              <span className="font-mono whitespace-nowrap">{a.label}</span>
            ),
          },
          {
            header: "Since",
            cell: (a) => (
              <span className="text-muted whitespace-nowrap">
                {since(a.since)}
              </span>
            ),
          },
          {
            header: "Message",
            className: "w-full",
            cell: (a) => <span className="text-muted">{a.message}</span>,
          },
        ]}
      />
    </Panel>
  );
}

export function describeRule(r: AlertRule) {
  const scope = [r.project, r.environment, r.service].filter(Boolean).join("/");
  const w = r.windowSeconds ? ` over ${r.windowSeconds / 60}m` : "";
  let cond = "";
  switch (r.type) {
    case "metric":
      cond = `${r.metric} ${r.op} ${r.threshold}${w}`;
      break;
    case "promql":
      cond = `(${r.query}) ${r.op} ${r.threshold}`;
      break;
    case "log":
      cond = `${[r.level && `level ${r.level}`, r.text && `"${r.text}"`].filter(Boolean).join(" ")} lines ${r.op} ${r.threshold}${w}`;
      break;
    case "health":
      cond = "service degraded or down";
      break;
    case "node":
      cond = "node not reporting";
      break;
    default:
      cond = `${r.type} failed`;
  }
  return {
    scope: scope || "everything",
    cond: cond + (r.forSeconds ? ` for ${r.forSeconds}s` : ""),
  };
}

function RulesTab() {
  const qc = useQueryClient();
  const { data, isLoading } = useAlertRules();
  const { data: channels = [] } = useAlertChannels();
  const names = Object.fromEntries(channels.map((c) => [c.id, c.name]));
  const toggle = useMutation({
    mutationFn: (r: AlertRule) =>
      api("PUT", `/alerts/rules/${r.id}`, { ...r, enabled: !r.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
  });
  const del = useMutation({
    mutationFn: (id: string) => api("DELETE", `/alerts/rules/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
  });
  return (
    <Panel title="Rules" flush>
      {(toggle.error || del.error) && (
        <div className="p-2">
          <Alert>{errText(toggle.error ?? del.error)}</Alert>
        </div>
      )}
      <DataTable
        rows={data?.items ?? []}
        rowKey={(r) => r.id}
        empty={
          !isLoading && (
            <EmptyState icon={BellRing} title="No alert rules">
              Watch error rates, latency, CPU, log patterns, service health,
              nodes and failed deployments.
            </EmptyState>
          )
        }
        columns={[
          {
            header: "Rule",
            cell: (r) => (
              <Link
                to={`/monitoring/alerts/rules/${r.id}` as string}
                className="hover:text-accent whitespace-nowrap"
              >
                {r.name}
              </Link>
            ),
          },
          {
            header: "Severity",
            cell: (r) => (
              <StatusBadge tone={sevTone[r.severity]}>{r.severity}</StatusBadge>
            ),
          },
          {
            header: "Scope",
            cell: (r) => (
              <span className="font-mono whitespace-nowrap">
                {describeRule(r).scope}
              </span>
            ),
          },
          {
            header: "Condition",
            cell: (r) => (
              <span className="text-muted">{describeRule(r).cond}</span>
            ),
          },
          {
            header: "Notifies",
            className: "w-full",
            cell: (r) => (
              <span className="text-muted">
                {r.channels.map((c) => names[c] ?? c).join(", ") ||
                  "— (history only)"}
              </span>
            ),
          },
          {
            header: "",
            cell: (r) => (
              <span className="flex items-center gap-1">
                <Button variant="ghost" onClick={() => toggle.mutate(r)}>
                  {r.enabled ? "Disable" : "Enable"}
                </Button>
                <IconButton
                  label="Delete"
                  onClick={async () =>
                    (await confirmAction(`Delete the rule ${r.name}?`)) &&
                    del.mutate(r.id)
                  }
                >
                  <Trash2 className="size-3.5" />
                </IconButton>
              </span>
            ),
          },
        ]}
      />
    </Panel>
  );
}

const channelFields: Record<
  AlertChannel["type"],
  { key: string; label: string; secret?: boolean; hint?: string }[]
> = {
  webhook: [
    {
      key: "url",
      label: "URL",
      secret: true,
      hint: "Receives a JSON POST per notification.",
    },
  ],
  slack: [{ key: "url", label: "Incoming webhook URL", secret: true }],
  discord: [{ key: "url", label: "Webhook URL", secret: true }],
  telegram: [
    { key: "botToken", label: "Bot token", secret: true },
    { key: "chatId", label: "Chat ID" },
  ],
  email: [
    { key: "smtpHost", label: "SMTP host" },
    { key: "smtpPort", label: "Port", hint: "587 (STARTTLS) or 465 (TLS)" },
    { key: "username", label: "User name" },
    { key: "password", label: "Password", secret: true },
    { key: "from", label: "From" },
    { key: "to", label: "To", hint: "Comma-separated addresses" },
  ],
};

function ChannelsTab() {
  const qc = useQueryClient();
  const { data = [], isLoading } = useAlertChannels();
  const [type, setType] = useState<AlertChannel["type"]>("slack");
  const [name, setName] = useState("");
  const [cfg, setCfg] = useState<Record<string, string>>({});
  const [tested, setTested] = useState<Record<string, string>>({});
  const create = useMutation({
    mutationFn: () => {
      const config: Record<string, unknown> = { ...cfg };
      if (type === "email") {
        config.to = (cfg.to ?? "")
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean);
        config.smtpPort = Number(cfg.smtpPort) || 0;
      }
      return api("POST", "/alerts/channels", { name, type, config });
    },
    onSuccess: () => {
      setName("");
      setCfg({});
      void qc.invalidateQueries({ queryKey: ["alerts"] });
    },
  });
  const test = useMutation({
    mutationFn: (id: string) => api("POST", `/alerts/channels/${id}/test`),
    onSuccess: (_, id) => setTested((t) => ({ ...t, [id]: "delivered" })),
    onError: (e, id) => setTested((t) => ({ ...t, [id]: errText(e) })),
  });
  const del = useMutation({
    mutationFn: (id: string) => api("DELETE", `/alerts/channels/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  const sel =
    "bg-bg border-line-strong h-8 w-full rounded-input border px-2 text-sm";
  return (
    <div className={cn("grid grid-cols-1 lg:grid-cols-[1fr_22rem]", gap)}>
      <Panel title="Channels" flush>
        {del.error && (
          <div className="p-2">
            <Alert>{errText(del.error)}</Alert>
          </div>
        )}
        <DataTable
          rows={data}
          rowKey={(c) => c.id}
          empty={
            !isLoading && (
              <EmptyState icon={Send} title="No channels">
                Alerts are recorded in the history; add a channel to be
                notified.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Name",
              cell: (c) => <span className="whitespace-nowrap">{c.name}</span>,
            },
            {
              header: "Type",
              cell: (c) => (
                <span className="text-muted capitalize">{c.type}</span>
              ),
            },
            {
              header: "Sends to",
              className: "w-full",
              cell: (c) => (
                <span className="text-muted font-mono">
                  {c.summary}
                  {tested[c.id] && (
                    <span
                      className={cn(
                        "ml-2 font-sans",
                        tested[c.id] === "delivered" ? "text-ok" : "text-bad",
                      )}
                    >
                      {tested[c.id]}
                    </span>
                  )}
                </span>
              ),
            },
            {
              header: "",
              cell: (c) => (
                <span className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    onClick={() => test.mutate(c.id)}
                    disabled={test.isPending}
                  >
                    <Send className="size-3.5" /> Test
                  </Button>
                  <IconButton
                    label="Delete"
                    onClick={async () =>
                      (await confirmAction(`Delete the channel ${c.name}?`)) &&
                      del.mutate(c.id)
                    }
                  >
                    <Trash2 className="size-3.5" />
                  </IconButton>
                </span>
              ),
            },
          ]}
        />
      </Panel>
      <Panel title="Add a channel">
        <form onSubmit={submit} className="flex flex-col gap-2">
          <Field label="Type">
            <select
              value={type}
              onChange={(e) => {
                setType(e.target.value as AlertChannel["type"]);
                setCfg({});
              }}
              className={sel}
            >
              <option value="slack">Slack</option>
              <option value="discord">Discord</option>
              <option value="telegram">Telegram</option>
              <option value="email">Email</option>
              <option value="webhook">Webhook</option>
            </select>
          </Field>
          <Field label="Name">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="ops"
            />
          </Field>
          {channelFields[type].map((f) => (
            <Field key={f.key} label={f.label} hint={f.hint}>
              <Input
                type={f.secret ? "password" : "text"}
                value={cfg[f.key] ?? ""}
                onChange={(e) =>
                  setCfg((c) => ({ ...c, [f.key]: e.target.value }))
                }
                autoComplete="off"
                className="font-mono"
              />
            </Field>
          ))}
          <p className="text-faint text-xs">
            URLs, tokens and passwords are stored encrypted and never shown
            again.
          </p>
          {create.error && <Alert>{errText(create.error)}</Alert>}
          <div>
            <Button
              type="submit"
              variant="primary"
              disabled={!name || create.isPending}
            >
              <Plus className="size-3.5" /> Add channel
            </Button>
          </div>
        </form>
      </Panel>
    </div>
  );
}

function HistoryTab() {
  const q = usePaged<AlertEvent>(["alerts", "events"], "/alerts/events", {
    refetchInterval: 30_000,
  });
  const { items: data, isLoading } = q;
  return (
    <Panel title="Notifications" flush>
      <DataTable
        {...q.table}
        rows={data}
        rowKey={(e) => String(e.id)}
        empty={
          !isLoading && (
            <EmptyState icon={BellRing} title="No alerts yet">
              Every alert that fires or resolves is listed here for 90 days.
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
            header: "",
            cell: (e) => (
              <StatusBadge
                tone={e.kind === "resolved" ? "ok" : sevTone[e.severity]}
              >
                {e.kind}
              </StatusBadge>
            ),
          },
          {
            header: "Rule",
            cell: (e) => <span className="whitespace-nowrap">{e.rule}</span>,
          },
          {
            header: "Instance",
            cell: (e) => (
              <span className="font-mono whitespace-nowrap">{e.label}</span>
            ),
          },
          {
            header: "Message",
            className: "w-full",
            cell: (e) => <span className="text-muted">{e.message}</span>,
          },
          {
            header: "Delivery",
            cell: (e) => (
              <span
                className={cn(
                  "block max-w-72 truncate",
                  e.delivery.includes(": ") && !/: ok(;|$)/.test(e.delivery)
                    ? "text-bad"
                    : "text-faint",
                )}
                title={e.delivery}
              >
                {e.delivery || "—"}
              </span>
            ),
          },
        ]}
      />
    </Panel>
  );
}
