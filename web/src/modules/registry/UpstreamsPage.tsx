import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { since } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface Credential {
  id: string;
  host: string;
  username: string;
  createdAt: string;
  updatedAt: string;
}

const presets = ["docker.io", "ghcr.io", "quay.io", "registry.gitlab.com"];

/** Credentials nodes and builds use for third-party registries (§5.9). */
export function UpstreamsPage() {
  const qc = useQueryClient();
  const key = ["registry", "upstreams"];
  const { data = [], isLoading } = useQuery({
    queryKey: key,
    queryFn: async () =>
      (await api<{ items: Credential[] }>("GET", "/registry/upstreams")).items,
  });
  const [host, setHost] = useState("docker.io");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const save = useMutation({
    mutationFn: () =>
      api<Credential>("PUT", "/registry/upstreams", {
        host,
        username,
        password,
      }),
    onSuccess: () => {
      setPassword("");
      qc.invalidateQueries({ queryKey: key });
    },
  });
  const del = useMutation({
    mutationFn: (id: string) => api("DELETE", `/registry/upstreams/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (host && username && password) save.mutate();
  };
  const err = save.error ?? del.error;

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Registry"]} title="Upstream credentials" />
      <p className="text-muted -mt-1 text-xs">
        Nodes pull public images straight from their registries. A credential
        here is sent with every pull from that host (a Docker Hub account avoids
        anonymous rate limits; a GHCR token unlocks private images) and is
        available to Git builds for private{" "}
        <span className="font-mono">FROM</span> images.
      </p>
      <div className={cn("grid grid-cols-1 lg:grid-cols-[1fr_22rem]", gap)}>
        <Panel title="Credentials" flush>
          <DataTable
            rows={data}
            rowKey={(c) => c.id}
            empty={
              !isLoading && (
                <EmptyState icon={KeyRound} title="No credentials">
                  Images are pulled anonymously.
                </EmptyState>
              )
            }
            columns={[
              {
                header: "Host",
                cell: (c) => <span className="font-mono">{c.host}</span>,
              },
              {
                header: "Username",
                cell: (c) => <span className="font-mono">{c.username}</span>,
              },
              {
                header: "Updated",
                className: "w-full",
                cell: (c) => (
                  <span className="text-muted">{since(c.updatedAt)}</span>
                ),
              },
              {
                header: "",
                cell: (c) => (
                  <IconButton
                    label="Delete"
                    onClick={() =>
                      confirm(
                        `Delete the credential for ${c.host}? Pulls become anonymous.`,
                      ) && del.mutate(c.id)
                    }
                  >
                    <Trash2 className="size-3.5" />
                  </IconButton>
                ),
              },
            ]}
          />
        </Panel>
        <Panel title="Add or replace">
          <form onSubmit={submit} className="flex flex-col gap-2">
            <Field
              label="Registry host"
              hint="One credential per host; saving again replaces it."
            >
              <Input
                value={host}
                onChange={(e) => setHost(e.target.value)}
                list="upstream-hosts"
                className="font-mono"
              />
              <datalist id="upstream-hosts">
                {presets.map((p) => (
                  <option key={p} value={p} />
                ))}
              </datalist>
            </Field>
            <Field label="Username">
              <Input
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="off"
                className="font-mono"
              />
            </Field>
            <Field
              label="Password or access token"
              hint="Stored encrypted; never shown again."
            >
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
              />
            </Field>
            {err && (
              <Alert>
                {err instanceof ApiError ? err.message : "Request failed"}
              </Alert>
            )}
            <div>
              <Button
                type="submit"
                variant="primary"
                disabled={!host || !username || !password || save.isPending}
              >
                {save.isPending ? "Saving…" : "Save credential"}
              </Button>
            </div>
          </form>
        </Panel>
      </div>
    </div>
  );
}
