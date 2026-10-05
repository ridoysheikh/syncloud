import { useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, KeyRound, Plus, Ticket, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { Dialog } from "@/ui/Dialog";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

interface AccessKey {
  id: string;
  description: string;
  createdAt: string;
  lastUsedAt: string | null;
  lastUsedIp: string;
  secretAccessKey?: string;
}

interface Token {
  id: string;
  name: string;
  createdAt: string;
  expiresAt: string | null;
  lastUsedAt: string | null;
  lastUsedIp: string;
  token?: string;
}

const MAX_KEYS = 2;

function when(iso: string | null) {
  if (!iso) return <span className="text-faint">never</span>;
  const d = new Date(iso);
  return <span title={d.toLocaleString()}>{d.toLocaleDateString()}</span>;
}

/** Your access keys and personal access tokens (§7.1). */
export function CredentialsPage() {
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["IAM"]} title="Access keys & tokens" />
      <AccessKeysPanel />
      <TokensPanel />
    </div>
  );
}

function AccessKeysPanel() {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ["iam", "access-keys"],
    queryFn: () => api<{ items: AccessKey[] }>("GET", "/iam/access-keys"),
  });
  const keys = data?.items ?? [];
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<AccessKey | null>(null);
  const del = useMutation({
    mutationFn: (id: string) => api("DELETE", `/iam/access-keys/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["iam", "access-keys"] }),
  });

  return (
    <Panel
      title={`Access keys (${keys.length}/${MAX_KEYS})`}
      flush
      actions={
        <Button onClick={() => setCreating(true)} disabled={keys.length >= MAX_KEYS} title={keys.length >= MAX_KEYS ? "Delete a key first (2 max, for rotation)" : undefined}>
          <Plus className="size-3.5" /> Create key
        </Button>
      }
    >
      <DataTable
        rows={keys}
        rowKey={(k) => k.id}
        empty={
          !isLoading && (
            <EmptyState icon={KeyRound} title="No access keys">
              Access keys sign requests from synctl, SDKs and CI. You can have two, so you can rotate without downtime.
            </EmptyState>
          )
        }
        columns={[
          { header: "Access key ID", cell: (k) => <span className="font-mono">{k.id}</span> },
          { header: "Description", cell: (k) => k.description || <span className="text-faint">—</span>, className: "w-full" },
          { header: "Created", cell: (k) => when(k.createdAt) },
          { header: "Last used", cell: (k) => when(k.lastUsedAt) },
          { header: "Last IP", cell: (k) => <span className="font-mono">{k.lastUsedIp || "—"}</span> },
          {
            header: "",
            cell: (k) => (
              <IconButton
                label={`Delete ${k.id}`}
                onClick={() => confirm(`Delete access key ${k.id}? Anything using it stops working immediately.`) && del.mutate(k.id)}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            ),
          },
        ]}
      />
      <CreateDialog
        open={creating}
        title="Create access key"
        fields={[{ name: "description", label: "Description", placeholder: "e.g. laptop, GitHub Actions" }]}
        onClose={() => setCreating(false)}
        submit={(v) => api<AccessKey>("POST", "/iam/access-keys", { description: v.description })}
        onCreated={(k) => {
          setCreated(k);
          qc.invalidateQueries({ queryKey: ["iam", "access-keys"] });
        }}
      />
      <SecretDialog
        open={!!created}
        onClose={() => setCreated(null)}
        title="Access key created"
        values={created ? [["Access key ID", created.id], ["Secret access key", created.secretAccessKey ?? ""]] : []}
        hint={
          <>
            Configure synctl with <code className="font-mono">synctl configure</code>.
          </>
        }
      />
    </Panel>
  );
}

function TokensPanel() {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ["iam", "tokens"],
    queryFn: () => api<{ items: Token[] }>("GET", "/iam/tokens"),
  });
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<Token | null>(null);
  const del = useMutation({
    mutationFn: (id: string) => api("DELETE", `/iam/tokens/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["iam", "tokens"] }),
  });

  return (
    <Panel
      title="Personal access tokens"
      flush
      actions={
        <Button onClick={() => setCreating(true)}>
          <Plus className="size-3.5" /> Create token
        </Button>
      }
    >
      <DataTable
        rows={data?.items ?? []}
        rowKey={(t) => t.id}
        empty={
          !isLoading && (
            <EmptyState icon={Ticket} title="No tokens">
              Tokens are sent as <code className="font-mono">Authorization: Bearer …</code>. Handy for curl and scripts.
            </EmptyState>
          )
        }
        columns={[
          { header: "Name", cell: (t) => t.name, className: "w-full" },
          { header: "Created", cell: (t) => when(t.createdAt) },
          { header: "Expires", cell: (t) => (t.expiresAt ? when(t.expiresAt) : <span className="text-warn">never</span>) },
          { header: "Last used", cell: (t) => when(t.lastUsedAt) },
          {
            header: "",
            cell: (t) => (
              <IconButton label={`Delete ${t.name}`} onClick={() => confirm(`Delete token "${t.name}"?`) && del.mutate(t.id)}>
                <Trash2 className="size-3.5" />
              </IconButton>
            ),
          },
        ]}
      />
      <CreateDialog
        open={creating}
        title="Create personal access token"
        fields={[
          { name: "name", label: "Name", placeholder: "e.g. deploy script", required: true },
          { name: "days", label: "Expires in (days, 0 = never)", type: "number", initial: "90" },
        ]}
        onClose={() => setCreating(false)}
        submit={(v) => api<Token>("POST", "/iam/tokens", { name: v.name, expiresInDays: Number(v.days) })}
        onCreated={(t) => {
          setCreated(t);
          qc.invalidateQueries({ queryKey: ["iam", "tokens"] });
        }}
      />
      <SecretDialog open={!!created} onClose={() => setCreated(null)} title="Token created" values={created ? [["Token", created.token ?? ""]] : []} />
    </Panel>
  );
}

interface FieldDef {
  name: string;
  label: string;
  placeholder?: string;
  type?: string;
  initial?: string;
  required?: boolean;
}

function CreateDialog<T>({
  open,
  title,
  fields,
  onClose,
  submit,
  onCreated,
}: {
  open: boolean;
  title: string;
  fields: FieldDef[];
  onClose: () => void;
  submit: (values: Record<string, string>) => Promise<T>;
  onCreated: (v: T) => void;
}) {
  const initial = () => Object.fromEntries(fields.map((f) => [f.name, f.initial ?? ""]));
  const [values, setValues] = useState<Record<string, string>>(initial);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const close = () => {
    setValues(initial());
    setError(null);
    onClose();
  };
  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const v = await submit(values);
      close();
      onCreated(v);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Request failed.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={close} title={title}>
      <form onSubmit={onSubmit} className="flex flex-col gap-2.5">
        {fields.map((f) => (
          <Field key={f.name} label={f.label}>
            <Input
              type={f.type ?? "text"}
              value={values[f.name] ?? ""}
              onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
              placeholder={f.placeholder}
              required={f.required}
            />
          </Field>
        ))}
        {error && <Alert>{error}</Alert>}
        <div className="flex justify-end gap-1.5">
          <Button type="button" variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            Create
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Shows freshly created secrets once, with copy buttons. */
function SecretDialog({
  open,
  onClose,
  title,
  values,
  hint,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  values: [string, string][];
  hint?: ReactNode;
}) {
  return (
    <Dialog open={open} onClose={onClose} title={title} footer={<Button variant="primary" onClick={onClose}>I have saved it</Button>}>
      <div className="flex flex-col gap-2.5">
        <Alert tone="warn">This is the only time the secret is shown. Store it somewhere safe now.</Alert>
        {values.map(([label, value]) => (
          <Field key={label} label={label}>
            <CopyValue value={value} />
          </Field>
        ))}
        {hint && <p className="text-muted text-xs">{hint}</p>}
      </div>
    </Dialog>
  );
}

function CopyValue({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="bg-bg border-line-strong flex h-8 items-center rounded-sm border pl-2">
      <code className="flex-1 truncate font-mono text-xs select-all">{value}</code>
      <IconButton
        label="Copy"
        onClick={() =>
          navigator.clipboard?.writeText(value).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          })
        }
      >
        {copied ? <Check className="text-ok size-3.5" /> : <Copy className="size-3.5" />}
      </IconButton>
    </div>
  );
}
