import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowRight, RotateCcw, Save } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input, StatusBadge, Toggle } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

export interface TraefikSettings {
  logLevel: "DEBUG" | "INFO" | "WARN" | "ERROR";
  readTimeout: string;
  writeTimeout: string;
  idleTimeout: string;
  trustedIPs: string[];
  proxyProtocol: boolean;
  http3: boolean;
  dialTimeout: string;
  responseHeaderTimeout: string;
  maxIdleConnsPerHost: number;
  redirectHttps: boolean;
  minTls: "1.2" | "1.3";
  sniStrict: boolean;
  retryAttempts: number;
  hstsSeconds: number;
  compress: boolean;
  maxBodyMb: number;
}

interface SettingsView {
  settings: TraefikSettings;
  defaults: TraefikSettings;
  staticArgs: string[];
  version: string;
  restarted?: boolean;
  replicas: { node: string; role: "controller" | "edge"; state: string; error?: string }[];
}

/** Settings passed to Traefik as flags: changing them restarts every replica. */
const STATIC: (keyof TraefikSettings)[] = [
  "logLevel",
  "readTimeout",
  "writeTimeout",
  "idleTimeout",
  "trustedIPs",
  "proxyProtocol",
  "http3",
  "dialTimeout",
  "responseHeaderTimeout",
  "maxIdleConnsPerHost",
];

/** Cloudflare's published edge ranges (cloudflare.com/ips). */
const CLOUDFLARE = [
  "173.245.48.0/20",
  "103.21.244.0/22",
  "103.22.200.0/22",
  "103.31.4.0/22",
  "141.101.64.0/18",
  "108.162.192.0/18",
  "190.93.240.0/20",
  "188.114.96.0/20",
  "197.234.240.0/22",
  "198.41.128.0/17",
  "162.158.0.0/15",
  "104.16.0.0/13",
  "104.24.0.0/14",
  "172.64.0.0/13",
  "131.0.72.0/22",
  "2400:cb00::/32",
  "2606:4700::/32",
  "2803:f800::/32",
  "2405:b500::/32",
  "2405:8100::/32",
  "2a06:98c0::/29",
  "2c0f:f248::/32",
];

const sel = "bg-bg border-line-strong focus:border-accent h-8 w-full rounded-sm border px-2 text-sm outline-none";
const stateTone = (s: string) => (s === "running" || s === "healthy" ? "ok" : s === "starting" || s === "pulling" ? "info" : "bad");
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

/** Network › Traefik (§5.7): global options for every Traefik replica. */
export function TraefikPage() {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ["traefik", "settings"],
    queryFn: () => api<SettingsView>("GET", "/traefik/settings"),
    refetchInterval: 5000,
  });
  const [form, setForm] = useState<TraefikSettings | null>(null);
  const [ips, setIps] = useState("");
  useEffect(() => {
    if (data && form === null) {
      setForm(data.settings);
      setIps(data.settings.trustedIPs.join("\n"));
    }
  }, [data, form]);
  const draft = useMemo(
    () =>
      form && {
        ...form,
        trustedIPs: ips
          .split(/[\s,]+/)
          .map((s) => s.trim())
          .filter(Boolean),
      },
    [form, ips],
  );
  const changed = useMemo(
    () => (draft && data ? (Object.keys(draft) as (keyof TraefikSettings)[]).filter((k) => !same(draft[k], data.settings[k])) : []),
    [draft, data],
  );
  const restarts = changed.some((k) => STATIC.includes(k));
  const save = useMutation({
    mutationFn: () => api<SettingsView>("PUT", "/traefik/settings", draft),
    onSuccess: (v) => {
      qc.setQueryData(["traefik", "settings"], v);
      setForm(v.settings);
      setIps(v.settings.trustedIPs.join("\n"));
      void qc.invalidateQueries({ queryKey: ["traefik"] });
    },
  });
  if (!form || !data) {
    return <PageHeader crumbs={["Network"]} title="Traefik" />;
  }
  const set = <K extends keyof TraefikSettings>(k: K, v: TraefikSettings[K]) => setForm({ ...form, [k]: v });
  const num = (k: "retryAttempts" | "hstsSeconds" | "maxBodyMb" | "maxIdleConnsPerHost", v: string) => set(k, Math.max(0, parseInt(v, 10) || 0));
  const dur = (k: "readTimeout" | "writeTimeout" | "idleTimeout" | "dialTimeout" | "responseHeaderTimeout", label: string, hint: string) => (
    <Field label={label} hint={hint}>
      <Input value={form[k]} placeholder="Traefik default" onChange={(e) => set(k, e.target.value)} />
    </Field>
  );
  const running = data.replicas.filter((r) => r.state === "running" || r.state === "healthy").length;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Network"]}
        title="Traefik"
        status={
          <StatusBadge tone={running === data.replicas.length ? "ok" : "warn"}>
            {data.version} · {running}/{data.replicas.length} replicas
          </StatusBadge>
        }
        actions={
          <>
            <Button
              variant="ghost"
              disabled={changed.length === 0}
              onClick={() => {
                setForm(data.settings);
                setIps(data.settings.trustedIPs.join("\n"));
              }}
            >
              <RotateCcw className="size-3.5" /> Discard
            </Button>
            <Button variant="primary" disabled={changed.length === 0 || save.isPending} onClick={() => save.mutate()}>
              <Save className="size-3.5" /> {save.isPending ? "Saving…" : restarts ? "Save and restart Traefik" : "Save"}
            </Button>
          </>
        }
      />
      {save.error && <Alert>{save.error instanceof ApiError ? save.error.message : "Could not save"}</Alert>}
      {save.data?.restarted && <Alert tone="info">Saved. Every Traefik replica is restarting with the new flags (a few seconds each).</Alert>}
      {restarts && (
        <Alert tone="warn">
          Unsaved changes to {changed.filter((k) => STATIC.includes(k)).join(", ")} restart Traefik on {data.replicas.length} replica
          {data.replicas.length === 1 ? "" : "s"}: open connections drop and new ones wait a few seconds.
        </Alert>
      )}

      <div className={cn("grid grid-cols-1 xl:grid-cols-2", gap)}>
        <Panel title="HTTPS and TLS · applies live">
          <div className="flex flex-col gap-3">
            <Toggle
              checked={form.redirectHttps}
              onChange={(v) => set("redirectHttps", v)}
              label="Redirect HTTP to HTTPS"
              hint="Plain HTTP requests to service domains get a permanent redirect. Off: services also answer on HTTP."
            />
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field label="Minimum TLS version">
                <select className={sel} value={form.minTls} onChange={(e) => set("minTls", e.target.value as TraefikSettings["minTls"])}>
                  <option value="1.2">TLS 1.2 (compatible)</option>
                  <option value="1.3">TLS 1.3 (modern clients only)</option>
                </select>
              </Field>
              <Field label="HSTS max-age (seconds)" hint="0 = no Strict-Transport-Security header. One year is 31536000.">
                <Input type="number" min={0} value={form.hstsSeconds} onChange={(e) => num("hstsSeconds", e.target.value)} />
              </Field>
            </div>
            <Toggle
              checked={form.sniStrict}
              onChange={(v) => set("sniStrict", v)}
              label="Strict SNI"
              hint="Refuse TLS clients that do not name a known domain (bare-IP HTTPS and old clients fail)."
            />
          </div>
        </Panel>

        <Panel title="Defaults for every service route · applies live">
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field label="Retry attempts" hint="On another task, idempotent requests only. 0 turns retries off.">
                <Input type="number" min={0} max={10} value={form.retryAttempts} onChange={(e) => num("retryAttempts", e.target.value)} />
              </Field>
              <Field label="Request body limit (MiB)" hint="0 = unlimited. Larger uploads get 413.">
                <Input type="number" min={0} value={form.maxBodyMb} onChange={(e) => num("maxBodyMb", e.target.value)} />
              </Field>
            </div>
            <Toggle
              checked={form.compress}
              onChange={(v) => set("compress", v)}
              label="Compress responses"
              hint="Gzip, Brotli or Zstandard, as the client accepts."
            />
            <p className="text-faint text-xs">
              Per-service middlewares (rate limits, passwords, allow-lists…) run after these.{" "}
              <Link to={"/network/routing" as string} className="text-accent hover:underline">
                Middlewares and advanced YAML <ArrowRight className="inline size-3" />
              </Link>
            </p>
          </div>
        </Panel>

        <Panel title="Client addresses · restarts Traefik">
          <div className="flex flex-col gap-3">
            <Field
              label="Trusted proxies"
              hint="IPs or CIDRs, one per line. Their X-Forwarded-For/Proto headers are kept; from anyone else Traefik replaces them."
            >
              <textarea
                value={ips}
                onChange={(e) => setIps(e.target.value)}
                rows={4}
                spellCheck={false}
                placeholder="e.g. 10.0.0.0/8"
                className="bg-bg border-line-strong focus:border-accent w-full rounded-sm border p-2 font-mono text-xs outline-none"
              />
            </Field>
            <div className="flex flex-wrap items-center gap-1.5">
              <Button onClick={() => setIps(CLOUDFLARE.join("\n"))}>Use Cloudflare ranges</Button>
              <Button variant="ghost" disabled={!ips} onClick={() => setIps("")}>
                Clear
              </Button>
            </div>
            <Toggle
              checked={form.proxyProtocol}
              onChange={(v) => set("proxyProtocol", v)}
              disabled={!ips.trim() && !form.proxyProtocol}
              label="Accept the PROXY protocol"
              hint="For TCP load balancers that pass the client address that way (only from trusted proxies)."
            />
          </div>
        </Panel>

        <Panel title="Entrypoints · restarts Traefik">
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              {dur("readTimeout", "Read timeout", "Whole request, incl. body. Default 60s.")}
              {dur("writeTimeout", "Write timeout", "Whole response. Default none.")}
              {dur("idleTimeout", "Idle timeout", "Keep-alive. Default 180s.")}
            </div>
            <Toggle checked={form.http3} onChange={(v) => set("http3", v)} label="HTTP/3 (QUIC)" hint="Also serve HTTP/3 on the HTTPS port over UDP." />
            <Field label="Log level">
              <select className={sel} value={form.logLevel} onChange={(e) => set("logLevel", e.target.value as TraefikSettings["logLevel"])}>
                {["ERROR", "WARN", "INFO", "DEBUG"].map((l) => (
                  <option key={l}>{l}</option>
                ))}
              </select>
            </Field>
          </div>
        </Panel>

        <Panel title="Connections to tasks · restarts Traefik">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            {dur("dialTimeout", "Connect timeout", "Default 2s, so a request to a crashed node's task is retried elsewhere quickly.")}
            {dur("responseHeaderTimeout", "Response header timeout", "Wait for the app's first byte. Default none.")}
            <Field label="Idle connections per task" hint="0 = Traefik default (200).">
              <Input type="number" min={0} value={form.maxIdleConnsPerHost} onChange={(e) => num("maxIdleConnsPerHost", e.target.value)} />
            </Field>
          </div>
        </Panel>

        <Panel title="Replicas">
          <div className="flex flex-col gap-2 text-xs">
            {data.replicas.map((r) => (
              <div key={r.role + r.node} className="flex items-center gap-2">
                <StatusBadge tone={stateTone(r.state)}>{r.state || "unknown"}</StatusBadge>
                <span className="font-medium">{r.node}</span>
                <span className="text-faint">{r.role === "edge" ? "edge node" : "controller"}</span>
                {r.error && (
                  <span className="text-bad truncate" title={r.error}>
                    {r.error}
                  </span>
                )}
              </div>
            ))}
            <div className="text-muted mt-1">Flags added to every replica</div>
            <pre className="bg-bg border-line overflow-x-auto rounded-sm border p-1.5 font-mono text-[11px] leading-relaxed">
              {data.staticArgs.join("\n")}
            </pre>
          </div>
        </Panel>
      </div>
    </div>
  );
}
