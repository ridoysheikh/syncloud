import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Panel } from "@/ui/Panel";
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
import { confirmAction } from "@/ui/dialogs";

interface DomainCheck {
  host: string;
  expected: string;
  addresses: string[];
  ready: boolean;
  record: string;
}

interface ServiceDomain {
  id: string;
  host: string;
  /** A path prefix ("" = the whole host). */
  path: string;
  port: string;
  stripPrefix: boolean;
  /** Answers with a 301 to this host instead of routing. */
  redirectTo: string;
  createdAt: string;
  dns: DomainCheck;
}

/** Custom domains of a service with live DNS status (§5.7, Phase 15c). */
export function DomainsPanel({
  path,
  httpPorts,
}: {
  path: string;
  httpPorts: string[];
}) {
  const qc = useQueryClient();
  const key = ["domains", path];
  const { data = [], isLoading } = useQuery({
    queryKey: key,
    queryFn: async () =>
      (await api<{ items: ServiceDomain[] }>("GET", `${path}/domains`)).items,
    refetchInterval: (q) =>
      q.state.data?.some((d) => !d.dns.ready) ? 10_000 : false,
  });
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [mode, setMode] = useState<"route" | "redirect">("route");
  const [prefix, setPrefix] = useState("");
  const [strip, setStrip] = useState(false);
  const [redirectTo, setRedirectTo] = useState("");
  const add = useMutation({
    mutationFn: () =>
      api(
        "POST",
        `${path}/domains`,
        mode === "redirect"
          ? { host, redirectTo }
          : { host, port, path: prefix, stripPrefix: strip && !!prefix },
      ),
    onSuccess: () => {
      setHost("");
      setPrefix("");
      setStrip(false);
      setRedirectTo("");
      qc.invalidateQueries({ queryKey: key });
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) =>
      api("DELETE", `${path}/domains/${encodeURIComponent(id)}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const ready =
    host.trim() &&
    (mode === "route"
      ? !prefix || prefix.startsWith("/")
      : !!redirectTo.trim());
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (ready) add.mutate();
  };
  if (httpPorts.length === 0) return null;
  const select =
    "bg-bg border-line-strong h-8 rounded-input border px-2 text-sm outline-none";

  return (
    <Panel title="Custom domains" flush>
      <form
        onSubmit={submit}
        className="border-line flex flex-wrap items-end gap-1.5 border-b p-3"
      >
        <Field label="Domain">
          <Input
            value={host}
            onChange={(e) => setHost(e.target.value)}
            placeholder="shop.example.com"
            className="w-56 font-mono"
            spellCheck={false}
          />
        </Field>
        <Field label="Action">
          <select
            value={mode}
            onChange={(e) => setMode(e.target.value as typeof mode)}
            className={select}
          >
            <option value="route">Route to this service</option>
            <option value="redirect">Redirect to another host</option>
          </select>
        </Field>
        {mode === "route" ? (
          <>
            <Field label="Path prefix (optional)">
              <Input
                value={prefix}
                onChange={(e) => setPrefix(e.target.value)}
                placeholder="/api"
                className="w-36 font-mono"
                spellCheck={false}
              />
            </Field>
            {httpPorts.length > 1 && (
              <Field label="Port">
                <select
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  className={select}
                >
                  {httpPorts.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </select>
              </Field>
            )}
            {prefix && (
              <label className="flex h-8 items-center gap-1.5 text-xs">
                <input
                  type="checkbox"
                  checked={strip}
                  onChange={(e) => setStrip(e.target.checked)}
                  className="size-3.5"
                />
                Strip the prefix
              </label>
            )}
          </>
        ) : (
          <Field label="Redirect to">
            <Input
              value={redirectTo}
              onChange={(e) => setRedirectTo(e.target.value)}
              placeholder="example.com"
              className="w-56 font-mono"
              spellCheck={false}
            />
          </Field>
        )}
        <Button
          type="submit"
          variant="primary"
          disabled={add.isPending || !ready}
        >
          Add domain
        </Button>
      </form>
      {add.error && (
        <div className="p-2">
          <Alert>
            {add.error instanceof ApiError
              ? add.error.message
              : "Could not add the domain"}
          </Alert>
        </div>
      )}
      <DataTable
        rows={data}
        rowKey={(d) => d.id}
        empty={
          !isLoading && (
            <EmptyState icon={Globe} title="No custom domains">
              Point your own domain, or a path of it, at this service. Several
              services can share a domain by path. HTTPS certificates are issued
              automatically once DNS is in place.
            </EmptyState>
          )
        }
        columns={[
          {
            header: "Domain",
            cell: (d) => (
              <span className="font-mono break-all">
                {d.host}
                {d.path}
              </span>
            ),
          },
          {
            header: "Target",
            cell: (d) => (
              <span className="text-muted whitespace-nowrap">
                {d.redirectTo ? (
                  <>
                    redirects to{" "}
                    <span className="text-fg font-mono">{d.redirectTo}</span>
                  </>
                ) : (
                  <>
                    port {d.port}
                    {d.stripPrefix && ", prefix stripped"}
                  </>
                )}
              </span>
            ),
          },
          {
            header: "DNS",
            className: "w-full",
            cell: (d) =>
              d.dns.ready ? (
                <StatusBadge tone="ok">points here</StatusBadge>
              ) : (
                <div className="flex flex-col gap-0.5 py-1">
                  <StatusBadge tone="warn">waiting for DNS</StatusBadge>
                  <span className="text-muted">
                    Create{" "}
                    <code className="text-fg font-mono break-all">
                      {d.dns.record || `A record → this server`}
                    </code>
                    {d.dns.addresses.length > 0 && (
                      <> (now resolves to {d.dns.addresses.join(", ")})</>
                    )}
                  </span>
                </div>
              ),
          },
          {
            header: "",
            cell: (d) => (
              <IconButton
                label="Remove domain"
                onClick={async () =>
                  (await confirmAction(`Stop routing ${d.host}${d.path}?`)) &&
                  remove.mutate(d.id)
                }
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            ),
          },
        ]}
      />
    </Panel>
  );
}
