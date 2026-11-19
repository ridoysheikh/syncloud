import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  Cloud,
  ExternalLink,
  KeyRound,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
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
import { Dialog } from "@/ui/Dialog";
import { confirmAction } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";
import {
  errText,
  invalidateAll,
  useProviders,
  type DnsProvider,
  type RemoteZone,
} from "./lib";
import { since } from "@/lib/nodes";

/* Domains › DNS providers (Phase 28): where SynCloud may write records.
   Cloudflare is optional: a domain without one is "manual" (SynCloud shows
   the records to create and checks them live). */

function ReplaceToken({ p, onClose }: { p: DnsProvider; onClose: () => void }) {
  const qc = useQueryClient();
  const [token, setToken] = useState("");
  const save = useMutation({
    mutationFn: () => api("PUT", `/dns/providers/${p.id}`, { token }),
    onSuccess: () => {
      invalidateAll(qc);
      onClose();
    },
  });
  return (
    <Dialog
      open
      onClose={onClose}
      title={`Replace the token of ${p.name}`}
      description="The new token is checked before it is saved. The old one is never shown."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={!token.trim() || save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending ? "Checking…" : "Replace token"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-2">
        <Field label="API token">
          <Input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="off"
            className="font-mono"
          />
        </Field>
        {save.error && (
          <Alert>{errText(save.error, "Could not replace the token")}</Alert>
        )}
      </div>
    </Dialog>
  );
}

export function ProvidersPage() {
  const qc = useQueryClient();
  const { data = [], isLoading } = useProviders();
  const [replace, setReplace] = useState<DnsProvider | null>(null);
  const sync = useMutation({
    mutationFn: (id: string) => api("POST", `/dns/providers/${id}/sync`),
    onSuccess: () => invalidateAll(qc),
  });
  const remove = useMutation({
    mutationFn: (id: string) => api("DELETE", `/dns/providers/${id}`),
    onSuccess: () => invalidateAll(qc),
  });
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Domains"]}
        title="DNS providers"
        actions={
          <Link to={"/domains/providers/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> Connect Cloudflare
            </Button>
          </Link>
        }
      />
      <p className="text-muted text-xs">
        A provider is optional. With one connected, SynCloud writes the DNS
        records itself (and removes the duplicates it finds), and wildcard
        certificates renew by themselves. Without one, a domain is <b>manual</b>
        : SynCloud shows exactly which records to create at your DNS host, and
        checks them live. SynCloud is not a name server.
      </p>
      <Panel title="Connected providers" flush>
        <DataTable
          rows={data}
          rowKey={(p) => p.id}
          empty={
            !isLoading && (
              <EmptyState icon={Cloud} title="No DNS provider">
                Connect Cloudflare to let SynCloud manage records and renew
                wildcard certificates by itself.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Name",
              cell: (p) => <span className="font-mono">{p.name}</span>,
            },
            {
              header: "Type",
              cell: (p) => <span className="capitalize">{p.type}</span>,
            },
            {
              header: "Status",
              cell: (p) => (
                <span className="flex items-center gap-1.5">
                  <StatusBadge tone={p.status === "ok" ? "ok" : "bad"}>
                    {p.status === "ok" ? "token works" : "token refused"}
                  </StatusBadge>
                  {p.lastError && (
                    <span className="text-bad max-w-xs truncate">
                      {p.lastError}
                    </span>
                  )}
                </span>
              ),
            },
            { header: "Domains", cell: (p) => p.domains },
            { header: "Connected", cell: (p) => since(p.createdAt) },
            {
              header: "",
              cell: (p) => (
                <span className="flex items-center justify-end gap-0.5">
                  <IconButton
                    label="Check the token again"
                    onClick={() => sync.mutate(p.id)}
                  >
                    <RefreshCw
                      className={cn(
                        "size-3.5",
                        sync.isPending && "animate-spin",
                      )}
                    />
                  </IconButton>
                  <IconButton
                    label="Replace the token"
                    onClick={() => setReplace(p)}
                  >
                    <KeyRound className="size-3.5" />
                  </IconButton>
                  <IconButton
                    label="Disconnect"
                    onClick={async () =>
                      (await confirmAction(
                        `Disconnect ${p.name}? Its ${p.domains} domain${p.domains === 1 ? "" : "s"} stay, as manual ones. Records and certificates are kept.`,
                      )) && remove.mutate(p.id)
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
      {(sync.error || remove.error) && (
        <Alert>
          {errText(sync.error ?? remove.error, "The change failed")}
        </Alert>
      )}
      {replace && <ReplaceToken p={replace} onClose={() => setReplace(null)} />}
    </div>
  );
}

/** Connect a provider: the token is checked, then its zones are offered. */
export function ProviderFormPage() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("Cloudflare");
  const [token, setToken] = useState("");
  const [made, setMade] = useState<DnsProvider | null>(null);
  const [zones, setZones] = useState<RemoteZone[]>([]);
  const create = useMutation({
    mutationFn: async () => {
      const p = await api<DnsProvider>("POST", "/dns/providers", {
        name,
        type: "cloudflare",
        token,
      });
      const z = await api<{ items: RemoteZone[] }>(
        "GET",
        `/dns/providers/${p.id}/zones`,
      );
      return { p, zones: z.items };
    },
    onSuccess: ({ p, zones }) => {
      invalidateAll(qc);
      setMade(p);
      setZones(zones);
      setToken("");
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (name.trim() && token.trim()) create.mutate();
  };
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Domains", "DNS providers"]}
        title="Connect Cloudflare"
      />
      {!made ? (
        <form
          onSubmit={submit}
          className="grid grid-cols-1 gap-2 lg:grid-cols-[1fr_22rem]"
        >
          <Panel title="Connection">
            <div className="flex flex-col gap-2">
              <Field label="Name" hint="Only to tell providers apart.">
                <Input value={name} onChange={(e) => setName(e.target.value)} />
              </Field>
              <Field
                label="API token"
                hint="Sealed in the database, never shown again; replace it any time."
              >
                <Input
                  type="password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  autoComplete="off"
                  className="font-mono"
                  placeholder="Cloudflare API token"
                />
              </Field>
              {create.error && (
                <Alert>{errText(create.error, "Could not connect")}</Alert>
              )}
              <div className="flex gap-1.5">
                <Button
                  type="submit"
                  variant="primary"
                  disabled={!name.trim() || !token.trim() || create.isPending}
                >
                  {create.isPending ? "Checking the token…" : "Connect"}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() =>
                    navigate({ to: "/domains/providers" as string })
                  }
                >
                  Cancel
                </Button>
              </div>
            </div>
          </Panel>
          <Panel title="Which token">
            <ol className="text-muted flex list-decimal flex-col gap-1.5 pl-4 text-xs">
              <li>
                In Cloudflare open{" "}
                <b className="text-fg">
                  My Profile → API Tokens → Create Token
                </b>
                <a
                  href="https://dash.cloudflare.com/profile/api-tokens"
                  target="_blank"
                  rel="noreferrer"
                  className="text-accent ml-1 inline-flex items-center gap-0.5"
                >
                  open <ExternalLink className="size-3" />
                </a>
              </li>
              <li>
                Permissions: <code className="text-fg">Zone · Zone · Read</code>{" "}
                and <code className="text-fg">Zone · DNS · Edit</code>
              </li>
              <li>
                For origin certificates also:{" "}
                <code className="text-fg">
                  Zone · SSL and Certificates · Edit
                </code>
              </li>
              <li>Zone resources: only the zones SynCloud should manage.</li>
            </ol>
          </Panel>
        </form>
      ) : (
        <Panel title={`${made.name} is connected`}>
          <div className="flex flex-col gap-2">
            <p className="text-muted text-xs">
              The token sees {zones.length} zone{zones.length === 1 ? "" : "s"}.
              Register the ones SynCloud should manage:
            </p>
            <DataTable
              rows={zones}
              rowKey={(z) => z.id}
              empty={
                <EmptyState icon={Cloud} title="No zones">
                  The token can see no zone: check its zone resources in
                  Cloudflare.
                </EmptyState>
              }
              columns={[
                {
                  header: "Zone",
                  cell: (z) => <span className="font-mono">{z.name}</span>,
                },
                { header: "Cloudflare", cell: (z) => z.status ?? "-" },
                {
                  header: "",
                  cell: (z) =>
                    z.registered ? (
                      <StatusBadge tone="ok">registered</StatusBadge>
                    ) : (
                      <Link
                        to={
                          `/domains/new?provider=${made.id}&zone=${z.name}` as string
                        }
                      >
                        <Button>
                          <Plus className="size-3.5" /> Register
                        </Button>
                      </Link>
                    ),
                },
              ]}
            />
            <div>
              <Link to={"/domains/providers" as string}>
                <Button variant="ghost">Done</Button>
              </Link>
            </div>
          </div>
        </Panel>
      )}
    </div>
  );
}
