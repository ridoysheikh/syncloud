import { useState, type FormEvent } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { servicePath, type Service } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

/* Project › Domains (Phase 15c): every way into the environment's services. */

interface Address {
  kind: "generated" | "domain" | "public";
  service: string;
  environment: string;
  port: string;
  protocol: string;
  address: string;
  domainId?: string;
  redirectTo?: string;
  stripPrefix?: boolean;
  label?: boolean;
  allow?: string[];
  dns?: { ready: boolean; record: string };
  certificate?: { issuer: string; status: string; error?: string };
}

const kindLabel = {
  generated: "address",
  domain: "custom domain",
  public: "public port",
} as const;

const certTone: Record<string, "ok" | "warn" | "bad" | "neutral"> = {
  valid: "ok",
  pending: "neutral",
  failed: "bad",
  rate_limited: "warn",
};

export function ProjectDomains({
  project,
  env,
  services,
}: {
  project: string;
  env: string;
  services: Service[];
}) {
  const qc = useQueryClient();
  const key = ["addresses", project, env];
  const q = useQuery({
    queryKey: key,
    queryFn: async () =>
      (
        await api<{ items: Address[] }>(
          "GET",
          `/projects/${project}/addresses?environment=${encodeURIComponent(env)}`,
        )
      ).items,
    refetchInterval: (query) =>
      query.state.data?.some((a) => a.dns && !a.dns.ready) ? 15_000 : false,
  });
  const web = services.filter((s) =>
    (s.spec.ports ?? []).some((p) => (p.protocol ?? "http") === "http"),
  );
  const [svc, setSvc] = useState("");
  const [host, setHost] = useState("");
  const [prefix, setPrefix] = useState("");
  const target = web.find((s) => s.name === svc) ?? web[0];
  const add = useMutation({
    mutationFn: () =>
      api("POST", `${servicePath(target!)}/domains`, { host, path: prefix }),
    onSuccess: () => {
      setHost("");
      setPrefix("");
      void qc.invalidateQueries({ queryKey: key });
      void qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (target && host.trim()) add.mutate();
  };
  const select =
    "bg-bg border-line-strong h-8 rounded-input border px-2 text-sm outline-none";
  return (
    <div className={cn("flex flex-col", gap)}>
      {web.length > 0 && (
        <Panel title="Add a custom domain">
          <form onSubmit={submit} className="flex flex-wrap items-end gap-1.5">
            <Field label="Domain">
              <Input
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder="shop.example.com"
                className="w-56 font-mono"
                spellCheck={false}
              />
            </Field>
            <Field label="Path prefix (optional)">
              <Input
                value={prefix}
                onChange={(e) => setPrefix(e.target.value)}
                placeholder="/api"
                className="w-32 font-mono"
                spellCheck={false}
              />
            </Field>
            <Field label="Service">
              <select
                value={target?.name ?? ""}
                onChange={(e) => setSvc(e.target.value)}
                className={cn(select, "max-w-56")}
              >
                {web.map((s) => (
                  <option key={s.name} value={s.name}>
                    {s.name}
                  </option>
                ))}
              </select>
            </Field>
            <Button
              type="submit"
              variant="primary"
              disabled={!host.trim() || add.isPending}
            >
              Add domain
            </Button>
          </form>
          {add.error && (
            <div className="mt-2">
              <Alert>
                {add.error instanceof ApiError
                  ? add.error.message
                  : "Could not add the domain"}
              </Alert>
            </div>
          )}
          <p className="text-faint mt-2 text-xs">
            Redirects, prefix stripping and other ports are on each service's
            Networking tab.
          </p>
        </Panel>
      )}
      <Panel flush>
        <DataTable
          rows={q.data ?? []}
          rowKey={(a) => `${a.kind}:${a.address}:${a.service}:${a.port}`}
          empty={
            !q.isLoading && (
              <EmptyState icon={Globe} title="No addresses">
                Services with an HTTP port get an address; add custom domains
                and public ports on their Networking tab.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Address",
              cell: (a) =>
                a.protocol === "http" && !a.redirectTo ? (
                  <a
                    href={`https://${a.address}`}
                    target="_blank"
                    rel="noreferrer"
                    className="hover:text-accent font-mono break-all"
                  >
                    {a.address}
                  </a>
                ) : (
                  <span className="font-mono break-all">
                    {a.protocol !== "http" && `${a.protocol}://`}
                    {a.address}
                  </span>
                ),
            },
            {
              header: "Kind",
              cell: (a) => (
                <span className="text-muted whitespace-nowrap">
                  {a.label ? "label" : kindLabel[a.kind]}
                </span>
              ),
            },
            {
              header: "Goes to",
              cell: (a) =>
                a.redirectTo ? (
                  <span className="text-muted whitespace-nowrap">
                    redirect →{" "}
                    <span className="text-fg font-mono">{a.redirectTo}</span>
                  </span>
                ) : (
                  <Link
                    to={
                      `/projects/${project}/${a.environment}/services/${a.service}?tab=networking` as string
                    }
                    className="hover:text-accent font-mono whitespace-nowrap"
                  >
                    {a.service}:{a.port}
                  </Link>
                ),
            },
            {
              header: "State",
              className: "w-full",
              cell: (a) => (
                <span className="flex flex-wrap items-center gap-1 py-0.5">
                  {a.dns &&
                    (a.dns.ready ? (
                      <StatusBadge tone="ok">DNS ok</StatusBadge>
                    ) : (
                      <span className="flex flex-col gap-0.5">
                        <StatusBadge tone="warn">waiting for DNS</StatusBadge>
                        {a.dns.record && (
                          <span className="text-muted font-mono break-all">
                            {a.dns.record}
                          </span>
                        )}
                      </span>
                    ))}
                  {a.certificate && (
                    <StatusBadge
                      tone={
                        a.certificate.issuer === "self-signed" &&
                        a.certificate.status !== "failed"
                          ? "neutral"
                          : (certTone[a.certificate.status] ?? "neutral")
                      }
                    >
                      {a.certificate.issuer === "self-signed" &&
                      a.certificate.status !== "failed"
                        ? "self-signed certificate"
                        : `certificate ${a.certificate.status.replace("_", " ")}`}
                    </StatusBadge>
                  )}
                  {a.kind === "public" && (
                    <span className="text-muted">
                      {a.allow?.length
                        ? `from ${a.allow.join(", ")}`
                        : "from anyone"}
                    </span>
                  )}
                </span>
              ),
            },
          ]}
        />
      </Panel>
    </div>
  );
}
