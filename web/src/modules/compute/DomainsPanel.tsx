import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, IconButton, Input, StatusBadge } from "@/ui/controls";

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
  port: string;
  createdAt: string;
  dns: DomainCheck;
}

/** Custom domains of a service with live DNS status (§5.7). */
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
  const add = useMutation({
    mutationFn: () => api("POST", `${path}/domains`, { host, port }),
    onSuccess: () => {
      setHost("");
      qc.invalidateQueries({ queryKey: key });
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const remove = useMutation({
    mutationFn: (h: string) =>
      api("DELETE", `${path}/domains/${encodeURIComponent(h)}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (host.trim()) add.mutate();
  };
  if (httpPorts.length === 0) return null;

  return (
    <Panel
      title="Custom domains"
      flush
      actions={
        <form onSubmit={submit} className="flex items-center gap-1.5">
          <Input
            value={host}
            onChange={(e) => setHost(e.target.value)}
            placeholder="shop.example.com"
            className="h-7 w-48 font-mono"
          />
          {httpPorts.length > 1 && (
            <select
              value={port}
              onChange={(e) => setPort(e.target.value)}
              className="bg-bg border-line h-7 rounded-sm border px-1.5 text-xs"
            >
              {httpPorts.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          )}
          <Button
            type="submit"
            variant="primary"
            disabled={add.isPending || !host.trim()}
          >
            Add domain
          </Button>
        </form>
      }
    >
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
              Point your own domain at this service. HTTPS certificates are
              issued automatically once DNS is in place.
            </EmptyState>
          )
        }
        columns={[
          {
            header: "Domain",
            cell: (d) => <span className="font-mono">{d.host}</span>,
          },
          {
            header: "Port",
            cell: (d) => <span className="text-muted">{d.port}</span>,
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
                    <code className="text-fg font-mono">
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
                onClick={() =>
                  confirm(`Stop routing ${d.host}?`) && remove.mutate(d.host)
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
